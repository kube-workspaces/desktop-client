package rfb

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// QualityTier is one step on an ascending quality ladder. Throughput ceilings
// are pressure thresholds, not estimates of available network capacity.
type QualityTier struct {
	Name string
	// Quality is a Tight JPEG level (0..9), or -1 to disable JPEG. In QEMU,
	// even level 9 is lossy; omitting the quality pseudo-encoding resets it.
	Quality  int
	Compress int
	// MaxBytesPerSec triggers a downward step above this rate during motion.
	// Zero disables the throughput limit for this tier.
	MaxBytesPerSec uint64
}

// DefaultQualityTiers are provisional pressure ceilings, pending a live probe
// sweep. A congested slow link can also trigger a drop through update latency.
var DefaultQualityTiers = []QualityTier{
	{Name: "poor", Quality: 3, Compress: 9, MaxBytesPerSec: 500_000},
	{Name: "fair", Quality: 6, Compress: 6, MaxBytesPerSec: 2_000_000},
	{Name: "good", Quality: 8, Compress: 3, MaxBytesPerSec: 8_000_000},
	{Name: "lossless", Quality: -1, Compress: 3, MaxBytesPerSec: 32_000_000},
}

// QualityConfig enables adaptive Tight tuning on a connection. Start with
// DefaultQualityConfig; zero numeric fields receive defaults. RefreshOnIdle is
// explicit so callers may disable forced repaints. The last tier must be lossless.
type QualityConfig struct {
	Tiers  []QualityTier
	Window time.Duration
	// MotionFraction is the changed fraction of the framebuffer per second
	// below which IdleDelay begins. Sparse cursor/text changes can stay idle.
	MotionFraction float64
	IdleDelay      time.Duration
	UpgradeGrace   time.Duration
	// MaxUpgradeLatency is a request-to-update pressure threshold, not a ping
	// RTT. Incremental requests can wait for guest damage before being answered.
	MaxUpgradeLatency time.Duration
	// MaxDecodeFraction limits decoder occupancy, including reads blocked on
	// the transport. It is not a measurement of CPU time alone.
	MaxDecodeFraction float64
	ActiveInterval    time.Duration
	IdleInterval      time.Duration
	RefreshOnIdle     bool
}

// DefaultQualityConfig returns independent tuning data for one connection.
func DefaultQualityConfig() QualityConfig {
	return QualityConfig{
		Tiers:             append([]QualityTier(nil), DefaultQualityTiers...),
		Window:            400 * time.Millisecond,
		MotionFraction:    0.05,
		IdleDelay:         600 * time.Millisecond,
		UpgradeGrace:      2 * time.Second,
		MaxUpgradeLatency: 200 * time.Millisecond,
		MaxDecodeFraction: 0.5,
		ActiveInterval:    16 * time.Millisecond,
		IdleInterval:      80 * time.Millisecond,
		RefreshOnIdle:     true,
	}
}

func normalizeQualityConfig(cfg QualityConfig) (QualityConfig, error) {
	d := DefaultQualityConfig()
	if len(cfg.Tiers) == 0 {
		cfg.Tiers = d.Tiers
	}
	cfg.Tiers = append([]QualityTier(nil), cfg.Tiers...)
	for _, t := range cfg.Tiers {
		if t.Quality < -1 || t.Quality > 9 || t.Compress < 0 || t.Compress > 9 {
			return cfg, fmt.Errorf("rfb: invalid quality tier %q", t.Name)
		}
	}
	if cfg.Tiers[len(cfg.Tiers)-1].Quality != -1 {
		return cfg, fmt.Errorf("rfb: last quality tier must disable JPEG (Quality: -1)")
	}
	for _, pair := range []struct {
		value    *time.Duration
		fallback time.Duration
	}{
		{&cfg.Window, d.Window}, {&cfg.IdleDelay, d.IdleDelay},
		{&cfg.UpgradeGrace, d.UpgradeGrace}, {&cfg.MaxUpgradeLatency, d.MaxUpgradeLatency},
		{&cfg.ActiveInterval, d.ActiveInterval}, {&cfg.IdleInterval, d.IdleInterval},
	} {
		if *pair.value < 0 {
			return cfg, fmt.Errorf("rfb: quality durations cannot be negative")
		}
		if *pair.value == 0 {
			*pair.value = pair.fallback
		}
	}
	if cfg.MotionFraction == 0 {
		cfg.MotionFraction = d.MotionFraction
	}
	if cfg.MaxDecodeFraction == 0 {
		cfg.MaxDecodeFraction = d.MaxDecodeFraction
	}
	for _, f := range []float64{cfg.MotionFraction, cfg.MaxDecodeFraction} {
		if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 || f > 1 {
			return cfg, fmt.Errorf("rfb: quality fractions must be in (0, 1]")
		}
	}
	return cfg, nil
}

