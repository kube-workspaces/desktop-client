// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

func sessionsRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{
			workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true),
			workspace("team", "vm-b", kwclient.WorkspaceTypeVM, true),
		}
	})
	r.start()
	return r
}

func openWorkspace(t *testing.T, r *rig, key string) {
	t.Helper()
	r.app.m.Selected = key
	if ws, ok := r.app.m.SelectedWorkspace(); ok {
		r.app.m.Open(ws)
	} else {
		t.Fatalf("no such workspace %s", key)
	}
	r.step()
	r.settle()
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("after %s: state = %v, want workspaces (parked)", key, r.app.m.State)
	}
}

// TestTwoSessionsStayConnected: opening two workspaces dials twice and holds
// both after their windows close.
func TestTwoSessionsStayConnected(t *testing.T) {
	r := sessionsRig(t)

	openWorkspace(t, r, "team/vm-a")
	openWorkspace(t, r, "team/vm-b")

	if len(r.opened) != 2 {
		t.Fatalf("dials = %d, want 2", len(r.opened))
	}
	if len(r.closed) != 0 {
		t.Fatalf("closes = %v, want none held", r.closed)
	}
	if len(r.app.m.Sessions) != 2 {
		t.Fatalf("sessions = %v, want 2 held", r.app.m.Sessions)
	}
}

// TestReopenResumesWithoutRedial: opening a held workspace re-attaches the
// same transport instead of dialling again.
func TestReopenResumesWithoutRedial(t *testing.T) {
	r := sessionsRig(t)

	openWorkspace(t, r, "team/vm-a")
	openWorkspace(t, r, "team/vm-a")

	if len(r.opened) != 1 {
		t.Fatalf("dials = %d, want 1 (second open resumes)", len(r.opened))
	}
	if len(r.app.m.Sessions) != 1 {
		t.Fatalf("sessions = %v, want 1 held", r.app.m.Sessions)
	}
}

// TestSessionsModalSwitchResumes: switching from the modal opens the held
// session's window without a new dial.
func TestSessionsModalSwitchResumes(t *testing.T) {
	r := sessionsRig(t)

	openWorkspace(t, r, "team/vm-a")
	openWorkspace(t, r, "team/vm-b")

	r.focus(idSessions)
	r.clickFocused()
	r.step()
	if !r.app.m.SessionList {
		t.Fatal("sessions modal did not open")
	}

	r.app.act(context.Background(), intent{kind: intentSwitchSession, sessionKey: "team/vm-a"})
	r.step()
	r.settle()

	if r.app.m.State != StateWorkspaces {
		t.Fatalf("after switch: state = %v, want workspaces (parked again)", r.app.m.State)
	}
	if len(r.opened) != 2 {
		t.Fatalf("dials = %d, want 2 (switch resumes)", len(r.opened))
	}
	if r.app.m.Opening.Name != "" {
		t.Fatalf("opening = %+v, want cleared after park", r.app.m.Opening)
	}
}

// TestSessionsModalCloseDisconnects: closing from the modal releases the
// transport; the rest stay held.
func TestSessionsModalCloseDisconnects(t *testing.T) {
	r := sessionsRig(t)

	openWorkspace(t, r, "team/vm-a")
	openWorkspace(t, r, "team/vm-b")

	r.app.act(context.Background(), intent{kind: intentCloseSession, sessionKey: "team/vm-a"})
	r.step()

	if len(r.closed) != 1 || r.closed[0] != "team/vm-a" {
		t.Fatalf("closes = %v, want [team/vm-a]", r.closed)
	}
	if len(r.app.m.Sessions) != 1 || r.app.m.Sessions[0].Key != "team/vm-b" {
		t.Fatalf("sessions = %v, want only vm-b held", r.app.m.Sessions)
	}
}

// TestSignOutReleasesSessions: signing out hands every held slot back.
func TestSignOutReleasesSessions(t *testing.T) {
	r := sessionsRig(t)

	openWorkspace(t, r, "team/vm-a")
	openWorkspace(t, r, "team/vm-b")

	r.app.act(context.Background(), intent{kind: intentSignOut})
	r.step()

	if len(r.closed) != 2 {
		t.Fatalf("closes = %v, want both sessions released", r.closed)
	}
	if len(r.app.m.Sessions) != 0 {
		t.Fatalf("sessions = %v, want none held", r.app.m.Sessions)
	}
}

// TestParkNoticeNamesWorkspace: a parked window tells the user the connection
// is still up, naming the workspace.
func TestParkNoticeNamesWorkspace(t *testing.T) {
	r := sessionsRig(t)

	openWorkspace(t, r, "team/vm-a")

	if got := r.app.m.Notice; got == "" || !contains(got, "team/vm-a") {
		t.Fatalf("notice = %q, want it to name the parked workspace", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
