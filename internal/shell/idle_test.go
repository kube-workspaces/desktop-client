// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// These tests are about the shell doing nothing. A workspace browser sitting
// on a list has no animation and no frame rate: it repaints when the user acts
// or when data arrives. The loop used to poll for that at 125Hz, which cost a
// couple of percent of a core forever; it now blocks, which means everything
// that used to be discovered by polling has to announce itself instead.

func TestWaitEventsHonoursTheTimeout(t *testing.T) {
	be := newFakeBackend(1280, 800)

	start := time.Now()
	events := be.WaitEvents(nil, 40*time.Millisecond)
	elapsed := time.Since(start)

	if len(events) != 0 {
		t.Fatalf("an idle wait produced %v", events)
	}
	if elapsed < 30*time.Millisecond {
		t.Fatalf("a 40ms wait returned after %v", elapsed)
	}
}

func TestWaitEventsReturnsWhenWoken(t *testing.T) {
	be := newFakeBackend(1280, 800)
	go func() {
		time.Sleep(10 * time.Millisecond)
		be.Wake()
	}()

	start := time.Now()
	if events := be.WaitEvents(nil, time.Minute); len(events) != 0 {
		t.Fatalf("a wake produced %v", events)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("a woken wait took %v", elapsed)
	}

	// And a wake that arrives before the wait does is still there when it
	// starts, which is what stops a result racing the loop into its sleep.
	be.Wake()
	start = time.Now()
	be.WaitEvents(nil, time.Minute)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a wake delivered before the wait was lost: waited %v", elapsed)
	}
}

func TestAppIdleTimeout(t *testing.T) {
	now := time.Now()
	r := newRig(nil, "")
	a := r.app

	// Nothing pending and nothing scheduled: sleep until something happens,
	// bounded only by the backstop.
	a.m.State = StateServer
	a.dirty = false
	if got := a.idleTimeout(now); got != maxIdleWait {
		t.Fatalf("idle timeout = %v on a static screen, want %v", got, maxIdleWait)
	}

	// A frame is owed, so there is nothing to wait for.
	a.dirty = true
	if got := a.idleTimeout(now); got != 0 {
		t.Fatalf("idle timeout = %v with a frame owed, want 0", got)
	}
	a.dirty = false

	// A widget asked to be drawn again — a caret, a spinner — and that is a
	// real deadline.
	a.repaintAt = now.Add(120 * time.Millisecond)
	if got := a.idleTimeout(now); got != 120*time.Millisecond {
		t.Fatalf("idle timeout = %v, want the deferred repaint's 120ms", got)
	}
	a.repaintAt = time.Time{}

	// The list refresh is the other deadline, and only on the screen that has
	// a list.
	a.m.State = StateWorkspaces
	a.opts.RefreshInterval = 5 * time.Second
	a.nextRefresh = now.Add(300 * time.Millisecond)
	if got := a.idleTimeout(now); got != 300*time.Millisecond {
		t.Fatalf("idle timeout = %v, want the refresh at 300ms", got)
	}
	// Capped, so that a missed wake costs a beat rather than the window.
	a.nextRefresh = now.Add(time.Hour)
	if got := a.idleTimeout(now); got != maxIdleWait {
		t.Fatalf("idle timeout = %v with a distant refresh, want the %v cap", got, maxIdleWait)
	}
	a.nextRefresh = time.Time{}
	if got := a.idleTimeout(now); got != 0 {
		t.Fatalf("idle timeout = %v with a refresh due immediately, want 0", got)
	}

	// A session is about to take the window; making it wait first would put
	// this timeout in front of every connection.
	a.m.State = StateSession
	a.nextRefresh = now.Add(time.Hour)
	if got := a.idleTimeout(now); got != 0 {
		t.Fatalf("idle timeout = %v with a session starting, want 0", got)
	}

	// A result that landed after this iteration drained the queue is work in
	// hand, whether or not its wake survived.
	a.m.State = StateServer
	a.results <- func() {}
	if got := a.idleTimeout(now); got != 0 {
		t.Fatalf("idle timeout = %v with a result waiting, want 0", got)
	}
}

// TestBackgroundWorkWakesTheLoop is the wake that matters most in the shell:
// every screen transition is the result of an HTTP call finishing on another
// goroutine, and the window produces no event when one does.
func TestBackgroundWorkWakesTheLoop(t *testing.T) {
	r := newRig(&config.Profile{Name: "default", Server: "https://kw.example.com"}, "token")
	r.start()

	if r.app.m.State != StateWorkspaces {
		t.Fatalf("state = %v, want the workspace list", r.app.m.State)
	}
	if r.be.wakeCount() == 0 {
		t.Fatal("background work never woke the loop; every result would wait for the timeout")
	}
}

// TestRunIdlesWithoutSpinning is the fix, stated as a test, against the real
// loop rather than a hand-driven one.
func TestRunIdlesWithoutSpinning(t *testing.T) {
	r := newRig(nil, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- r.app.Run(ctx) }()

	waitFor(t, "the first frame", func() bool { return r.be.presentCount() > 0 })

	const idle = 500 * time.Millisecond
	before := r.be.pollCount()
	time.Sleep(idle)
	polls := r.be.pollCount() - before

	// A shell on the server screen has no timer at all, so the only thing
	// that can wake it in half a second is the one-second backstop — usually
	// not even that. The old 8ms poll would be around 60.
	if polls > 5 {
		t.Fatalf("an idle shell polled %d times in %v", polls, idle)
	}

	// Asleep, not deaf: a keystroke is still acted on immediately.
	presents := r.be.presentCount()
	r.be.send(ui.EventKey{Key: keysym.KeyTab, Down: true})
	waitFor(t, "the keystroke to be drawn", func() bool { return r.be.presentCount() > presents })

	r.be.send(viewer.EventQuit{})
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an idle shell did not notice the window closing")
	}
}

// TestRunWakesOnCancellation: with the loop blocking in the backend rather
// than in a select, a cancelled context has to arrive as an event.
func TestRunWakesOnCancellation(t *testing.T) {
	r := newRig(nil, "")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- r.app.Run(ctx) }()
	waitFor(t, "the first frame", func() bool { return r.be.presentCount() > 0 })

	start := time.Now()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("a cancelled shell returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled shell kept running")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("cancellation took %v to be noticed", elapsed)
	}
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
