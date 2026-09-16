// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// ---------------------------------------------------------------------------
// fakeBackend: a viewer.Backend the tests drive explicitly. Every interaction
// is recorded under a mutex so the window's own goroutine and the test's
// goroutine never race.

type fakeBackend struct {
	mu        sync.Mutex
	w, h      int
	evs       []viewer.Event
	wakeCh    chan struct{}
	opts      viewer.WindowOptions
	title     string
	texW      int
	texH      int
	img       *image.RGBA
	uploads   int
	lastDirty viewer.Rect
	presents  int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{wakeCh: make(chan struct{}, 1)}
}

func (f *fakeBackend) Open(opts viewer.WindowOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opts = opts
	f.w = opts.Width
	f.h = opts.Height
	if f.w == 0 || f.h == 0 {
		f.w, f.h = 1200, 660
	}
	return nil
}

func (f *fakeBackend) Close() {}

func (f *fakeBackend) SetTextureSize(w, h int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texW, f.texH = w, h
	f.img = image.NewRGBA(image.Rect(0, 0, w, h))
	return nil
}

func (f *fakeBackend) Upload(r viewer.Rect, pix []byte, stride int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads++
	f.lastDirty = r
	for y := 0; y < r.H && y < f.texH; y++ {
		src := y*stride + r.X*4
		if y+r.Y >= f.texH {
			break
		}
		dst := (y+r.Y)*f.texW*4 + r.X*4
		copy(f.img.Pix[dst:], pix[src:src+r.W*4])
	}
	return nil
}

func (f *fakeBackend) SetOverlaySize(w, h int) error { return nil }
func (f *fakeBackend) UploadOverlay(r viewer.Rect, pix []byte, stride int) error {
	return nil
}

func (f *fakeBackend) Present(frame viewer.Rect, ov viewer.Overlay) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presents++
	return nil
}

func (f *fakeBackend) PollEvents(dst []viewer.Event) []viewer.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	dst = append(dst, f.evs...)
	f.evs = f.evs[:0]
	return dst
}

func (f *fakeBackend) WaitEvents(dst []viewer.Event, timeout time.Duration) []viewer.Event {
	if timeout > 0 {
		select {
		case <-f.wakeCh:
		case <-time.After(timeout):
		}
	}
	return f.PollEvents(dst)
}

func (f *fakeBackend) Wake() {
	select {
	case f.wakeCh <- struct{}{}:
	default:
	}
}

func (f *fakeBackend) Size() (w, h int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.w, f.h
}

func (f *fakeBackend) SetSize(w, h int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.w, f.h = w, h
	return nil
}

func (f *fakeBackend) SetTitle(title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.title = title
	return nil
}

func (f *fakeBackend) SetFullscreen(on bool) error { return nil }
func (f *fakeBackend) Fullscreen() bool            { return false }
func (f *fakeBackend) Clipboard() (string, error)  { return "", nil }
func (f *fakeBackend) SetClipboard(string) error   { return nil }

func (f *fakeBackend) push(e viewer.Event) {
	f.mu.Lock()
	f.evs = append(f.evs, e)
	f.mu.Unlock()
	f.Wake()
}

func (f *fakeBackend) readTitle() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.title
}

func (f *fakeBackend) pixelAt(x, y int) color.RGBA {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.img == nil {
		return color.RGBA{}
	}
	return f.img.RGBAAt(x, y)
}

// ---------------------------------------------------------------------------
// fakeConn: one /exec transport. Reads return bytes the test pushes on in;
// writes land in out; WriteText lands in titled phrases; exit closes the
// connection cleanly and kill delivers a transport error.

type fakeConn struct {
	mu      sync.Mutex
	in      chan []byte
	out     bytes.Buffer
	phrases []string
	closed  bool

	exit chan struct{}
	kill chan error
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		in:   make(chan []byte, 32),
		exit: make(chan struct{}),
		kill: make(chan error, 1),
	}
}

func (c *fakeConn) Read(p []byte) (int, error) {
	select {
	case b := <-c.in:
		return copy(p, b), nil
	case <-c.exit:
		return 0, io.EOF
	case err := <-c.kill:
		return 0, err
	}
}

func (c *fakeConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, errors.New("fakeConn: closed")
	}
	return c.out.Write(p)
}

func (c *fakeConn) WriteText(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, errors.New("fakeConn: closed")
	}
	c.phrases = append(c.phrases, string(p))
	return len(p), nil
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	select {
	case <-c.exit:
	default:
		close(c.exit)
	}
	return nil
}

