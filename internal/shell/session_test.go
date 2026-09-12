// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/session"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// TestBorrowedBackendKeepsTheWindow is the invariant the single-window design
// rests on: the viewer's Open and Close are session lifecycle calls, not
// window lifecycle calls, and the window has to outlive both.
func TestBorrowedBackendKeepsTheWindow(t *testing.T) {
	be := newFakeBackend(1280, 800)
	if err := be.Open(viewer.WindowOptions{Title: "Kube Workspaces", Width: 1280, Height: 800}); err != nil {
		t.Fatal(err)
	}

	borrowed := &borrowedBackend{Backend: be, title: "Kube Workspaces"}
	if err := borrowed.Open(viewer.WindowOptions{Title: "team/vm-a", Width: 640, Height: 480}); err != nil {
		t.Fatal(err)
	}

	if be.opened != 1 {
		t.Fatalf("the window was opened %d times; the session created one of its own", be.opened)
	}
	if be.title != "team/vm-a" {
		t.Fatalf("the session did not get the title it asked for: %q", be.title)
	}
	// The user chose the window size; a session must not reset it.
	if w, h := be.size(); w != 1280 || h != 800 {
		t.Fatalf("the window was resized to %dx%d by a session opening", w, h)
	}

	borrowed.Close()
	if be.closed != 0 {
		t.Fatal("ending a session destroyed the window")
	}
	if be.title != "Kube Workspaces" {
		t.Fatalf("the shell's title was not restored: %q", be.title)
	}

	// Everything else is forwarded untouched: the session owns the textures
	// and the input for its duration.
	if err := borrowed.SetTextureSize(640, 480); err != nil {
		t.Fatal(err)
	}
	if be.texW != 640 || be.texH != 480 {
		t.Fatal("SetTextureSize was not forwarded")
	}
}

// TestBorrowedBackendRefusesToResizeTheWindow is the sizing rule: a session
// borrows the window, it does not get to reshape it. The viewer asks once, on
// its first connection, to fit the guest's resolution; inside the shell that
// request is declined and the guest is letterboxed instead.
func TestBorrowedBackendRefusesToResizeTheWindow(t *testing.T) {
	be := newFakeBackend(1280, 800)
	borrowed := &borrowedBackend{Backend: be, title: "Kube Workspaces"}
	if err := borrowed.Open(viewer.WindowOptions{Title: "team/vm-a"}); err != nil {
		t.Fatal(err)
	}

	// What viewer.fitWindow does on the first connection to a 1024x768 guest.
	if err := borrowed.SetSize(1024, 768); err != nil {
		t.Fatalf("a refused resize must not be an error: %v", err)
	}
	if w, h := be.size(); w != 1280 || h != 800 {
		t.Fatalf("the session resized the shell's window to %dx%d", w, h)
	}
	// And the viewer, which reads the size back rather than assuming, sees
	// the window it actually has.
	if w, h := borrowed.Size(); w != 1280 || h != 800 {
		t.Fatalf("Size reported %dx%d after a refused resize", w, h)
	}
	if len(be.sized) != 0 {
		t.Fatalf("the window was resized to %v", be.sized)
	}
}

// TestBorrowedBackendRestoresTheWindowSize covers the whole life of a session:
// the size before, during and after.
func TestBorrowedBackendRestoresTheWindowSize(t *testing.T) {
	be := newFakeBackend(1280, 800)
	borrowed := &borrowedBackend{Backend: be, title: "Kube Workspaces"}

	if err := borrowed.Open(viewer.WindowOptions{Title: "team/vm-a", Width: 1920, Height: 1080}); err != nil {
		t.Fatal(err)
	}
	if w, h := be.size(); w != 1280 || h != 800 {
		t.Fatalf("opening a session changed the window to %dx%d", w, h)
	}

	// Something other than the wrapper moves the window — a window manager
	// forcing a size, a fullscreen transition — and the session runs on at
	// that size.
	be.resize(1600, 900)
	if w, h := be.size(); w != 1600 || h != 900 {
		t.Fatalf("the window is %dx%d mid-session", w, h)
	}

	borrowed.Close()
	if w, h := be.size(); w != 1280 || h != 800 {
		t.Fatalf("the shell got its window back at %dx%d, want the 1280x800 it lent out", w, h)
	}
}

