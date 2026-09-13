// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

// --- fake backend -----------------------------------------------------------

// fakeBackend records what the viewer asked the window to do and replays a
// scripted event queue. It is how the loop is tested without a display: every
// SDL-specific concern lives behind Backend, so a table-driven fake is enough
// to exercise all of viewer.go.
//
// Every method takes the mutex, because the tests that exercise [Viewer.Run]
// drive the loop from another goroutine and push events from the test's. Tests
// that step the loop by hand are single-threaded and read the fields directly.
type fakeBackend struct {
	mu sync.Mutex

	opened bool
	closed int
	opts   WindowOptions

	w, h       int
	texW, texH int
	// texSizes records every texture allocation, so that a reconnect at a
	// different guest resolution can be shown to reallocate exactly once.
	texSizes   [][2]int
	ovW, ovH   int
	fullscreen bool
	sized      [][2]int

	queue []Event

	uploads     []Rect
	ovUploads   []Rect
	presents    []Rect
	overlays    []Overlay
	titles      []string
	presentCals int

	clipboard    string
	clipboardSet []string
	clipboardErr error

	uploadErr  error
	presentErr error

	// wake stands in for SDL's event queue as far as WaitEvents is concerned:
	// a buffered slot, so that a wake delivered while nobody is waiting is
	// still there for the next wait. That is the property the real backend
	// gets from pushing a user event, and the property the render loop's
	// correctness rests on.
	wake  chan struct{}
	wakes int
	// polls counts drains of the event queue, which is how a test measures
	// how hard the loop is spinning.
	polls int
}

func newFakeBackend(w, h int) *fakeBackend {
	return &fakeBackend{w: w, h: h, wake: make(chan struct{}, 1)}
}

func (f *fakeBackend) Open(opts WindowOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = true
	f.opts = opts
	if opts.Width > 0 && opts.Height > 0 {
		f.w, f.h = opts.Width, opts.Height
	}
	f.fullscreen = opts.Fullscreen
	return nil
}

func (f *fakeBackend) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
}

func (f *fakeBackend) SetTextureSize(w, h int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texW, f.texH = w, h
	f.texSizes = append(f.texSizes, [2]int{w, h})
	return nil
}

func (f *fakeBackend) SetOverlaySize(w, h int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w <= 0 || h <= 0 {
		return fmt.Errorf("invalid overlay size %dx%d", w, h)
	}
	f.ovW, f.ovH = w, h
	return nil
}

func (f *fakeBackend) UploadOverlay(r Rect, pix []byte, stride int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if stride > 0 && !r.Empty() {
		if last := (r.Y+r.H-1)*stride + (r.X+r.W)*4; last > len(pix) {
			return fmt.Errorf("overlay upload %s exceeds %d bytes at stride %d", r, len(pix), stride)
		}
	}
	f.ovUploads = append(f.ovUploads, r)
	return nil
}

func (f *fakeBackend) Upload(r Rect, pix []byte, stride int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploadErr != nil {
		return f.uploadErr
	}
	// Mirror the bounds check a real backend must do, so that a plan which
	// would read past the framebuffer fails here rather than in a driver.
	if stride > 0 && !r.Empty() {
		last := (r.Y+r.H-1)*stride + (r.X+r.W)*4
		if last > len(pix) {
			return fmt.Errorf("upload %s exceeds %d bytes at stride %d", r, len(pix), stride)
		}
	}
	f.uploads = append(f.uploads, r)
	return nil
}

func (f *fakeBackend) Present(frame Rect, ov Overlay) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.presentErr != nil {
		return f.presentErr
	}
	f.presents = append(f.presents, frame)
	f.overlays = append(f.overlays, ov)
	f.presentCals++
	return nil
}

func (f *fakeBackend) PollEvents(dst []Event) []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls++
	dst = append(dst, f.queue...)
	f.queue = nil
	return dst
}

// WaitEvents blocks until an event is queued, a wake arrives, or the timeout
// expires, then drains like PollEvents.
func (f *fakeBackend) WaitEvents(dst []Event, timeout time.Duration) []Event {
	if timeout > 0 && !f.queued() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-f.wake:
		case <-timer.C:
		}
	}
	return f.PollEvents(dst)
}

func (f *fakeBackend) Wake() {
	f.mu.Lock()
	f.wakes++
	f.mu.Unlock()
	f.nudge()
}

// nudge fills the wake slot if it is empty, which is what both Wake and a
// pushed event do to a real event queue.
func (f *fakeBackend) nudge() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *fakeBackend) queued() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queue) > 0
}

// wakeCount reports how many times the loop has been nudged from off-thread.
func (f *fakeBackend) wakeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wakes
}

// pollCount reports how many times the event queue has been drained.
func (f *fakeBackend) pollCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls
}

func (f *fakeBackend) Size() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.w, f.h
}

func (f *fakeBackend) SetSize(w, h int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.w, f.h = w, h
	f.sized = append(f.sized, [2]int{w, h})
	return nil
}

func (f *fakeBackend) SetTitle(title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.titles = append(f.titles, title)
	return nil
}

func (f *fakeBackend) SetFullscreen(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fullscreen = on
	return nil
}

func (f *fakeBackend) Fullscreen() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fullscreen
}

func (f *fakeBackend) Clipboard() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clipboardErr != nil {
		return "", f.clipboardErr
	}
	return f.clipboard, nil
}

func (f *fakeBackend) SetClipboard(text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clipboard = text
	f.clipboardSet = append(f.clipboardSet, text)
	return nil
}

// push queues events for the next PollEvents.
func (f *fakeBackend) push(events ...Event) {
	f.mu.Lock()
	f.queue = append(f.queue, events...)
	f.mu.Unlock()
	// A real backend's queue ends a blocking wait; this one has to say so.
	f.nudge()
}

