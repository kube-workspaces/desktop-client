// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"context"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

// These tests are about what the render loop does when nothing is happening.
// The loop used to answer "wake up every two milliseconds and find out", which
// costs a measurable slice of a core for a window that is showing a frozen
// frame under a status message. It now blocks in the backend, which moves the
// burden onto the wake: anything that changes what should be on screen has to
// say so, or the frame is late.

// --- the contract WaitEvents has to satisfy ---------------------------------

func TestWaitEventsHonoursTheTimeout(t *testing.T) {
	be := newFakeBackend(800, 600)

	start := time.Now()
	events := be.WaitEvents(nil, 40*time.Millisecond)
	elapsed := time.Since(start)

	if len(events) != 0 {
		t.Fatalf("an idle wait produced %v", events)
	}
	// Timers fire late, never early; a wait that returned early is either
	// spinning or ignoring its argument.
	if elapsed < 30*time.Millisecond {
		t.Fatalf("a 40ms wait returned after %v", elapsed)
	}
}

func TestWaitEventsReturnsQueuedEventsAtOnce(t *testing.T) {
	be := newFakeBackend(800, 600)
	be.push(EventQuit{})

	start := time.Now()
	events := be.WaitEvents(nil, time.Minute)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a wait with an event already queued took %v", elapsed)
	}
	if len(events) != 1 {
		t.Fatalf("drained %v, want the queued quit", events)
	}
}

func TestWaitEventsReturnsWhenWoken(t *testing.T) {
	be := newFakeBackend(800, 600)

	go func() {
		time.Sleep(10 * time.Millisecond)
		be.Wake()
	}()

	start := time.Now()
	events := be.WaitEvents(nil, time.Minute)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("a woken wait took %v", elapsed)
	}
	// A wake is not input: it ends the wait and produces nothing, so a loop
	// that treats the returned batch as user activity is not misled.
	if len(events) != 0 {
		t.Fatalf("a wake produced %v", events)
	}
	if be.wakeCount() != 1 {
		t.Fatalf("the backend recorded %d wakes, want 1", be.wakeCount())
	}
}

// TestWakeIsNotLostBeforeTheWait is the property that makes the wake an
// optimisation rather than a race: the loop decides to wait *after* the
// goroutine decided to wake it, and must not then sleep through the news.
func TestWakeIsNotLostBeforeTheWait(t *testing.T) {
	be := newFakeBackend(800, 600)
	be.Wake()

	start := time.Now()
	be.WaitEvents(nil, time.Minute)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a wake delivered before the wait was lost: waited %v", elapsed)
	}
}

// --- what the viewer asks for ------------------------------------------------

func TestViewerIdleTimeout(t *testing.T) {
	now := time.Now()

	// The heartbeat repaint is the deadline in the common case, and every
	// other timer can only pull the wait earlier.
	v := New(newFakeBackend(800, 600), Config{})
	v.presentDue = now.Add(500 * time.Millisecond)
	v.statsDue = now.Add(time.Second)
	if got := v.idleTimeout(now); got != 500*time.Millisecond {
		t.Fatalf("idle timeout = %v, want the heartbeat's 500ms", got)
	}

	v.statsDue = now.Add(100 * time.Millisecond)
	if got := v.idleTimeout(now); got != 100*time.Millisecond {
		t.Fatalf("idle timeout = %v, want the earlier stats deadline", got)
	}

	// A deadline in the past is work owed now.
	v.statsDue = now.Add(-time.Second)
	if got := v.idleTimeout(now); got != 0 {
		t.Fatalf("idle timeout = %v with an overdue deadline, want 0", got)
	}

	// Anything already in the inbox means the same, whether or not a wake
	// happened to survive: this is what keeps a frame that landed between the
	// redraw and the wait from sitting there until the heartbeat.
	v.statsDue = now.Add(time.Second)
	v.markPresent()
	if got := v.idleTimeout(now); got != 0 {
		t.Fatalf("idle timeout = %v with a present pending, want 0", got)
	}
}