func (c *fakeConn) pushShell(b string) { c.in <- []byte(b) }

func (c *fakeConn) readOut() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.String()
}

func (c *fakeConn) readPhrases() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.phrases))
	copy(out, c.phrases)
	return out
}

func (c *fakeConn) closeClean() { close(c.exit) }
func (c *fakeConn) breakWire()  { c.kill <- errors.New("wire dropped") }

// ---------------------------------------------------------------------------
// dialServer hands out fresh fakeConns and remembers every dial.

type dialServer struct {
	mu      sync.Mutex
	conns   []*fakeConn
	cols    uint16
	rows    uint16
	calls   int
	failErr error // non-nil: fail every dial
}

func (d *dialServer) dial(ctx context.Context, cols, rows uint16) (io.ReadWriteCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	d.cols, d.rows = cols, rows
	if d.failErr != nil {
		return nil, d.failErr
	}
	c := newFakeConn()
	d.conns = append(d.conns, c)
	return c, nil
}

func (d *dialServer) last() *fakeConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.conns) == 0 {
		return nil
	}
	return d.conns[len(d.conns)-1]
}

func (d *dialServer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// ---------------------------------------------------------------------------

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met: " + msg)
}

// runSession starts Run on a goroutine with the given dial and backend.
func runSession(t *testing.T, be viewer.Backend, dial Dial) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		err := Run(ctx, dial, Options{Backend: be, Title: "linux workspace"})
		done <- err
	}()
	return cancel, done
}

func TestRunConnectsAndRenders(t *testing.T) {
	be := newFakeBackend()
	ds := &dialServer{}
	cancel, done := runSession(t, be, ds.dial)
	defer cancel()

	waitUntil(t, 2*time.Second, func() bool { return ds.callCount() == 1 }, "no dial")
	conn := ds.last()
	if conn == nil {
		t.Fatal("dial returned no conn")
	}
	if be.readTitle() != "linux workspace" {
		t.Fatalf("title = %q", be.readTitle())
	}

	// The initial dial already announced the default grid.
	ph := conn.readPhrases()
	if len(ph) != 1 || ph[0] != `{"type":"resize","cols":100,"rows":30}` {
		t.Fatalf("initial resize = %q", ph)
	}

	// Shell output flows into the texture.
	conn.pushShell("hi\n")
	bg := color.RGBA{R: 0x14, G: 0x16, B: 0x1a, A: 0xff}
	waitUntil(t, 2*time.Second, func() bool {
		// The 'h' glyph replaces background in cell (0,0).
		return be.pixelAt(3, 5) != bg
	}, "shell output did not reach the texture")

	// The session is still live: no clean-close, no quit.
	select {
	case err := <-done:
		t.Fatalf("Run returned %v while still connected", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v on cancel", err)
	}
}

func TestInputReachesShell(t *testing.T) {
	be := newFakeBackend()
	ds := &dialServer{}
	cancel, done := runSession(t, be, ds.dial)
	defer cancel()

	waitUntil(t, 2*time.Second, func() bool { return ds.callCount() == 1 }, "no dial")
	conn := ds.last()
	be.push(viewer.EventText{Text: "ls -la\r"})

	waitUntil(t, 2*time.Second, func() bool {
		return conn.readOut() == "ls -la\r"
	}, "input did not reach the shell")

	// A Ctrl+C key chord is translated to the wire byte, not typed twice.
	be.push(viewer.EventKey{Key: keysym.KeyUnknown, Rune: 'c', Down: true, Mods: keysym.ModControl})
	waitUntil(t, 2*time.Second, func() bool {
		return strings.Contains(conn.readOut(), "\x03")
	}, "Ctrl+C did not reach the shell")
	_ = done
}

func TestQuitChordEndsSession(t *testing.T) {
	be := newFakeBackend()
	ds := &dialServer{}
	cancel, done := runSession(t, be, ds.dial)
	defer cancel()

	waitUntil(t, 2*time.Second, func() bool { return ds.callCount() == 1 }, "no dial")
	conn := ds.last()
	be.push(viewer.EventKey{Key: keysym.KeyUnknown, Rune: 'q', Down: true, Mods: keysym.ModControl | keysym.ModAlt})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after quit chord", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session did not quit")
	}
	// The shell must never have seen a stray 'q' from the chord's key-up.
	waitUntil(t, time.Second, func() bool { return conn.readOut() == "" }, "'q' leaked to shell")
}