type qualityObservation struct {
	bytes    uint64
	elapsed  time.Duration
	motion   uint64
	decode   time.Duration
	latency  time.Duration
	fbPixels uint64
	updating bool
}

type qualityOutcome struct {
	tier     int
	refresh  bool
	interval time.Duration
}

// qualityMachine takes a scripted clock. Upgrades need continuous headroom;
// any pressure resets that grace period, including pressure at the bottom tier.
type qualityMachine struct {
	cfg          QualityConfig
	tier         int
	healthySince time.Time
	idleSince    time.Time
	idle         bool
	seenPixels   bool
}

func newQualityMachine(cfg QualityConfig) *qualityMachine {
	return &qualityMachine{cfg: cfg, tier: max(0, len(cfg.Tiers)-2)}
}

func (m *qualityMachine) observe(o qualityObservation, now time.Time) qualityOutcome {
	cfg := m.cfg
	out := qualityOutcome{tier: m.tier, interval: cfg.ActiveInterval}
	if o.elapsed <= 0 || o.fbPixels == 0 {
		return out
	}
	m.seenPixels = m.seenPixels || o.motion > 0
	motion := float64(o.motion) / o.elapsed.Seconds()
	if !o.updating && motion < cfg.MotionFraction*float64(o.fbPixels) {
		m.healthySince = time.Time{}
		if m.idleSince.IsZero() {
			m.idleSince = now
		}
		if now.Sub(m.idleSince) >= cfg.IdleDelay && m.seenPixels {
			out.refresh = !m.idle && cfg.RefreshOnIdle
			m.idle = true
			m.tier = len(cfg.Tiers) - 1
			out.tier = m.tier
			out.interval = cfg.IdleInterval
		}
		return out
	}
	m.idleSince = time.Time{}
	m.idle = false
	rate := float64(o.bytes) / o.elapsed.Seconds()
	limit := cfg.Tiers[m.tier].MaxBytesPerSec
	strained := (limit > 0 && rate > float64(limit)) ||
		float64(o.decode)/float64(o.elapsed) >= cfg.MaxDecodeFraction ||
		o.latency > cfg.MaxUpgradeLatency
	if strained {
		m.tier = max(0, m.tier-1)
		m.healthySince = time.Time{}
	} else if o.motion > 0 && o.latency > 0 &&
		(limit == 0 || rate < float64(limit)*0.8) {
		if m.healthySince.IsZero() {
			m.healthySince = now
		}
		if now.Sub(m.healthySince) >= cfg.UpgradeGrace {
			m.tier = min(len(cfg.Tiers)-1, m.tier+1)
			m.healthySince = now
		}
	} else {
		m.healthySince = time.Time{}
	}
	out.tier = m.tier
	return out
}

func isQualityLevel(e Encoding) bool  { return e >= -32 && e <= -23 }
func isCompressLevel(e Encoding) bool { return e >= -256 && e <= -247 }

