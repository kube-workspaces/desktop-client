// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"context"
	"fmt"
	"image"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

// Tier1Config configures the interactive Tier 1 presenter. The zero value is
// usable: every field has a documented default.
type Tier1Config struct {
	// Title is the initial window title, typically "namespace/workspace".
	Title string

	// Width and Height are the initial window size in pixels. Zero means
	// 1280x800 until the first frame reveals the guest, when the window is
	// fitted to it.
	Width, Height int

	// Fullscreen starts the session fullscreen.
	Fullscreen bool

	// ScaleQuality selects the texture filter. Empty means [ScaleLinear].
	ScaleQuality ScaleQuality

	// NoVSync disables presentation synchronisation (default: on).
	NoVSync bool

	// Audio plays decoded guest audio through the backend's [AudioSink]. A
	// backend without audio output disables audio; so does a device that
	// fails to open.
	Audio bool

	// ResizeDebounce is the quiet period before a window resize is forwarded
	// to the guest. Values below [DefaultResizeDebounce] are raised to it.
	ResizeDebounce time.Duration
	// NoResize keeps the guest resolution fixed and scales into the window.
	NoResize bool
	// Takeover requests ownership after explicit Enter consent on a busy
	// display. It must return immediately; networking belongs to the producer.
	Takeover func()

	// ClipboardInterval is how often the host clipboard is sampled. Zero
	// means [DefaultClipboardInterval]; a negative value disables
	// host-to-guest clipboard sharing (guest-to-host still works).
	ClipboardInterval time.Duration

	// FullscreenKey toggles fullscreen with no modifier. Zero means F11.
	FullscreenKey keysym.Key

	// QuitRune ends the session when pressed with Ctrl+Alt. Zero means 'q'.
	QuitRune rune

	// MaxUploadRects bounds how many sub-rectangles one frame may upload.
	// Tier 1 carries whole frames, so this is inert; kept for symmetry.
	MaxUploadRects int

	// Logf, if set, receives diagnostic messages.
	Logf func(format string, args ...any)
}

func (c *Tier1Config) applyDefaults() {
	if c.ScaleQuality == "" {
		c.ScaleQuality = ScaleLinear
	}
	if c.ResizeDebounce < DefaultResizeDebounce {
		c.ResizeDebounce = DefaultResizeDebounce
	}
	if c.ClipboardInterval == 0 {
		c.ClipboardInterval = DefaultClipboardInterval
	}
	if c.FullscreenKey == keysym.KeyUnknown {
		c.FullscreenKey = keysym.KeyF11
	}
	if c.QuitRune == 0 {
		c.QuitRune = 'q'
	}
}

func (c *Tier1Config) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// Tier1Input is the guest-facing control surface an interactive Tier 1
// presenter drives.
//
// It is a deliberate subset of the Selkies Control adapter, so the renderer
// knows only the events a window can produce. Implementations must be safe for
// use from the renderer's single goroutine; the wire encoding happens inside
// them.
type Tier1Input interface {
	// Key sends one keysym press or release.
	Key(sym keysym.Keysym, down bool) error
	// Pointer sends an absolute pointer position and held buttons.
	Pointer(x, y int, mask rfb.ButtonMask) error
	// Wheel sends scroll notches in the input's convention (positive dy
	// scrolls down, per the browser delta convention).
	Wheel(dx, dy int) error
	// Resize asks the guest to follow a window size.
	Resize(w, h int) error
	// SetClipboard pushes text to the guest clipboard.
	SetClipboard(text string) error
	// ResetKeys releases every key the guest believes is held.
	ResetKeys() error
}

// Tier1Sink is the bounded handoff a produce worker pushes decoded media and
// server clipboard pushes into. Only the producer writes it; only the render
// loop reads it, and the two sides are synchronised inside.
type Tier1Sink struct {
	frames       MediaFrames
	reconnecting atomic.Bool
	busy         atomic.Bool
	epoch        atomic.Uint64

	clipMu       sync.Mutex
	guestClip    string
	hasGuestClip bool
	// droppedClipboard counts pushes overwritten before the render loop could
	// apply them, so diagnostics can tell congestion from loss.
	droppedClipboard uint64

	// wake nudges the render loop that may be parked in a blocking event
	// wait. It is set once the backend is open and is nil-safe.
	wake func()
}

