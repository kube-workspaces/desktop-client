// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

func savedProfile() *config.Profile {
	return &config.Profile{Name: "kw.example.com", Server: "https://kw.example.com"}
}

// TestFirstRunAsksForAServer: with nothing stored, the shell opens on the
// server screen rather than on a login form for an instance it cannot name.
func TestFirstRunAsksForAServer(t *testing.T) {
	r := newRig(nil, "")
	r.start()

	if r.app.m.State != StateServer {
		t.Fatalf("a first run opened on %v", r.app.m.State)
	}
	if r.be.presents == 0 {
		t.Fatal("nothing was drawn")
	}
}

func TestServerScreenProbesAndAdvances(t *testing.T) {
	r := newRig(nil, "")
	r.start()

	r.focus(idServer)
	r.typeText("kw.example.com")
	r.clickFocused()
	r.settle()

	if r.app.m.State != StateLogin {
		t.Fatalf("after connecting, state = %v (err %q)", r.app.m.State, r.app.m.Err)
	}
	// The scheme was supplied: nobody types https:// into a field that
	// already reads like a host name.
	if r.app.m.Server != "https://kw.example.com" {
		t.Fatalf("server = %q", r.app.m.Server)
	}
	if r.store.profile == nil || r.store.profile.Server != "https://kw.example.com" {
		t.Fatal("the instance was not remembered")
	}
	if !r.app.m.CanUseLocalAuth() {
		t.Fatal("the probe did not learn that local auth is available")
	}
}

func TestServerScreenReportsAnUnreachableInstance(t *testing.T) {
	r := newRig(nil, "")
	r.api.set(func(f *fakeAPI) {
		f.authErr = fmt.Errorf("kwclient: GET /auth/config: %w",
			&netError{msg: "dial tcp 10.0.0.1:443: connect: connection refused"})
	})
	r.start()

	r.focus(idServer)
	r.typeText("kw.example.com")
	r.clickFocused()
	r.settle()

	if r.app.m.State != StateServer {
		t.Fatalf("an unreachable instance advanced to %v", r.app.m.State)
	}
	if r.app.m.Err == "" {
		t.Fatal("an unreachable instance failed silently")
	}
	if r.app.m.Busy {
		t.Fatal("the busy state was not cleared after the failure")
	}
}

func TestEmptyServerIsRejectedWithoutARequest(t *testing.T) {
	r := newRig(nil, "")
	r.start()
	r.focus(idConnect)
	r.clickFocused()
	r.step()

	if r.app.m.Err == "" {
		t.Fatal("an empty server address was accepted")
	}
	if r.app.m.State != StateServer {
		t.Fatalf("an empty server address advanced to %v", r.app.m.State)
	}
}

// TestStoredSessionGoesStraightToTheList is what a returning user should see:
// the window they left, not a login form.
func TestStoredSessionGoesStraightToTheList(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{
			workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true),
			workspace("team", "web", kwclient.WorkspaceTypeContainer, true),
		}
	})
	r.start()

	if r.app.m.State != StateWorkspaces {
		t.Fatalf("a stored session opened on %v (err %q)", r.app.m.State, r.app.m.Err)
	}
	if r.api.Token() != "stored-token" {
		t.Fatalf("the stored token was not used: %q", r.api.Token())
	}
	if len(r.app.m.Workspaces) != 2 {
		t.Fatalf("the list holds %d workspaces", len(r.app.m.Workspaces))
	}
	if r.app.m.Identity == nil || r.app.m.Identity.Email != "user@example.com" {
		t.Fatal("the signed-in identity was not recorded")
	}
}

func TestStoredButRejectedTokenGoesToLogin(t *testing.T) {
	r := newRig(savedProfile(), "stale-token")
	r.api.set(func(f *fakeAPI) {
		f.meErr = fmt.Errorf("kwclient: GET /auth/me: %w", kwclient.ErrUnauthorized)
	})
	r.start()

	if r.app.m.State != StateLogin {
		t.Fatalf("a rejected token opened on %v", r.app.m.State)
	}
	if r.app.m.Err == "" {
		t.Fatal("the user was not told why they have to sign in")
	}
	// The login screen needs to know what to offer, so the auth config is
	// fetched on the way.
	if r.app.m.Auth == nil {
		t.Fatal("the login screen has no idea how to authenticate")
	}
}

