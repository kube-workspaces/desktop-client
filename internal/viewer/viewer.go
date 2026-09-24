// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
	"github.com/kube-workspaces/desktop-client/internal/transport"
)

// Defaults for the timings in [Config]. They are separate constants because
// each one is a decision, not a taste.
const (
	// DefaultResizeDebounce is how long window resizing must be quiet before
	// the guest is asked to follow.
	//
	// It is a floor, not a preference. The guest applies a new EDID mode from
	// a systemd poller that runs twice a second (kw-display-resize, xrandr
	// --auto), so a client that re-asks faster than that interrupts a resize
	// that is still being applied and the display thrashes between modes. The
	// web client's 150ms is exactly this bug. Values below this constant are
	// raised to it.
	DefaultResizeDebounce = 500 * time.Millisecond

	// DefaultClipboardInterval is how often the host clipboard is sampled.
	//
	// Polling is not laziness: no windowing system offers a reliable "the
	// clipboard changed" signal that is both cross-platform and free of
	// self-notification, and SDL's EVENT_CLIPBOARD_UPDATE fires for our own
	// writes too. A cheap poll is the only implementation that behaves the
	// same everywhere.
	DefaultClipboardInterval = 500 * time.Millisecond

	// DefaultStatsInterval is how often the title bar's fps and bitrate are
	// recomputed. Faster looks jittery and reads worse.
	DefaultStatsInterval = time.Second

	// DefaultFailureLinger is how long a terminal failure stays on screen
	// before the viewer exits.
	//
	// Exiting the instant a session fails takes the window — and the only
	// place the reason was written — away before anybody can read it. A few
	// seconds is long enough to read one line and short enough that the
	// client does not feel stuck; the user can also close the window at once.
	DefaultFailureLinger = 4 * time.Second

	// forcedPresentInterval bounds how long the window can go without being
	// redrawn. Nothing in the RFB stream tells a client that the host
	// compositor lost the window contents, so a slow heartbeat repaint is the
	// portable defence against a window that comes back from an occlusion or
	// a workspace switch showing garbage. It is also what keeps the frozen
	// frame and its overlay on screen while there is no connection at all.
	forcedPresentInterval = 500 * time.Millisecond

	// maxPendingDamage bounds the damage list the RFB goroutine hands to the
	// render loop. Past this many rectangles the next frame is a full repaint
	// regardless, so there is nothing to gain by remembering more.
	maxPendingDamage = 4096

	// maxPendingAudio bounds how much PCM may wait in the inbox between the
	// network and the audio device. 64 KiB is a few hundred milliseconds of
	// stereo 44.1kHz; beyond that the link is outpacing the device and the
	// newest samples win. Bounding it also keeps a talkative guest from
	// growing the queue unboundedly.
	maxPendingAudio = 1 << 16

	// maxWheelTicks caps how many wheel clicks one event may expand into.
	// A high-resolution trackpad can report a large accumulated delta, and
	// each tick costs two RFB messages.
	maxWheelTicks = 16

	// fallbackWidth/fallbackHeight size the window when it opens before any
	// connection has revealed the guest's resolution and the caller did not
	// ask for a size.
	fallbackWidth  = 1280
	fallbackHeight = 800
)

// Config configures a [Viewer]. The zero value is usable: every field has a
// documented default.
type Config struct {
	// AdaptiveQuality enables per-connection Tight tuning and a lossless idle
	// refresh. Graphical entry points enable it by default; diagnostic callers
	// can leave it off or supply their own rfb.Config.Quality.
	AdaptiveQuality bool

	// Title is the base window title, typically "namespace/workspace".
	Title string

	// Width and Height are the initial window size. Zero means "match the
	// guest's current resolution", clamped by MaxInitialWidth/Height.
	Width, Height int

	// MaxInitialWidth and MaxInitialHeight cap the automatic initial size, so
	// that connecting to a 4K guest does not open a 4K window. Zero means
	// 1920x1080.
	MaxInitialWidth, MaxInitialHeight int

	// Fullscreen starts the session fullscreen.
	Fullscreen bool

	// ScaleQuality selects the scaling filter. Empty means [ScaleLinear].
	ScaleQuality ScaleQuality

	// VSync synchronises presentation with the display. Defaults to true;
	// set NoVSync to override.
	NoVSync bool

	// ResizeDebounce is the quiet period before a window resize is forwarded
	// to the guest. Values below [DefaultResizeDebounce] are raised to it.
	ResizeDebounce time.Duration

	// ClipboardInterval is how often the host clipboard is sampled. Zero means
	// [DefaultClipboardInterval]; a negative value disables host-to-guest
	// clipboard sharing.
	ClipboardInterval time.Duration

	// StatsInterval is how often the title bar is refreshed. Zero means
	// [DefaultStatsInterval].
	StatsInterval time.Duration

	// FailureLinger is how long a terminal failure is shown before the viewer
	// exits. Zero means [DefaultFailureLinger]; a negative value exits as soon
	// as the failure is known.
	FailureLinger time.Duration

	// FullscreenKey toggles fullscreen with no modifier. Zero means F11.
	FullscreenKey keysym.Key

	// QuitRune ends the session when pressed with Ctrl+Alt. Zero means 'q'.
	// It is a rune rather than a keysym.Key because keysym.Key covers only
	// non-text keys, and the quit binding is a letter.
	QuitRune rune

	// ControlRune, when non-zero, is the shared-display control binding:
	// pressed with Ctrl+Alt it fires the handler registered with
	// [Viewer.SetControlHandler]. Zero disables the binding, which is the
	// default for sessions with no control to transfer.
	ControlRune rune

	// SendCtrlAltDelKey sends Ctrl-Alt-Del to the guest when pressed with
	// Ctrl+Alt. Zero means End.
	SendCtrlAltDelKey keysym.Key

	// MaxUploadRects bounds how many sub-rectangles one frame may upload
	// before the damage is collapsed into its bounding box. Zero means 32.
	MaxUploadRects int

	// ReadOnly prevents sending any guest-mutating messages (keyboard,
	// pointer, wheel, clipboard, guest resize). It is the initial value;
	// [Viewer.SetReadOnly] changes it at runtime when a shared display
	// session's role changes.
	ReadOnly bool

	// Logf, if set, receives diagnostic messages.
	Logf func(format string, args ...any)
}

func (c *Config) applyDefaults() {
	if c.MaxInitialWidth <= 0 {
		c.MaxInitialWidth = 1920
	}
	if c.MaxInitialHeight <= 0 {
		c.MaxInitialHeight = 1080
	}
	if c.ScaleQuality == "" {
		c.ScaleQuality = ScaleLinear
	}
	if c.ResizeDebounce < DefaultResizeDebounce {
		c.ResizeDebounce = DefaultResizeDebounce
	}
	if c.ClipboardInterval == 0 {
		c.ClipboardInterval = DefaultClipboardInterval
	}
	if c.StatsInterval <= 0 {
		c.StatsInterval = DefaultStatsInterval
	}
	if c.FailureLinger == 0 {
		c.FailureLinger = DefaultFailureLinger
	}
	if c.FullscreenKey == keysym.KeyUnknown {
		c.FullscreenKey = keysym.KeyF11
	}
	if c.QuitRune == 0 {
		c.QuitRune = 'q'
	}
	if c.SendCtrlAltDelKey == keysym.KeyUnknown {
		c.SendCtrlAltDelKey = keysym.KeyEnd
	}
	if c.MaxUploadRects <= 0 {
		c.MaxUploadRects = defaultMaxUploadRects
	}
}

