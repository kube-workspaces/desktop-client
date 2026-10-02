// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/reconnect"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// resizeWriter is the optional capability a transport has to send a text
// frame. The exec bridge distinguishes frame types: binary is raw stdin, text
// is a JSON control message. A transport without text frames simply never
// learns about grid changes; everything else keeps working.
type resizeWriter interface {
	io.ReadWriteCloser
	WriteText(p []byte) (int, error)
}

// The built-in window geometry and pacing. One hundred columns by thirty rows
// at the default scale 2 is 1200x660 pixels, a comfortable terminal.
const (
	defaultCols = 100
	defaultRows = 30

	// resizeDebounce folds a burst of window resizes into one grid change.
	// Every resize event fires while the user drags a corner, and reflowing a
	// terminal screen is visible work; waiting 150ms merges the burst.
	resizeDebounce = 150 * time.Millisecond

	// idleWait is how long the loop sleeps when nothing is breathing. It is
	// long enough to stay quiet and short enough that a scheduled reconnect
	// or a pending resize never drifts visibly.
	idleWait = 200 * time.Millisecond

	// seatRecheckInterval is how often a window waiting on a held console
	// re-asks whether the seat cleared. It matches the shared-display
	// membership poll: gentle enough to be invisible, prompt enough that a
	// freed seat connects without the user having to press anything. A dial
	// never evicts, so polling needs no consent; only the takeover call
	// does, and that stays behind Enter.
	seatRecheckInterval = 5 * time.Second
)

// window owns the live terminal session: the backend, the emulator, the
// renderer and the connection pumping in between.
//
// One goroutine runs the loop; another pumps network input. The two meet only
// through [window.mu]-guarded state and the backend's Wake mechanism, which is
// exactly the shape the viewer's own loop uses.
type window struct {
	surface   connection.Surface
	be        viewer.Backend
	emu       *Emulator
	ren       *renderer
	in        *input
	dial      Dial
	checkSeat func(ctx context.Context) (bool, error)
	takeSeat  func(ctx context.Context) error
	ctx       context.Context
	backoff   reconnect.Policy
	logf      func(string, ...any)

	// seatApproved records explicit takeover consent for this window's
	// lifetime: once the user has pressed Enter on the busy plate, later
	// dials skip the seat check the way the web console's approved flag
	// does. seatInUse is the busy plate itself. Both are owned by the loop
	// goroutine — the network pump never touches them — like the rest of
	// the draw path.
	seatApproved bool
	seatInUse    bool

	title string
	scale int
	// cellW and cellH are the fixed pixel size of one cell at this session's
	// scale. They never change while the session lives.
	cellW, cellH int

	// cols and rows are the current grid size in cells; winW and winH the
	// window size in pixels. Both are owned by the loop goroutine.
	cols, rows int
	winW, winH int

	// quit ends the loop without retrying.
	quit        bool
	disposition connection.CloseDisposition

	// resize debounce, owned by the loop goroutine.
	resizing bool
	resizeAt time.Time

	// mu guards the connection state shared with the pump goroutine.
	mu       sync.Mutex
	conn     io.ReadWriteCloser
	rw       resizeWriter
	failed   bool // a transport failure; the loop must redial
	ended    bool // the shell closed cleanly; the loop must return
	attempts int
	nextTry  time.Time

	// lastErr is the most recent failure's text, shown under the status plate.
	// It is written by tryConnect and onWireClosed, which may run on the pump
	// goroutine, so it lives under mu.
	lastErr string
	// liveOnce records that the window has held a real session at least once,
	// so the plate can tell "connecting" (the first dial) from "reconnecting"
	// (it had a session and lost it). It is loop-goroutine state like the rest
	// of the draw path, but it is set while holding mu for consistency.
	liveOnce bool

	// plate caches the rasterised status plate between presents.
	plate viewer.StatusLayer
}

// run opens the window and drives the session until it ends. A nil return
// means the session ended normally: the shell exited, the user quit (window
// close or Ctrl+Alt+Q), or the context was cancelled.
func (w *window) run(ctx context.Context) error {
	if err := w.openWindow(ctx); err != nil {
		return err
	}
	defer w.closeWindow()

	var evs []viewer.Event
	for {
		if ctx.Err() != nil {
			return nil
		}
		if w.endedNow() {
			return nil
		}
		now := time.Now()
		w.process(now)
		if w.endedNow() {
			return nil
		}
		if w.quit {
			return nil
		}

		evs = w.be.WaitEvents(evs[:0], w.nextWait(now))
		evs = w.filterEvents(now, evs)
		for _, e := range evs {
			w.handleEvent(e)
			if w.quit {
				break
			}
		}
		w.present()
	}
}

