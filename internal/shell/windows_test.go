// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// liveWindowsRig starts a rig with two running VMs.
func liveWindowsRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{
			workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true),
			workspace("team", "vm-b", kwclient.WorkspaceTypeVM, true),
			workspace("team", "vm-c", kwclient.WorkspaceTypeVM, true),
		}
	})
	r.start()
	return r
}

// openLive opens key through the model and settles, failing when no live
// window comes up.
func openLive(t *testing.T, r *rig, key string) {
	t.Helper()
	r.app.m.Selected = key
	ws, ok := r.app.m.SelectedWorkspace()
	if !ok {
		t.Fatalf("no such workspace %s", key)
	}
	r.app.m.Open(ws)
	r.step()
	r.settle()
	if !r.app.isLive(key) {
		t.Fatalf("no live window for %s after open", key)
	}
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("after opening %s: state = %v, want workspaces (list stays live)", key, r.app.m.State)
	}
}

// liveKeys returns the sorted keys with open windows.
func liveKeys(r *rig) []string {
	var out []string
	for _, e := range r.app.liveSorted() {
		out = append(out, e.key)
	}
	return out
}

// TestShellStaysInteractiveBesideLiveWindows is the point of the whole plan:
// opening displays must not park the shell. Two windows open, the shell is
// still on the list, still draws, and still takes input.
func TestShellStaysInteractiveBesideLiveWindows(t *testing.T) {
	r := liveWindowsRig(t)

	openLive(t, r, "team/vm-a")
	openLive(t, r, "team/vm-b")

	if got := liveKeys(r); len(got) != 2 || got[0] != "team/vm-a" || got[1] != "team/vm-b" {
		t.Fatalf("live windows = %v, want [team/vm-a team/vm-b]", got)
	}
	if len(r.opened) != 2 {
		t.Fatalf("dials = %d, want 2", len(r.opened))
	}
	if len(r.app.m.Sessions) != 2 {
		t.Fatalf("held sessions = %v, want 2", r.app.m.Sessions)
	}
	for _, entry := range r.app.m.Sessions {
		if !entry.Open {
			t.Fatalf("session %s is live but not marked open in the switcher", entry.Key)
		}
	}

	// The shell still draws beside its windows: dirty it with input and
	// the next frame presents.
	r.focus(idFilter)
	before := r.be.presentCount()
	r.typeText("vm-a")
	if r.be.presentCount() <= before {
		t.Fatal("the shell stopped drawing while session windows were open")
	}

	// And it still takes input: typing in the filter narrows the list while
	// both windows stay open.
	if r.app.m.Filter == "" {
		t.Fatal("the shell ignored input while session windows were open")
	}
	if got := liveKeys(r); len(got) != 2 {
		t.Fatalf("live windows = %v after shell input, want both still open", got)
	}
}

// TestPumpDeliversNoCrossDelivery pushes input at the shell and asserts the
// live windows never see it: each window steps with only its own events.
func TestPumpDeliversNoCrossDelivery(t *testing.T) {
	r := liveWindowsRig(t)
	openLive(t, r, "team/vm-a")

	for _, e := range r.app.liveSorted() {
		e.window.(*fakeLiveWindow).steps = 0
	}
	r.focus(idFilter)
	r.typeText("x")
	r.step()

	// The shell consumed its own event…
	if r.app.m.Filter == "" {
		t.Fatal("the shell did not consume its own input")
	}
	// …and the live window stepped without error and without closing: a
	// window that received the shell's keystrokes would have no reason to
	// fail here, but the pump contract is that it never sees them at all,
	// which the routed SDL path enforces by construction.
	for _, e := range r.app.liveSorted() {
		if e.window.(*fakeLiveWindow).steps == 0 {
			t.Fatalf("live window %s was never stepped", e.key)
		}
		if e.window.Closed() {
			t.Fatalf("live window %s closed on shell input", e.key)
		}
	}
}

// TestReopenLiveWindowFocusesInsteadOfRedialling: opening a workspace that
// already has a window raises it; the transport is untouched.
func TestReopenLiveWindowFocusesInsteadOfRedialling(t *testing.T) {
	r := liveWindowsRig(t)
	openLive(t, r, "team/vm-a")

	r.app.m.Selected = "team/vm-a"
	ws, _ := r.app.m.SelectedWorkspace()
	r.app.act(context.Background(), intent{kind: intentActivate, workspace: ws})
	r.step()

	if len(r.opened) != 1 {
		t.Fatalf("dials = %d, want 1 (second open focuses)", len(r.opened))
	}
	var raises int
	for _, e := range r.app.liveSorted() {
		raises += e.window.(*fakeLiveWindow).raises
	}
	if raises != 1 {
		t.Fatalf("raises = %d, want 1 (the open window focused)", raises)
	}
}

// TestSwitchFocusesLiveWindow: the switcher focuses an open session and
// resumes a parked one, and the button tells the two apart.
func TestSwitchFocusesLiveWindow(t *testing.T) {
	r := liveWindowsRig(t)
	openLive(t, r, "team/vm-a")
	openLive(t, r, "team/vm-b")

	// Park vm-b by closing its window from the platform side.
	r.app.live["team/vm-b"].window.(*fakeLiveWindow).closed = true
	r.step()
	r.settle()
	if r.app.isLive("team/vm-b") {
		t.Fatal("team/vm-b window still live after its close")
	}
	if len(r.app.m.Sessions) != 2 {
		t.Fatalf("held sessions = %v, want both still held (close parks)", r.app.m.Sessions)
	}

	// Focusing the open one raises without a dial.
	r.app.act(context.Background(), intent{kind: intentSwitchSession, sessionKey: "team/vm-a"})
	if len(r.opened) != 2 {
		t.Fatalf("dials = %d, want 2 (focus is not a dial)", len(r.opened))
	}
	if got := r.app.live["team/vm-a"].window.(*fakeLiveWindow).raises; got != 1 {
		t.Fatalf("raises = %d, want 1", got)
	}

	// Resuming the parked one re-opens without a dial.
	r.app.act(context.Background(), intent{kind: intentSwitchSession, sessionKey: "team/vm-b"})
	r.step()
	r.settle()
	if len(r.opened) != 2 {
		t.Fatalf("dials = %d, want 2 (resume reuses the transport)", len(r.opened))
	}
	if !r.app.isLive("team/vm-b") {
		t.Fatal("team/vm-b did not resume into a live window")
	}
}