// Hotkeys returns the host key bindings, for help text and the first lines of
// a session's output.
//
// Host hotkeys are never forwarded to the guest: a combination that the client
// acts on must not also reach the remote desktop, or the user gets both
// effects at once.
func (c *Config) Hotkeys() []string {
	cfg := *c
	cfg.applyDefaults()
	lines := []string{
		fmt.Sprintf("%-16s toggle fullscreen", strings.ToUpper(cfg.FullscreenKey.String())),
		fmt.Sprintf("%-16s send Ctrl-Alt-Del to the guest", "Ctrl+Alt+"+cfg.SendCtrlAltDelKey.String()),
	}
	if cfg.ControlRune != 0 {
		lines = append(lines,
			fmt.Sprintf("%-16s request/release control", "Ctrl+Alt+"+string(cfg.ControlRune)))
	}
	return append(lines,
		fmt.Sprintf("%-16s disconnect", "Ctrl+Alt+"+string(cfg.QuitRune)))
}

// Viewer owns a window and presents a succession of [rfb.Conn] connections in
// it.
//
// The window, the renderer and the textures belong to the viewer for its whole
// lifetime; connections come and go underneath it, supplied by a [ConnSource].
// That asymmetry is the entire design. A reconnect produces a new *rfb.Conn,
// and a viewer that owned one for its lifetime would have to be rebuilt — and
// the window destroyed and recreated, and SDL loaded and unloaded — once per
// connection generation, so the user would watch their desktop vanish and
// reappear on every blip. A real VDI client freezes the last frame under a
// status overlay instead, which is what this does.
//
// The RFB read loop runs on its own goroutine, the connection pump on another,
// and the windowing library demands to be driven from one specific thread, so
// all three communicate through the small mutex-guarded inbox below rather
// than by sharing state. Nothing in [Viewer.Run]'s call graph is reachable
// from the RFB goroutine, and nothing in the callbacks touches the backend.
type Viewer struct {
	cfg Config
	be  Backend

	// inbox is written by the RFB read loop and the connection pump, and
	// drained by the render loop.
	inbox struct {
		sync.Mutex
		damage       []rfb.Rect
		fullRepaint  bool
		needsPresent bool
		resized      bool
		gotUpdate    bool
		cutText      string
		hasCutText   bool

		// status and detail drive the overlay. They are in the inbox because
		// the session supervisor sets them from its own goroutine.
		status Status
		detail string

		// readOnly and title are the live forms of Config.ReadOnly and
		// Config.Title: a shared display session changes them mid-flight when
		// its role changes, from a goroutine the render loop does not own.
		readOnly bool
		title    string

		// takeoverHandler is the optional action run when the user presses
		// Enter while the display-in-use overlay is up.
		takeoverHandler func() error

		// controlHandler is the shared-display control action fired by the
		// Ctrl+Alt+<ControlRune> binding. Nil means no such session.
		controlHandler func()

		// nextConn/nextCtx is a connection the pump has taken out and not yet
		// handed over.
		nextConn transport.Conn
		nextCtx  context.Context
		hasNext  bool

		// srcErr is a terminal failure reported by the connection source.
		srcErr error
		hasErr bool

		// audio accumulates PCM batches from the RFB read loop until the
		// render loop plays them. It is bounded: a link that outpaces the
		// device for any length of time is being cut down to its newest audio,
		// which is what the ear prefers to a growing backlog.
		audio []byte
	}

	// Everything below is owned by the render loop and must not be touched
	// from any other goroutine.
	events  []Event
	mods    keysym.Tracker
	held    []keysym.Keysym
	swallow map[hotkeyID]bool

	// conn is the connection being displayed, or nil while there is none.
	// connCtx bounds it: the render loop notices a drop by watching it rather
	// than by being told, which removes a whole class of ordering bug.
	conn    transport.Conn
	connCtx context.Context

	// audioSink is the backend narrowed to its audio capability, or nil when
	// the backend has no audio output at all. The zero value is a valid
	// backend that simply plays no sound.
	audioSink AudioSink
	// audioFmt is the PCM layout the current connection's guest is sending,
	// from its negotiated rfb.AudioFormat, or nil when the connection (or the
	// backend) has no audio. It is owned by the render loop.
	audioFmt *rfb.AudioFormat
	// audioOpened reports that the sink's device is open, audioEnabled that
	// the guest has been told to stream. Both are reset on attach and cleared
	// on drop, like everything else that belongs to one connection.
	audioOpened  bool
	audioEnabled bool

	// fresh is true from the moment a connection is installed until it has
	// decoded its first framebuffer update. Until then the texture still holds
	// the previous connection's last frame, and a new connection's framebuffer
	// is all zeroes: uploading it would blank the screen, which is exactly
	// what freezing the frame exists to avoid.
	fresh bool

	// haveFrame reports whether the texture holds a real image, and
	// frameW/frameH is its size. They are the geometry the frozen frame is
	// presented with, so a reconnect at a different guest resolution cannot
	// stretch the old frame into the new one's aspect ratio.
	haveFrame      bool
	frameW, frameH int

	// fitted records that the window has already been sized to a guest, and
	// generation counts the connections this window has shown.
	fitted     bool
	generation int

	buttons  Buttons
	present  Rect // last presented rectangle, in surface pixels
	texW     int
	texH     int
	winW     int
	winH     int
	ptrX     int
	ptrY     int
	ptrKnown bool
	sentX    int
	sentY    int
	sentMask rfb.ButtonMask
	sentAny  bool

	resizeW       int
	resizeH       int
	resizeDue     time.Time
	resizePending bool

	hostClip      string
	clipDue       time.Time
	clipboardHint bool

	frames      int
	statsDue    time.Time
	lastBytes   uint64
	lastStatsAt time.Time
	title       string

	// statusLayer owns the rasterised status plate, rebuilt only when the
	// text or the window size changes — a reconnect can last minutes and
	// re-rasterising a glyph plate 500 times a second for it would be absurd.
	statusLayer StatusLayer

	presentDue time.Time
	quit       bool

	// srcErr and failedAt implement the linger: a terminal failure is shown
	// for a moment before the window closes.
	srcErr   error
	failedAt time.Time
	failed   bool
}

// hotkeyID identifies a key for the purpose of suppressing its release event.
// A key is either a named key or a character, never both.
type hotkeyID struct {
	key keysym.Key
	r   rune
}

// New returns a viewer that will draw into be.
func New(be Backend, cfg Config) *Viewer {
	cfg.applyDefaults()
	v := &Viewer{
		be:      be,
		cfg:     cfg,
		swallow: make(map[hotkeyID]bool),
	}
	v.inbox.readOnly = cfg.ReadOnly
	v.inbox.title = cfg.Title
	if sink, ok := be.(AudioSink); ok {
		v.audioSink = sink
	}
	return v
}

// AudioFormat returns the PCM layout audio would be requested in, or nil when
// the backend cannot play audio.
//
// A caller that runs a guest with sound (an image whose spec sets
// soundDevice, say) installs the returned value as rfb.Config.AudioFormat on
// every connection it creates, so the viewer's OnAudio handler applies to that
// connection's stream. Connections that never opted in — a guest with no audio
// device — simply never send audio, and the return here being non-nil costs
// nothing.
func (v *Viewer) AudioFormat() *rfb.AudioFormat {
	if v.audioSink == nil {
		return nil
	}
	return &rfb.AudioFormatPCM
}