// TestLocalSignInJourney is the full unauthenticated-to-list path through the
// real screens and the real widgets.
func TestLocalSignInJourney(t *testing.T) {
	r := newRig(savedProfile(), "")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()

	if r.app.m.State != StateLogin {
		t.Fatalf("a profile with no token opened on %v", r.app.m.State)
	}

	r.focus(idEmail)
	r.typeText("user@example.com")
	r.press(keysym.KeyTab, keysym.ModNone)
	r.typeText("hunter2")
	r.clickFocused()
	r.settle()

	if r.app.m.State != StateWorkspaces {
		t.Fatalf("signing in ended on %v (err %q)", r.app.m.State, r.app.m.Err)
	}
	if r.api.Token() != "fresh-token" {
		t.Fatalf("the issued token was not installed: %q", r.api.Token())
	}
	if r.store.token != "fresh-token" {
		t.Fatalf("the issued token was not stored: %q", r.store.token)
	}
	if r.app.passwordField.Value() != "" {
		t.Fatal("the password was left in the field after a successful sign-in")
	}
}

func TestBadPasswordStaysOnLoginAndClearsTheField(t *testing.T) {
	r := newRig(savedProfile(), "")
	r.api.set(func(f *fakeAPI) {
		f.loginErr = fmt.Errorf("kwclient: POST /auth/login/local: http 401: %w", kwclient.ErrInvalidCredentials)
	})
	r.start()

	r.focus(idEmail)
	r.typeText("user@example.com")
	r.press(keysym.KeyTab, keysym.ModNone)
	r.typeText("wrong")
	r.clickFocused()
	r.settle()

	if r.app.m.State != StateLogin {
		t.Fatalf("a rejected password moved to %v", r.app.m.State)
	}
	if !strings.Contains(r.app.m.Err, "not accepted") {
		t.Fatalf("the error shown was %q", r.app.m.Err)
	}
	if r.app.passwordField.Value() != "" {
		t.Fatal("the rejected password was left in the field")
	}
	if r.app.emailField.Value() != "user@example.com" {
		t.Fatal("the email was cleared too; the user would have to retype it")
	}
	if r.store.token != "" {
		t.Fatal("a failed sign-in stored a token")
	}
}

func TestMissingCredentialsAreRejectedLocally(t *testing.T) {
	r := newRig(savedProfile(), "")
	r.start()

	r.focus(idSignIn)
	r.clickFocused()
	r.step()
	if r.app.m.Err == "" {
		t.Fatal("an empty email was sent to the server")
	}

	r.focus(idEmail)
	r.typeText("user@example.com")
	r.focus(idSignIn)
	r.clickFocused()
	r.step()
	if !strings.Contains(r.app.m.Err, "password") {
		t.Fatalf("an empty password produced %q", r.app.m.Err)
	}
}

// TestBrowserSignIn covers the OIDC path: the loopback flow, the waiting
// screen, and the cancel that has to work while it is up.
func TestBrowserSignIn(t *testing.T) {
	r := newRig(savedProfile(), "")
	gate := make(chan struct{})
	r.api.set(func(f *fakeAPI) {
		f.authConfig = &kwclient.AuthConfig{
			Enabled:   true,
			IssuerURL: "https://idp.example.com/realms/kw",
		}
		f.native = &kwclient.NativeAuthConfig{Enabled: true, Methods: []string{"loopback-pkce"}}
		f.browserGate = gate
	})
	r.start()

	if r.app.m.CanUseLocalAuth() {
		t.Fatal("an OIDC-only instance offered a password form")
	}
	if !r.app.m.CanUseBrowserAuth() {
		t.Fatal("an OIDC instance did not offer browser sign-in")
	}

	r.focus(idBrowser)
	r.clickFocused()
	for i := 0; i < 5; i++ {
		r.step()
	}

	if !r.app.m.Busy || !strings.HasPrefix(r.app.m.BusyText, "Waiting") {
		t.Fatalf("the waiting screen was not shown: busy=%t %q", r.app.m.Busy, r.app.m.BusyText)
	}
	if r.app.authorizeURL == "" {
		t.Fatal("the authorization URL was not surfaced; a browser that opened elsewhere would strand the user")
	}

	close(gate)
	r.api.set(func(f *fakeAPI) { f.browserGate = nil })
	r.settle()

	if r.app.m.State != StateWorkspaces {
		t.Fatalf("the browser sign-in ended on %v (err %q)", r.app.m.State, r.app.m.Err)
	}
	if r.store.token != "browser-token" {
		t.Fatalf("the browser token was not stored: %q", r.store.token)
	}
}