// TestViewerIdleTimeoutIgnoresConnectionTimersWithNoConnection: the clipboard
// poll and the guest-resize debounce are only reachable through a connection,
// so waking for them while there is none is a wake for nothing.
func TestViewerIdleTimeoutIgnoresConnectionTimersWithNoConnection(t *testing.T) {
	now := time.Now()
	v := New(newFakeBackend(800, 600), Config{})
	v.presentDue = now.Add(500 * time.Millisecond)
	v.statsDue = now.Add(time.Second)
	v.clipDue = now.Add(10 * time.Millisecond)
	v.resizePending, v.resizeDue = true, now.Add(20*time.Millisecond)

	if got := v.idleTimeout(now); got != 500*time.Millisecond {
		t.Fatalf("idle timeout = %v with no connection, want the heartbeat's 500ms", got)
	}
}

// --- the loop itself ---------------------------------------------------------

// TestViewerRunIdlesWithoutSpinning is the fix, stated as a test: a session
// with nothing to show must not be draining the event queue hundreds of times
// a second to discover it.
func TestViewerRunIdlesWithoutSpinning(t *testing.T) {
	be := newFakeBackend(800, 600)
	v := New(be, Config{Width: 800, Height: 600, Title: "ns/vm"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- v.Run(ctx, blockingSource{}) }()

	waitFor(t, "the window to open", func() bool { return be.presentCount() > 0 })

	const idle = 1200 * time.Millisecond
	before := be.pollCount()
	time.Sleep(idle)
	polls := be.pollCount() - before

	// The heartbeat repaint runs twice a second and the stats tick once, so a
	// correct loop wakes about four times in 1.2s. The old 2ms poll would be
	// around 600. Twenty leaves room for a slow machine without leaving room
	// for a spin.
	if polls > 20 {
		t.Fatalf("an idle viewer polled %d times in %v", polls, idle)
	}
	// It is asleep, not dead: the heartbeat is what keeps a frozen frame on
	// screen after a compositor loses the window contents.
	if polls == 0 {
		t.Fatal("an idle viewer stopped polling altogether")
	}

	be.push(EventQuit{})
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an idle viewer did not notice the window closing")
	}
}

// TestViewerRunWakesOnADecodedFrame is the other half: the loop may sleep, but
// not through a frame. The RFB read loop is on another goroutine and the
// window produces no event when a frame arrives, so without the wake the frame
// would wait for the heartbeat — a session at 60fps would present at 2.
func TestViewerRunWakesOnADecodedFrame(t *testing.T) {
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
	// An update that lands before the loop has picked the connection up
	// belongs to whatever was on screen before and is deliberately dropped,
	// so keep announcing until the first one sticks.
	waitFor(t, "the first frame", func() bool {
		deliver(cfg, conn)
		return be.uploadCount() > 0
	})

	wakesBefore := be.wakeCount()
	uploadsBefore := be.uploadCount()
	deliver(cfg, conn, rfb.Rect{X: 0, Y: 0, Width: 32, Height: 32})

	// The decode is what has to reach the backend, and it has to do it
	// without the user touching anything. Well under the 500ms heartbeat,
	// which is what a broken wake would fall back to.
	deadline := time.Now().Add(300 * time.Millisecond)
	for be.uploadCount() == uploadsBefore && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if be.uploadCount() == uploadsBefore {
		t.Fatal("a decoded frame did not reach the window; the loop slept through it")
	}
	if be.wakeCount() <= wakesBefore {
		t.Fatal("the RFB callback did not wake the render loop")
	}

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

// TestViewerRunWakesOnCancellation: with the loop blocking in the backend
// rather than in a select, a cancelled context is just another thing that has
// to be turned into an event.
func TestViewerRunWakesOnCancellation(t *testing.T) {
	be := newFakeBackend(800, 600)
	v := New(be, Config{Width: 800, Height: 600})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- v.Run(ctx, blockingSource{}) }()
	waitFor(t, "the window to open", func() bool { return be.presentCount() > 0 })

	start := time.Now()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("a cancelled viewer returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled viewer kept running")
	}
	// The heartbeat would have got there eventually; the point is that it did
	// not have to.
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("cancellation took %v to be noticed", elapsed)
	}
}