// RFBConfig returns base with the viewer's callbacks installed.
//
// It must be used when a connection is created, because rfb.Config is read
// once at handshake time. A reconnecting caller therefore calls this once per
// connection — the callbacks are stable and all point at this one viewer, so
// they can be installed on every generation without the viewer knowing.
// Any callbacks already set on base are preserved and called first, so a
// caller can still observe the stream for diagnostics.
func (v *Viewer) RFBConfig(base rfb.Config) rfb.Config {
	cfg := base
	if v.cfg.AdaptiveQuality && cfg.Quality == nil {
		quality := rfb.DefaultQualityConfig()
		cfg.Quality = &quality
	}

	prevUpdate := base.OnFramebufferUpdate
	cfg.OnFramebufferUpdate = func(fb *rfb.Framebuffer, damage []rfb.Rect) {
		if prevUpdate != nil {
			prevUpdate(fb, damage)
		}
		v.inbox.Lock()
		// The framebuffer's damage slice is reused by the connection, so the
		// rectangles are copied rather than retained.
		v.inbox.damage = append(v.inbox.damage, damage...)
		// If the render loop falls far enough behind that updates pile up,
		// stop accumulating: the list is about to be collapsed into a full
		// repaint anyway, and an unbounded queue fed by the network is not a
		// queue this process should be growing.
		if len(v.inbox.damage) > maxPendingDamage {
			v.inbox.damage = v.inbox.damage[:0]
			v.inbox.fullRepaint = true
		}
		// gotUpdate is what releases the frozen frame: it says that the
		// connection now holds pixels of its own.
		v.inbox.gotUpdate = true
		v.inbox.needsPresent = true
		v.inbox.Unlock()
		// This runs on the RFB read loop's goroutine, and the render loop is
		// very likely parked in a blocking event wait with nothing to show
		// for it. Waking it here is what keeps frame latency at the decode
		// time rather than at the heartbeat interval.
		v.wake()
	}

	prevResize := base.OnResize
	cfg.OnResize = func(w, h int) {
		if prevResize != nil {
			prevResize(w, h)
		}
		v.inbox.Lock()
		// The texture is reallocated by the render loop, which is the only
		// goroutine allowed to talk to the backend.
		v.inbox.resized = true
		v.inbox.fullRepaint = true
		v.inbox.needsPresent = true
		v.inbox.Unlock()
		v.wake()
	}

	prevCut := base.OnCutText
	cfg.OnCutText = func(text string) {
		if prevCut != nil {
			prevCut(text)
		}
		v.inbox.Lock()
		v.inbox.cutText = text
		v.inbox.hasCutText = true
		v.inbox.Unlock()
		v.wake()
	}

	prevAudio := base.OnAudio
	cfg.OnAudio = func(data []byte) {
		if prevAudio != nil {
			prevAudio(data)
		}
		// The connection hands the callback a fresh buffer per batch but asks
		// the callback not to retain it, so the data is copied before it goes
		// in the queue.
		clone := make([]byte, len(data))
		copy(clone, data)
		v.inbox.Lock()
		audio := append(v.inbox.audio, clone...)
		if len(audio) > maxPendingAudio {
			// Keep the newest samples: throw the oldest away.
			audio = append([]byte(nil), audio[len(audio)-maxPendingAudio:]...)
		}
		v.inbox.audio = audio
		v.inbox.Unlock()
		v.wake()
	}

	return cfg
}

// SetStatus updates the modal overlay drawn over the frame.
//
// It is safe to call from any goroutine and never blocks, so it can be wired
// straight to a session supervisor's state callback. Passing [StatusLive]
// removes the overlay. The detail is the reason, if there is one; it is
// flattened to a single line and truncated to fit.
func (v *Viewer) SetStatus(status Status, detail string) {
	v.inbox.Lock()
	if v.inbox.status == status && v.inbox.detail == detail {
		v.inbox.Unlock()
		return
	}
	v.inbox.status, v.inbox.detail = status, detail
	v.inbox.needsPresent = true
	v.inbox.Unlock()
	v.wake()
}

// Status returns the status the overlay is currently showing.
func (v *Viewer) Status() (Status, string) {
	v.inbox.Lock()
	defer v.inbox.Unlock()
	return v.inbox.status, v.inbox.detail
}

// SetTakeoverHandler registers the action run when the user presses Enter
// while the display-in-use overlay is up. Safe from any goroutine; nil
// disables it.
func (v *Viewer) SetTakeoverHandler(h func() error) {
	v.inbox.Lock()
	v.inbox.takeoverHandler = h
	v.inbox.Unlock()
}

// takeoverAction returns the registered take-over action, or nil.
func (v *Viewer) takeoverAction() func() error {
	v.inbox.Lock()
	defer v.inbox.Unlock()
	return v.inbox.takeoverHandler
}

// SetReadOnly changes whether guest-mutating input is forwarded, for a shared
// display session whose role changed underneath a live window. Safe from any
// goroutine.
func (v *Viewer) SetReadOnly(readOnly bool) {
	v.inbox.Lock()
	v.inbox.readOnly = readOnly
	v.inbox.Unlock()
}

// readOnly reports whether guest-mutating input is currently suppressed.
func (v *Viewer) readOnly() bool {
	v.inbox.Lock()
	defer v.inbox.Unlock()
	return v.inbox.readOnly
}

// SetTitle changes the base window title. Safe from any goroutine; the change
// is picked up on the next title refresh.
func (v *Viewer) SetTitle(title string) {
	v.inbox.Lock()
	v.inbox.title = title
	v.inbox.Unlock()
	v.wake()
}

// titleBase returns the current base window title.
func (v *Viewer) titleBase() string {
	v.inbox.Lock()
	defer v.inbox.Unlock()
	return v.inbox.title
}

// SetControlHandler registers the shared-display control action fired when
// the user presses Ctrl+Alt+[Config.ControlRune]. The handler runs on its own
// goroutine, so it may block on network calls; it reports through the
// viewer's own methods. Safe from any goroutine; nil disables the binding's
// action.
func (v *Viewer) SetControlHandler(h func()) {
	v.inbox.Lock()
	v.inbox.controlHandler = h
	v.inbox.Unlock()
}

// controlAction returns the registered control action, or nil.
func (v *Viewer) controlAction() func() {
	v.inbox.Lock()
	defer v.inbox.Unlock()
	return v.inbox.controlHandler
}

// setStatusIfLive raises a status only when nothing more specific has been
// reported, so that a supervisor's "display in use" is never overwritten by
// the viewer's own generic "reconnecting".
func (v *Viewer) setStatusIfLive(status Status, detail string) {
	v.inbox.Lock()
	if v.inbox.status != StatusLive {
		v.inbox.Unlock()
		return
	}
	v.inbox.status, v.inbox.detail = status, detail
	v.inbox.needsPresent = true
	v.inbox.Unlock()
	v.wake()
}