// resize simulates the window manager resizing the window.
func (f *fakeBackend) resize(w, h int) {
	f.mu.Lock()
	f.w, f.h = w, h
	f.mu.Unlock()
	f.push(EventResize{W: w, H: h})
}

// presentCount reports how many frames have been shown. It exists for the
// tests that run the loop on another goroutine.
func (f *fakeBackend) presentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.presentCals
}

// uploadCount reports how many texture uploads have been made.
func (f *fakeBackend) uploadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.uploads)
}

// closedCount reports how many times the window has been torn down.
func (f *fakeBackend) closedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// lastFrame returns the most recent presented rectangle and overlay.
func (f *fakeBackend) lastFrame(t *testing.T) (Rect, Overlay) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.presents) == 0 {
		t.Fatal("nothing has been presented")
	}
	return f.presents[len(f.presents)-1], f.overlays[len(f.overlays)-1]
}

var _ Backend = (*fakeBackend)(nil)

// --- fake RFB server --------------------------------------------------------
//
// The viewer is tested against a real rfb.Conn rather than an interface, so
// the assertions are about the bytes that reach the wire. Everything the
// viewer sends is a client-to-server message, so the fake server only has to
// complete a handshake and then decode.

type keyMsg struct {
	sym  uint32
	down bool
}

type pointerMsg struct {
	x, y uint16
	mask rfb.ButtonMask
}

type cutTextMsg struct{ text string }

type desktopSizeMsg struct{ w, h uint16 }

type updateRequestMsg struct {
	incremental bool
	rect        rfb.Rect
}

type fakeServer struct {
	t    *testing.T
	msgs chan any
}

// startFakeServer returns the client side of a connection whose peer speaks
// just enough RFB to complete a handshake and decode what the viewer sends.
func startFakeServer(t *testing.T, width, height int) (net.Conn, *fakeServer) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	srv := &fakeServer{t: t, msgs: make(chan any, 256)}

	// The handshake runs concurrently with the client's: net.Pipe is
	// unbuffered, so the server's first write does not complete until the
	// client reads it. Waiting for the server to finish before returning
	// would deadlock, because the client has not been created yet.
	go func() {
		defer serverSide.Close()
		if err := srv.handshake(serverSide, width, height); err != nil {
			return
		}
		srv.readLoop(serverSide)
	}()

	t.Cleanup(func() { clientSide.Close() })
	return clientSide, srv
}

func (s *fakeServer) handshake(c net.Conn, width, height int) error {
	if _, err := c.Write([]byte(rfb.ProtocolVersion)); err != nil {
		return err
	}
	buf := make([]byte, 12)
	if _, err := io.ReadFull(c, buf); err != nil {
		return err
	}
	// One security type: None.
	if _, err := c.Write([]byte{1, 1}); err != nil {
		return err
	}
	if _, err := io.ReadFull(c, buf[:1]); err != nil {
		return err
	}
	// Security result: OK.
	if _, err := c.Write([]byte{0, 0, 0, 0}); err != nil {
		return err
	}
	// ClientInit's shared flag.
	if _, err := io.ReadFull(c, buf[:1]); err != nil {
		return err
	}

	name := "fake"
	init := make([]byte, 24, 24+len(name))
	binary.BigEndian.PutUint16(init[0:], uint16(width))
	binary.BigEndian.PutUint16(init[2:], uint16(height))
	init[4] = 32 // bits per pixel
	init[5] = 24 // depth
	init[6] = 0  // little endian
	init[7] = 1  // true colour
	binary.BigEndian.PutUint16(init[8:], 255)
	binary.BigEndian.PutUint16(init[10:], 255)
	binary.BigEndian.PutUint16(init[12:], 255)
	init[14], init[15], init[16] = 0, 8, 16
	binary.BigEndian.PutUint32(init[20:], uint32(len(name)))
	init = append(init, name...)
	_, err := c.Write(init)
	return err
}

// readLoop decodes client messages until the connection closes. Unknown
// message types end the loop rather than desynchronising silently.
func (s *fakeServer) readLoop(c net.Conn) {
	hdr := make([]byte, 1)
	for {
		if _, err := io.ReadFull(c, hdr); err != nil {
			return
		}
		var err error
		switch hdr[0] {
		case 0: // SetPixelFormat
			err = discard(c, 19)
		case 2: // SetEncodings
			buf := make([]byte, 3)
			if _, err = io.ReadFull(c, buf); err != nil {
				return
			}
			err = discard(c, int(binary.BigEndian.Uint16(buf[1:]))*4)
		case 3: // FramebufferUpdateRequest
			buf := make([]byte, 9)
			if _, err = io.ReadFull(c, buf); err != nil {
				return
			}
			s.emit(updateRequestMsg{
				incremental: buf[0] != 0,
				rect: rfb.Rect{
					X:      binary.BigEndian.Uint16(buf[1:]),
					Y:      binary.BigEndian.Uint16(buf[3:]),
					Width:  binary.BigEndian.Uint16(buf[5:]),
					Height: binary.BigEndian.Uint16(buf[7:]),
				},
			})
		case 4: // KeyEvent
			buf := make([]byte, 7)
			if _, err = io.ReadFull(c, buf); err != nil {
				return
			}
			s.emit(keyMsg{sym: binary.BigEndian.Uint32(buf[3:]), down: buf[0] != 0})
		case 5: // PointerEvent
			buf := make([]byte, 5)
			if _, err = io.ReadFull(c, buf); err != nil {
				return
			}
			s.emit(pointerMsg{
				mask: rfb.ButtonMask(buf[0]),
				x:    binary.BigEndian.Uint16(buf[1:]),
				y:    binary.BigEndian.Uint16(buf[3:]),
			})
		case 6: // ClientCutText
			buf := make([]byte, 7)
			if _, err = io.ReadFull(c, buf); err != nil {
				return
			}
			text := make([]byte, binary.BigEndian.Uint32(buf[3:]))
			if _, err = io.ReadFull(c, text); err != nil {
				return
			}
			s.emit(cutTextMsg{text: string(text)})
		case 251: // SetDesktopSize
			buf := make([]byte, 23)
			if _, err = io.ReadFull(c, buf); err != nil {
				return
			}
			s.emit(desktopSizeMsg{
				w: binary.BigEndian.Uint16(buf[1:]),
				h: binary.BigEndian.Uint16(buf[3:]),
			})
		default:
			return
		}
		if err != nil {
			return
		}
	}
}