func TestBrowserSignInCanBeCancelled(t *testing.T) {
	r := newRig(savedProfile(), "")
	gate := make(chan struct{})
	defer close(gate)
	r.api.set(func(f *fakeAPI) {
		f.authConfig = &kwclient.AuthConfig{Enabled: true}
		f.native = &kwclient.NativeAuthConfig{Enabled: true}
		f.browserGate = gate
	})
	r.start()

	r.focus(idBrowser)
	r.clickFocused()
	for i := 0; i < 5; i++ {
		r.step()
	}
	if !r.app.m.Busy {
		t.Fatal("the flow did not start")
	}

	r.press(keysym.KeyEscape, keysym.ModNone)
	for i := 0; i < 50 && r.app.m.Busy; i++ {
		r.step()
		time.Sleep(time.Millisecond)
	}
	if r.app.m.Busy {
		t.Fatal("Escape did not cancel the browser sign-in")
	}
	if r.app.m.State != StateLogin {
		t.Fatalf("cancelling moved to %v", r.app.m.State)
	}
	if r.store.token != "" {
		t.Fatal("a cancelled sign-in stored a token")
	}
}

// TestOpeningAVMWorkspaceRunsASessionAndComesBack is the requirement the whole
// single-window design exists to meet.
func TestOpeningAVMWorkspaceRunsASessionAndComesBack(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	vm := workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)
	r.api.set(func(f *fakeAPI) { f.workspaces = []kwclient.Workspace{vm} })
	r.start()

	r.focus(idList)
	r.clickFocused()
	// In the new architecture, the session runs synchronously within the first
	// Step of StateSession.
	r.settle()

	if len(r.opened) != 1 || r.opened[0].Key() != "team/vm-a" {
		t.Fatalf("the connector was called with %v", r.opened)
	}
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("after the session, state = %v; the app must not exit", r.app.m.State)
	}
	if r.app.quit {
		t.Fatal("the shell quit when the session ended")
	}
	if !strings.Contains(r.app.m.Notice, "team/vm-a") {
		t.Fatalf("the disconnect was not reported: %q", r.app.m.Notice)
	}
}

func TestSessionFailureIsReportedOnTheList(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.connect = func(context.Context, kwclient.Workspace) error {
		return fmt.Errorf("open display: %w", kwclient.ErrSessionInUse)
	}
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()

	r.focus(idList)
	r.clickFocused()
	r.settle()

	if r.app.m.State != StateWorkspaces {
		t.Fatalf("a failed session left the shell in %v", r.app.m.State)
	}
	if !strings.Contains(r.app.m.Err, "display") {
		t.Fatalf("the failure was reported as %q", r.app.m.Err)
	}
}

func TestSessionEndingWithAnExpiredTokenGoesToLogin(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.connect = func(context.Context, kwclient.Workspace) error {
		return fmt.Errorf("vnc bridge: %w", kwclient.ErrUnauthorized)
	}
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()

	r.focus(idList)
	r.clickFocused()
	r.settle()

	if r.app.m.State != StateLogin {
		t.Fatalf("a session that failed with a 401 left the shell in %v", r.app.m.State)
	}
}