// Run opens the window and drives it until the context is cancelled, the user
// quits, or the connection source gives up.
//
// The window is opened before the first connection and closed only on the way
// out: a session that is waiting for a busy display, or reconnecting over a
// flaky link, must still be visible and closable. Connections are taken from
// src as they become available and swapped in underneath the window.
//
// It must be called from the goroutine that owns the main OS thread; see
// [Backend]. It does not close any connection: the caller owns the session,
// and the server's single VNC slot is released by closing it.
func (v *Viewer) Run(ctx context.Context, src ConnSource) error {
	if src == nil {
		return fmt.Errorf("viewer: nil connection source")
	}
	if err := v.start(time.Now()); err != nil {
		return err
	}
	defer v.stop()

	pumpCtx, stopPump := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		v.pump(pumpCtx, src)
	}()
	// The pump writes to the inbox, so it must be stopped and drained before
	// the viewer goes away. Deferred after v.stop so that it runs first.
	defer func() {
		stopPump()
		<-done
	}()

	// The loop below blocks in the backend rather than in a select, so
	// cancellation has to arrive as an event like everything else. Registered
	// after the defers above so that it is cancelled before the window is
	// closed, and safe regardless: Wake is the one method a foreign goroutine
	// may call.
	stopWake := context.AfterFunc(ctx, v.be.Wake)
	defer stopWake()

	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := v.step(time.Now()); err != nil {
			return err
		}
		if v.quit {
			return v.srcErr
		}
		// Sleep until something needs doing instead of spinning on a 2ms
		// timer. Input ends the wait by itself; a decoded frame, a new
		// connection or a status change ends it through [Viewer.wake], which
		// every writer to the inbox calls. The timeout is only the loop's own
		// next deadline — the heartbeat repaint, the stats tick — so nothing
		// depends on it being short.
		v.events = v.be.WaitEvents(v.events[:0], v.idleTimeout(time.Now()))
	}
}

// RunConn drives the viewer against a single connection, for callers that do
// not supervise reconnection. See [SingleConn] for the lifetime rules.
func (v *Viewer) RunConn(ctx context.Context, conn transport.Conn) error {
	if conn == nil {
		return fmt.Errorf("viewer: nil connection")
	}
	return v.Run(ctx, SingleConn(conn))
}

// idleTimeout is how long the loop may block waiting for input before it has
// work to do anyway.
//
// It is deliberately computed from the deadlines the loop already keeps rather
// than being a constant: a constant that is short wastes the wait, and one
// that is long silently delays whichever timer it overshoots.
func (v *Viewer) idleTimeout(now time.Time) time.Duration {
	// Anything already in the inbox is work in hand. Checking it here rather
	// than trusting the wake is what makes the wake an optimisation instead of
	// a correctness requirement: a frame that lands between this iteration's
	// redraw and its wait would otherwise sit undrawn until the heartbeat.
	v.inbox.Lock()
	pending := v.inbox.needsPresent || v.inbox.hasNext || v.inbox.hasErr || v.inbox.hasCutText || len(v.inbox.audio) > 0
	v.inbox.Unlock()
	if pending {
		return 0
	}

	// presentDue is always set and never further away than
	// forcedPresentInterval, so it is the natural upper bound; every other
	// deadline can only pull it earlier.
	due := v.presentDue
	earlier := func(t time.Time) {
		if !t.IsZero() && t.Before(due) {
			due = t
		}
	}
	earlier(v.statsDue)
	if v.conn != nil {
		if v.cfg.ClipboardInterval >= 0 {
			earlier(v.clipDue)
		}
		if v.resizePending {
			earlier(v.resizeDue)
		}
	}
	if v.failed && v.cfg.FailureLinger > 0 {
		earlier(v.failedAt.Add(v.cfg.FailureLinger))
	}

	if d := due.Sub(now); d > 0 {
		return d
	}
	return 0
}

// wake nudges the render loop out of a blocking [Backend.WaitEvents].
//
// Every goroutine that writes to the inbox calls it, because the loop's
// timeout is measured in hundreds of milliseconds and a frame that arrived a
// moment after the wait began must not wait that long to be drawn. It is
// harmless — one dropped event — when called from the render loop itself,
// which is why the writers do not have to know which goroutine they are on.
func (v *Viewer) wake() { v.be.Wake() }

// start opens the window. It is separate from Run so that tests can drive the
// loop a step at a time with a controlled clock.
func (v *Viewer) start(now time.Time) error {
	winW, winH := v.initialSize(0, 0)
	opts := WindowOptions{
		Title:        v.cfg.Title,
		Width:        winW,
		Height:       winH,
		Fullscreen:   v.cfg.Fullscreen,
		ScaleQuality: v.cfg.ScaleQuality,
		VSync:        !v.cfg.NoVSync,
	}
	if err := v.be.Open(opts); err != nil {
		return fmt.Errorf("viewer: open window: %w", err)
	}
	v.winW, v.winH = v.be.Size()
	// A caller that pinned a size gets it; nothing later resizes the window
	// out from under them.
	v.fitted = v.cfg.Width > 0 && v.cfg.Height > 0

	v.inbox.Lock()
	v.inbox.status = StatusConnecting
	v.inbox.needsPresent = true
	v.inbox.Unlock()

	v.statsDue = now.Add(v.cfg.StatsInterval)
	v.lastStatsAt = now
	v.clipDue = now.Add(v.cfg.ClipboardInterval)
	v.presentDue = now.Add(forcedPresentInterval)
	return nil
}

// stop leaves the guest in a sane state and tears the window down.
func (v *Viewer) stop() {
	// Order matters: tell the guest every key is up while the connection is
	// still open, then drop the window.
	v.releaseInput()
	// The backend's own Close destroys any audio device it opened; closing the
	// sink here instead keeps playback from persisting across backends that
	// do not fold it into Close.
	if v.audioOpened && v.audioSink != nil {
		v.audioSink.CloseAudio()
	}
	v.closeAudio()
	v.be.Close()
}

// initialSize picks the window size, preferring the guest's own resolution but
// refusing to open a window larger than most desktops can show. A zero guest
// size means the window is opening before any connection.
func (v *Viewer) initialSize(fbW, fbH int) (int, int) {
	w, h := v.cfg.Width, v.cfg.Height
	if w <= 0 || h <= 0 {
		w, h = fbW, fbH
	}
	if w <= 0 || h <= 0 {
		w, h = fallbackWidth, fallbackHeight
	}
	if w > v.cfg.MaxInitialWidth || h > v.cfg.MaxInitialHeight {
		fit := FitLetterbox(w, h, v.cfg.MaxInitialWidth, v.cfg.MaxInitialHeight)
		w, h = fit.W, fit.H
	}
	return w, h
}

// step runs one iteration of the render loop. It is separate from Run so that
// tests can drive the loop with a controlled clock instead of racing it.
func (v *Viewer) step(now time.Time) error {
	v.syncConn(now)

	// v.events arrives holding whatever the wait at the bottom of [Viewer.Run]
	// harvested — that wait consumes events, it does not merely observe them —
	// and PollEvents appends anything that has landed since. The buffer is
	// handed back to the field empty before any of it is handled, so an early
	// return cannot leave an event to be handled twice.
	events := v.be.PollEvents(v.events)
	v.events = events[:0]
	for _, ev := range events {
		if err := v.handleEvent(now, ev); err != nil {
			return err
		}
	}
	if v.quit {
		return nil
	}

	if v.conn != nil {
		// Pointer motion is coalesced to at most one message per iteration: a
		// 1000Hz mouse would otherwise put 1000 messages a second on a link
		// whose whole point is to carry pixels.
		if err := v.flushPointer(false); err != nil {
			return err
		}
		if err := v.applyGuestResize(now); err != nil {
			return err
		}
		if err := v.syncAudioEnable(); err != nil {
			return err
		}
		v.syncAudio()
		if err := v.syncClipboard(now); err != nil {
			return err
		}
	}
	if err := v.redraw(now); err != nil {
		return err
	}
	if err := v.updateTitle(now); err != nil {
		return err
	}
	v.checkFailure(now)
	return nil
}