// Video hands one decoded frame to the presenter (whole-frame; Tier 1 has no
// damage rectangles).
func (s *Tier1Sink) Video(frame *image.RGBA) {
	s.frames.Video(frame)
	s.reconnecting.Store(false)
	s.wakeUp()
}

// DisplayBusy shows the ownership-consent plate while the producer waits for
// the display slot. It does not itself revoke or claim ownership.
func (s *Tier1Sink) DisplayBusy(busy bool) {
	s.busy.Store(busy)
	s.wakeUp()
}

// Reconnecting preserves the displayed texture but drops queued media/input
// state at the generation boundary. Only the producer calls this, after the
// previous session and its decoder workers have stopped.
func (s *Tier1Sink) Reconnecting() {
	s.frames.take()
	s.clipMu.Lock()
	s.guestClip, s.hasGuestClip = "", false
	s.clipMu.Unlock()
	s.reconnecting.Store(true)
	s.epoch.Add(1)
	s.wakeUp()
}

// Audio hands one batch of decoded PCM (stereo s16le 48 kHz) to the presenter.
func (s *Tier1Sink) Audio(pcm []byte) {
	s.frames.Audio(pcm)
	s.wakeUp()
}

// GuestClipboard queues a server clipboard push (guest-to-host). The render
// loop drains it on the window thread, the only thread allowed to touch the
// host clipboard.
func (s *Tier1Sink) GuestClipboard(text string) {
	s.clipMu.Lock()
	if s.hasGuestClip {
		s.droppedClipboard++
	}
	s.guestClip, s.hasGuestClip = text, true
	s.clipMu.Unlock()
	s.wakeUp()
}

// takeClipboard drains the latest guest clipboard push, or ("", false) if
// there is none. It is called from the render loop only.
func (s *Tier1Sink) takeClipboard() (string, bool) {
	s.clipMu.Lock()
	defer s.clipMu.Unlock()
	text, has := s.guestClip, s.hasGuestClip
	s.guestClip, s.hasGuestClip = "", false
	return text, has
}

func (s *Tier1Sink) wakeUp() {
	if s.wake != nil {
		s.wake()
	}
}