// openWindow opens the window without driving the event loop, for the
// multi-window pump that owns the main thread. See [Detached].
func (w *window) openWindow(ctx context.Context) error {
	gw, gh := w.ren.Size()
	if err := w.be.Open(viewer.WindowOptions{
		Title:        w.title,
		Width:        gw,
		Height:       gh,
		ScaleQuality: viewer.ScaleNearest,
	}); err != nil {
		// Close is safe on a backend that was never opened, so a failed
		// Open still cleans up any half-created surface.
		w.be.Close()
		return fmt.Errorf("terminal: open window: %w", err)
	}

	if err := w.be.SetTextureSize(gw, gh); err != nil {
		w.be.Close()
		return fmt.Errorf("terminal: texture: %w", err)
	}

	w.ctx = ctx
	w.winW, w.winH = w.be.Size()
	w.cols, w.rows = w.gridFor(w.winW, w.winH)

	// The session starts needing a connection, so the first process pass
	// dials.
	w.mu.Lock()
	w.failed = true
	w.nextTry = time.Now()
	w.mu.Unlock()
	return nil
}

// stepExternal runs one iteration against already-polled events, without
// touching the backend's event queue. The multi-window pump routes first
// and calls [Detached.Step], which delegates here.
func (w *window) stepExternal(now time.Time, events []viewer.Event) {
	w.process(now)
	events = w.filterEvents(now, events)
	for _, e := range events {
		w.handleEvent(e)
		if w.quit {
			break
		}
	}
	w.present()
}

// closeWindow tears the window down and releases the live connection, if
// any, so its pump goroutine ends instead of lingering on a window nobody
// shows. Close is safe on a backend that was never opened.
func (w *window) closeWindow() {
	w.mu.Lock()
	conn := w.conn
	w.conn = nil
	w.rw = nil
	w.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	w.be.Close()
}

// Detached is one live terminal window driven by the multi-window pump
// instead of its own loop. It must be used from the goroutine that owns the
// main OS thread, like every [viewer.Backend] window.
type Detached struct {
	w *window
}

// OpenDetached opens the terminal window without driving it, for the
// multi-window pump that steps every live window cooperatively. The caller
// feeds routed events to [Detached.Step], waits on [Detached.IdleWait], and
// calls [Detached.Close] when [Detached.Closed] reports the window is done.
func OpenDetached(ctx context.Context, dial Dial, opts Options) (*Detached, error) {
	w, err := buildWindow(dial, opts)
	if err != nil {
		return nil, err
	}
	if err := w.openWindow(ctx); err != nil {
		return nil, err
	}
	return &Detached{w: w}, nil
}

// Step runs one iteration against the window's own routed events.
func (d *Detached) Step(now time.Time, events []viewer.Event) {
	d.w.stepExternal(now, events)
}

// IdleWait reports how long the pump may block before this window has
// scheduled work of its own.
func (d *Detached) IdleWait(now time.Time) time.Duration {
	return d.w.nextWait(now)
}

// Closed reports whether the window asked to close: its close button, the
// quit chord, or a clean shell exit.
func (d *Detached) Closed() bool {
	return d.w.quit || d.w.endedNow()
}

// Result is always nil: a terminal has no terminal failure to report — a
// dropped bridge redials inside the window, and a clean shell exit ends it
// — so the pump uses CloseDisposition to distinguish Disconnect from park.
func (d *Detached) Result() error { return nil }

// CloseDisposition distinguishes explicit Disconnect from window-close.
func (d *Detached) CloseDisposition() connection.CloseDisposition { return d.w.disposition }

// RequestDisconnect ends the window and releases its held shell entry.
func (d *Detached) RequestDisconnect() { d.w.requestDisconnect() }

func (w *window) requestDisconnect() {
	w.disposition = connection.Release
	w.quit = true
}

// Close tears the window down.
func (d *Detached) Close() { d.w.closeWindow() }

// Backend returns the backend owning this window, for the pump's routing
// and waiting.
func (d *Detached) Backend() viewer.Backend { return d.w.be }

// ReleaseInput is a no-op for the terminal: it sends bytes, not held keys,
// so there is no per-guest modifier state to release on focus loss. It
// exists so the pump can treat every live window alike.
func (d *Detached) ReleaseInput() {}