// syncConn installs a connection the pump has taken out, and notices when the
// current one has ended.
func (v *Viewer) syncConn(now time.Time) {
	if v.conn != nil && v.connCtx != nil && v.connCtx.Err() != nil {
		v.dropConn(nil)
	}

	v.inbox.Lock()
	next, nextCtx, hasNext := v.inbox.nextConn, v.inbox.nextCtx, v.inbox.hasNext
	v.inbox.nextConn, v.inbox.nextCtx, v.inbox.hasNext = nil, nil, false
	srcErr, hasErr := v.inbox.srcErr, v.inbox.hasErr
	v.inbox.hasErr = false
	v.inbox.Unlock()

	if hasNext {
		v.attach(next, nextCtx, now)
	}
	if hasErr && !v.failed {
		v.failed, v.srcErr, v.failedAt = true, srcErr, now
		v.dropConn(srcErr)
		v.SetStatus(StatusFailed, errText(srcErr))
	}
}

// attach installs a new connection under the existing window.
func (v *Viewer) attach(conn transport.Conn, connCtx context.Context, now time.Time) {
	if v.conn != nil {
		v.dropConn(nil)
	}
	v.conn, v.connCtx = conn, connCtx
	v.fresh = true
	v.sentAny = false
	v.generation++
	v.forgetInput()

	fbW, fbH := conn.Size()
	v.fitWindow(fbW, fbH)

	// A guest that has been rebooted, or a QEMU process that has been
	// restarted, comes back at its default resolution with no memory of the
	// size this client asked for. On any connection but the first, ask again
	// when the two disagree — through the normal debounce, so that a caller
	// who disabled guest resizing still gets nothing.
	if v.generation > 1 && (fbW != v.winW || fbH != v.winH) {
		v.scheduleGuestResize(now, v.winW, v.winH)
	}

	// Everything still in the inbox describes the previous connection's
	// framebuffer, down to its dimensions, and is meaningless against this
	// one. That includes gotUpdate: the cost of clearing it is that an update
	// this connection decoded before the render loop got here is not counted,
	// so the frame stays frozen for one more update interval, and the benefit
	// is that a leftover flag from the old connection can never unfreeze the
	// screen onto a framebuffer that is still all zeroes.
	v.inbox.Lock()
	v.inbox.damage = v.inbox.damage[:0]
	v.inbox.gotUpdate = false
	v.inbox.resized = false
	v.inbox.hasCutText, v.inbox.cutText = false, ""
	v.inbox.audio = nil
	v.inbox.fullRepaint = true
	v.inbox.needsPresent = true
	v.inbox.Unlock()

	// Audio, like everything else, belongs to one connection: capture the
	// format it negotiated and let the enable step below open the device once
	// the ack lands.
	v.audioOpened, v.audioEnabled = false, false
	v.audioFmt = nil
	if v.audioSink != nil {
		if fm, ok := conn.AudioFormat(); ok {
			v.audioFmt = &fm
		}
	}

	v.SetStatus(StatusLive, "")
	v.clipDue = now.Add(v.cfg.ClipboardInterval)
	v.logf("attached to a new connection (%dx%d)", fbW, fbH)

	// The texture holds the previous connection's last frame, or nothing at
	// all; either way this connection has to send a complete one. The session
	// supervisor asks too, and a duplicate request costs one frame.
	if err := v.conn.RequestUpdate(false); err != nil {
		v.dropConn(err)
	}
}

// dropConn detaches the current connection, leaving the last frame on screen.
//
// A dead connection is not a dead session: the supervisor will bring another
// one, and the window has to stay up in the meantime — with the last frame
// still showing, because a user watching their desktop go black assumes they
// have lost their work, while a user watching it freeze under "Reconnecting…"
// knows exactly what is happening.
func (v *Viewer) dropConn(cause error) {
	if v.conn == nil {
		return
	}
	if cause != nil {
		v.logf("connection ended: %v", cause)
	}
	v.conn, v.connCtx = nil, nil
	v.fresh = false
	v.forgetInput()
	v.closeAudio()
	v.setStatusIfLive(StatusReconnecting, "")
	v.markPresent()
}

// closeAudio tears down the current connection's audio: the device closes and
// anything still queued in the inbox is dropped, since it belongs to a guest
// that is no longer there.
func (v *Viewer) closeAudio() {
	if v.audioOpened && v.audioSink != nil {
		v.audioSink.CloseAudio()
	}
	v.audioOpened, v.audioEnabled = false, false
	v.audioFmt = nil
	v.inbox.Lock()
	v.inbox.audio = nil
	v.inbox.Unlock()
}

// connWrite handles the failure of a write to the guest.
//
// Writing to a connection that has just dropped is expected, not exceptional,
// so it ends the connection rather than the session: returning the error would
// take the window down with the link.
func (v *Viewer) connWrite(err error) error {
	if err != nil {
		v.dropConn(err)
	}
	return nil
}

// fitWindow sizes the window to the first guest that connects.
func (v *Viewer) fitWindow(fbW, fbH int) {
	if v.fitted || fbW <= 0 || fbH <= 0 {
		return
	}
	v.fitted = true
	if v.be.Fullscreen() {
		return
	}
	w, h := v.initialSize(fbW, fbH)
	if w == v.winW && h == v.winH {
		return
	}
	if err := v.be.SetSize(w, h); err != nil {
		// A window manager that refuses a resize is not a reason to end a
		// session; the guest is scaled into whatever size the window is.
		v.logf("resize window to %dx%d: %v", w, h, err)
		return
	}
	v.winW, v.winH = v.be.Size()
	v.updatePresent()
}

// checkFailure ends the loop once a terminal failure has been on screen long
// enough to read.
func (v *Viewer) checkFailure(now time.Time) {
	if !v.failed {
		return
	}
	if v.cfg.FailureLinger < 0 || !now.Before(v.failedAt.Add(v.cfg.FailureLinger)) {
		v.quit = true
	}
}

func (v *Viewer) handleEvent(now time.Time, ev Event) error {
	switch e := ev.(type) {
	case EventQuit:
		v.quit = true
		return nil

	case EventResize:
		v.winW, v.winH = e.W, e.H
		v.updatePresent()
		v.markPresent()
		// The request is scheduled even with no connection to send it on: a
		// window resized during an outage would otherwise stay letterboxed
		// after the reconnect, until the user happened to resize it again.
		v.scheduleGuestResize(now, e.W, e.H)
		return nil

	case EventFocus:
		if !e.Gained {
			// The host window manager keeps the key-up events that arrive
			// after focus moves away, so without this the guest is left
			// believing Ctrl (or Alt, or any held key) is still down, and
			// every later keystroke arrives as a shortcut.
			v.releaseInput()
		}
		v.markPresent()
		return nil

	case EventClipboard:
		v.clipboardHint = true
		return nil

	case EventKey:
		return v.handleKey(e)

	case EventText:
		// Deliberately dropped. RFB has no "insert this text" message: the
		// guest is driven by one keysym per physical key transition, which
		// [Viewer.handleKey] already sends, and it runs its own layout and
		// input method over that stream. Forwarding composed text as well
		// would type everything twice. Text events exist for the local
		// widgets in internal/ui, which is the other consumer of this
		// backend.
		return nil

	case EventPointer:
		return v.handlePointer(e)

	case EventWheel:
		return v.handleWheel(e)
	}
	return nil
}