// TestContainerWorkspaceOpensInApp: a container workspace's primary in-app
// surface is the embedded webview (Track B); the integrated terminal (Track A)
// and the system browser remain as explicit secondary actions.
func TestContainerWorkspaceOpensInApp(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	web := workspace("team", "code", kwclient.WorkspaceTypeContainer, true)
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{web}
		f.images = []kwclient.Image{{Name: "code", Image: "img/code", DefaultPath: "?folder=/workspace"}}
	})
	r.start()

	// The primary action (row Enter / the primary footer button) spawns the
	// embedded webview child; the in-app terminal session and the browser stay
	// strictly secondary.
	r.focus(idList)
	r.clickFocused()
	r.settle()

	if len(r.webbed) != 1 || r.webbed[0].Key() != "team/code" {
		t.Fatalf("opening spawned webview children %v, want team/code", r.webbed)
	}
	if len(r.opened) != 0 {
		t.Fatalf("the primary action connected a session: %v", r.opened)
	}
	if len(r.browsed) != 0 {
		t.Fatalf("the primary action handed the workspace to the browser: %v", r.browsed)
	}
	if !strings.Contains(r.app.m.Notice, "web") {
		t.Fatalf("the spawn was not reported: %q", r.app.m.Notice)
	}

	// Secondary "Console" runs the in-app terminal connector (Track A).
	r.focus(idConsole)
	r.clickFocused()
	r.settle()

	if len(r.opened) != 1 || r.opened[0].Key() != "team/code" {
		t.Fatalf("the secondary action opened session %v, want team/code", r.opened)
	}
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("after the session, state = %v; the shell must not exit", r.app.m.State)
	}

	// Tertiary "Open in browser" is the explicit browser hand-off, at the
	// single-use redeem URL.
	r.focus(idOpenInBrowser)
	r.clickFocused()
	r.settle()

	if want := "https://kw.example.com/auth/browser-session?code=grant-code"; len(r.browsed) != 1 || r.browsed[0] != want {
		t.Fatalf("opened %v, want %q", r.browsed, want)
	}
	// ...and the grant asked to land on the proxy path plus the catalog's
	// default path.
	if want := "/proxy/team/code/?folder=/workspace"; len(r.api.grantPaths) != 1 || r.api.grantPaths[0] != want {
		t.Fatalf("grant redirects = %v, want %q", r.api.grantPaths, want)
	}
	if !strings.Contains(r.app.m.Notice, "browser") {
		t.Fatalf("the user was not told what happened: %q", r.app.m.Notice)
	}
}

// TestOpenInBrowserWaitsForGrant: minting the code is a network round trip,
// and the shell is honest about that instead of freezing.
func TestOpenInBrowserWaitsForGrant(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	gate := make(chan struct{})
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "code", kwclient.WorkspaceTypeContainer, true)}
		f.grantGate = gate
	})
	r.start()

	r.focus(idOpenInBrowser)
	r.clickFocused()
	for i := 0; i < 5; i++ {
		r.step()
	}

	if !r.app.m.Busy || !strings.HasPrefix(r.app.m.BusyText, "Opening") {
		t.Fatalf("the grant in flight was not shown: busy=%t %q", r.app.m.Busy, r.app.m.BusyText)
	}
	if len(r.browsed) != 0 {
		t.Fatalf("the browser was opened before the grant returned")
	}

	close(gate)
	r.api.set(func(f *fakeAPI) { f.grantGate = nil })
	r.settle()

	if len(r.browsed) != 1 || r.browsed[0] != "https://kw.example.com/auth/browser-session?code=grant-code" {
		t.Fatalf("browser opened with %v, want the redeem URL", r.browsed)
	}
	if r.app.m.Busy {
		t.Fatal("the shell stayed busy after the grant completed")
	}
}

// TestOpenInBrowserGrantFailure: a failed handoff is reported and no browser
// is opened at a URL that cannot sign in.
func TestOpenInBrowserGrantFailure(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "code", kwclient.WorkspaceTypeContainer, true)}
		f.grantErr = fmt.Errorf("server refused the handoff")
	})
	r.start()

	r.focus(idOpenInBrowser)
	r.clickFocused()
	r.settle()

	if len(r.browsed) != 0 {
		t.Fatalf("a browser was opened despite the failed grant: %v", r.browsed)
	}
	if r.app.m.Err == "" {
		t.Fatal("the failed grant produced no error")
	}
	if r.app.m.Busy {
		t.Fatal("the shell stayed busy after the failure")
	}
}