func (s *fakeServer) emit(m any) {
	select {
	case s.msgs <- m:
	default:
	}
}

func discard(c net.Conn, n int) error {
	_, err := io.CopyN(io.Discard, c, int64(n))
	return err
}

// next returns the next decoded client message, failing the test if none
// arrives.
func (s *fakeServer) next(t *testing.T) any {
	t.Helper()
	select {
	case m := <-s.msgs:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a client message")
		return nil
	}
}

// drain collects every message that arrives within a short settling window.
// The viewer writes from the test's own goroutine, so the window only has to
// cover the pipe handoff.
func (s *fakeServer) drain(t *testing.T) []any {
	t.Helper()
	var out []any
	deadline := time.After(250 * time.Millisecond)
	for {
		select {
		case m := <-s.msgs:
			out = append(out, m)
		case <-deadline:
			return out
		case <-time.After(20 * time.Millisecond):
			return out
		}
	}
}

func (s *fakeServer) expectNone(t *testing.T) {
	t.Helper()
	if got := s.drain(t); len(got) != 0 {
		t.Fatalf("expected no client messages, got %v", got)
	}
}

func (s *fakeServer) keys(t *testing.T) []keyMsg {
	t.Helper()
	var out []keyMsg
	for _, m := range s.drain(t) {
		if k, ok := m.(keyMsg); ok {
			out = append(out, k)
		}
	}
	return out
}

func (s *fakeServer) pointers(t *testing.T) []pointerMsg {
	t.Helper()
	var out []pointerMsg
	for _, m := range s.drain(t) {
		if p, ok := m.(pointerMsg); ok {
			out = append(out, p)
		}
	}
	return out
}

// --- harness ----------------------------------------------------------------

type harness struct {
	v      *Viewer
	be     *fakeBackend
	srv    *fakeServer
	conn   *rfb.Conn
	cfg    rfb.Config
	now    time.Time
	cancel context.CancelFunc
}