// handleKey translates one key event and forwards it, unless it is a host
// hotkey.
//
// Hotkeys are handled whether or not a connection is live: the fullscreen and
// quit bindings belong to the window, and a user staring at a "Reconnecting…"
// overlay must be able to leave without reaching for the terminal.
func (v *Viewer) handleKey(e EventKey) error {
	id := hotkeyID{key: e.Key, r: e.Rune}

	// A hotkey's release must be swallowed too, even though the modifiers may
	// already be gone by then: forwarding a lone key-up for a press the guest
	// never saw is how stray characters appear in the remote session.
	if !e.Down {
		if v.swallow[id] {
			delete(v.swallow, id)
			return nil
		}
	} else if v.isHotkey(e) {
		v.swallow[id] = true
		// Auto-repeat must not re-fire the action: holding F11 down would
		// otherwise toggle fullscreen dozens of times a second.
		if e.Repeat {
			return nil
		}
		return v.runHotkey(e)
	}

	// Enter while the display-in-use overlay is up is a take-over, not a
	// key press for a guest that isn't there.
	if e.Down && !e.Repeat && e.Key == keysym.KeyReturn {
		if h := v.takeoverAction(); h != nil {
			if status, _ := v.Status(); status == StatusDisplayInUse {
				go func() {
					if err := h(); err != nil {
						v.SetStatus(StatusDisplayInUse, "Take over failed: "+err.Error())
					}
				}()
				return nil
			}
		}
	}

	if v.readOnly() || v.conn == nil {
		return nil
	}
	sym := symbolFor(e)
	if sym == keysym.NoSymbol {
		return nil
	}
	if e.Down {
		v.noteHeld(sym)
	} else {
		v.forgetHeld(sym)
	}
	v.mods.Track(sym, e.Down)
	return v.connWrite(v.conn.KeyEvent(uint32(sym), e.Down))
}

// symbolFor resolves an event to the keysym to put on the wire. Named keys win
// over characters so that the keypad stays distinguishable: a guest treats
// KP_Enter and Return, or KP_1 and End, as different keys.
func symbolFor(e EventKey) keysym.Keysym {
	if e.Key != keysym.KeyUnknown {
		return e.Key.Keysym()
	}
	if e.Rune != 0 {
		return keysym.FromRune(e.Rune)
	}
	return keysym.NoSymbol
}

func (v *Viewer) isHotkey(e EventKey) bool {
	if e.Key == v.cfg.FullscreenKey && e.Key != keysym.KeyUnknown {
		return true
	}
	const chordMods = keysym.ModControl | keysym.ModAlt
	if !e.Mods.Has(chordMods) {
		return false
	}
	if e.Key != keysym.KeyUnknown && e.Key == v.cfg.SendCtrlAltDelKey {
		return true
	}
	// Ctrl+Alt+Del itself is caught where the host lets it through (X11 and
	// most Wayland compositors do; Windows never will), so that the obvious
	// combination works as well as the portable one.
	if e.Key == keysym.KeyDelete {
		return true
	}
	if e.Rune == 0 {
		return false
	}
	if v.cfg.ControlRune != 0 && lowerRune(e.Rune) == lowerRune(v.cfg.ControlRune) {
		return true
	}
	return lowerRune(e.Rune) == lowerRune(v.cfg.QuitRune)
}

func (v *Viewer) runHotkey(e EventKey) error {
	switch {
	case e.Key == v.cfg.FullscreenKey && e.Key != keysym.KeyUnknown:
		return v.toggleFullscreen()

	case e.Key == v.cfg.SendCtrlAltDelKey || e.Key == keysym.KeyDelete:
		if v.readOnly() {
			return nil
		}
		if v.conn == nil {
			return nil
		}

		return v.sendChord(keysym.ChordCtrlAltDel)

	case e.Rune != 0 && v.cfg.ControlRune != 0 && lowerRune(e.Rune) == lowerRune(v.cfg.ControlRune):
		if h := v.controlAction(); h != nil {
			go h()
		}
		return nil

	default:
		v.quit = true
		return nil
	}
}

func (v *Viewer) toggleFullscreen() error {
	if err := v.be.SetFullscreen(!v.be.Fullscreen()); err != nil {
		return fmt.Errorf("viewer: toggle fullscreen: %w", err)
	}
	v.winW, v.winH = v.be.Size()
	v.updatePresent()
	v.markPresent()
	return nil
}

// sendChord injects a synthetic key sequence, such as Ctrl-Alt-Del, that the
// host would otherwise intercept.
func (v *Viewer) sendChord(c keysym.Chord) error {
	for _, a := range c.Sequence() {
		if err := v.conn.KeyEvent(uint32(a.Sym), a.Down); err != nil {
			return v.connWrite(err)
		}
		// The chord's own releases tell the guest those modifiers are up, so
		// the tracker must forget them or it would later send a second
		// release for a key the guest already considers released.
		v.mods.TrackAction(a)
	}
	return nil
}

func (v *Viewer) handlePointer(e EventPointer) error {
	if v.readOnly() || v.conn == nil {
		return nil
	}
	srcW, srcH := v.sourceSize()
	x, y, _ := MapToSource(e.X, e.Y, v.present, srcW, srcH)
	v.ptrX, v.ptrY = x, y
	v.ptrKnown = true

	if e.Buttons != v.buttons {
		v.buttons = e.Buttons
		// A button transition is ordered with respect to motion, so it goes
		// out immediately rather than waiting for the frame boundary.
		return v.flushPointer(true)
	}
	return nil
}

func (v *Viewer) handleWheel(e EventWheel) error {
	if v.readOnly() || v.conn == nil || !v.ptrKnown {
		return nil
	}
	// The wheel is reported at the current pointer position, so make sure the
	// guest agrees where that is before the clicks arrive.
	if err := v.flushPointer(false); err != nil {
		return err
	}
	if v.conn == nil {
		return nil
	}
	base := rfbMask(v.buttons)
	for _, bit := range wheelBits(e.DX, e.DY) {
		// RFB has no wheel axis: a click is a press of one of the wheel bits
		// followed immediately by its release.
		if err := v.conn.PointerEvent(uint16(v.ptrX), uint16(v.ptrY), base|bit); err != nil {
			return v.connWrite(err)
		}
		if err := v.conn.PointerEvent(uint16(v.ptrX), uint16(v.ptrY), base); err != nil {
			return v.connWrite(err)
		}
	}
	return nil
}

// wheelBits expands a wheel delta into one button mask bit per click, vertical
// axis first. Positive dy is away from the user (up) and positive dx is right.
func wheelBits(dx, dy int) []rfb.ButtonMask {
	bits := make([]rfb.ButtonMask, 0, 4)
	add := func(n int, bit rfb.ButtonMask) {
		if n < 0 {
			n = -n
		}
		if n > maxWheelTicks {
			n = maxWheelTicks
		}
		for i := 0; i < n; i++ {
			bits = append(bits, bit)
		}
	}
	switch {
	case dy > 0:
		add(dy, rfb.ButtonWheelUp)
	case dy < 0:
		add(dy, rfb.ButtonWheelDown)
	}
	switch {
	case dx > 0:
		add(dx, rfb.ButtonWheelRight)
	case dx < 0:
		add(dx, rfb.ButtonWheelLeft)
	}
	return bits
}

// rfbMask converts backend buttons to the protocol's mask.
func rfbMask(b Buttons) rfb.ButtonMask {
	var m rfb.ButtonMask
	if b.Has(ButtonLeft) {
		m |= rfb.ButtonLeft
	}
	if b.Has(ButtonMiddle) {
		m |= rfb.ButtonMiddle
	}
	if b.Has(ButtonRight) {
		m |= rfb.ButtonRight
	}
	return m
}

func (v *Viewer) flushPointer(force bool) error {
	if v.conn == nil || !v.ptrKnown {
		return nil
	}
	mask := rfbMask(v.buttons)
	unchanged := v.sentAny && v.sentX == v.ptrX && v.sentY == v.ptrY && v.sentMask == mask
	if unchanged && !force {
		return nil
	}
	if err := v.conn.PointerEvent(uint16(v.ptrX), uint16(v.ptrY), mask); err != nil {
		return v.connWrite(err)
	}
	v.sentX, v.sentY, v.sentMask, v.sentAny = v.ptrX, v.ptrY, mask, true
	return nil
}