// TestEnterOnStoppedWorkspaceStartsIt: Enter used to be refused on a stopped
// workspace; now it is the prompt to bring it back up, which is the one thing a
// stopped row is good for.
func TestEnterOnStoppedWorkspaceStartsIt(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, false)}
	})
	r.start()

	r.focus(idList)
	r.clickFocused()
	r.settle()

	if want := []string{"team/vm-a"}; len(r.api.startCalls) != 1 || r.api.startCalls[0] != want[0] {
		t.Fatalf("StartWorkspace calls = %v, want %v", r.api.startCalls, want)
	}
	if r.app.m.Err != "" {
		t.Fatalf("start failed: %s", r.app.m.Err)
	}
	if !strings.Contains(r.app.m.Notice, "Started") {
		t.Fatalf("the user was not told it started: %q", r.app.m.Notice)
	}
	// The fake server flipped the stopped flag on its copy, so the refresh
	// after the start shows the workspace no longer stopped.
	if ws, ok := r.app.m.SelectedWorkspace(); !ok || ws.Stopped {
		t.Fatalf("workspace is still stopped after StartWorkspace")
	}
}

// TestStopRunningWorkspaceViaFooter: a running workspace's quiet inverse
// action is Stop, and it feeds a refresh that shows the workspace stopped.
func TestStopRunningWorkspaceViaFooter(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()

	r.focus(idStop)
	r.clickFocused()
	r.settle()

	if len(r.api.stopCalls) != 1 || r.api.stopCalls[0] != "team/vm-a" {
		t.Fatalf("StopWorkspace calls = %v, want [team/vm-a]", r.api.stopCalls)
	}
	if r.app.m.Err != "" {
		t.Fatalf("stop failed: %s", r.app.m.Err)
	}
	if !strings.Contains(r.app.m.Notice, "Stopped") {
		t.Fatalf("the user was not told it stopped: %q", r.app.m.Notice)
	}
	if ws, ok := r.app.m.SelectedWorkspace(); !ok || !ws.Stopped {
		t.Fatalf("workspace is still running after StopWorkspace")
	}
}

// TestInfoModalStartsAStoppedWorkspace: start/stop is the one action the
// detail sheet is good for, and it works from behind the modal.
func TestInfoModalStartsAStoppedWorkspace(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, false)}
	})
	r.start()

	r.focus(idInfo)
	r.clickFocused()
	r.step()
	if r.app.m.Info == nil {
		t.Fatal("info modal did not open")
	}
	r.focus(idInfoStart)
	r.clickFocused()
	r.settle()

	if len(r.api.startCalls) != 1 || r.api.startCalls[0] != "team/vm-a" {
		t.Fatalf("StartWorkspace calls = %v, want [team/vm-a]", r.api.startCalls)
	}
	// Starting from the modal closes it, so there is no stale sheet after the
	// action it took.
	if r.app.m.Info != nil {
		t.Fatal("info modal stayed open after the start")
	}
}

// TestAutoRefreshDoesNotDisturbTheUser is the promise the five-second poll
// makes. It must not move the keyboard, the scroll position or the selection.
func TestAutoRefreshDoesNotDisturbTheUser(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.app.opts.RefreshInterval = 50 * time.Millisecond

	var workspaces []kwclient.Workspace
	for i := 0; i < 40; i++ {
		workspaces = append(workspaces, workspace("team", fmt.Sprintf("vm-%02d", i), kwclient.WorkspaceTypeVM, true))
	}
	r.api.set(func(f *fakeAPI) { f.workspaces = workspaces })
	r.start()

	// Scroll and select somewhere in the middle, and put the focus on the
	// filter box rather than the list.
	r.focus(idList)
	for i := 0; i < len(workspaces) && r.app.list.Offset < 3; i++ {
		r.press(keysym.KeyDown, keysym.ModNone)
	}
	selected, offset := r.app.m.Selected, r.app.list.Offset
	if offset == 0 {
		t.Fatal("the list did not scroll; the test would prove nothing")
	}
	r.focus(idFilter)

	before := r.api.listCalls
	// Reverse the order the server reports them in, so a refresh that trusted
	// positions would visibly move the cursor.
	r.api.set(func(f *fakeAPI) {
		reversed := make([]kwclient.Workspace, len(workspaces))
		for i := range workspaces {
			reversed[i] = workspaces[len(workspaces)-1-i]
		}
		f.workspaces = reversed
	})
	r.now = r.now.Add(time.Second)
	r.settle()

	if r.api.listCalls <= before {
		t.Fatal("the automatic refresh never fired")
	}
	if r.app.m.Selected != selected {
		t.Fatalf("the refresh moved the selection from %q to %q", selected, r.app.m.Selected)
	}
	if r.app.list.Offset != offset {
		t.Fatalf("the refresh moved the scroll position from %d to %d", offset, r.app.list.Offset)
	}
	if got := r.app.ctx.Focus().Focus(); got != idFilter {
		t.Fatalf("the refresh moved the keyboard focus to %q", got)
	}
	if r.app.m.Busy {
		t.Fatal("the background refresh put the screen into a busy state")
	}
}

