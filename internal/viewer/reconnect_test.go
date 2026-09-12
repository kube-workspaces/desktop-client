// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

// These tests are about the one property the window has that a connection does
// not: it survives. Everything here drives the render loop by hand with a
// controlled clock, except the two that must exercise Run itself.

// connect opens another fake server and wires a connection to the same viewer,
// exactly as a reconnecting supervisor would when it redials.
func (h *harness) connect(t *testing.T, w, height int) (*rfb.Conn, *fakeServer, rfb.Config) {
	t.Helper()
	transport, srv := startFakeServer(t, w, height)
	cfg := h.v.RFBConfig(rfb.Config{})
	conn, err := rfb.NewConn(transport, cfg)
	if err != nil {
		t.Fatalf("rfb handshake: %v", err)
	}
	return conn, srv, cfg
}

// deliver announces a framebuffer update on conn the way its read loop would.
func deliver(cfg rfb.Config, conn *rfb.Conn, rects ...rfb.Rect) {
	conn.WithFramebuffer(func(fb *rfb.Framebuffer) { cfg.OnFramebufferUpdate(fb, rects) })
}

func TestViewerSwapsConnectionsWithoutTouchingTheWindow(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	if len(h.be.uploads) == 0 {
		t.Fatal("the first connection never painted")
	}
	closedBefore := h.be.closed
	h.be.uploads = nil

	conn2, srv2, cfg2 := h.connect(t, 800, 600)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	h.v.attach(conn2, ctx2, h.now)

	// The whole point: no window was destroyed to get a new connection.
	if h.be.closed != closedBefore {
		t.Fatalf("the backend was closed %d time(s) across a reconnect", h.be.closed-closedBefore)
	}
	if !h.be.opened || h.be.closed != 0 {
		t.Fatal("the window did not survive the swap")
	}

	// A new connection shares no history with the old one, so it is asked for
	// a complete frame.
	var sawFull bool
	for _, m := range srv2.drain(t) {
		if r, ok := m.(updateRequestMsg); ok && !r.incremental {
			sawFull = true
		}
	}
	if !sawFull {
		t.Fatal("the new connection was not asked for a non-incremental update")
	}

	// Uploads resume from the new connection.
	deliver(cfg2, conn2)
	h.step(t)
	if len(h.be.uploads) == 0 || h.be.uploads[0] != (Rect{0, 0, 800, 600}) {
		t.Fatalf("uploads after the swap were %v, want a full frame from the new connection", h.be.uploads)
	}

	// And input goes to the new connection only. The old one must not be
	// written to again: it is closed by the supervisor, and writing to it
	// would at best be discarded and at worst block.
	h.be.push(keyDown(keysym.KeyF5, 0), keyUp(keysym.KeyF5, 0))
	h.step(t)

	if got := srv2.keys(t); len(got) != 2 {
		t.Fatalf("the new connection received %v, want a press and a release", got)
	}
	h.srv.expectNone(t)
}

// TestViewerFreezesTheLastFrameWhenTheConnectionDrops is the requirement that
// motivated the rework: a drop must look like a frozen desktop under a status
// message, not like a window that went black or vanished.
func TestViewerFreezesTheLastFrameWhenTheConnectionDrops(t *testing.T) {
	h := newHarness(t, 1024, 768, 1024, 768, Config{})
	defer h.v.stop()
	h.step(t)
	liveFrame, liveOverlay := h.be.lastFrame(t)
	if liveFrame.Empty() {
		t.Fatal("nothing was presented while connected")
	}
	if !liveOverlay.Empty() {
		t.Fatalf("a live connection drew an overlay: %+v", liveOverlay)
	}

	// The supervisor cancels the generation context when the link dies.
	h.cancel()
	h.be.uploads = nil
	h.be.presents = nil
	h.step(t)

	if h.v.conn != nil {
		t.Fatal("the viewer kept a connection whose context had ended")
	}
	frame, ov := h.be.lastFrame(t)
	if frame != liveFrame {
		t.Fatalf("presented %v after the drop, want the frozen %v", frame, liveFrame)
	}
	if ov.Dim == 0 {
		t.Fatal("the frozen frame was not dimmed, so the overlay reads as part of the guest's screen")
	}
	if ov.Rect.Empty() {
		t.Fatal("no status plate was drawn over the frozen frame")
	}
	if len(h.be.uploads) != 0 {
		t.Fatalf("a disconnected viewer uploaded %v; the texture must keep the last frame", h.be.uploads)
	}
	if got := h.v.overlayLines(); len(got) == 0 || got[0] != StatusReconnecting.Text() {
		t.Fatalf("overlay says %v, want it to start with %q", got, StatusReconnecting.Text())
	}

	// And it keeps presenting: a window that stops drawing shows whatever the
	// compositor kept when it is uncovered.
	h.be.presents = nil
	h.advance(forcedPresentInterval + time.Millisecond)
	h.step(t)
	if len(h.be.presents) != 1 {
		t.Fatalf("a frozen viewer presented %d times in one heartbeat, want 1", len(h.be.presents))
	}
	if h.be.presents[0] != liveFrame {
		t.Fatalf("heartbeat presented %v, want the frozen %v", h.be.presents[0], liveFrame)
	}
}

