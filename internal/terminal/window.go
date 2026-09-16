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
)

// window owns the live terminal session: the backend, the emulator, the
// renderer and the connection pumping in between.
//
// One goroutine runs the loop; another pumps network input. The two meet only
// through [window.mu]-guarded state and the backend's Wake mechanism, which is
// exactly the shape the viewer's own loop uses.
type window struct {
	be      viewer.Backend
	emu     *Emulator
	ren     *renderer
	in      *input
	dial    Dial
	ctx     context.Context
	backoff reconnect.Policy
	logf    func(string, ...any)

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
	quit bool

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
}

// run opens the window and drives the session until it ends. A nil return
// means the session ended normally: the shell exited, the user quit (window
// close or Ctrl+Alt+Q), or the context was cancelled.
func (w *window) run(ctx context.Context) error {
	gw, gh := w.ren.Size()
	// Close is safe on a backend that was never opened, so it is registered
	// before Open: an Open failure still cleans up any half-created surface.
	defer w.be.Close()
	if err := w.be.Open(viewer.WindowOptions{
		Title:        w.title,
		Width:        gw,
		Height:       gh,
		ScaleQuality: viewer.ScaleNearest,
	}); err != nil {
		return fmt.Errorf("terminal: open window: %w", err)
	}

	if err := w.be.SetTextureSize(gw, gh); err != nil {
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
		for _, e := range evs {
			w.handleEvent(e)
			if w.quit {
				break
			}
		}
		w.present()
	}
}

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
// attach.
func (w *window) tryConnect() {
	conn, err := w.dial(w.ctx, uint16(w.cols), uint16(w.rows))
	if err != nil {
		if w.ctx.Err() != nil {
			w.quit = true
			return
		}
		w.mu.Lock()
		w.attempts++
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
	w.mu.Unlock()

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

// handleEvent translates one backend event into state or bytes.
func (w *window) handleEvent(e viewer.Event) {
	switch ev := e.(type) {
	case viewer.EventQuit:
		w.quit = true
	case viewer.EventResize:
		w.scheduleResize(ev.W, ev.H)
	default:
		out, quit := w.in.handle(e)
		if quit {
			w.quit = true
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
	if err := w.be.Present(fit, viewer.Overlay{}); err != nil {
		w.logf("terminal: present: %v", err)
	}
}