// TestBorrowedBackendKeepsAManualResize is the exception that makes the rule
// tolerable: the user dragging the window frame is a decision about the
// window, not about the session, and undoing it when the session ends would be
// the same kind of surprise the restore exists to prevent.
func TestBorrowedBackendKeepsAManualResize(t *testing.T) {
	for _, drain := range []struct {
		name string
		fn   func(b *borrowedBackend) []viewer.Event
	}{
		{"polled", func(b *borrowedBackend) []viewer.Event { return b.PollEvents(nil) }},
		{"waited", func(b *borrowedBackend) []viewer.Event { return b.WaitEvents(nil, time.Second) }},
	} {
		t.Run(drain.name, func(t *testing.T) {
			be := newFakeBackend(1280, 800)
			borrowed := &borrowedBackend{Backend: be, title: "Kube Workspaces"}
			if err := borrowed.Open(viewer.WindowOptions{Title: "team/vm-a"}); err != nil {
				t.Fatal(err)
			}

			be.userResize(1000, 700)
			events := drain.fn(borrowed)
			// The event still reaches the viewer: it has a letterbox to
			// recompute and a guest to tell.
			if len(events) != 1 {
				t.Fatalf("the resize did not reach the session: %v", events)
			}
			if _, ok := events[0].(viewer.EventResize); !ok {
				t.Fatalf("event %T reached the session, want a resize", events[0])
			}

			borrowed.Close()
			if w, h := be.size(); w != 1000 || h != 700 {
				t.Fatalf("the window is %dx%d after the session; the user's own resize was undone", w, h)
			}
		})
	}
}

// TestBorrowedBackendIgnoresAResizeBackToTheShellSize guards the corner the
// restore logic could get wrong: a window that is pushed off its size and back
// again — which is exactly what leaving fullscreen looks like — has not been
// resized by the user, and the shell's size still stands.
func TestBorrowedBackendIgnoresAResizeBackToTheShellSize(t *testing.T) {
	be := newFakeBackend(1280, 800)
	borrowed := &borrowedBackend{Backend: be, title: "Kube Workspaces"}
	if err := borrowed.Open(viewer.WindowOptions{Title: "team/vm-a"}); err != nil {
		t.Fatal(err)
	}

	be.userResize(1280, 800)
	borrowed.PollEvents(nil)
	be.resize(640, 480)

	borrowed.Close()
	if w, h := be.size(); w != 1280 || h != 800 {
		t.Fatalf("the window is %dx%d, want the shell's 1280x800 back", w, h)
	}
}

func TestBorrowedBackendRestoresFullscreen(t *testing.T) {
	be := newFakeBackend(1280, 800)
	borrowed := &borrowedBackend{Backend: be, title: "Kube Workspaces"}

	if err := borrowed.Open(viewer.WindowOptions{Title: "team/vm-a", Fullscreen: true}); err != nil {
		t.Fatal(err)
	}
	if !be.isFullscreen() {
		t.Fatal("a fullscreen session did not go fullscreen")
	}

	borrowed.Close()
	if be.isFullscreen() {
		t.Fatal("the shell inherited the session's fullscreen state")
	}
}

// TestTheWindowKeepsItsSizeAcrossASession drives the whole thing: the shell
// opens a session on a workspace, the session does to the window exactly what
// [viewer.Viewer] does — fit it to the guest, letterbox, hand it back — and
// the user's list comes back at the size they left it.
func TestTheWindowKeepsItsSizeAcrossASession(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	vm := workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)
	r.api.set(func(f *fakeAPI) { f.workspaces = []kwclient.Workspace{vm} })

	var during [2]int
	r.connect = func(context.Context, kwclient.Workspace) error {
		// What SessionConnector builds, minus the network.
		borrowed := &borrowedBackend{Backend: r.be, title: "Kube Workspaces"}
		if err := borrowed.Open(viewer.WindowOptions{Title: vm.Key()}); err != nil {
			return err
		}
		defer borrowed.Close()
		// viewer.fitWindow, on the first connection to a 1920x1080 guest.
		if err := borrowed.SetSize(1920, 1080); err != nil {
			return err
		}
		w, h := borrowed.Size()
		during = [2]int{w, h}
		return nil
	}
	r.start()

	before := [2]int{}
	before[0], before[1] = r.be.size()
	if before != [2]int{1280, 800} {
		t.Fatalf("the shell opened at %v, want 1280x800", before)
	}

	r.focus(idList)
	r.clickFocused()
	r.step()
	r.step()
	r.settle()

	if len(r.opened) != 1 {
		t.Fatalf("the connector was called %d times", len(r.opened))
	}
	if during != before {
		t.Fatalf("the window was %v during the session, want the %v it started at", during, before)
	}
	if w, h := r.be.size(); [2]int{w, h} != before {
		t.Fatalf("the window is %dx%d after the session, want %v", w, h, before)
	}
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("state after the session = %v", r.app.m.State)
	}
}

func TestViewerStatusMapping(t *testing.T) {
	tests := []struct {
		state session.State
		want  viewer.Status
		ok    bool
	}{
		{session.StateConnecting, viewer.StatusConnecting, true},
		{session.StateConnected, viewer.StatusLive, true},
		{session.StateReconnecting, viewer.StatusReconnecting, true},
		{session.StateDisplayInUse, viewer.StatusDisplayInUse, true},
		{session.StateFailed, viewer.StatusFailed, true},
		{session.StateClosed, viewer.StatusLive, false},
	}
	for _, tt := range tests {
		got, detail, ok := viewerStatus(tt.state, nil)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Fatalf("viewerStatus(%v) = (%v, %q, %t), want (%v, _, %t)", tt.state, got, detail, ok, tt.want, tt.ok)
		}
		if tt.state == session.StateFailed && detail == "" {
			t.Fatal("a failure with no error must still say something")
		}
	}
}
