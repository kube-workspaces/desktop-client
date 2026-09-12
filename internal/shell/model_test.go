// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

var testNow = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// TestStateMachineHappyPath walks the journey the whole client exists for:
// nothing configured, a server, a sign-in, a list, a session, and back to the
// list — never to an exit.
func TestStateMachineHappyPath(t *testing.T) {
	var m Model

	m.NeedServer("")
	if m.State != StateServer {
		t.Fatalf("state = %v, want %v", m.State, StateServer)
	}

	m.ServerReady("https://kw.example.com", false, &kwclient.AuthConfig{
		Enabled:   true,
		LocalAuth: kwclient.LocalAuthConfig{Enabled: true},
	}, true)
	if m.State != StateLogin {
		t.Fatalf("after probing the server, state = %v, want %v", m.State, StateLogin)
	}
	if !m.CanUseLocalAuth() || !m.CanUseBrowserAuth() {
		t.Fatal("an instance offering both methods should advertise both")
	}

	m.SignedIn(&kwclient.Identity{Authenticated: true, AuthEnabled: true, Email: "u@example.com"})
	if m.State != StateWorkspaces {
		t.Fatalf("after signing in, state = %v, want %v", m.State, StateWorkspaces)
	}

	vm := workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)
	m.WorkspacesLoaded([]kwclient.Workspace{vm}, testNow)
	if m.Selected != "team/vm-a" {
		t.Fatalf("selection = %q, want the only connectable workspace", m.Selected)
	}

	m.Open(vm)
	if m.State != StateSession || m.Opening.Name != "vm-a" {
		t.Fatalf("after opening, state = %v opening = %q", m.State, m.Opening.Name)
	}

	m.SessionEnded(nil)
	if m.State != StateWorkspaces {
		t.Fatalf("after a session, state = %v; the app must never exit into nothing", m.State)
	}
	if m.Err != "" {
		t.Fatalf("a clean disconnect reported an error: %q", m.Err)
	}
	if m.Notice == "" {
		t.Fatal("a clean disconnect said nothing at all")
	}
	if m.Opening.Name != "" {
		t.Fatal("the session's workspace was left behind on the model")
	}
}

func TestSessionFailureReturnsToTheList(t *testing.T) {
	m := Model{State: StateWorkspaces}
	m.Open(workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true))
	m.SessionEnded(errors.New("dial tcp: connection refused"))

	if m.State != StateWorkspaces {
		t.Fatalf("a failed session left the shell in %v", m.State)
	}
	if m.Err == "" {
		t.Fatal("a failed session failed silently")
	}
	if want := "team/vm-a"; m.Err[:len(want)] != want {
		t.Fatalf("the error does not name the workspace: %q", m.Err)
	}
}

// TestExpiredSessionRoutesToLogin is the rule that keeps the client honest:
// whatever screen a 401 arrives on, the user ends up somewhere they can do
// something about it.
func TestExpiredSessionRoutesToLogin(t *testing.T) {
	unauthorized := fmt.Errorf("listing: %w", kwclient.ErrUnauthorized)

	m := Model{State: StateWorkspaces, Identity: &kwclient.Identity{Email: "u@example.com"}}
	m.Workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	m.Selected = "team/vm-a"
	m.Fail(unauthorized)

	if m.State != StateLogin {
		t.Fatalf("a 401 on the list left the shell in %v", m.State)
	}
	if m.Identity != nil || m.Workspaces != nil || m.Selected != "" {
		t.Fatal("the expired session's data was not cleared")
	}
	if m.Err == "" {
		t.Fatal("the user was not told why they are back at the login screen")
	}

	// A session that ends with a 401 does the same.
	m = Model{State: StateWorkspaces}
	m.Open(workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true))
	m.SessionEnded(unauthorized)
	if m.State != StateLogin {
		t.Fatalf("a 401 from a session left the shell in %v", m.State)
	}

	// On the login screen itself a 401 is just a message: routing there from
	// there would erase the reason the user is looking at.
	m = Model{State: StateLogin}
	m.Fail(fmt.Errorf("%w", kwclient.ErrInvalidCredentials))
	if m.State != StateLogin || m.Err == "" {
		t.Fatalf("a rejected password produced state=%v err=%q", m.State, m.Err)
	}
}

func TestFailIsANoOpForNil(t *testing.T) {
	m := Model{State: StateWorkspaces, Busy: true, BusyText: "Refreshing"}
	m.Fail(nil)
	if m.State != StateWorkspaces || !m.Busy {
		t.Fatal("Fail(nil) changed the model")
	}
}

func TestSignOutKeepsTheInstance(t *testing.T) {
	m := Model{
		State:    StateWorkspaces,
		Server:   "https://kw.example.com",
		Identity: &kwclient.Identity{Email: "u@example.com"},
		Auth:     &kwclient.AuthConfig{Enabled: true},
	}
	m.SignOut("You are signed out.")

	if m.State != StateLogin {
		t.Fatalf("sign out went to %v", m.State)
	}
	if m.Server == "" || m.Auth == nil {
		t.Fatal("signing out forgot the instance; the user would have to retype the server URL")
	}
	if m.Identity != nil {
		t.Fatal("signing out kept the identity")
	}
	if m.Notice == "" {
		t.Fatal("signing out said nothing")
	}
}