// RunTier1 presents a live interactive Tier 1 (Selkies) desktop in a window.
//
// The window, renderer and texture belong to this function for its whole
// lifetime; the transport lives behind produce, which runs on its own
// goroutine and must stop promptly when its context is cancelled. That is the
// same asymmetry as the RFB [Viewer]: the transport fails, reconnects or is
// torn down without the window flickering.
//
// The caller supplies the transport through the produce closure and hands the
// sink to whichever session implementation drives it:
//
//	RunTier1(ctx, be, sess.Control(), func(ctx context.Context, s *viewer.Tier1Sink) error {
//	    return sess.Run(ctx) // sess wired with Sink{Video: s.Video, Audio: s.Audio, ...}
//	}, opts)
//
// inp forwards this window's events to the guest. All [Backend] calls happen
// on the calling goroutine, which must own the OS thread as described on
// [Backend]; produced media arrives via the sink and is joined before the
// window is closed, including on quit or error. A returned error means the
// transport failed; nil means the user quit or the window closed.
func RunTier1(ctx context.Context, be Backend, inp Tier1Input,
	produce func(context.Context, *Tier1Sink) error, opts Tier1Config) error {

	opts.applyDefaults()
	if inp == nil {
		return fmt.Errorf("viewer: nil tier-1 input")
	}
	if produce == nil {
		return fmt.Errorf("viewer: nil tier-1 producer")
	}

	w := &tier1Window{be: be, opts: opts, inp: inp}
	winW, winH := w.initialSize(0, 0)
	if err := be.Open(WindowOptions{
		Title: opts.Title, Width: winW, Height: winH,
		Fullscreen: opts.Fullscreen, ScaleQuality: opts.ScaleQuality, VSync: !opts.NoVSync,
	}); err != nil {
		return fmt.Errorf("viewer: open tier-1 window: %w", err)
	}
	defer be.Close()
	w.winW, w.winH = be.Size()
	w.pinned = opts.Width > 0 && opts.Height > 0

	ctx, cancel := context.WithCancel(ctx)
	w.sink = &Tier1Sink{wake: be.Wake}
	done := make(chan error, 1)
	go func() { done <- produce(ctx, w.sink) }()
	joined := false
	end := func(err error) error {
		if !joined {
			cancel()
			<-done
			joined = true
		}
		_ = inp.ResetKeys()
		return err
	}
	defer func() {
		// If the loop already joined, this is a no-op.
		cancel()
		if !joined {
			<-done
		}
	}()

	stopWake := context.AfterFunc(ctx, be.Wake)
	defer stopWake()

	// Audio: the Opus side always emits two-channel little-endian s16 48 kHz,
	// so the device is opened once with that layout. A missing device merely
	// silences the session.
	if opts.Audio {
		if a, ok := be.(AudioSink); ok {
			if err := a.OpenAudio(AudioFormat{Channels: 2, SampleRate: 48000, BytesPerSample: 2, LittleEndian: true}); err != nil {
				opts.logf("audio disabled: %v", err)
			} else {
				w.audio = a
				defer w.audio.CloseAudio()
			}
		}
	}

	now := time.Now()
	w.clipDue = now.Add(opts.ClipboardInterval)
	w.lastPresent = now

	for {
		if w.quit || ctx.Err() != nil {
			// Quit and cancellation are both ordinary ends; a transport
			// failure is the only error this presentation reports.
			return end(nil)
		}

		// A transport that has already ended surfaces before the next wait, so
		// a dead link is reported at once.
		select {
		case err := <-done:
			joined = true
			if err == nil {
				err = context.Canceled
			}
			return end(err)
		default:
		}

		w.syncGeneration()
		w.events = be.PollEvents(w.events)
		for _, ev := range w.events {
			if err := w.handleEvent(time.Now(), ev); err != nil {
				return end(err)
			}
		}
		w.events = w.events[:0]
		if w.quit {
			continue
		}

		if err := w.step(time.Now()); err != nil {
			return end(err)
		}

		w.events = be.WaitEvents(w.events, w.idleTimeout(time.Now()))
	}
}

// tier1Window owns the render-loop state of an interactive Tier 1 session.
type tier1Window struct {
	be   Backend
	opts Tier1Config
	inp  Tier1Input
	sink *Tier1Sink

	audio AudioSink

	events  []Event
	pinned  bool
	quit    bool
	mods    keysym.Tracker
	held    []keysym.Keysym
	swallow map[hotkeyID]bool

	buttons    Buttons
	ptrX, ptrY int
	ptrKnown   bool
	sentX      int
	sentY      int
	sentMask   rfb.ButtonMask
	sentAny    bool

	present    Rect // last presented rectangle, in surface pixels
	texW, texH int
	haveFrame  bool
	winW, winH int

	resizeW, resizeH int
	resizeDue        time.Time
	resizePending    bool

	hostClip string
	clipDue  time.Time
	clipHint bool

	ovKey    overlayKey
	ovW, ovH int
	ovPix    []byte
	ovStride int

	lastPresent time.Time
	epoch       uint64
}