// noteHeld and forgetHeld track non-modifier keys so that focus loss can
// release them too. keysym.Tracker deliberately covers only modifiers; a stuck
// letter key is rarer but just as destructive, because the guest's auto-repeat
// will happily type it forever.
func (v *Viewer) noteHeld(sym keysym.Keysym) {
	if sym.IsModifier() {
		return
	}
	for _, s := range v.held {
		if s == sym {
			return
		}
	}
	v.held = append(v.held, sym)
}

func (v *Viewer) forgetHeld(sym keysym.Keysym) {
	for i, s := range v.held {
		if s == sym {
			v.held = append(v.held[:i], v.held[i+1:]...)
			return
		}
	}
}

// releaseInput tells the guest that every key and button this client reported
// as pressed is now released. Errors are ignored: it runs on focus loss and on
// shutdown, when a dead connection is expected and there is nothing useful to
// do about it.
func (v *Viewer) releaseInput() {
	if v.conn == nil {
		v.forgetInput()
		return
	}
	for i := len(v.held) - 1; i >= 0; i-- {
		_ = v.conn.KeyEvent(uint32(v.held[i]), false)
	}
	v.held = v.held[:0]

	for _, a := range v.mods.ReleaseAll() {
		_ = v.conn.KeyEvent(uint32(a.Sym), a.Down)
	}

	if v.buttons != 0 {
		v.buttons = 0
		if v.ptrKnown {
			_ = v.conn.PointerEvent(uint16(v.ptrX), uint16(v.ptrY), 0)
			v.sentMask = 0
		}
	}
	v.swallow = make(map[hotkeyID]bool)
}

// forgetInput discards the local record of what is held without telling
// anybody.
//
// It is what a connection change needs: the old connection cannot be told and
// the new one never knew, so carrying the state across would leave the fresh
// guest holding modifiers the user released during the outage.
func (v *Viewer) forgetInput() {
	v.held = v.held[:0]
	v.mods.Reset()
	v.buttons = 0
	v.sentMask = 0
	v.swallow = make(map[hotkeyID]bool)
}

// scheduleGuestResize records that the guest should eventually be asked to
// match the window, pushing the deadline out on every event so that the
// request is sent once the user stops dragging.
func (v *Viewer) scheduleGuestResize(now time.Time, w, h int) {
	// A view-only participant must not even ask: the resize is a guest
	// mutation, and the window still scales the picture locally.
	if v.readOnly() {
		return
	}
	// Odd sizes are rounded down: virtio-gpu's EDID modes and QEMU's surface
	// allocation are both happier with even dimensions, and one pixel is not
	// worth the risk of a rejected mode.
	w, h = w&^1, h&^1
	if w <= 0 || h <= 0 {
		return
	}
	v.resizeW, v.resizeH = w, h
	v.resizeDue = now.Add(v.cfg.ResizeDebounce)
	v.resizePending = true
}

func (v *Viewer) applyGuestResize(now time.Time) error {
	if !v.resizePending || now.Before(v.resizeDue) {
		return nil
	}
	v.resizePending = false

	fbW, fbH := v.conn.Size()
	if fbW == v.resizeW && fbH == v.resizeH {
		return nil
	}
	v.logf("resizing guest display to %dx%d", v.resizeW, v.resizeH)
	return v.connWrite(v.conn.SetDesktopSize(uint16(v.resizeW), uint16(v.resizeH)))
}

// syncAudioEnable opens the audio device and asks the guest to start
// streaming, exactly once per connection and only after the guest has
// acknowledged the audio encoding.
//
// Nothing here fails the session: a missing device, an unsupported format or a
// connection that cannot stream audio merely drops audio for that connection.
func (v *Viewer) syncAudioEnable() error {
	if v.audioEnabled || v.audioFmt == nil || v.audioSink == nil {
		return nil
	}
	if !v.conn.AudioOK() {
		// QEMU ignores — and under some versions kills — a connection that
		// enables audio before the encoding was acknowledged. Wait.
		return nil
	}
	sinkFmt := AudioFormat{
		Channels:       int32(v.audioFmt.Channels),
		SampleRate:     int32(v.audioFmt.SamplesPerSec),
		BytesPerSample: int32(v.audioFmt.BytesPerSample()),
		LittleEndian:   v.audioFmt.LittleEndian,
	}
	if err := v.audioSink.OpenAudio(sinkFmt); err != nil {
		v.logf("audio disabled: %v", err)
		v.audioFmt = nil
		v.inbox.Lock()
		v.inbox.audio = nil
		v.inbox.Unlock()
		return nil
	}
	// A write that lands on a dead connection drops the connection, not the
	// session; the device closes with it.
	if err := v.connWrite(v.conn.SetAudioFormat(*v.audioFmt)); err != nil {
		return err
	}
	if err := v.connWrite(v.conn.EnableAudio()); err != nil {
		return err
	}
	v.audioOpened = true
	v.audioEnabled = true
	v.logf("audio on (%dch %dHz, %d bytes/sample)", v.audioFmt.Channels, v.audioFmt.SamplesPerSec, v.audioFmt.BytesPerSample())
	return nil
}

// syncAudio plays whatever audio has arrived since the last pass. It runs
// every iteration of the render loop while a connection is live, so playback
// latency is one frame, not one queue's worth.
func (v *Viewer) syncAudio() {
	if !v.audioOpened {
		return
	}
	v.inbox.Lock()
	audio := v.inbox.audio
	v.inbox.audio = nil
	v.inbox.Unlock()
	if len(audio) == 0 {
		return
	}
	v.audioSink.PlayPCM(audio)
}

func (v *Viewer) syncClipboard(now time.Time) error {
	v.inbox.Lock()
	text, has := v.inbox.cutText, v.inbox.hasCutText
	v.inbox.cutText, v.inbox.hasCutText = "", false
	v.inbox.Unlock()

	if has {
		if err := v.be.SetClipboard(text); err != nil {
			return fmt.Errorf("viewer: set host clipboard: %w", err)
		}
		// Remember what the guest sent so the poll below does not immediately
		// send it straight back and start a loop.
		v.hostClip = text
	}

	if v.cfg.ClipboardInterval < 0 || v.readOnly() {
		return nil
	}
	if !v.clipboardHint && now.Before(v.clipDue) {
		return nil
	}
	v.clipboardHint = false
	v.clipDue = now.Add(v.cfg.ClipboardInterval)

	text, err := v.be.Clipboard()
	if err != nil {
		// A clipboard that cannot be read is not a reason to end a session.
		v.logf("read host clipboard: %v", err)
		return nil
	}
	if text == "" || text == v.hostClip {
		return nil
	}
	v.hostClip = text
	return v.connWrite(v.conn.CutText(text))
}

// resizeTexture reallocates the framebuffer texture when the guest resolution
// changes, which a reconnect can do as easily as a mode switch can.
//
// It is only ever called with pixels in hand: reallocating leaves the texture
// undefined, so doing it speculatively — on attach, say — would destroy the
// frozen frame and leave a window of garbage until the new connection's first
// update arrived.
func (v *Viewer) resizeTexture(w, h int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("viewer: guest reported an empty framebuffer (%dx%d)", w, h)
	}
	if w == v.texW && h == v.texH {
		return nil
	}
	if err := v.be.SetTextureSize(w, h); err != nil {
		return fmt.Errorf("viewer: allocate %dx%d texture: %w", w, h, err)
	}
	v.texW, v.texH = w, h
	// The old contents are gone, so nothing may be presented from this texture
	// until something has been uploaded into it.
	v.haveFrame = false
	return nil
}