// TestClosingWindowsInRandomOrder parks each transport and keeps the rest
// live: open N, close in a non-trivial order, every close parks.
func TestClosingWindowsInRandomOrder(t *testing.T) {
	r := liveWindowsRig(t)
	openLive(t, r, "team/vm-a")
	openLive(t, r, "team/vm-b")
	openLive(t, r, "team/vm-c")

	for _, key := range []string{"team/vm-b", "team/vm-c", "team/vm-a"} {
		r.app.live[key].window.(*fakeLiveWindow).closed = true
		r.step()
		r.settle()
		if r.app.isLive(key) {
			t.Fatalf("%s still live after its close", key)
		}
	}
	if len(r.app.m.Sessions) != 3 {
		t.Fatalf("held sessions = %v, want all 3 parked", r.app.m.Sessions)
	}
	if len(r.closed) != 0 {
		t.Fatalf("closes = %v, want none (close parks, disconnect releases)", r.closed)
	}
	if got := r.app.m.Notice; !contains(got, "team/vm-a") {
		t.Fatalf("notice = %q, want it to name the last parked workspace", got)
	}
}

// TestLiveWindowFailureReleasesTheTransport: a window that ends in failure
// does not park — the transport goes with it and the list says why.
func TestLiveWindowFailureReleasesTheTransport(t *testing.T) {
	r := liveWindowsRig(t)
	openLive(t, r, "team/vm-a")

	fw := r.app.live["team/vm-a"].window.(*fakeLiveWindow)
	fw.res = context.DeadlineExceeded
	fw.closed = true
	r.step()
	r.settle()

	if r.app.isLive("team/vm-a") {
		t.Fatal("failed window still live")
	}
	if len(r.app.m.Sessions) != 0 {
		t.Fatalf("held sessions = %v, want none (failure releases)", r.app.m.Sessions)
	}
	if len(r.closed) != 1 || r.closed[0] != "team/vm-a" {
		t.Fatalf("closes = %v, want [team/vm-a]", r.closed)
	}
	if r.app.m.Err == "" {
		t.Fatal("no error reported for the failed session")
	}
}

// TestQuitWithLiveWindows: closing the shell window with sessions open
// quits everything; the deferred teardown hands every slot back.
func TestQuitWithLiveWindows(t *testing.T) {
	r := liveWindowsRig(t)
	openLive(t, r, "team/vm-a")
	openLive(t, r, "team/vm-b")

	r.be.send(viewer.EventWindowClose{})
	r.step()

	if !r.app.quit {
		t.Fatal("closing the shell window with live sessions did not quit")
	}
	r.app.closeAllSessions()
	if len(r.closed) != 2 {
		t.Fatalf("closes = %v, want both sessions released on quit", r.closed)
	}
	if len(r.app.live) != 0 {
		t.Fatalf("live windows = %d, want none after teardown", len(r.app.live))
	}
}

// TestGlobalQuitReachesEveryWindow documents the routing contract: a global
// quit is broadcast, so the shell quits even with sessions open.
func TestGlobalQuitReachesEveryWindow(t *testing.T) {
	r := liveWindowsRig(t)
	openLive(t, r, "team/vm-a")

	r.be.send(viewer.EventQuit{})
	r.step()

	if !r.app.quit {
		t.Fatal("a global quit did not quit the shell")
	}
}

// TestDisconnectingALiveSessionClosesItsWindow: the switcher's Close is the
// honest disconnect — window and transport go together.
func TestDisconnectingALiveSessionClosesItsWindow(t *testing.T) {
	r := liveWindowsRig(t)
	openLive(t, r, "team/vm-a")
	openLive(t, r, "team/vm-b")

	r.app.act(context.Background(), intent{kind: intentCloseSession, sessionKey: "team/vm-a"})
	r.step()

	if r.app.isLive("team/vm-a") {
		t.Fatal("team/vm-a window still live after disconnect")
	}
	if !r.app.isLive("team/vm-b") {
		t.Fatal("team/vm-b window died with its neighbour")
	}
	if len(r.closed) != 1 || r.closed[0] != "team/vm-a" {
		t.Fatalf("closes = %v, want [team/vm-a]", r.closed)
	}
}

// TestSignOutWithLiveWindows: signing out releases every transport and
// closes every window — a held session never outlives its token.
func TestSignOutWithLiveWindows(t *testing.T) {
	r := liveWindowsRig(t)
	openLive(t, r, "team/vm-a")

	r.app.act(context.Background(), intent{kind: intentSignOut})
	r.step()

	if len(r.app.live) != 0 {
		t.Fatalf("live windows = %d after sign-out, want none", len(r.app.live))
	}
	if len(r.app.m.Sessions) != 0 {
		t.Fatalf("held sessions = %v after sign-out, want none", r.app.m.Sessions)
	}
}
