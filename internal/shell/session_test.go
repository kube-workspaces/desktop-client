// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/session"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// TestTheWindowKeepsItsSizeAcrossASession drives the whole thing: the shell
// opens a session on a workspace, and since it runs in its own window, the
// shell's window is untouched and the user's list comes back exactly as they
// left it.
func TestTheWindowKeepsItsSizeAcrossASession(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	vm := workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)
	r.api.set(func(f *fakeAPI) { f.workspaces = []kwclient.Workspace{vm} })

	r.connect = func(ctx context.Context, ws kwclient.Workspace) error {
		// In the new architecture, the connector creates a fresh backend
		// and runs the session in it. The shell's own backend (r.be) is
		// never touched.
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
	// In the new architecture, the session runs synchronously within the first
	// Step of StateSession.
	r.settle()

	if len(r.opened) != 1 {
		t.Fatalf("the connector was called %d times", len(r.opened))
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