// newHarness wires a viewer to a fake backend and a real connection, and runs
// the start-up sequence. guestW/guestH is the guest resolution; winW/winH the
// window size.
func newHarness(t *testing.T, guestW, guestH, winW, winH int, cfg Config) *harness {
	t.Helper()

	transport, srv := startFakeServer(t, guestW, guestH)
	be := newFakeBackend(winW, winH)
	if cfg.Width == 0 && cfg.Height == 0 {
		cfg.Width, cfg.Height = winW, winH
	}
	v := New(be, cfg)

	rfbCfg := v.RFBConfig(rfb.Config{})
	conn, err := rfb.NewConn(transport, rfbCfg)
	if err != nil {
		t.Fatalf("rfb handshake: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h := &harness{v: v, be: be, srv: srv, conn: conn, cfg: rfbCfg, now: time.Now(), cancel: cancel}
	if err := v.start(h.now); err != nil {
		t.Fatalf("viewer start: %v", err)
	}
	v.attach(conn, ctx, h.now)
	// A freshly attached connection holds no pixels of its own, so the viewer
	// keeps showing whatever the texture already had. Announce one update, as
	// the read loop would, to put the harness in the steady state every test
	// below assumes.
	h.damage()
	// The handshake and the start-up full-frame request are not what any test
	// is about.
	srv.drain(t)
	return h
}

func (h *harness) step(t *testing.T) {
	t.Helper()
	if err := h.v.step(h.now); err != nil {
		t.Fatalf("step: %v", err)
	}
}

// advance moves the harness clock, which is how the debounce and interval
// timers are tested without sleeping.
func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

// damage invokes the framebuffer callback the way the RFB read loop would.
func (h *harness) damage(rects ...rfb.Rect) {
	var fb *rfb.Framebuffer
	h.conn.WithFramebuffer(func(f *rfb.Framebuffer) { fb = f })
	h.cfg.OnFramebufferUpdate(fb, rects)
}

func keyDown(k keysym.Key, mods keysym.Modifiers) EventKey {
	return EventKey{Key: k, Down: true, Mods: mods}
}

func keyUp(k keysym.Key, mods keysym.Modifiers) EventKey {
	return EventKey{Key: k, Down: false, Mods: mods}
}

// --- tests ------------------------------------------------------------------

// TestViewerOpensTheWindowBeforeAnyConnection pins the reason the viewer no
// longer takes a connection at start-up: a session that is waiting for a busy
// display, or retrying a flaky link, must already be on screen and closable.
func TestViewerOpensTheWindowBeforeAnyConnection(t *testing.T) {
	be := newFakeBackend(0, 0)
	v := New(be, Config{Title: "ns/vm"})
	now := time.Now()
	if err := v.start(now); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer v.stop()

	if !be.opened {
		t.Fatal("start did not open the window")
	}
	if be.opts.Title != "ns/vm" {
		t.Fatalf("window title = %q", be.opts.Title)
	}
	if be.opts.Width != fallbackWidth || be.opts.Height != fallbackHeight {
		t.Fatalf("window opened at %dx%d, want the %dx%d fallback",
			be.opts.Width, be.opts.Height, fallbackWidth, fallbackHeight)
	}
	if status, _ := v.Status(); status != StatusConnecting {
		t.Fatalf("status before any connection = %v, want %v", status, StatusConnecting)
	}

	// And it says so, over an empty frame, rather than showing a black window
	// with no explanation.
	if err := v.step(now); err != nil {
		t.Fatalf("step: %v", err)
	}
	frame, ov := be.lastFrame(t)
	if !frame.Empty() {
		t.Fatalf("presented %v before any frame existed", frame)
	}
	if ov.Empty() {
		t.Fatal("no overlay was drawn while connecting")
	}
	if got := v.overlayLines(); len(got) == 0 || got[0] != StatusConnecting.Text() {
		t.Fatalf("overlay says %q, want %q", got, StatusConnecting.Text())
	}

	v.stop()
	if be.closed == 0 {
		t.Fatal("stop did not close the backend")
	}
}

func TestViewerFirstConnectionSizesWindowAndRequestsFullFrame(t *testing.T) {
	transport, srv := startFakeServer(t, 1024, 768)
	be := newFakeBackend(800, 600)
	v := New(be, Config{Title: "ns/vm"})

	rfbCfg := v.RFBConfig(rfb.Config{})
	conn, err := rfb.NewConn(transport, rfbCfg)
	if err != nil {
		t.Fatalf("rfb handshake: %v", err)
	}
	now := time.Now()
	if err := v.start(now); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer v.stop()
	v.attach(conn, t.Context(), now)

	// The window follows the guest's resolution when it fits, which is only
	// knowable once a connection exists.
	if be.w != 1024 || be.h != 768 {
		t.Fatalf("window is %dx%d after the first connection, want 1024x768", be.w, be.h)
	}

	// A fresh texture holds nothing, so the first request must be
	// non-incremental or the screen stays blank until something moves.
	var sawFull bool
	for _, m := range srv.drain(t) {
		if r, ok := m.(updateRequestMsg); ok && !r.incremental {
			sawFull = true
		}
	}
	if !sawFull {
		t.Fatal("attach did not request a non-incremental update")
	}

	// The texture is allocated when there are pixels to put in it, not before:
	// allocating leaves it undefined, which would destroy a frozen frame.
	if be.texW != 0 || be.texH != 0 {
		t.Fatalf("texture allocated at %dx%d before any update arrived", be.texW, be.texH)
	}
	conn.WithFramebuffer(func(fb *rfb.Framebuffer) { rfbCfg.OnFramebufferUpdate(fb, nil) })
	if err := v.step(now); err != nil {
		t.Fatalf("step: %v", err)
	}
	if be.texW != 1024 || be.texH != 768 {
		t.Fatalf("texture allocated at %dx%d, want 1024x768", be.texW, be.texH)
	}
}

func TestViewerInitialSizeIsCapped(t *testing.T) {
	transport, _ := startFakeServer(t, 3840, 2160)
	be := newFakeBackend(0, 0)
	v := New(be, Config{MaxInitialWidth: 1920, MaxInitialHeight: 1080})
	conn, err := rfb.NewConn(transport, v.RFBConfig(rfb.Config{}))
	if err != nil {
		t.Fatalf("rfb handshake: %v", err)
	}
	now := time.Now()
	if err := v.start(now); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer v.stop()
	v.attach(conn, t.Context(), now)

	if be.w != 1920 || be.h != 1080 {
		t.Fatalf("4K guest produced a %dx%d window", be.w, be.h)
	}
}

func TestViewerKeepsAPinnedWindowSize(t *testing.T) {
	transport, _ := startFakeServer(t, 1024, 768)
	be := newFakeBackend(0, 0)
	v := New(be, Config{Width: 640, Height: 480})
	conn, err := rfb.NewConn(transport, v.RFBConfig(rfb.Config{}))
	if err != nil {
		t.Fatalf("rfb handshake: %v", err)
	}
	now := time.Now()
	if err := v.start(now); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer v.stop()
	v.attach(conn, t.Context(), now)

	if len(be.sized) != 0 {
		t.Fatalf("a pinned window was resized to %v", be.sized)
	}
	if be.w != 640 || be.h != 480 {
		t.Fatalf("window is %dx%d, want the pinned 640x480", be.w, be.h)
	}
}

func TestViewerUploadsOnlyDamagedRectangles(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()

	// The first frame after start is a full repaint; get it out of the way.
	h.step(t)
	if len(h.be.uploads) != 1 || h.be.uploads[0] != (Rect{0, 0, 800, 600}) {
		t.Fatalf("first frame uploaded %v, want one full-frame upload", h.be.uploads)
	}
	h.be.uploads = nil

	h.damage(
		rfb.Rect{X: 10, Y: 20, Width: 30, Height: 40},
		rfb.Rect{X: 700, Y: 500, Width: 20, Height: 20},
	)
	h.step(t)

	want := []Rect{{10, 20, 30, 40}, {700, 500, 20, 20}}
	if len(h.be.uploads) != len(want) {
		t.Fatalf("uploaded %v, want %v", h.be.uploads, want)
	}
	for i := range want {
		if h.be.uploads[i] != want[i] {
			t.Fatalf("upload %d = %v, want %v", i, h.be.uploads[i], want[i])
		}
	}

	// A frame with no damage must not upload anything at all; re-uploading an
	// unchanged screen is exactly the cost this design exists to avoid.
	h.be.uploads = nil
	h.advance(time.Second)
	h.step(t)
	if len(h.be.uploads) != 0 {
		t.Fatalf("an undamaged frame uploaded %v", h.be.uploads)
	}
}

func TestViewerPresentsLetterboxed(t *testing.T) {
	// A 4:3 guest in a 16:9 window: the image is pillarboxed.
	h := newHarness(t, 1024, 768, 1920, 1080, Config{})
	defer h.v.stop()

	h.step(t)
	if len(h.be.presents) == 0 {
		t.Fatal("nothing was presented")
	}
	want := Rect{X: 240, Y: 0, W: 1440, H: 1080}
	if got := h.be.presents[len(h.be.presents)-1]; got != want {
		t.Fatalf("presented into %v, want %v", got, want)
	}
}

func TestViewerPointerMapsThroughLetterbox(t *testing.T) {
	h := newHarness(t, 1024, 768, 1920, 1080, Config{})
	defer h.v.stop()
	h.step(t) // establish the presented rectangle
	h.srv.drain(t)

	// The centre of the window is the centre of the guest.
	h.be.push(EventPointer{X: 960, Y: 540})
	h.step(t)

	msg, ok := h.srv.next(t).(pointerMsg)
	if !ok {
		t.Fatal("expected a pointer event")
	}
	if msg.x != 512 || msg.y != 384 {
		t.Fatalf("window centre mapped to (%d,%d), want (512,384)", msg.x, msg.y)
	}

	// The left pillarbox bar clamps to the first column rather than being
	// dropped or wrapping around.
	h.be.push(EventPointer{X: 10, Y: 540})
	h.step(t)
	msg, ok = h.srv.next(t).(pointerMsg)
	if !ok {
		t.Fatal("expected a pointer event from the letterbox bar")
	}
	if msg.x != 0 || msg.y != 384 {
		t.Fatalf("letterbox bar mapped to (%d,%d), want (0,384)", msg.x, msg.y)
	}
}

func TestViewerPointerCoalescesMotionButNotButtons(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	// Three moves in one frame collapse into the last position.
	h.be.push(
		EventPointer{X: 10, Y: 10},
		EventPointer{X: 20, Y: 20},
		EventPointer{X: 30, Y: 30},
	)
	h.step(t)
	got := h.srv.pointers(t)
	if len(got) != 1 {
		t.Fatalf("three motion events produced %d messages: %v", len(got), got)
	}
	if got[0].x != 30 || got[0].y != 30 {
		t.Fatalf("coalesced motion reported (%d,%d), want (30,30)", got[0].x, got[0].y)
	}

	// A button press is ordered with respect to motion and goes out at once.
	h.be.push(EventPointer{X: 40, Y: 40, Buttons: ButtonLeft})
	h.step(t)
	got = h.srv.pointers(t)
	if len(got) != 1 {
		t.Fatalf("button press produced %d messages: %v", len(got), got)
	}
	if got[0].mask != rfb.ButtonLeft || got[0].x != 40 {
		t.Fatalf("button press = %+v, want left at x=40", got[0])
	}

	h.be.push(EventPointer{X: 40, Y: 40, Buttons: 0})
	h.step(t)
	got = h.srv.pointers(t)
	if len(got) != 1 || got[0].mask != 0 {
		t.Fatalf("button release = %v, want a single message with an empty mask", got)
	}
}

func TestViewerWheelSendsPressAndRelease(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.be.push(EventPointer{X: 100, Y: 100})
	h.step(t)
	h.srv.drain(t)

	h.be.push(EventWheel{DY: 1})
	h.step(t)

	got := h.srv.pointers(t)
	if len(got) != 2 {
		t.Fatalf("one wheel click produced %d pointer messages: %v", len(got), got)
	}
	// RFB has no wheel axis: a click is a press of the wheel bit immediately
	// followed by its release, both at the current pointer position.
	if got[0].mask != rfb.ButtonWheelUp || got[1].mask != 0 {
		t.Fatalf("wheel click sent masks %#x then %#x, want %#x then 0",
			got[0].mask, got[1].mask, rfb.ButtonWheelUp)
	}
	if got[0].x != 100 || got[0].y != 100 || got[1].x != 100 || got[1].y != 100 {
		t.Fatalf("wheel click was not sent at the pointer position: %v", got)
	}
}

func TestViewerWheelPreservesHeldButtons(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.be.push(EventPointer{X: 50, Y: 50, Buttons: ButtonLeft})
	h.step(t)
	h.srv.drain(t)

	h.be.push(EventWheel{DY: -1})
	h.step(t)

	got := h.srv.pointers(t)
	if len(got) != 2 {
		t.Fatalf("wheel produced %d messages: %v", len(got), got)
	}
	// Scrolling while dragging must not tell the guest the button was let go.
	if got[0].mask != rfb.ButtonLeft|rfb.ButtonWheelDown {
		t.Fatalf("wheel press mask = %#x, want left+wheel-down", got[0].mask)
	}
	if got[1].mask != rfb.ButtonLeft {
		t.Fatalf("wheel release mask = %#x, want left still held", got[1].mask)
	}
}

func TestWheelBits(t *testing.T) {
	tests := []struct {
		name   string
		dx, dy int
		want   []rfb.ButtonMask
	}{
		{"none", 0, 0, nil},
		{"up", 0, 1, []rfb.ButtonMask{rfb.ButtonWheelUp}},
		{"down", 0, -1, []rfb.ButtonMask{rfb.ButtonWheelDown}},
		{"right", 1, 0, []rfb.ButtonMask{rfb.ButtonWheelRight}},
		{"left", -1, 0, []rfb.ButtonMask{rfb.ButtonWheelLeft}},
		{"three up", 0, 3, []rfb.ButtonMask{rfb.ButtonWheelUp, rfb.ButtonWheelUp, rfb.ButtonWheelUp}},
		{
			"diagonal is vertical first",
			1, 1,
			[]rfb.ButtonMask{rfb.ButtonWheelUp, rfb.ButtonWheelRight},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wheelBits(tt.dx, tt.dy)
			if len(got) != len(tt.want) {
				t.Fatalf("wheelBits(%d,%d) = %v, want %v", tt.dx, tt.dy, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("wheelBits(%d,%d)[%d] = %#x, want %#x", tt.dx, tt.dy, i, got[i], tt.want[i])
				}
			}
		})
	}

	// A runaway trackpad delta must not turn into thousands of messages.
	if got := wheelBits(0, 10_000); len(got) != maxWheelTicks {
		t.Fatalf("wheelBits capped at %d clicks, want %d", len(got), maxWheelTicks)
	}
}

func TestRFBMask(t *testing.T) {
	tests := []struct {
		in   Buttons
		want rfb.ButtonMask
	}{
		{0, 0},
		{ButtonLeft, rfb.ButtonLeft},
		{ButtonMiddle, rfb.ButtonMiddle},
		{ButtonRight, rfb.ButtonRight},
		{ButtonLeft | ButtonRight, rfb.ButtonLeft | rfb.ButtonRight},
		{ButtonLeft | ButtonMiddle | ButtonRight, rfb.ButtonLeft | rfb.ButtonMiddle | rfb.ButtonRight},
	}
	for _, tt := range tests {
		if got := rfbMask(tt.in); got != tt.want {
			t.Fatalf("rfbMask(%d) = %#x, want %#x", tt.in, got, tt.want)
		}
	}
}

func TestViewerKeyboardSendsKeysyms(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	h.be.push(
		keyDown(keysym.KeyShiftL, 0),
		EventKey{Rune: 'A', Down: true, Mods: keysym.ModShift},
		EventKey{Rune: 'A', Down: false, Mods: keysym.ModShift},
		keyUp(keysym.KeyShiftL, keysym.ModShift),
	)
	h.step(t)

	want := []keyMsg{
		{uint32(keysym.ShiftL), true},
		{uint32(keysym.FromRune('A')), true},
		{uint32(keysym.FromRune('A')), false},
		{uint32(keysym.ShiftL), false},
	}
	got := h.srv.keys(t)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("key %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestViewerFocusLossReleasesEverything is the stuck-modifier guard. Without
// it, a user who Alt-Tabs away leaves the guest believing Alt is held down,
// and every subsequent keystroke arrives as a shortcut.
func TestViewerFocusLossReleasesEverything(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)

	h.be.push(
		keyDown(keysym.KeyControlL, 0),
		keyDown(keysym.KeyAltL, keysym.ModControl),
		EventKey{Rune: 'a', Down: true, Mods: keysym.ModControl | keysym.ModAlt},
		EventPointer{X: 10, Y: 10, Buttons: ButtonLeft},
	)
	h.step(t)
	h.srv.drain(t)

	h.be.push(EventFocus{Gained: false})
	h.step(t)

	var keyReleases []keyMsg
	var pointers []pointerMsg
	for _, m := range h.srv.drain(t) {
		switch v := m.(type) {
		case keyMsg:
			if v.down {
				t.Fatalf("focus loss sent a key press: %+v", v)
			}
			keyReleases = append(keyReleases, v)
		case pointerMsg:
			pointers = append(pointers, v)
		}
	}

	// Non-modifier keys first, then modifiers unwound in reverse press order:
	// releasing Alt before Ctrl is what a real keyboard does, and a lone Alt
	// press-and-release pops the menu bar on Windows guests.
	want := []keyMsg{
		{uint32(keysym.FromRune('a')), false},
		{uint32(keysym.AltL), false},
		{uint32(keysym.ControlL), false},
	}
	if len(keyReleases) != len(want) {
		t.Fatalf("focus loss released %v, want %v", keyReleases, want)
	}
	for i := range want {
		if keyReleases[i] != want[i] {
			t.Fatalf("release %d = %+v, want %+v", i, keyReleases[i], want[i])
		}
	}

	// A held mouse button is just as damaging as a held modifier.
	if len(pointers) != 1 || pointers[0].mask != 0 {
		t.Fatalf("focus loss produced pointer events %v, want one with an empty mask", pointers)
	}

	// Releasing twice must not send a second round of key-ups for keys the
	// guest already believes are released.
	h.be.push(EventFocus{Gained: false})
	h.step(t)
	h.srv.expectNone(t)
}

// TestViewerIgnoresComposedText protects the guest side of the text-input
// change.
//
// The window collects text for the shell's fields, and it keeps collecting it
// while a session is on screen, so [EventText] reaches the viewer too. RFB has
// no way to say "insert this text": the guest is driven by one keysym per
// physical key transition and runs its own layout and input method over that
// stream. Forwarding the composed text as well would type everything twice.
func TestViewerIgnoresComposedText(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	h.be.push(EventText{Text: "https://kw.example.com"}, EventText{Text: "日本語"})
	h.step(t)
	h.srv.expectNone(t)

	// The key events beside it are still forwarded: this drops the text, not
	// the keyboard.
	h.be.push(
		keyDown(keysym.KeyShiftL, 0),
		EventKey{Rune: ':', Down: true, Mods: keysym.ModShift},
		EventText{Text: ":"},
		EventKey{Rune: ':', Down: false, Mods: keysym.ModShift},
		keyUp(keysym.KeyShiftL, keysym.ModShift),
	)
	h.step(t)

	want := []keyMsg{
		{uint32(keysym.ShiftL), true},
		{uint32(keysym.FromRune(':')), true},
		{uint32(keysym.FromRune(':')), false},
		{uint32(keysym.ShiftL), false},
	}
	got := h.srv.keys(t)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("key %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestViewerFullscreenHotkeyIsNotForwarded(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	h.be.push(keyDown(keysym.KeyF11, 0), keyUp(keysym.KeyF11, 0))
	h.step(t)

	if !h.be.fullscreen {
		t.Fatal("F11 did not enter fullscreen")
	}
	// The guest must not also see F11, or the user gets two fullscreen
	// toggles for one keypress.
	h.srv.expectNone(t)

	h.be.push(keyDown(keysym.KeyF11, 0), keyUp(keysym.KeyF11, 0))
	h.step(t)
	if h.be.fullscreen {
		t.Fatal("F11 did not leave fullscreen")
	}
	h.srv.expectNone(t)
}

// TestViewerHotkeyAutoRepeatDoesNotRefire: holding F11 down makes the platform
// resend the press, which must not toggle fullscreen on every repeat.
func TestViewerHotkeyAutoRepeatDoesNotRefire(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	h.be.push(keyDown(keysym.KeyF11, 0))
	h.step(t)
	if !h.be.fullscreen {
		t.Fatal("F11 did not enter fullscreen")
	}
	for i := 0; i < 5; i++ {
		h.be.push(EventKey{Key: keysym.KeyF11, Down: true, Repeat: true})
		h.step(t)
	}
	if !h.be.fullscreen {
		t.Fatal("auto-repeat toggled fullscreen back off")
	}
	h.be.push(keyUp(keysym.KeyF11, 0))
	h.step(t)
	h.srv.expectNone(t)
}

// TestViewerPointerUsesNewGeometryAfterResize: a click that arrives in the
// same batch as a resize must be mapped through the new window geometry, not
// the previous frame's.
func TestViewerPointerUsesNewGeometryAfterResize(t *testing.T) {
	h := newHarness(t, 640, 480, 640, 480, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	h.be.resize(1280, 960)
	h.be.push(EventPointer{X: 640, Y: 480})
	h.step(t)

	got := h.srv.pointers(t)
	if len(got) != 1 {
		t.Fatalf("got %d pointer messages, want 1: %v", len(got), got)
	}
	// The window doubled, so the centre of it is still the centre of the guest.
	if got[0].x != 320 || got[0].y != 240 {
		t.Fatalf("after resize, (640,480) mapped to (%d,%d), want (320,240)", got[0].x, got[0].y)
	}
}

func TestViewerSendsCtrlAltDel(t *testing.T) {
	for _, tt := range []struct {
		name string
		key  keysym.Key
	}{
		// The portable binding, because Windows hosts never deliver the real
		// secure attention sequence to an application...
		{"ctrl+alt+end", keysym.KeyEnd},
		// ...and the obvious one, which X11 and most Wayland compositors do
		// deliver.
		{"ctrl+alt+del", keysym.KeyDelete},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, 800, 600, 800, 600, Config{})
			defer h.v.stop()
			h.step(t)

			mods := keysym.ModControl | keysym.ModAlt
			h.be.push(
				keyDown(keysym.KeyControlL, 0),
				keyDown(keysym.KeyAltL, keysym.ModControl),
			)
			h.step(t)
			h.srv.drain(t)

			h.be.push(keyDown(tt.key, mods), keyUp(tt.key, mods))
			h.step(t)

			got := h.srv.keys(t)
			want := []keyMsg{
				{uint32(keysym.ControlL), true},
				{uint32(keysym.AltL), true},
				{uint32(keysym.Delete), true},
				{uint32(keysym.Delete), false},
				{uint32(keysym.AltL), false},
				{uint32(keysym.ControlL), false},
			}
			if len(got) != len(want) {
				t.Fatalf("got %v, want the ctrl-alt-del sequence %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
				}
			}
		})
	}
}

func TestViewerQuitHotkey(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	mods := keysym.ModControl | keysym.ModAlt
	h.be.push(EventKey{Rune: 'q', Down: true, Mods: mods})
	h.step(t)

	if !h.v.quit {
		t.Fatal("Ctrl+Alt+Q did not end the session")
	}
	h.srv.expectNone(t)
}

func TestViewerQuitEventEndsSession(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.be.push(EventQuit{})
	h.step(t)
	if !h.v.quit {
		t.Fatal("EventQuit did not end the session")
	}
}

// TestViewerResizeIsDebounced pins the interval to the guest's behaviour: the
// in-guest poller applies a new EDID mode twice a second, so asking faster
// interrupts a resize that is still being applied.
func TestViewerResizeIsDebounced(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	h.be.resize(1024, 768)
	h.step(t)
	h.srv.expectNone(t)

	// Just short of the debounce: still nothing.
	h.advance(DefaultResizeDebounce - time.Millisecond)
	h.step(t)
	h.srv.expectNone(t)

	h.advance(2 * time.Millisecond)
	h.step(t)

	msg, ok := h.srv.next(t).(desktopSizeMsg)
	if !ok {
		t.Fatal("expected a SetDesktopSize after the debounce elapsed")
	}
	if msg.w != 1024 || msg.h != 768 {
		t.Fatalf("resized guest to %dx%d, want 1024x768", msg.w, msg.h)
	}

	// One request per settled resize, not one per step.
	h.advance(time.Second)
	h.step(t)
	h.srv.expectNone(t)
}

func TestViewerResizeDebounceRestartsWhileDragging(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	// A drag produces a stream of sizes. Only the last one should be sent,
	// and only once the drag has stopped.
	for _, size := range [][2]int{{900, 700}, {1000, 750}, {1100, 800}} {
		h.be.resize(size[0], size[1])
		h.step(t)
		h.advance(DefaultResizeDebounce - 10*time.Millisecond)
		h.step(t)
		h.srv.expectNone(t)
	}

	h.advance(DefaultResizeDebounce)
	h.step(t)

	msg, ok := h.srv.next(t).(desktopSizeMsg)
	if !ok {
		t.Fatal("expected a SetDesktopSize once the drag settled")
	}
	if msg.w != 1100 || msg.h != 800 {
		t.Fatalf("sent %dx%d, want only the final 1100x800", msg.w, msg.h)
	}
	h.srv.expectNone(t)
}

func TestViewerResizeDebounceFloor(t *testing.T) {
	// A caller asking for a faster debounce gets the floor instead: below it
	// the guest thrashes between modes.
	h := newHarness(t, 800, 600, 800, 600, Config{ResizeDebounce: 50 * time.Millisecond})
	defer h.v.stop()
	if h.v.cfg.ResizeDebounce != DefaultResizeDebounce {
		t.Fatalf("debounce = %s, want it raised to %s", h.v.cfg.ResizeDebounce, DefaultResizeDebounce)
	}
}

func TestViewerDoesNotResizeGuestToItsCurrentSize(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	// Fullscreen toggles and expose events produce resize notifications that
	// do not actually change anything.
	h.be.resize(800, 600)
	h.step(t)
	h.advance(2 * DefaultResizeDebounce)
	h.step(t)
	h.srv.expectNone(t)
}

func TestViewerServerResizeReallocatesTexture(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	// Simulate the server changing resolution: the connection resizes its own
	// framebuffer and then calls OnResize.
	h.conn.WithFramebuffer(func(fb *rfb.Framebuffer) { fb.Resize(1280, 720) })
	h.cfg.OnResize(1280, 720)
	h.step(t)

	if h.be.texW != 1280 || h.be.texH != 720 {
		t.Fatalf("texture is %dx%d after a server resize, want 1280x720", h.be.texW, h.be.texH)
	}
	// The old contents describe an image that no longer exists, so an
	// incremental request would never repaint the new one.
	var sawFull bool
	for _, m := range h.srv.drain(t) {
		if r, ok := m.(updateRequestMsg); ok && !r.incremental {
			sawFull = true
		}
	}
	if !sawFull {
		t.Fatal("a server resize did not trigger a non-incremental update request")
	}

	// And the whole new framebuffer must be uploaded, not just the old area.
	var sawFullUpload bool
	for _, u := range h.be.uploads {
		if u == (Rect{0, 0, 1280, 720}) {
			sawFullUpload = true
		}
	}
	if !sawFullUpload {
		t.Fatalf("uploads after resize were %v, want a full 1280x720 upload", h.be.uploads)
	}
}

func TestViewerClipboardGuestToHost(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	h.cfg.OnCutText("from the guest")
	h.step(t)

	if got := h.be.clipboard; got != "from the guest" {
		t.Fatalf("host clipboard = %q, want %q", got, "from the guest")
	}
	// The text must not be echoed straight back to the guest on the next poll.
	h.advance(2 * DefaultClipboardInterval)
	h.step(t)
	h.srv.expectNone(t)
}

func TestViewerClipboardHostToGuest(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	h.be.clipboard = "from the host"
	// Nothing happens until the poll interval elapses: there is no reliable
	// clipboard-change event to hang this on.
	h.step(t)
	h.srv.expectNone(t)

	h.advance(DefaultClipboardInterval)
	h.step(t)

	msg, ok := h.srv.next(t).(cutTextMsg)
	if !ok {
		t.Fatal("expected the host clipboard to reach the guest")
	}
	if msg.text != "from the host" {
		t.Fatalf("sent %q", msg.text)
	}

	// Unchanged clipboards are not resent on every poll.
	h.advance(2 * DefaultClipboardInterval)
	h.step(t)
	h.srv.expectNone(t)
}

func TestViewerClipboardHintPollsImmediately(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	h.be.clipboard = "copied"
	h.be.push(EventClipboard{})
	h.step(t)

	if _, ok := h.srv.next(t).(cutTextMsg); !ok {
		t.Fatal("a clipboard hint should trigger a poll without waiting")
	}
}

func TestViewerClipboardErrorDoesNotKillTheSession(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)

	h.be.clipboardErr = fmt.Errorf("no clipboard manager")
	h.advance(2 * DefaultClipboardInterval)
	if err := h.v.step(h.now); err != nil {
		t.Fatalf("an unreadable clipboard ended the session: %v", err)
	}
}