func (w *tier1Window) syncGeneration() {
	if epoch := w.sink.epoch.Load(); epoch != w.epoch {
		w.epoch = epoch
		w.mods = keysym.Tracker{}
		w.held = nil
		w.buttons = 0
		w.sentMask, w.sentAny = 0, false
		w.scheduleGuestResize(time.Now(), w.winW, w.winH)
		w.hostClip, w.clipHint = "", false
		// Clear any PCM already queued in the output device, not just the
		// producer queue. All device calls remain on the window thread.
		if w.audio != nil {
			w.audio.CloseAudio()
			if err := w.audio.OpenAudio(AudioFormat{Channels: 2, SampleRate: 48000, BytesPerSample: 2, LittleEndian: true}); err != nil {
				w.opts.logf("audio disabled after reconnect: %v", err)
				w.audio = nil
			}
		}
	}
}

func (w *tier1Window) initialSize(fbW, fbH int) (int, int) {
	width, height := w.opts.Width, w.opts.Height
	if width <= 0 || height <= 0 {
		width, height = fbW, fbH
	}
	if width <= 0 || height <= 0 {
		width, height = fallbackWidth, fallbackHeight
	}
	if width > 1920 || height > 1080 {
		fit := FitLetterbox(width, height, 1920, 1080)
		width, height = fit.W, fit.H
	}
	return width, height
}

// step runs one iteration of the render loop.
func (w *tier1Window) step(now time.Time) error {
	// Pointer motion is coalesced to one message per iteration: a high
	// frequency mouse would otherwise drown a link whose whole point is to
	// carry pixels.
	if err := w.flushPointer(false); err != nil {
		return err
	}
	if err := w.applyGuestResize(now); err != nil {
		return err
	}
	if err := w.syncGuestClipboard(now); err != nil {
		return err
	}
	return w.presentFrame(now)
}

// idleTimeout is how long the loop may block waiting for events before it has
// work to do anyway.
func (w *tier1Window) idleTimeout(now time.Time) time.Duration {
	if w.sinkHasWork() {
		return 0
	}
	due := w.lastPresent.Add(forcedPresentInterval)
	ready := w.haveFrame && (w.sink == nil || !w.sink.reconnecting.Load())
	if ready && w.opts.ClipboardInterval >= 0 {
		if due.IsZero() || w.clipDue.Before(due) {
			due = w.clipDue
		}
	}
	if ready && w.resizePending && w.resizeDue.Before(due) {
		due = w.resizeDue
	}
	if d := due.Sub(now); d > 0 {
		return d
	}
	return 0
}

func (w *tier1Window) sinkHasWork() bool {
	return w.sink != nil && w.sink.frames.pending()
}

func (w *tier1Window) handleEvent(now time.Time, ev Event) error {
	switch e := ev.(type) {
	case EventQuit:
		w.quit = true
		return nil

	case EventResize:
		if e.W <= 0 || e.H <= 0 {
			return nil
		}
		w.winW, w.winH = e.W, e.H
		// The request is scheduled even while no transport is live: a window
		// resized during connect must still reach the newly attached guest.
		w.scheduleGuestResize(now, e.W, e.H)
		return nil

	case EventFocus:
		if !e.Gained {
			// Without this the guest is left believing a held key is still
			// down, and the first keystroke after focus returns arrives as a
			// shortcut. See [Viewer.handleEvent].
			w.releaseInput()
		}
		return nil

	case EventClipboard:
		w.clipHint = true
		return nil

	case EventKey:
		return w.handleKey(e)

	case EventText:
		// Deliberately dropped, for the same reason the RFB viewer drops it:
		// the guest is driven by one keysym per physical key transition and
		// runs its own layout and input method over that stream.
		return nil

	case EventPointer:
		return w.handlePointer(e)

	case EventWheel:
		return w.handleWheel(e)
	}
	return nil
}

