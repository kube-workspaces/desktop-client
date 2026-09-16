package rfb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"slices"
	"testing"
	"time"
)

func TestQualityMachinePressureAndRecovery(t *testing.T) {
	cfg := DefaultQualityConfig()
	now := time.Unix(100, 0)
	for _, signal := range []string{"throughput", "latency", "decode"} {
		t.Run(signal, func(t *testing.T) {
			m := newQualityMachine(cfg)
			o := qualityObservation{elapsed: time.Second, motion: 10000, fbPixels: 10000, latency: time.Millisecond}
			switch signal {
			case "throughput":
				o.bytes = 9_000_000
			case "latency":
				o.latency = time.Second
			case "decode":
				o.decode = time.Second
			}
			if got := m.observe(o, now); got.tier != 1 {
				t.Fatalf("pressure: %+v", got)
			}
			o.bytes, o.decode, o.latency = 100, 0, time.Millisecond
			if got := m.observe(o, now.Add(time.Second)); got.tier != 1 {
				t.Fatalf("premature recovery: %+v", got)
			}
			if got := m.observe(o, now.Add(3*time.Second)); got.tier != 2 {
				t.Fatalf("one-step recovery: %+v", got)
			}
			if got := m.observe(o, now.Add(3100*time.Millisecond)); got.tier != 2 {
				t.Fatalf("flapping: %+v", got)
			}
		})
	}
}

func TestQualityMachineIdle(t *testing.T) {
	cfg := DefaultQualityConfig()
	m := newQualityMachine(cfg)
	now := time.Unix(100, 0)
	o := qualityObservation{elapsed: time.Second, fbPixels: 10000}
	m.observe(o, now)
	if got := m.observe(o, now.Add(time.Second)); got.refresh {
		t.Fatal("refresh before first pixels")
	}
	o.motion = 10000
	m.observe(o, now.Add(2*time.Second))
	o.motion = 0
	if got := m.observe(o, now.Add(3*time.Second)); got.tier != 2 || got.refresh {
		t.Fatalf("idle grace: %+v", got)
	}
	if got := m.observe(o, now.Add(4*time.Second)); got.tier != 3 || !got.refresh || got.interval != cfg.IdleInterval {
		t.Fatalf("idle transition: %+v", got)
	}
	if got := m.observe(o, now.Add(5*time.Second)); got.refresh || got.tier != 3 {
		t.Fatalf("repeated refresh: %+v", got)
	}
	o.updating = true
	o.latency = time.Second
	if got := m.observe(o, now.Add(6*time.Second)); got.tier != 2 || got.refresh {
		t.Fatalf("stalled update treated as idle: %+v", got)
	}
}

func TestQualityConfiguration(t *testing.T) {
	for _, mutate := range []func(*QualityConfig){
		func(c *QualityConfig) { c.Tiers[0].Quality = 10 },
		func(c *QualityConfig) { c.Tiers[3].Quality = 9 },
		func(c *QualityConfig) { c.Window = -1 },
		func(c *QualityConfig) { c.MotionFraction = 2 },
	} {
		cfg := DefaultQualityConfig()
		mutate(&cfg)
		if _, err := normalizeQualityConfig(cfg); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	cfg := DefaultQualityConfig()
	normalized, err := normalizeQualityConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tiers[0].Quality = 0
	if normalized.Tiers[0].Quality != 3 {
		t.Fatal("caller owns controller tier slice")
	}
}

func TestQualityWireRefreshAndMetrics(t *testing.T) {
	cfg := DefaultQualityConfig()
	var wire bytes.Buffer
	c := &Conn{w: bufio.NewWriter(&wire), fb: NewFramebuffer(100, 100)}
	q := newQualityController(c, cfg, []Encoding{EncodingTight, EncodingQEMUAudio, QualityLevel(1), CompressLevel(1)})
	c.quality = q
	now := time.Unix(100, 0)
	q.noteRequest(now)
	q.noteRequest(now.Add(time.Second))
	if got := q.beginUpdate(now.Add(2 * time.Second)); got != 2*time.Second {
		t.Fatalf("oldest request latency = %v", got)
	}
	q.noteUpdate([]Rect{{Width: 100, Height: 100}}, 400, time.Millisecond, time.Millisecond, 10000)
	q.lastTick = now
	if err := q.tick(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := q.tick(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := q.tick(now.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	encs, err := drainClientMessages(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(encs, int32(EncodingQEMUAudio)) {
		t.Fatal("lost audio advertisement")
	}
	for _, e := range encs {
		if isQualityLevel(Encoding(e)) {
			t.Fatal("lossless refresh advertised JPEG")
		}
	}
	var request [10]byte
	if _, err := io.ReadFull(&wire, request[:]); err != nil {
		t.Fatal(err)
	}
	if request[0] != msgFramebufferUpdateRequest || request[1] != 0 || binary.BigEndian.Uint16(request[6:]) != 100 {
		t.Fatalf("refresh: %x", request)
	}
	q.noteUpdate(nil, 16, 0, 0, 10000) // audio/feature acknowledgement
	q.noteUpdate([]Rect{{Width: 100, Height: 100}}, 40000, time.Second, time.Second, 10000)
	if err := q.tick(now.Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := q.tick(now.Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 {
		t.Fatal("forced repaint rearmed refresh")
	}
	if c.QualityInterval() != cfg.IdleInterval {
		t.Fatal("idle cadence missing")
	}
}

func TestQualityHandshakeAndRunEOF(t *testing.T) {
	p := newPeer(t)
	msgs := make(chan []byte)
	encodings := make(chan []int32, 1)
	errors := make(chan error, 1)
	feed(t, p, 100, 100, msgs, encodings, errors)
	cfg := DefaultQualityConfig()
	c, err := NewConn(p.client, Config{Quality: &cfg, AudioFormat: &AudioFormatPCM})
	if err != nil {
		t.Fatal(err)
	}
	encs := <-encodings
	if !slices.Contains(encs, int32(QualityLevel(8))) || !slices.Contains(encs, int32(EncodingQEMUAudio)) {
		t.Fatalf("initial encodings: %v", encs)
	}
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	close(msgs)
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	_ = p.server.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not join quality controller on EOF")
	}
}