// TestViewerReallocatesTextureForANewGuestSize covers the case a reconnect
// makes easy to get wrong: the guest came back at a different resolution.
func TestViewerReallocatesTextureForANewGuestSize(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)

	frozen, _ := h.be.lastFrame(t)
	allocs := len(h.be.texSizes)
	if allocs != 1 {
		t.Fatalf("texture allocated %d times for one connection", allocs)
	}

	h.cancel()
	h.step(t)

	conn2, _, cfg2 := h.connect(t, 1280, 720)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	h.v.attach(conn2, ctx2, h.now)
	h.step(t)

	// Until the new connection has decoded something, the texture still holds
	// the old frame: reallocating now would leave undefined pixels on screen,
	// and presenting the old texture through the new geometry would stretch a
	// 4:3 desktop into 16:9.
	if len(h.be.texSizes) != allocs {
		t.Fatalf("texture reallocated to %v before the new connection sent a frame", h.be.texSizes)
	}
	if frame, _ := h.be.lastFrame(t); frame != frozen {
		t.Fatalf("presented %v while waiting for the new connection, want the frozen %v", frame, frozen)
	}

	deliver(cfg2, conn2)
	h.be.uploads = nil
	h.step(t)

	if h.be.texW != 1280 || h.be.texH != 720 {
		t.Fatalf("texture is %dx%d after reconnecting to a bigger guest, want 1280x720", h.be.texW, h.be.texH)
	}
	if len(h.be.texSizes) != allocs+1 {
		t.Fatalf("texture allocations were %v, want exactly one more", h.be.texSizes)
	}
	if len(h.be.uploads) != 1 || h.be.uploads[0] != (Rect{0, 0, 1280, 720}) {
		t.Fatalf("uploads after the resize were %v, want one full 1280x720 frame", h.be.uploads)
	}
	// The window is 800x600, so a 16:9 guest is now letterboxed in it.
	want := FitLetterbox(1280, 720, 800, 600)
	if frame, _ := h.be.lastFrame(t); frame != want {
		t.Fatalf("presented %v, want %v", frame, want)
	}
}