func TestManualRefreshShortcuts(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()

	for _, press := range []func(){
		func() { r.press(keysym.KeyF5, keysym.ModNone) },
		func() {
			r.be.send(ui.EventKey{Rune: 'r', Down: true, Mods: keysym.ModControl})
			r.step()
		},
	} {
		before := r.api.listCalls
		press()
		r.settle()
		if r.api.listCalls <= before {
			t.Fatal("a manual refresh did not reach the server")
		}
	}
}

// TestExpiredTokenDuringRefreshRoutesToLogin: the window has been open all
// night and the token has expired. This is the most common way a user meets
// an expired session, and it must not be a silent failure.
func TestExpiredTokenDuringRefreshRoutesToLogin(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("setup failed: state = %v", r.app.m.State)
	}

	r.api.set(func(f *fakeAPI) {
		f.listErr = fmt.Errorf("kwclient: GET /v1/workspaces: http 401: %w", kwclient.ErrUnauthorized)
	})
	r.press(keysym.KeyF5, keysym.ModNone)
	r.settle()

	if r.app.m.State != StateLogin {
		t.Fatalf("an expired token left the shell on %v", r.app.m.State)
	}
	if r.app.m.Err == "" {
		t.Fatal("the expiry was not explained")
	}
	if r.app.m.Workspaces != nil {
		t.Fatal("the stale list was left on screen")
	}
}

func TestSignOutKeepsTheProfileAndForgetsTheToken(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()

	r.focus(idSignOut)
	r.clickFocused()
	r.step()

	if r.app.m.State != StateLogin {
		t.Fatalf("signing out went to %v", r.app.m.State)
	}
	if r.store.forgot != 1 {
		t.Fatalf("the token was forgotten %d times", r.store.forgot)
	}
	if r.api.Token() != "" {
		t.Fatal("the client kept the token")
	}
	if r.store.profile == nil {
		t.Fatal("signing out removed the profile")
	}
}

func TestChangeServerReturnsToTheServerScreen(t *testing.T) {
	r := newRig(savedProfile(), "")
	r.start()

	r.focus(idBack)
	r.clickFocused()
	r.step()

	if r.app.m.State != StateServer {
		t.Fatalf("the escape hatch went to %v", r.app.m.State)
	}
	if r.app.m.Auth != nil {
		t.Fatal("what was learned about the previous instance was kept")
	}
}

func TestFilterNarrowsTheList(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{
			workspace("team", "cf-debian-gnome-vm-0", kwclient.WorkspaceTypeVM, true),
			workspace("team", "code-server", kwclient.WorkspaceTypeContainer, true),
		}
	})
	r.start()

	r.focus(idFilter)
	r.typeText("debian")
	r.step()
	if got := len(r.app.m.Filtered()); got != 1 {
		t.Fatalf("the filter matched %d workspaces, want 1", got)
	}

	// Escape clears the filter rather than leaving the user stuck with it.
	r.press(keysym.KeyEscape, keysym.ModNone)
	if r.app.filterField.Value() != "" {
		t.Fatalf("Escape left %q in the filter", r.app.filterField.Value())
	}
	if got := len(r.app.m.Filtered()); got != 2 {
		t.Fatalf("after clearing, the filter matches %d workspaces", got)
	}
}