// handleKey forwards one key event, reserving the host hotkeys.
func (w *tier1Window) handleKey(e EventKey) error {
	id := hotkeyID{key: e.Key, r: e.Rune}
	if e.Down && e.Key == keysym.KeyReturn && w.sink != nil && w.sink.busy.Load() && w.opts.Takeover != nil {
		if w.swallow == nil {
			w.swallow = map[hotkeyID]bool{}
		}
		w.swallow[id] = true
		if !e.Repeat {
			w.opts.Takeover()
		}
		return nil
	}
	if !e.Down {
		if w.swallow[id] {
			delete(w.swallow, id)
			return nil
		}
	} else if w.isHotkey(e) {
		if w.swallow == nil {
			w.swallow = map[hotkeyID]bool{}
		}
		w.swallow[id] = true
		if e.Repeat {
			return nil
		}
		return w.runHotkey(e)
	}

	sym := symbolFor(e)
	if sym == keysym.NoSymbol {
		return nil
	}
	if e.Down {
		w.noteHeld(sym)
	} else {
		w.forgetHeld(sym)
	}
	w.mods.Track(sym, e.Down)
	return w.inp.Key(sym, e.Down)
}

func (w *tier1Window) isHotkey(e EventKey) bool {
	if e.Key == w.opts.FullscreenKey && e.Key != keysym.KeyUnknown {
		return true
	}
	const chordMods = keysym.ModControl | keysym.ModAlt
	if !e.Mods.Has(chordMods) {
		return false
	}
	// Ctrl+Alt+Del itself is caught where the host lets it through.
	if e.Key == keysym.KeyDelete || e.Key == keysym.KeyEnd {
		return true
	}
	return e.Rune != 0 && lowerRune(e.Rune) == lowerRune(w.opts.QuitRune)
}

func (w *tier1Window) runHotkey(e EventKey) error {
	switch {
	case e.Key == w.opts.FullscreenKey && e.Key != keysym.KeyUnknown:
		if err := w.be.SetFullscreen(!w.be.Fullscreen()); err != nil {
			return fmt.Errorf("viewer: toggle fullscreen: %w", err)
		}
		w.winW, w.winH = w.be.Size()
		w.scheduleGuestResize(time.Now(), w.winW, w.winH)
		return nil

	case e.Key == keysym.KeyDelete || e.Key == keysym.KeyEnd:
		return w.sendChord(keysym.ChordCtrlAltDel)

	default:
		w.quit = true
		return nil
	}
}

// sendChord injects a synthetic key sequence the host would otherwise
// intercept, such as Ctrl-Alt-Del.
func (w *tier1Window) sendChord(c keysym.Chord) error {
	for _, a := range c.Sequence() {
		if err := w.inp.Key(a.Sym, a.Down); err != nil {
			return err
		}
		w.mods.TrackAction(a)
	}
	return nil
}

func (w *tier1Window) handlePointer(e EventPointer) error {
	x, y, _ := MapToSource(e.X, e.Y, w.present, w.texW, w.texH)
	w.ptrX, w.ptrY = x, y
	w.ptrKnown = true
	if e.Buttons != w.buttons {
		w.buttons = e.Buttons
		// A button transition is ordered with respect to motion, so it goes
		// out immediately.
		return w.flushPointer(true)
	}
	return nil
}

func (w *tier1Window) handleWheel(e EventWheel) error {
	if !w.ptrKnown {
		return nil
	}
	// The wheel is reported at the current pointer position, so make sure the
	// guest agrees where that is before the clicks arrive.
	if err := w.flushPointer(false); err != nil {
		return err
	}
	// Wheel sign: the backend's DY is positive scrolling away from the user
	// (up); the Selkies wire carries browser deltas, positive scrolling down.
	return w.inp.Wheel(e.DX, -e.DY)
}

func (w *tier1Window) flushPointer(force bool) error {
	if !w.ptrKnown {
		return nil
	}
	mask := rfbMask(w.buttons)
	unchanged := w.sentAny && w.sentX == w.ptrX && w.sentY == w.ptrY && w.sentMask == mask
	if unchanged && !force {
		return nil
	}
	if err := w.inp.Pointer(w.ptrX, w.ptrY, mask); err != nil {
		return err
	}
	w.sentX, w.sentY, w.sentMask, w.sentAny = w.ptrX, w.ptrY, mask, true
	return nil
}