func TestViewerClipboardCanBeDisabled(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{ClipboardInterval: -1})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)

	h.be.clipboard = "should stay on the host"
	h.advance(10 * time.Second)
	h.step(t)
	h.srv.expectNone(t)
}

func TestViewerTitleShowsResolutionAndRate(t *testing.T) {
	h := newHarness(t, 1280, 720, 1280, 720, Config{Title: "ns/vm"})
	defer h.v.stop()
	h.step(t)

	h.advance(DefaultStatsInterval)
	h.step(t)

	if len(h.be.titles) == 0 {
		t.Fatal("the title was never updated")
	}
	title := h.be.titles[len(h.be.titles)-1]
	for _, want := range []string{"ns/vm", "1280x720", "fps", "bit/s"} {
		if !contains(title, want) {
			t.Fatalf("title %q does not mention %q", title, want)
		}
	}
}

func TestFormatBitrate(t *testing.T) {
	tests := []struct {
		kbits float64
		want  string
	}{
		{0, "0 kbit/s"},
		{512, "512 kbit/s"},
		{999, "999 kbit/s"},
		{1000, "1.0 Mbit/s"},
		{3400, "3.4 Mbit/s"},
	}
	for _, tt := range tests {
		if got := formatBitrate(tt.kbits); got != tt.want {
			t.Fatalf("formatBitrate(%v) = %q, want %q", tt.kbits, got, tt.want)
		}
	}
}