func TestCleanCloseEndsSession(t *testing.T) {
	be := newFakeBackend()
	ds := &dialServer{}
	cancel, done := runSession(t, be, ds.dial)
	defer cancel()

	waitUntil(t, 2*time.Second, func() bool { return ds.callCount() == 1 }, "no dial")
	ds.last().closeClean()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after shell exit", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session did not end on clean close")
	}
}

func TestTransportErrorReconnects(t *testing.T) {
	be := newFakeBackend()
	ds := &dialServer{}
	cancel, done := runSession(t, be, ds.dial)
	defer cancel()

	waitUntil(t, 2*time.Second, func() bool { return ds.callCount() == 1 }, "first dial")
	ds.last().breakWire()

	// The loop must dial a second connection and keep the session alive; the
	// reconnect handshake restores the plain title.
	waitUntil(t, 2*time.Second, func() bool { return ds.callCount() == 2 }, "no redial")
	waitUntil(t, 2*time.Second, func() bool {
		return be.readTitle() == "linux workspace"
	}, "title not restored after reconnect")
	ds.last().closeClean()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after reconnect", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session did not end after reconnect")
	}
}

func TestWindowResizeAnnouncesNewGrid(t *testing.T) {
	be := newFakeBackend()
	ds := &dialServer{}
	cancel, done := runSession(t, be, ds.dial)
	defer cancel()

	waitUntil(t, 2*time.Second, func() bool { return ds.callCount() == 1 }, "no dial")
	conn := ds.last()

	// 600x660 at 12px cells is a 50-column grid; the text frame must follow.
	be.push(viewer.EventResize{W: 600, H: 660})
	waitUntil(t, 2*time.Second, func() bool {
		for _, p := range conn.readPhrases() {
			if p == `{"type":"resize","cols":50,"rows":30}` {
				return true
			}
		}
		return false
	}, "resize frame for 50-column grid not sent")
	_ = done
}

func TestTinyWindowClampsToOneCell(t *testing.T) {
	be := newFakeBackend()
	ds := &dialServer{}
	cancel, done := runSession(t, be, ds.dial)
	defer cancel()

	waitUntil(t, 2*time.Second, func() bool { return ds.callCount() == 1 }, "no dial")
	conn := ds.last()

	be.push(viewer.EventResize{W: 1, H: 1})
	waitUntil(t, 2*time.Second, func() bool {
		for _, p := range conn.readPhrases() {
			if p == `{"type":"resize","cols":1,"rows":1}` {
				return true
			}
		}
		return false
	}, "resize frame for 1x1 grid not sent")
	_ = done
}

func TestFailedDialRetries(t *testing.T) {
	be := newFakeBackend()
	ds := &dialServer{}

	first := true
	dial := func(ctx context.Context, cols, rows uint16) (io.ReadWriteCloser, error) {
		if first {
			first = false
			return nil, errors.New("network partition")
		}
		return ds.dial(ctx, cols, rows)
	}
	cancel, done := runSession(t, be, dial)
	defer cancel()

	// The reconnecting title is set while the first dial is retried...
	waitUntil(t, 2*time.Second, func() bool {
		return strings.Contains(be.readTitle(), "reconnecting")
	}, "title did not report reconnecting during retry")
	// ...and a later dial succeeds.
	waitUntil(t, 2*time.Second, func() bool { return ds.callCount() == 1 }, "no successful redial")
	waitUntil(t, 2*time.Second, func() bool {
		return be.readTitle() == "linux workspace"
	}, "title not restored after reconnect")
	_ = done
}

func TestRunRejectsNilDial(t *testing.T) {
	be := newFakeBackend()
	if err := Run(context.Background(), nil, Options{Backend: be}); err == nil {
		t.Fatal("Run with nil dial succeeded")
	}
}

func TestRunOpenFailure(t *testing.T) {
	be := &openFailingBackend{}
	_ = Run(context.Background(), func(ctx context.Context, c, r uint16) (io.ReadWriteCloser, error) {
		return newFakeConn(), nil
	}, Options{Backend: be})
	if !be.closed {
		t.Fatal("backend not closed after open failure")
	}
}

type openFailingBackend struct {
	fakeBackend
	closed bool
}

func (o *openFailingBackend) Open(viewer.WindowOptions) error {
	return fmt.Errorf("no display")
}

func (o *openFailingBackend) Close() { o.closed = true }

func (o *openFailingBackend) Size() (int, int) { return 1200, 660 }