func (w *tier1Window) noteHeld(sym keysym.Keysym) {
	if sym.IsModifier() {
		return
	}
	for _, s := range w.held {
		if s == sym {
			return
		}
	}
	w.held = append(w.held, sym)
}

func (w *tier1Window) forgetHeld(sym keysym.Keysym) {
	for i, s := range w.held {
		if s == sym {
			w.held = append(w.held[:i], w.held[i+1:]...)
			return
		}
	}
}

// releaseInput tells the guest every key and button this client reported as
// pressed is now released. Errors are ignored: it runs on focus loss and on
// shutdown, when a dead link is expected.
func (w *tier1Window) releaseInput() {
	for i := len(w.held) - 1; i >= 0; i-- {
		_ = w.inp.Key(w.held[i], false)
	}
	w.held = w.held[:0]
	for _, a := range w.mods.ReleaseAll() {
		_ = w.inp.Key(a.Sym, a.Down)
	}
	if w.buttons != 0 {
		w.buttons = 0
		if w.ptrKnown {
			_ = w.inp.Pointer(w.ptrX, w.ptrY, 0)
			w.sentMask = 0
		}
	}
	w.swallow = map[hotkeyID]bool{}
}

// scheduleGuestResize requests the guest match the window after the debounce,
// pushed back on every event so it lands once the user stops dragging. Sizes
// are rounded down to even as the RFB viewer does, so the guest's EDID mode
// allocation stays happy.
func (w *tier1Window) scheduleGuestResize(now time.Time, width, height int) {
	if w.opts.NoResize {
		return
	}
	width, height = width&^1, height&^1
	if width <= 0 || height <= 0 {
		return
	}
	w.resizeW, w.resizeH = width, height
	w.resizeDue = now.Add(w.opts.ResizeDebounce)
	w.resizePending = true
}

func (w *tier1Window) applyGuestResize(now time.Time) error {
	if !w.haveFrame || (w.sink != nil && w.sink.reconnecting.Load()) {
		return nil
	}
	if !w.resizePending || now.Before(w.resizeDue) {
		return nil
	}
	w.resizePending = false
	if w.texW == w.resizeW && w.texH == w.resizeH {
		return nil
	}
	w.opts.logf("resizing guest display to %dx%d", w.resizeW, w.resizeH)
	return w.inp.Resize(w.resizeW, w.resizeH)
}

// syncGuestClipboard moves clipboard content in both directions: guest pushes
// land on the host, and the host clipboard is sampled for the guest.
func (w *tier1Window) syncGuestClipboard(now time.Time) error {
	if w.sink != nil {
		if text, ok := w.sink.takeClipboard(); ok {
			if err := w.be.SetClipboard(text); err != nil {
				return fmt.Errorf("viewer: set host clipboard: %w", err)
			}
			// Remember what the guest sent so the poll does not echo it back.
			w.hostClip = text
		}
	}

	if w.opts.ClipboardInterval < 0 {
		return nil
	}
	// Do not mark clipboard text as sent while the session's input gate is
	// closed. Otherwise a copy during startup/recovery is lost permanently.
	if !w.haveFrame || (w.sink != nil && w.sink.reconnecting.Load()) {
		return nil
	}
	if !w.clipHint && now.Before(w.clipDue) {
		return nil
	}
	w.clipHint = false
	w.clipDue = now.Add(w.opts.ClipboardInterval)

	text, err := w.be.Clipboard()
	if err != nil {
		// A clipboard that cannot be read is not a reason to end a session.
		w.opts.logf("read host clipboard: %v", err)
		return nil
	}
	if text == "" || text == w.hostClip {
		return nil
	}
	w.hostClip = text
	return w.inp.SetClipboard(text)
}