// qualityController owns the machine on its ticker goroutine. The read loop
// supplies completed framebuffer measurements without taking any connection
// lock from inside mu. Audio and bufio read-ahead are excluded from these bytes.
type qualityController struct {
	conn           *Conn
	cfg            QualityConfig
	base           []Encoding
	machine        *qualityMachine
	appliedTier    int
	interval       atomic.Int64
	mu             sync.Mutex
	observation    qualityObservation
	requestedAt    time.Time
	updateStarted  time.Time
	refreshPending bool
	lastTick       time.Time
}

func newQualityController(conn *Conn, cfg QualityConfig, base []Encoding) *qualityController {
	q := &qualityController{conn: conn, cfg: cfg, machine: newQualityMachine(cfg)}
	for _, e := range base {
		if !isQualityLevel(e) && !isCompressLevel(e) {
			q.base = append(q.base, e)
		}
	}
	q.appliedTier = q.machine.tier
	q.interval.Store(int64(cfg.ActiveInterval))
	return q
}

func (q *qualityController) noteRequest(now time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	// Keep the oldest outstanding mark: a fast request ticker must not make
	// a congested link appear to answer in just a few milliseconds.
	if q.requestedAt.IsZero() {
		q.requestedAt = now
	}
}

func (q *qualityController) beginUpdate(now time.Time) time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.updateStarted = now
	var latency time.Duration
	if !q.requestedAt.IsZero() {
		latency = now.Sub(q.requestedAt)
	}
	q.requestedAt = time.Time{}
	return latency
}

func (q *qualityController) noteUpdate(damage []Rect, bytes uint64, decode, latency time.Duration, pixels uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.updateStarted = time.Time{}
	q.observation.fbPixels = pixels
	// RFB has no response IDs. Ignore the first pixel-bearing update after
	// our forced repaint; otherwise its full-screen damage re-arms idle
	// refresh indefinitely. Pseudo-encoding acknowledgements do not consume it.
	if q.refreshPending && len(damage) > 0 {
		q.refreshPending = false
		return
	}
	q.observation.bytes += bytes
	q.observation.decode += decode
	for _, r := range damage {
		q.observation.motion += uint64(r.Area())
	}
	if len(damage) > 0 {
		q.observation.latency = max(q.observation.latency, latency)
	}
}

func (q *qualityController) encodingsFor(tier int) []Encoding {
	t := q.cfg.Tiers[tier]
	encs := append([]Encoding(nil), q.base...)
	if t.Quality >= 0 {
		encs = append(encs, QualityLevel(t.Quality))
	}
	return append(encs, CompressLevel(t.Compress))
}

func (q *qualityController) run(ctx context.Context) error {
	q.lastTick = time.Now()
	ticker := time.NewTicker(q.cfg.Window)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if ctx.Err() != nil {
				return nil
			}
			if err := q.tick(time.Now()); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

func (q *qualityController) tick(now time.Time) error {
	q.mu.Lock()
	o := q.observation
	refreshPending := q.refreshPending
	q.observation = qualityObservation{fbPixels: o.fbPixels}
	if !q.updateStarted.IsZero() {
		o.updating = true
		o.latency = max(o.latency, now.Sub(q.updateStarted))
	}
	q.mu.Unlock()
	o.elapsed = now.Sub(q.lastTick)
	q.lastTick = now
	if refreshPending {
		return nil
	}
	return q.applyOutcome(q.machine.observe(o, now))
}

func (q *qualityController) applyOutcome(out qualityOutcome) error {
	if out.tier != q.appliedTier {
		if err := q.conn.SetEncodings(q.encodingsFor(out.tier)); err != nil {
			return err
		}
		q.appliedTier = out.tier
	}
	if out.refresh {
		q.mu.Lock()
		q.refreshPending = true
		q.mu.Unlock()
		if err := q.conn.RequestUpdate(false); err != nil {
			return err
		}
	}
	q.interval.Store(int64(out.interval))
	return nil
}