// process performs the loop's scheduled work: applying a pending resize and
// retrying a dropped connection.
func (w *window) process(now time.Time) {
	if w.resizing && !now.Before(w.resizeAt) {
		w.applyResize()
	}
	if w.needConnect() && !now.Before(w.nextTryTime()) {
		w.tryConnect()
	}
}

// needConnect reports whether the session is currently without a live
// connection and should dial again.
func (w *window) needConnect() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.failed && !w.ended
}

func (w *window) nextTryTime() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.nextTry
}

// tryConnect dials once. A failure schedules the next attempt with backoff
// and reflects it in the window title; a success hands the connection to
// attach. A held single-seat console is not a failure: the window parks on
// the busy plate (rechecking gently, dialling when the seat clears) until
// the user explicitly takes it over with Enter.
func (w *window) tryConnect() {
	if w.seatHeld() {
		return
	}
	conn, err := w.dial(w.ctx, uint16(w.cols), uint16(w.rows))
	if err != nil {
		if w.ctx.Err() != nil {
			w.quit = true
			return
		}
		if errors.Is(err, ErrInUse) {
			// Raced 409: the seat filled between the status check and the
			// dial. Back on the busy plate — no silent steal, same as the
			// pre-dial check.
			w.markSeatBusy("")
			w.logf("terminal: seat taken (raced status); waiting for consent")
			return
		}
		w.mu.Lock()
		w.attempts++
		w.lastErr = err.Error()
		delay := w.backoff.Backoff(w.attempts)
		w.nextTry = time.Now().Add(delay)
		w.mu.Unlock()
		w.logf("terminal: dial failed: %v; retrying in %s", err, delay)
		if w.title != "" {
			_ = w.be.SetTitle(w.title + " — reconnecting")
		}
		return
	}
	w.mu.Lock()
	w.attempts = 0
	w.mu.Unlock()
	w.attach(conn)
}

// seatHeld reports whether a single-seat console is currently held, parking
// the window on the busy plate when it is. A check failure is fail-open: a
// status hiccup must not block the console, so the window dials and lets
// the bridge answer instead.
func (w *window) seatHeld() bool {
	if w.seatApproved || w.checkSeat == nil {
		return false
	}
	inUse, err := w.checkSeat(w.ctx)
	if err != nil {
		w.logf("terminal: seat check failed (fail-open): %v", err)
		return false
	}
	if !inUse {
		return false
	}
	w.markSeatBusy("")
	return true
}

// markSeatBusy parks the window on the busy plate with a gentle recheck. A
// dial never evicts, so re-polling needs no consent; only the takeover call
// does.
func (w *window) markSeatBusy(detail string) {
	w.seatInUse = true
	w.mu.Lock()
	w.attempts = 0
	w.nextTry = time.Now().Add(seatRecheckInterval)
	if detail != "" {
		w.lastErr = detail
	}
	w.mu.Unlock()
	if w.title != "" {
		_ = w.be.SetTitle(w.title)
	}
}

// takeSeatNow runs the explicit-consent takeover and dials immediately on
// success. A failed takeover stays on the busy plate with the reason, so
// Enter retries and closing the window backs out.
func (w *window) takeSeatNow() {
	if w.takeSeat == nil {
		return
	}
	if err := w.takeSeat(w.ctx); err != nil {
		w.logf("terminal: takeover failed: %v", err)
		w.mu.Lock()
		w.lastErr = err.Error()
		w.nextTry = time.Now().Add(seatRecheckInterval)
		w.mu.Unlock()
		return
	}
	w.seatApproved = true
	w.seatInUse = false
	w.mu.Lock()
	w.failed = true
	w.attempts = 0
	w.nextTry = time.Now()
	w.mu.Unlock()
}

// attach wires a freshly dialed connection into the session: a clean screen, a
// grid announcement, and a read pump.
func (w *window) attach(conn io.ReadWriteCloser) {
	if w.title != "" {
		_ = w.be.SetTitle(w.title)
	}
	rw, _ := conn.(resizeWriter)
	// The terminal starts from a blank canvas on every reconnect: the shell
	// that answers the new dial has its own idea of the screen.
	w.emu.Reset()
	w.emu.SetOnData(w.dataHandler(conn))

	w.mu.Lock()
	w.conn = conn
	w.rw = rw
	w.failed = false
	w.liveOnce = true
	w.mu.Unlock()
	// A connection proves the seat is ours; the busy plate goes away. The
	// approval stays for the window's lifetime so a later drop redials
	// without re-prompting, exactly like the web console's approved flag.
	w.seatInUse = false

	w.sendResize()
	go w.pump(conn)
}

