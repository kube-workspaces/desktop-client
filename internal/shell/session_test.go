// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"testing"

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
	if err := borrowed.SetSize(1024, 768); err != nil {
		t.Fatal(err)
	}
	if w, _ := be.size(); w != 1024 {
		t.Fatal("SetSize was not forwarded; the viewer could not fit the guest")
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