// TestRefreshPreservesTheSelection is the promise the auto-refresh makes: a
// list that reorders under the user must not move the cursor onto a different
// workspace between them deciding to press Enter and pressing it.
func TestRefreshPreservesTheSelection(t *testing.T) {
	var m Model
	a := workspace("team", "alpha", kwclient.WorkspaceTypeVM, true)
	b := workspace("team", "beta", kwclient.WorkspaceTypeVM, true)
	c := workspace("team", "gamma", kwclient.WorkspaceTypeVM, false)

	m.WorkspacesLoaded([]kwclient.Workspace{a, b, c}, testNow)
	m.Selected = "team/beta"

	// beta stops, gamma starts: both rows move, and the selection does not.
	b.Stopped, b.ReadyReplicas = true, 0
	c.Stopped, c.ReadyReplicas = false, 1
	m.WorkspacesLoaded([]kwclient.Workspace{c, b, a}, testNow.Add(5*time.Second))
	if m.Selected != "team/beta" {
		t.Fatalf("the selection moved to %q after a refresh", m.Selected)
	}

	// When the selected workspace goes away, the cursor lands somewhere
	// sensible rather than nowhere.
	m.WorkspacesLoaded([]kwclient.Workspace{a, c}, testNow.Add(10*time.Second))
	if m.Selected != "team/alpha" && m.Selected != "team/gamma" {
		t.Fatalf("after the selection was deleted, it is %q", m.Selected)
	}
	if ws, ok := m.SelectedWorkspace(); !ok || !ws.Running() {
		t.Fatal("the fallback selection should prefer a connectable workspace")
	}

	// An empty list has no selection at all.
	m.WorkspacesLoaded(nil, testNow.Add(15*time.Second))
	if m.Selected != "" {
		t.Fatalf("an empty list kept the selection %q", m.Selected)
	}
	if _, ok := m.SelectedWorkspace(); ok {
		t.Fatal("an empty list resolved a selection")
	}
}

func TestWorkspacesAreSortedConnectableFirst(t *testing.T) {
	var m Model
	m.WorkspacesLoaded([]kwclient.Workspace{
		workspace("z", "zulu", kwclient.WorkspaceTypeVM, false),
		workspace("b", "bravo", kwclient.WorkspaceTypeVM, true),
		workspace("a", "alpha", kwclient.WorkspaceTypeVM, false),
		workspace("a", "amber", kwclient.WorkspaceTypeVM, true),
	}, testNow)

	var keys []string
	for _, ws := range m.Workspaces {
		keys = append(keys, ws.Key())
	}
	want := []string{"a/amber", "b/bravo", "a/alpha", "z/zulu"}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("order = %v, want %v", keys, want)
		}
	}
}

func TestFilter(t *testing.T) {
	var m Model
	m.WorkspacesLoaded([]kwclient.Workspace{
		workspace("team-a", "cf-debian-gnome-vm-0", kwclient.WorkspaceTypeVM, true),
		workspace("team-b", "code-server", kwclient.WorkspaceTypeContainer, true),
		workspace("team-b", "scratchpad", kwclient.WorkspaceTypeScratch, false),
	}, testNow)

	tests := []struct {
		filter string
		want   int
	}{
		{"", 3},
		{"  ", 3},
		{"debian", 1},
		{"DEBIAN", 1},
		{"team-b", 2},
		{"vm", 1},
		{"container", 1},
		{"nothing-matches-this", 0},
	}
	for _, tt := range tests {
		m.Filter = tt.filter
		if got := len(m.Filtered()); got != tt.want {
			t.Errorf("filter %q matched %d workspaces, want %d", tt.filter, got, tt.want)
		}
	}
}

func TestSelectedIndex(t *testing.T) {
	rows := []kwclient.Workspace{
		workspace("a", "one", kwclient.WorkspaceTypeVM, true),
		workspace("a", "two", kwclient.WorkspaceTypeVM, true),
	}
	if got := SelectedIndex(rows, "a/two"); got != 1 {
		t.Fatalf("SelectedIndex = %d, want 1", got)
	}
	if got := SelectedIndex(rows, "a/three"); got != -1 {
		t.Fatalf("SelectedIndex of a missing key = %d, want -1", got)
	}
	if got := SelectedIndex(nil, "a/one"); got != -1 {
		t.Fatalf("SelectedIndex over no rows = %d, want -1", got)
	}
}

func TestAuthDisabledInstance(t *testing.T) {
	var m Model
	m.ServerReady("https://kw.example.com", false, &kwclient.AuthConfig{Enabled: false}, false)
	if !m.AuthDisabled() {
		t.Fatal("an instance with auth off was not recognised")
	}
	if m.CanUseLocalAuth() || m.CanUseBrowserAuth() {
		t.Fatal("an instance with auth off offered a sign-in method")
	}
	if m.Notice == "" {
		t.Fatal("the user was not told why the login screen is empty")
	}
}

func TestStatusText(t *testing.T) {
	tests := []struct {
		name string
		ws   kwclient.Workspace
		want string
	}{
		{"running", kwclient.Workspace{ReadyReplicas: 1}, "running"},
		{"stopped", kwclient.Workspace{Stopped: true}, "stopped"},
		{"stopped wins over ready", kwclient.Workspace{Stopped: true, ReadyReplicas: 1}, "stopped"},
		{"starting", kwclient.Workspace{}, "starting"},
		{
			"starting with a reason",
			kwclient.Workspace{ContainerState: &kwclient.ContainerState{Reason: "ImagePullBackOff"}},
			"starting: ImagePullBackOff",
		},
	}
	for _, tt := range tests {
		if got := StatusText(tt.ws); got != tt.want {
			t.Errorf("StatusText(%s) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestStateString(t *testing.T) {
	for _, s := range []State{StateServer, StateLogin, StateWorkspaces, StateSession} {
		if s.String() == "unknown" {
			t.Fatalf("state %d has no name", s)
		}
	}
	if State(99).String() != "unknown" {
		t.Fatal("an out-of-range state should be named as such")
	}
}