// dataHandler returns the callback that carries terminal-derived output (such
// as answerback responses) back into the session transport. It is wired to
// its own connection and stops writing the moment that connection is no
// longer the live one, so a stale pump can never speak into a new session.
func (w *window) dataHandler(conn io.ReadWriteCloser) func(string) {
	return func(s string) {
		w.mu.Lock()
		live := w.conn == conn
		w.mu.Unlock()
		if !live {
			return
		}
		if _, err := conn.Write([]byte(s)); err != nil {
			w.onWireClosed(false, err)
		}
	}
}

// pump reads one connection until it dies, feeding every byte to the emulator
// and waking the loop to repaint.
func (w *window) pump(conn io.ReadWriteCloser) {
	buf := make([]byte, 16<<10)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if werr := w.emu.Write(buf[:n]); werr != nil {
				w.logf("terminal: decode: %v", werr)
			}
			w.be.Wake()
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				w.onWireClosed(true, nil)
			} else {
				w.onWireClosed(false, err)
			}
			return
		}
	}
}

// onWireClosed records the end of a connection: a clean close ends the
// session, any other error marks it for redial.
func (w *window) onWireClosed(ended bool, err error) {
	if err != nil {
		w.logf("terminal: connection: %v", err)
	}
	w.mu.Lock()
	conn := w.conn
	w.conn = nil
	w.rw = nil
	if err != nil {
		w.lastErr = err.Error()
	}
	if ended {
		w.ended = true
	} else {
		w.failed = true
		w.attempts = 0
		w.nextTry = time.Now()
	}
	w.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	w.be.Wake()
}

func (w *window) endedNow() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ended
}

// statusNow is the connection state the in-window plate should show: nothing
// while a session is live, "connecting" until the first session has ever been
// established, "reconnecting" after a drop, and "display in use" while a
// single-seat console is held by somebody else. lastErr, when there is one,
// sits under the headline.
func (w *window) statusNow() (viewer.Status, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.failed || w.ended {
		return viewer.StatusLive, ""
	}
	if w.seatInUse {
		return viewer.StatusDisplayInUse, w.lastErr
	}
	if !w.liveOnce {
		return viewer.StatusConnecting, w.lastErr
	}
	return viewer.StatusReconnecting, w.lastErr
}

// handleEvent translates one backend event into state or bytes.
func (w *window) handleEvent(e viewer.Event) {
	switch ev := e.(type) {
	case viewer.EventConnectionAction:
		if !w.connectionSnapshot().Availability(ev.Action).Enabled {
			return
		}
		switch ev.Action {
		case connection.Fullscreen:
			w.toggleFullscreen()
		case connection.Disconnect:
			w.requestDisconnect()
		case connection.Paste:
			text, err := w.be.Clipboard()
			if err == nil {
				w.handleEvent(viewer.EventText{Text: text})
			}
		}
		return
	case viewer.EventQuit:
		w.quit = true
	case viewer.EventWindowClose:
		w.quit = true
	case viewer.EventResize:
		w.scheduleResize(ev.W, ev.H)
	default:
		if ev, ok := e.(viewer.EventKey); ok && ev.Down && !ev.Repeat {
			// F11 is the palette's fullscreen affordance, shared with the
			// display viewer: it belongs to the window and is never
			// forwarded, so a guest fullscreen app cannot swallow the
			// binding. The grid follows through the resize the window
			// manager answers with.
			if ev.Key == keysym.KeyF11 {
				w.toggleFullscreen()
				return
			}
			// Enter on the busy plate is explicit takeover consent:
			// evict the holder and dial. It is consumed here, never
			// typed into a shell that is not there, and the plate names
			// the alternative (Ctrl+Alt+Q closes).
			if ev.Key == keysym.KeyReturn && w.seatInUse {
				w.takeSeatNow()
				return
			}
		}
		out, quit := w.in.handle(e)
		if quit {
			w.requestDisconnect()
			return
		}
		if len(out) == 0 {
			return
		}
		w.mu.Lock()
		conn := w.conn
		w.mu.Unlock()
		if conn == nil {
			return
		}
		if _, err := conn.Write(out); err != nil {
			w.onWireClosed(false, err)
		}
	}
}

func (w *window) connectionSnapshot() connection.Snapshot {
	status, _ := w.statusNow()
	surface := w.surface
	if surface == "" {
		surface = connection.Console
	}
	return connection.Snapshot{Workspace: w.title, Surface: surface, State: viewer.ConnectionState(status),
		Transport: string(surface), Capabilities: connection.Capabilities{Paste: true}}
}