// presentFrame uploads the newest frame, plays any PCM, and draws the frame
// (or the connecting plate) at the letterboxed rectangle.
func (w *tier1Window) presentFrame(now time.Time) error {
	frame, pcm := w.sink.frames.take()
	if w.audio != nil && len(pcm) > 0 {
		w.audio.PlayPCM(pcm)
	}

	overlay := Overlay{}
	var draw Rect
	if frame != nil {
		fw, fh := frame.Rect.Dx(), frame.Rect.Dy()
		if fw != w.texW || fh != w.texH {
			if err := w.be.SetTextureSize(fw, fh); err != nil {
				return fmt.Errorf("viewer: allocate %dx%d tier-1 texture: %w", fw, fh, err)
			}
			w.texW, w.texH = fw, fh
			if !w.haveFrame {
				w.fitGuest(fw, fh)
			}
		}
		if err := w.be.Upload(Rect{W: w.texW, H: w.texH}, frame.Pix, frame.Stride); err != nil {
			return fmt.Errorf("viewer: upload tier-1 frame: %w", err)
		}
		w.haveFrame = true
		draw = FitLetterbox(w.texW, w.texH, w.winW, w.winH)
	} else if !w.haveFrame {
		var err error
		if overlay, err = w.connectingOverlay(); err != nil {
			return err
		}
		draw = Rect{} // no guest pixels to show yet
	} else {
		draw = FitLetterbox(w.texW, w.texH, w.winW, w.winH)
	}
	w.present = draw
	if w.sink.reconnecting.Load() {
		var err error
		if overlay, err = w.statusOverlay(StatusReconnecting); err != nil {
			return err
		}
	}
	if w.sink.busy.Load() {
		var err error
		if overlay, err = w.statusOverlay(StatusDisplayInUse); err != nil {
			return err
		}
	}

	if err := w.be.Present(draw, overlay); err != nil {
		return fmt.Errorf("viewer: tier-1 present: %w", err)
	}
	w.lastPresent = now
	return nil
}

// fitGuest sizes the window to the first guest that connects, copy of the RFB
// viewer's policy: pinned sizes are kept, an unrequested size is followed once.
func (w *tier1Window) fitGuest(fbW, fbH int) {
	if w.pinned || fbW <= 0 || fbH <= 0 {
		return
	}
	if w.be.Fullscreen() {
		return
	}
	width, height := w.initialSize(fbW, fbH)
	if width == w.winW && height == w.winH {
		return
	}
	if err := w.be.SetSize(width, height); err != nil {
		w.opts.logf("resize window to %dx%d: %v", width, height, err)
		return
	}
	w.winW, w.winH = w.be.Size()
}

// connectingOverlay renders the status plate shown until the first frame
// arrives, cached per window size.
func (w *tier1Window) connectingOverlay() (Overlay, error) {
	return w.statusOverlay(StatusConnecting)
}

func (w *tier1Window) statusOverlay(status Status) (Overlay, error) {
	lines := statusLines(status, "")
	if status == StatusDisplayInUse && w.opts.Takeover != nil {
		lines = append(lines, "Press Enter to take over the display")
	}
	key := overlayKey{text: strings.Join(lines, "\n"), w: w.winW, h: w.winH}
	if key != w.ovKey {
		img := renderOverlay(lines, w.winW, w.winH)
		if img.w <= 0 || img.h <= 0 {
			return Overlay{Dim: overlayDim}, nil
		}
		if img.w != w.ovW || img.h != w.ovH {
			if err := w.be.SetOverlaySize(img.w, img.h); err != nil {
				return Overlay{}, fmt.Errorf("viewer: allocate overlay texture: %w", err)
			}
			w.ovW, w.ovH = img.w, img.h
		}
		if err := w.be.UploadOverlay(Rect{W: img.w, H: img.h}, img.pix, img.stride); err != nil {
			return Overlay{}, fmt.Errorf("viewer: upload overlay: %w", err)
		}
		w.ovKey, w.ovPix, w.ovStride = key, img.pix, img.stride
	}
	return Overlay{
		Dim: overlayDim,
		Rect: Rect{
			X: (w.winW - w.ovW) / 2,
			Y: (w.winH - w.ovH) / 2,
			W: w.ovW,
			H: w.ovH,
		},
	}, nil
}