// TestWorkspaceInfoModalShowsAndBlocksTheList: the Info button beside the
// primary action opens a modal, and while it is up the list behind it must be
// out of reach — its shortcuts must not even reach the server.
func TestWorkspaceInfoModalShowsAndBlocksTheList(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("setup failed: state = %v", r.app.m.State)
	}

	r.focus(idInfo)
	r.clickFocused()
	r.step()
	if r.app.m.Info == nil || r.app.m.Info.Key() != "team/vm-a" {
		t.Fatalf("Info opened the modal on %+v", r.app.m.Info)
	}

	// The modal owns the frame: F5, the list's refresh shortcut, must not
	// reach the server underneath it.
	before := r.api.listCalls
	r.press(keysym.KeyF5, keysym.ModNone)
	if r.api.listCalls != before {
		t.Fatal("F5 reached the server while the modal was open")
	}
	if r.app.m.Info == nil {
		t.Fatal("the modal closed itself")
	}

	// Escape is the modal's own way out, and it lands back on the list.
	r.press(keysym.KeyEscape, keysym.ModNone)
	if r.app.m.Info != nil {
		t.Fatal("Escape did not close the modal")
	}
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("closing the modal left the shell on %v", r.app.m.State)
	}
}

func TestWorkspaceInfoModalClosesWithTheButton(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()

	r.focus(idInfo)
	r.clickFocused()
	r.step()
	if r.app.m.Info == nil {
		t.Fatal("the modal did not open")
	}

	r.focus(idInfoClose)
	r.clickFocused()
	r.step()
	if r.app.m.Info != nil {
		t.Fatal("the Close button did not dismiss the modal")
	}
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("closing the modal left the shell on %v", r.app.m.State)
	}
}

func TestQuitEventEndsTheLoop(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()

	r.be.send(viewer.EventQuit{})
	r.step()
	if !r.app.quit {
		t.Fatal("the close request was ignored")
	}
}

func TestResizeReallocatesTheSurface(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()
	if r.be.texW != 1280 || r.be.texH != 800 {
		t.Fatalf("texture is %dx%d, want the window size", r.be.texW, r.be.texH)
	}

	r.be.resize(900, 600)
	r.be.send(viewer.EventResize{W: 900, H: 600})
	r.step()

	if r.be.texW != 900 || r.be.texH != 600 {
		t.Fatalf("after a resize the texture is %dx%d", r.be.texW, r.be.texH)
	}
	if b := r.app.canvas.Bounds(); b.W != 900 || b.H != 600 {
		t.Fatalf("the canvas is %dx%d", b.W, b.H)
	}

	// A degenerate size must not panic or allocate nonsense.
	r.be.resize(0, 0)
	r.be.send(viewer.EventResize{W: 0, H: 0})
	r.step()
}

func TestRunOpensAndClosesTheWindowOnce(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- r.app.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for {
		r.be.mu.Lock()
		opened, presents := r.be.opened, r.be.presents
		r.be.mu.Unlock()
		if opened == 1 && presents > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the window never opened")
		case <-time.After(2 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when the context was cancelled")
	}

	r.be.mu.Lock()
	defer r.be.mu.Unlock()
	if r.be.opened != 1 || r.be.closed != 1 {
		t.Fatalf("window opened %d times and closed %d", r.be.opened, r.be.closed)
	}
}

func TestNewRejectsAnIncompleteConfiguration(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("an App was built with no backend")
	}
	if _, err := New(Options{Backend: newFakeBackend(10, 10)}); err == nil {
		t.Fatal("an App was built with no client factory")
	}
}

// netError is a net.Error whose message a test can control.
type netError struct {
	msg     string
	timeout bool
}

func (e *netError) Error() string   { return e.msg }
func (e *netError) Timeout() bool   { return e.timeout }
func (e *netError) Temporary() bool { return false }

var _ net.Error = (*netError)(nil)

// TestEnterInTheFilterOpensTheSelection: the filter box is laid out before the
// list that resolves the selection, so an Enter there arrives without a
// workspace attached and has to be resolved afterwards. Getting this wrong
// produces the memorable error "  is starting and cannot be opened yet".
func TestEnterInTheFilterOpensTheSelection(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()

	r.focus(idFilter)
	r.typeText("vm")
	r.clickFocused()
	r.settle()

	if len(r.opened) != 1 || r.opened[0].Name != "vm-a" {
		t.Fatalf("it opened %v", r.opened)
	}
}