func (w *window) filterEvents(now time.Time, events []viewer.Event) []viewer.Event {
	return viewer.FilterConnectionInput(w.be, now, w.connectionSnapshot(), events)
}

// scheduleResize updates the window size and, when the resulting grid would
// change, marks a debounced resize for the next process pass.
func (w *window) scheduleResize(winW, winH int) {
	w.winW, w.winH = winW, winH
	cols, rows := w.gridFor(winW, winH)
	if cols == w.cols && rows == w.rows {
		return
	}
	w.resizing = true
	w.resizeAt = time.Now().Add(resizeDebounce)
}

// applyResize reflows the emulator and the texture to the window's current
// grid, and tells the shell.
func (w *window) applyResize() {
	w.resizing = false
	cols, rows := w.gridFor(w.winW, w.winH)
	w.cols, w.rows = cols, rows

	w.emu.Resize(cols, rows)
	w.ren.resize(cols, rows)
	gw, gh := w.ren.Size()
	if err := w.be.SetTextureSize(gw, gh); err != nil {
		w.logf("terminal: texture: %v", err)
	}
	w.sendResize()
}

// gridFor converts a window size in pixels into the grid that fills it.
func (w *window) gridFor(winW, winH int) (cols, rows int) {
	cols = winW / w.cellW
	if cols < 1 {
		cols = 1
	}
	rows = winH / w.cellH
	if rows < 1 {
		rows = 1
	}
	return cols, rows
}

// sendResize announces the current grid to the shell with a text frame.
func (w *window) sendResize() {
	w.mu.Lock()
	rw := w.rw
	w.mu.Unlock()
	if rw == nil {
		w.logf("terminal: resize not announced: transport has no text frames")
		return
	}
	msg := fmt.Sprintf(`{"type":"resize","cols":%d,"rows":%d}`, w.cols, w.rows)
	if _, err := rw.WriteText([]byte(msg)); err != nil {
		w.onWireClosed(false, fmt.Errorf("resize: %w", err))
	}
}

// nextWait computes how long WaitEvents may sleep before scheduled work is
// due.
func (w *window) nextWait(now time.Time) time.Duration {
	wait := idleWait
	if w.resizing {
		if d := w.resizeAt.Sub(now); d < wait {
			wait = d
		}
	}
	w.mu.Lock()
	if w.failed {
		if d := w.nextTry.Sub(now); d < wait {
			wait = d
		}
	}
	w.mu.Unlock()
	if wait < 0 {
		wait = 0
	}
	return wait
}

// toggleFullscreen flips the window's fullscreen state, the terminal half
// of the in-session palette (the display viewer owns the other half behind
// the same F11). A backend that cannot do it — a tiling compositor owns
// the state — reports the error and keeps the window as it is.
func (w *window) toggleFullscreen() {
	if err := w.be.SetFullscreen(!w.be.Fullscreen()); err != nil {
		w.logf("terminal: fullscreen: %v", err)
		return
	}
	if gw, gh := w.be.Size(); gw > 0 && gh > 0 {
		w.winW, w.winH = gw, gh
	}
}

// plateLines renders a status into the overlay plate's lines. Everything but
// the busy seat shares the viewer's plate; the busy seat names the console
// and its two exits, because "Display in use" would not tell a terminal
// user what is held or how to take it.
func (w *window) plateLines(status viewer.Status, detail string) []string {
	if status != viewer.StatusDisplayInUse {
		return viewer.StatusLines(status, detail)
	}
	lines := []string{
		"Console in use by another session",
		"Enter: take over  ·  Ctrl+Alt+Q: close",
	}
	if detail != "" {
		lines = append(lines, detail)
	}
	return lines
}

// present redraws the changed cells into the texture and shows the window.
func (w *window) present() {
	gw, gh := w.ren.Size()
	dirty, changed := w.ren.frame(w.emu)
	if changed {
		img := w.ren.Image()
		if err := w.be.Upload(dirty, img.Pix, img.Stride); err != nil {
			w.logf("terminal: upload: %v", err)
		}
	}
	fit := viewer.FitLetterbox(gw, gh, w.winW, w.winH)
	status, detail := w.statusNow()
	ov, err := w.plate.Build(w.be, w.plateLines(status, detail), w.winW, w.winH)
	if err != nil {
		w.logf("terminal: status plate: %v", err)
	}
	if err := w.be.Present(fit, ov); err != nil {
		w.logf("terminal: present: %v", err)
	}
}