// redraw uploads whatever changed and presents the frame.
//
// When there is no connection — or the new one has not decoded a frame yet —
// it presents the texture as it stands, which is the last frame the guest
// sent, under the status overlay.
func (v *Viewer) redraw(now time.Time) error {
	v.inbox.Lock()
	// Decide whether to draw before taking the damage: returning early after
	// draining it would silently discard the damage and leave stale pixels.
	if !v.inbox.needsPresent && !now.After(v.presentDue) {
		v.inbox.Unlock()
		return nil
	}
	if v.fresh && v.inbox.gotUpdate {
		v.fresh = false
	}
	live := v.conn != nil && !v.fresh

	var (
		damage  []rfb.Rect
		full    bool
		resized bool
	)
	if live {
		damage = v.inbox.damage
		v.inbox.damage = make([]rfb.Rect, 0, cap(damage))
		full, resized = v.inbox.fullRepaint, v.inbox.resized
		v.inbox.fullRepaint, v.inbox.resized, v.inbox.gotUpdate = false, false, false
	}
	v.inbox.needsPresent = false
	v.inbox.Unlock()

	if live {
		if err := v.uploadFrame(damage, full); err != nil {
			return err
		}
	}

	v.updatePresent()
	overlay, err := v.buildOverlay()
	if err != nil {
		return err
	}
	if err := v.be.Present(v.present, overlay); err != nil {
		return fmt.Errorf("viewer: present: %w", err)
	}
	v.frames++
	v.presentDue = now.Add(forcedPresentInterval)

	if live && resized {
		// The server changed resolution, so the old contents are meaningless
		// and an incremental request would only describe changes to an image
		// we no longer have.
		if err := v.conn.RequestUpdate(false); err != nil {
			return v.connWrite(err)
		}
	}
	return nil
}

// uploadFrame copies the damaged parts of the live framebuffer into the
// texture.
func (v *Viewer) uploadFrame(damage []rfb.Rect, full bool) error {
	// Everything that depends on the framebuffer's size happens under its
	// lock. Reading the size separately would let the read loop resize the
	// framebuffer in between, leaving the texture and the pixels disagreeing
	// about how big the guest screen is.
	var innerErr error
	v.conn.WithFramebuffer(func(fb *rfb.Framebuffer) {
		fbW, fbH := fb.Width, fb.Height
		if innerErr = v.resizeTexture(fbW, fbH); innerErr != nil {
			return
		}
		if full || !v.haveFrame {
			damage = []rfb.Rect{{X: 0, Y: 0, Width: uint16(fbW), Height: uint16(fbH)}}
		}
		for _, r := range planUploads(damage, fbW, fbH, v.cfg.MaxUploadRects) {
			rect := Rect{X: int(r.X), Y: int(r.Y), W: int(r.Width), H: int(r.Height)}
			if err := v.be.Upload(rect, fb.Pix, fb.Stride); err != nil {
				innerErr = fmt.Errorf("viewer: upload %s: %w", rect, err)
				return
			}
		}
		v.frameW, v.frameH = fbW, fbH
		v.haveFrame = true
	})
	return innerErr
}

// buildOverlay rasterises and uploads the status plate when it has changed,
// and returns how this frame should be composited.
func (v *Viewer) buildOverlay() (Overlay, error) {
	ov, err := v.statusLayer.Build(v.be, v.overlayLines(), v.winW, v.winH)
	if err != nil {
		return Overlay{}, fmt.Errorf("viewer: %w", err)
	}
	return ov, nil
}

// overlayLines is the text the overlay should show right now, or nil for none.
func (v *Viewer) overlayLines() []string {
	status, detail := v.Status()
	if status == StatusLive {
		if v.haveFrame {
			return nil
		}
		// Connected, but nothing has been drawn yet: an empty black window
		// with no explanation looks like a broken client.
		status, detail = StatusConnecting, ""
	}
	lines := statusLines(status, detail)
	if status == StatusDisplayInUse && v.takeoverAction() != nil {
		lines = append(lines, "Press Enter to take over the display")
	}
	return lines
}

// sourceSize is the size of the image the presented rectangle is computed
// from: the texture's actual contents when there are any, and otherwise the
// size the guest has reported.
//
// Preferring the contents is what keeps a frozen frame honest across a
// reconnect at a different resolution — the last 1024x768 frame is presented
// as 4:3 even though the new connection says the guest is now 1920x1080.
func (v *Viewer) sourceSize() (int, int) {
	if v.haveFrame {
		return v.frameW, v.frameH
	}
	if v.conn != nil {
		return v.conn.Size()
	}
	return 0, 0
}

// updatePresent recomputes the letterboxed destination rectangle.
//
// It is kept current outside the draw path as well, because the pointer
// mapping reads it: a click that arrives in the same batch as a resize must
// not be mapped through the previous frame's geometry.
func (v *Viewer) updatePresent() {
	w, h := v.sourceSize()
	v.present = FitLetterbox(w, h, v.winW, v.winH)
}

func (v *Viewer) markPresent() {
	v.inbox.Lock()
	v.inbox.needsPresent = true
	v.inbox.Unlock()
}

// updateTitle refreshes the window title with the guest resolution and a
// throughput sample, which is the cheapest honest connection-quality
// indicator a client can offer. While there is no connection it carries the
// status instead, so the state is legible from a taskbar too.
func (v *Viewer) updateTitle(now time.Time) error {
	if now.Before(v.statsDue) {
		return nil
	}
	elapsed := now.Sub(v.lastStatsAt).Seconds()
	v.statsDue = now.Add(v.cfg.StatsInterval)
	v.lastStatsAt = now

	var fps float64
	if elapsed > 0 {
		fps = float64(v.frames) / elapsed
	}
	v.frames = 0

	base := v.titleBase()
	title := base
	if status, _ := v.Status(); v.conn == nil || status != StatusLive {
		if status == StatusLive {
			status = StatusConnecting
		}
		title += " — " + status.Text()
		// The byte counter belongs to a connection that is gone; restart the
		// sample rather than reporting a nonsensical rate on reconnect.
		v.lastBytes = 0
	} else {
		stats := v.conn.Stats()
		bytes := stats.BytesRead
		var kbits float64
		if elapsed > 0 && bytes >= v.lastBytes {
			kbits = float64(bytes-v.lastBytes) * 8 / 1000 / elapsed
		}
		v.lastBytes = bytes

		fbW, fbH := v.sourceSize()
		title = fmt.Sprintf("%s — %dx%d — %.0f fps · %s", base, fbW, fbH, fps, formatBitrate(kbits))
	}
	if title == v.title {
		return nil
	}
	v.title = title
	if err := v.be.SetTitle(title); err != nil {
		return fmt.Errorf("viewer: set title: %w", err)
	}
	return nil
}

func formatBitrate(kbits float64) string {
	if kbits >= 1000 {
		return fmt.Sprintf("%.1f Mbit/s", kbits/1000)
	}
	return fmt.Sprintf("%.0f kbit/s", kbits)
}

func (v *Viewer) logf(format string, args ...any) {
	if v.cfg.Logf != nil {
		v.cfg.Logf(format, args...)
	}
}

// errText renders an error for the overlay, naming the anonymous case rather
// than showing the user an empty reason.
func errText(err error) string {
	if err == nil {
		return "the connection was closed"
	}
	return err.Error()
}

func lowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}