func TestViewerForcesAPeriodicRepaint(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.be.presents = nil

	// No damage, no events: nothing to draw.
	h.step(t)
	if len(h.be.presents) != 0 {
		t.Fatalf("an idle frame presented %v", h.be.presents)
	}

	// But the window must not be left unrepainted indefinitely, or it comes
	// back from an occlusion showing whatever the compositor kept.
	h.advance(forcedPresentInterval + time.Millisecond)
	h.step(t)
	if len(h.be.presents) != 1 {
		t.Fatalf("expected one heartbeat repaint, got %v", h.be.presents)
	}
}

func TestViewerPropagatesBackendErrors(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()

	h.be.presentErr = fmt.Errorf("device lost")
	if err := h.v.step(h.now); err == nil {
		t.Fatal("a failing Present should end the session")
	}
}

func TestConfigHotkeysDocumentsTheBindings(t *testing.T) {
	cfg := Config{}
	lines := cfg.Hotkeys()
	if len(lines) != 3 {
		t.Fatalf("Hotkeys returned %d lines, want 3: %v", len(lines), lines)
	}
	joined := ""
	for _, l := range lines {
		joined += l + "\n"
	}
	for _, want := range []string{"F11", "fullscreen", "Ctrl+Alt+end", "Ctrl-Alt-Del", "Ctrl+Alt+q", "disconnect"} {
		if !contains(joined, want) {
			t.Fatalf("hotkey help %q does not mention %q", joined, want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