func TestViewerOverlayTextFollowsStatus(t *testing.T) {
	tests := []struct {
		name   string
		status Status
		detail string
		want   []string
	}{
		{"connecting", StatusConnecting, "", []string{"Connecting…"}},
		{"reconnecting", StatusReconnecting, "", []string{"Reconnecting…"}},
		{
			"reconnecting with a reason",
			StatusReconnecting, "connection reset by peer",
			[]string{"Reconnecting…", "connection reset by peer"},
		},
		{
			"display in use",
			StatusDisplayInUse, "",
			[]string{"Display in use by another session", "Waiting for it to be released"},
		},
		{
			"failed",
			StatusFailed, "401 unauthorized",
			[]string{"Reconnecting failed — 401 unauthorized"},
		},
		{"failed without a reason", StatusFailed, "", []string{"Reconnecting failed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statusLines(tt.status, tt.detail)
			if len(got) != len(tt.want) {
				t.Fatalf("statusLines(%v, %q) = %q, want %q", tt.status, tt.detail, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("line %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}

	// A live connection with a frame on screen has nothing to say.
	if got := statusLines(StatusLive, ""); got != nil {
		t.Fatalf("a live session drew %q", got)
	}
}

func TestViewerOverlayTracksStateChanges(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	if n := len(h.be.ovUploads); n != 0 {
		t.Fatalf("a live session rasterised %d overlay(s)", n)
	}

	h.cancel()
	h.v.SetStatus(StatusDisplayInUse, "")
	h.step(t)

	if got := h.v.overlayLines(); len(got) == 0 || got[0] != StatusDisplayInUse.Text() {
		t.Fatalf("overlay says %q after the display was taken", got)
	}
	uploads := len(h.be.ovUploads)
	if uploads != 1 {
		t.Fatalf("the overlay was uploaded %d times for one state", uploads)
	}
	if h.be.ovW <= 0 || h.be.ovH <= 0 {
		t.Fatalf("overlay texture is %dx%d", h.be.ovW, h.be.ovH)
	}

	// Unchanged text is not re-rasterised on every frame of a reconnect that
	// may last minutes.
	h.advance(forcedPresentInterval + time.Millisecond)
	h.step(t)
	if len(h.be.ovUploads) != uploads {
		t.Fatalf("an unchanged overlay was re-uploaded %d times", len(h.be.ovUploads)-uploads)
	}

	// A new state is.
	h.v.SetStatus(StatusFailed, "gave up after waiting 2m0s")
	h.step(t)
	if len(h.be.ovUploads) != uploads+1 {
		t.Fatal("a new status did not rebuild the overlay")
	}
	if got := h.v.overlayLines(); len(got) != 1 || got[0] != "Reconnecting failed — gave up after waiting 2m0s" {
		t.Fatalf("overlay says %q", got)
	}

	// Reconnecting removes it again, and the frame underneath is the one the
	// guest last sent.
	conn2, _, cfg2 := h.connect(t, 800, 600)
	h.v.attach(conn2, t.Context(), h.now)
	deliver(cfg2, conn2)
	h.step(t)
	if _, ov := h.be.lastFrame(t); !ov.Empty() {
		t.Fatalf("an overlay survived the reconnect: %+v", ov)
	}
}

// TestViewerOverlayScalesWithTheWindow: a status message that is legible in a
// 640x480 window and a dot in a 4K one would be no better than no message.
func TestViewerOverlayScalesWithTheWindow(t *testing.T) {
	lines := []string{"Reconnecting…"}
	small := renderOverlay(lines, 640, 480)
	large := renderOverlay(lines, 3840, 2160)

	if small.w <= 0 || large.w <= 0 {
		t.Fatalf("overlay sizes: %dx%d and %dx%d", small.w, small.h, large.w, large.h)
	}
	if large.scale <= small.scale {
		t.Fatalf("glyph scale did not grow with the window: %d then %d", small.scale, large.scale)
	}
	// Both stay a sensible fraction of their window rather than filling it.
	if small.w > 640 || large.w > 3840 {
		t.Fatalf("overlay is wider than its window: %d in 640, %d in 3840", small.w, large.w)
	}
	if large.h > 2160/2 {
		t.Fatalf("overlay is %d tall in a 2160 window", large.h)
	}
	// Even an absurdly small window produces something rather than panicking.
	tiny := renderOverlay(lines, 40, 20)
	if tiny.w > 40 || tiny.h > 20 {
		t.Fatalf("overlay %dx%d does not fit a 40x20 window", tiny.w, tiny.h)
	}
}

// desktopSize returns the size the client last asked the guest for, if any.
func desktopSize(t *testing.T, srv *fakeServer) (desktopSizeMsg, bool) {
	t.Helper()
	var (
		got desktopSizeMsg
		ok  bool
	)
	for _, m := range srv.drain(t) {
		if d, is := m.(desktopSizeMsg); is {
			got, ok = d, true
		}
	}
	return got, ok
}

// TestViewerAsksAReconnectedGuestToMatchTheWindow covers the guest that was
// rebooted during the outage: it comes back at its default resolution with no
// memory of the size this client asked for, and nothing else would ever ask
// again until the user happened to resize the window.
func TestViewerAsksAReconnectedGuestToMatchTheWindow(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.cancel()
	h.step(t)

	conn2, srv2, cfg2 := h.connect(t, 1024, 768)
	h.v.attach(conn2, t.Context(), h.now)
	deliver(cfg2, conn2)
	h.step(t)
	srv2.drain(t)

	h.advance(DefaultResizeDebounce + time.Millisecond)
	h.step(t)

	msg, ok := desktopSize(t, srv2)
	if !ok {
		t.Fatal("a guest that came back at the wrong size was never asked to change")
	}
	if msg.w != 800 || msg.h != 600 {
		t.Fatalf("asked the guest for %dx%d, want the window's 800x600", msg.w, msg.h)
	}
}

// TestViewerRemembersAResizeMadeDuringAnOutage: the window is usable while
// disconnected, so it can be resized then too, and the request has to survive
// until there is something to send it on.
func TestViewerRemembersAResizeMadeDuringAnOutage(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.cancel()
	h.step(t)

	h.be.resize(1024, 768)
	h.step(t)
	h.advance(DefaultResizeDebounce + time.Millisecond)
	h.step(t) // no connection: the request has nowhere to go yet

	conn2, srv2, cfg2 := h.connect(t, 800, 600)
	h.v.attach(conn2, t.Context(), h.now)
	deliver(cfg2, conn2)
	h.step(t)
	srv2.drain(t)

	h.advance(DefaultResizeDebounce + time.Millisecond)
	h.step(t)

	msg, ok := desktopSize(t, srv2)
	if !ok {
		t.Fatal("the resize made during the outage was lost")
	}
	if msg.w != 1024 || msg.h != 768 {
		t.Fatalf("asked the guest for %dx%d, want 1024x768", msg.w, msg.h)
	}
}

func TestViewerHotkeysWorkWhileDisconnected(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.cancel()
	h.step(t)

	// Fullscreen still belongs to the window.
	h.be.push(keyDown(keysym.KeyF11, 0), keyUp(keysym.KeyF11, 0))
	h.step(t)
	if !h.be.fullscreen {
		t.Fatal("F11 did nothing while disconnected")
	}

	// And so does quitting: the old design left the user with no window at all
	// during a reconnect, so Ctrl-C was the only way out.
	mods := keysym.ModControl | keysym.ModAlt
	h.be.push(EventKey{Rune: 'q', Down: true, Mods: mods})
	h.step(t)
	if !h.v.quit {
		t.Fatal("Ctrl+Alt+Q did not end a disconnected session")
	}
}

func TestViewerInputIsDroppedWhileDisconnected(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{})
	defer h.v.stop()
	h.step(t)
	h.cancel()
	h.step(t)

	h.be.push(
		keyDown(keysym.KeyControlL, 0),
		EventPointer{X: 10, Y: 10, Buttons: ButtonLeft},
		EventWheel{DY: 1},
	)
	h.step(t)
	h.srv.expectNone(t)

	// The keys pressed during the outage are not replayed at the new guest,
	// which never saw them go down.
	conn2, srv2, cfg2 := h.connect(t, 800, 600)
	h.v.attach(conn2, t.Context(), h.now)
	deliver(cfg2, conn2)
	h.step(t)
	for _, m := range srv2.drain(t) {
		if k, ok := m.(keyMsg); ok {
			t.Fatalf("the new connection was sent %+v from before it existed", k)
		}
	}
}

// --- sources ----------------------------------------------------------------

// blockingSource never yields a connection, which is what a session waiting
// for a display somebody else holds looks like from here.
type blockingSource struct{}

func (blockingSource) Attach(ctx context.Context) (*rfb.Conn, context.Context, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

// failingSource is a session that has given up.
type failingSource struct{ err error }

func (s failingSource) Attach(context.Context) (*rfb.Conn, context.Context, error) {
	return nil, nil, s.err
}

// waitFor polls cond until it holds or the test gives up.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestViewerClosesPromptlyWhileDisconnected exercises Run itself, because the
// bug it guards against is in the loop: with no connection to read from, the
// old design had no window either, so there was nothing to close.
func TestViewerClosesPromptlyWhileDisconnected(t *testing.T) {
	be := newFakeBackend(800, 600)
	v := New(be, Config{Width: 800, Height: 600, Title: "ns/vm"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- v.Run(ctx, blockingSource{}) }()

	waitFor(t, "the window to open", func() bool { return be.presentCount() > 0 })
	be.push(EventQuit{})

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("closing the window returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing the window did not end the viewer")
	}
	if be.closedCount() == 0 {
		t.Fatal("the backend was not closed")
	}
}

// TestViewerShowsFatalFailureThenExits covers auth failures and not-a-VM: the
// reason has to reach the user, and then the process has to end.
func TestViewerShowsFatalFailureThenExits(t *testing.T) {
	be := newFakeBackend(640, 480)
	// A negative linger exits as soon as the failure has been drawn once,
	// which keeps the test honest about ordering without making it slow.
	v := New(be, Config{Width: 640, Height: 480, FailureLinger: -1})

	fatal := errors.New("workspace is not a VM")
	err := v.Run(t.Context(), failingSource{err: fatal})
	if !errors.Is(err, fatal) {
		t.Fatalf("Run returned %v, want %v", err, fatal)
	}
	if status, detail := v.Status(); status != StatusFailed || detail != fatal.Error() {
		t.Fatalf("status = %v/%q, want failed with the reason", status, detail)
	}
	if _, ov := be.lastFrame(t); ov.Rect.Empty() {
		t.Fatal("the failure was never drawn")
	}
	if len(be.ovUploads) == 0 {
		t.Fatal("no overlay was rasterised for the failure")
	}
	if be.closedCount() == 0 {
		t.Fatal("the window was not closed on the way out")
	}
}

// TestRunConnDrivesOneConnection is the simple case the API must not have
// made harder: one connection, no supervisor, one call.
func TestRunConnDrivesOneConnection(t *testing.T) {
	transport, _ := startFakeServer(t, 640, 480)
	be := newFakeBackend(640, 480)
	v := New(be, Config{Width: 640, Height: 480})

	cfg := v.RFBConfig(rfb.Config{})
	conn, err := rfb.NewConn(transport, cfg)
	if err != nil {
		t.Fatalf("rfb handshake: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- v.RunConn(ctx, conn) }()

	waitFor(t, "the window to open", func() bool { return be.presentCount() > 0 })
	// Keep announcing updates, the way the session's update ticker does: an
	// update that lands before the render loop has picked the connection up
	// belongs to whatever was on screen before and is deliberately dropped.
	waitFor(t, "the first frame", func() bool {
		deliver(cfg, conn)
		return be.uploadCount() > 0
	})

	be.push(EventQuit{})
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunConn returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunConn ignored the close")
	}
}

// TestViewerRunRejectsANilSource keeps the API honest about its one hard
// requirement.
func TestViewerRunRejectsANilSource(t *testing.T) {
	be := newFakeBackend(640, 480)
	v := New(be, Config{})
	if err := v.Run(t.Context(), nil); err == nil {
		t.Fatal("Run accepted a nil source")
	}
	if be.opened {
		t.Fatal("Run opened a window before validating its arguments")
	}
}

func TestSingleConnYieldsOneConnectionThenWaits(t *testing.T) {
	transport, _ := startFakeServer(t, 320, 240)
	conn, err := rfb.NewConn(transport, rfb.Config{})
	if err != nil {
		t.Fatalf("rfb handshake: %v", err)
	}

	src := SingleConn(conn)
	ctx, cancel := context.WithCancel(context.Background())
	got, connCtx, err := src.Attach(ctx)
	if err != nil || got != conn {
		t.Fatalf("Attach = %v, %v", got, err)
	}
	if connCtx != ctx {
		t.Fatal("a single connection's lifetime should be the caller's context")
	}

	// The second attach blocks: there is no second connection, and returning
	// one that is already dead would be worse than freezing the frame.
	done := make(chan error, 1)
	go func() {
		_, _, err := src.Attach(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the second Attach returned %v instead of waiting", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the second Attach returned %v after cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the second Attach ignored its context")
	}
}
