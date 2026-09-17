// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

// Timeouts for the shell's own requests.
//
// They are shorter than the client's default because these are foreground
// operations with a spinner in front of them: a user watching a "Contacting…"
// message needs to be told it failed while they are still watching.
const (
	probeTimeout = 15 * time.Second
	listTimeout  = 20 * time.Second
	loginTimeout = 30 * time.Second
)

// intentKind is what the user asked a frame to do.
//
// Screens return an intent instead of performing the action, so that drawing
// stays a pure function of the model and every side effect in the shell starts
// in [App.act]. That is what makes a screen testable: lay it out against a
// synthetic input and assert on the value it returns.
type intentKind int

const (
	intentNone intentKind = iota
	intentConnectServer
	intentSignInLocal
	intentSignInBrowser
	intentCancel
	intentRefresh
	intentActivate
	intentOpenWeb
	intentOpenInBrowser
	intentStartWorkspace
	intentStopWorkspace
	intentInfoWorkspace
	intentInfoClose
	intentSignOut
	intentChangeServer
	intentOpenSettings
	intentSettingsDone
	intentQuit
)

// intent is one frame's outcome.
type intent struct {
	kind intentKind
	// workspace is the subject of intentActivate, intentOpenInBrowser,
	// intentStartWorkspace, intentStopWorkspace and intentInfoWorkspace.
	workspace kwclient.Workspace
}

// act performs the frame's intent.
func (a *App) act(ctx context.Context, in intent) {
	switch in.kind {
	case intentNone:
		return
	case intentConnectServer:
		a.connectToServer(ctx, a.serverField.Value(), a.insecureBox.Checked)
	case intentSignInLocal:
		a.signInLocal(ctx, a.emailField.Value(), a.passwordField.Value())
	case intentSignInBrowser:
		a.signInBrowser(ctx)
	case intentCancel:
		a.cancelInFlight()
		a.m.Done()
		a.m.Notice = "Cancelled."
	case intentRefresh:
		a.refreshWorkspaces(ctx, true)
		a.refreshImages(ctx)
	case intentActivate:
		if ws, ok := a.resolve(in.workspace); ok {
			a.activate(ctx, ws)
		}
	case intentOpenWeb:
		if ws, ok := a.resolve(in.workspace); ok {
			a.openWeb(ctx, ws)
		}
	case intentOpenInBrowser:
		if ws, ok := a.resolve(in.workspace); ok {
			a.openInBrowser(ctx, ws)
		}
	case intentStartWorkspace:
		if ws, ok := a.resolve(in.workspace); ok && ws.Stopped {
			a.setStopped(ctx, ws, false)
		}
	case intentStopWorkspace:
		if ws, ok := a.resolve(in.workspace); ok && ws.Running() {
			a.setStopped(ctx, ws, true)
		}
	case intentInfoWorkspace:
		if ws, ok := a.resolve(in.workspace); ok {
			a.m.ShowInfo(ws)
			// Focus belongs to the modal while it is up: its Close button is
			// the only reachable control, and it is what Tab users land on.
			a.ctx.Focus().Set(idInfoClose)
		}
	case intentInfoClose:
		a.m.CloseInfo()
		// Return the keyboard to where the user was — the selected row's
		// primary action — rather than letting it fall onto the filter.
		a.ctx.Focus().Set(idOpen)
	case intentSignOut:
		a.signOut()
	case intentChangeServer:
		a.cancelInFlight()
		a.m.NeedServer("")
	case intentOpenSettings:
		// Settings are only ever opened from the workspace list, so getting
		// out of them is a return, not a hop.
		a.m.Err, a.m.Notice = "", ""
		a.m.State = StateSettings
	case intentSettingsDone:
		a.m.State = StateWorkspaces
	case intentQuit:
		a.quit = true
	}
	a.dirty = true
}

// connectToServer probes an instance and, if it answers, moves on to signing
// in.
//
// The probe is GET /auth/config, which is unauthenticated and cheap, and
// therefore doubles as the reachability, DNS and TLS check. Discovering all
// four failures in one request is why the server screen can say something
// specific about each of them.
func (a *App) connectToServer(ctx context.Context, server string, insecure bool) {
	server = NormaliseServer(server)
	if server == "" {
		a.m.Err = "Enter the address of your Kube Workspaces instance."
		return
	}
	if err := a.useServer(server, insecure); err != nil {
		a.m.Done()
		a.m.Err = Describe(err)
		return
	}
	a.m.Working("Contacting " + server)

	api := a.api
	opCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	a.cancelInFlight()
	a.cancelPending = cancel

	a.background(func() func() {
		defer cancel()
		cfg, err := api.AuthConfig(opCtx)
		native := false
		if err == nil {
			// An instance that predates the native flow simply has no
			// nativeAuth object; that is not a failure of the probe.
			if n, nerr := api.NativeAuth(opCtx); nerr == nil {
				native = n.Supported()
			}
		}
		return func() {
			a.cancelPending = nil
			if err != nil {
				a.m.Done()
				a.m.Err = Describe(err)
				return
			}
			a.m.ServerReady(server, insecure, cfg, native)
			a.rememberProfile(server, insecure, "")
			if err := a.opts.Store.Save(a.profile, ""); err != nil {
				a.logf("save profile: %v", err)
			}
			if a.m.AuthDisabled() {
				// Nothing to sign in with; the API will accept anonymous
				// requests, so go straight to the list.
				a.verifySession(ctx)
			}
		}
	})
}

// ensureAuthConfig fetches the auth configuration if the shell does not have
// it, without changing the screen. The login screen needs it to know which
// controls to show.
func (a *App) ensureAuthConfig(ctx context.Context) {
	if a.api == nil || a.m.Auth != nil || a.m.Busy {
		return
	}
	api, server, insecure := a.api, a.m.Server, a.m.Insecure
	// Working clears the message line, and the caller's message — "your
	// session has expired", typically — is the reason the user is on this
	// screen at all. Carry it across the probe and put it back.
	was := a.m.Err
	a.m.Working("Contacting " + server)
	opCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	a.cancelInFlight()
	a.cancelPending = cancel

	a.background(func() func() {
		defer cancel()
		cfg, err := api.AuthConfig(opCtx)
		native := false
		if err == nil {
			if n, nerr := api.NativeAuth(opCtx); nerr == nil {
				native = n.Supported()
			}
		}
		return func() {
			a.cancelPending = nil
			a.m.Done()
			if err != nil {
				a.m.Err = Describe(err)
				return
			}
			a.m.ServerReady(server, insecure, cfg, native)
			if was != "" {
				a.m.Err = was
			}
		}
	})
}

// verifySession asks the server who the stored token belongs to.
//
// The token is not trusted just because it decodes: it is an opaque bearer
// credential the server can have revoked, and a client that lists workspaces
// with a dead token shows the user an empty list and an error instead of a
// sign-in screen.
func (a *App) verifySession(ctx context.Context) {
	if a.api == nil {
		a.m.NeedServer("")
		return
	}
	api := a.api
	a.m.Working("Checking your session")
	opCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	a.cancelInFlight()
	a.cancelPending = cancel

	a.background(func() func() {
		defer cancel()
		identity, err := api.Me(opCtx)
		return func() {
			a.cancelPending = nil
			a.m.Done()
			switch {
			case err != nil:
				a.m.State = StateLogin
				a.m.Identity = nil
				a.m.Err = Describe(err)
				a.ensureAuthConfig(ctx)

			case identity.AuthEnabled && !identity.Authenticated:
				a.m.State = StateLogin
				a.m.Identity = nil
				a.m.Err = "Your session has expired. Please sign in again."
				a.ensureAuthConfig(ctx)

			default:
				a.m.SignedIn(identity)
				if identity.MustChangePassword {
					a.m.Notice = "This account must change its password in the web UI."
				}
				a.rememberProfile(a.m.Server, a.m.Insecure, identity.Email)
				a.refreshWorkspaces(ctx, true)
				a.refreshImages(ctx)
			}
		}
	})
}

// signInLocal exchanges an email and password for a session token.
func (a *App) signInLocal(ctx context.Context, email, password string) {
	switch {
	case a.api == nil:
		a.m.NeedServer("")
		return
	case strings.TrimSpace(email) == "":
		a.m.Err = "Enter your email address."
		return
	case password == "":
		a.m.Err = "Enter your password."
		return
	}

	api := a.api
	a.m.Working("Signing in")
	opCtx, cancel := context.WithTimeout(ctx, loginTimeout)
	a.cancelInFlight()
	a.cancelPending = cancel

	a.background(func() func() {
		defer cancel()
		token, mustChange, err := api.LoginLocal(opCtx, email, password)
		return func() {
			a.cancelPending = nil
			if err != nil {
				a.m.Done()
				a.m.Err = Describe(err)
				// The password is wrong, or the account is locked; either way
				// it should not be sitting in the field for the next attempt.
				a.passwordField.Clear()
				a.ctx.Focus().Set(idPassword)
				return
			}
			a.passwordField.Clear()
			a.finishLogin(ctx, token, email, mustChange)
		}
	})
}

// signInBrowser runs the RFC 8252 loopback + PKCE flow.
//
// The system browser is used and never an embedded webview: the whole point of
// the native-app flow is that this process never sees the user's credentials
// for a third-party identity provider. See internal/kwclient/native_auth.go.
func (a *App) signInBrowser(ctx context.Context) {
	if a.api == nil {
		a.m.NeedServer("")
		return
	}
	api := a.api
	a.authorizeURL = ""
	a.m.Working("Waiting for your browser")

	// No timeout of ours: LoginBrowser applies its own, generous enough for a
	// consent screen and an MFA prompt. Cancellation is the user's, through
	// the Cancel button.
	opCtx, cancel := context.WithCancel(ctx)
	a.cancelInFlight()
	a.cancelPending = cancel

	a.background(func() func() {
		defer cancel()
		login, err := api.LoginBrowser(opCtx, &kwclient.BrowserLoginOptions{
			Notify: func(authorizeURL string, notifyErr error) {
				// This runs on the login flow's goroutine, so it may not touch
				// the model: it posts to the loop instead.
				a.post(func() {
					a.authorizeURL = authorizeURL
					if notifyErr != nil {
						a.m.Notice = "No browser could be opened. Copy the address below."
					}
					a.dirty = true
				})
			},
		})
		return func() {
			a.cancelPending = nil
			a.authorizeURL = ""
			if err != nil {
				a.m.Done()
				if errors.Is(err, context.Canceled) {
					a.m.Notice = "Sign-in cancelled."
					a.m.Err = ""
					return
				}
				a.m.Err = Describe(err)
				return
			}
			a.finishLogin(ctx, login.Token, login.Email, false)
		}
	})
}

// finishLogin stores a freshly issued token and verifies it.
//
// The token is verified before being trusted and after being stored, in that
// order, because a stored token that the server will not accept leaves the
// user with a client that fails mysteriously on every screen.
func (a *App) finishLogin(ctx context.Context, token, email string, mustChange bool) {
	a.api.SetToken(token)
	a.rememberProfile(a.m.Server, a.m.Insecure, email)
	if err := a.opts.Store.Save(a.profile, token); err != nil {
		// Not fatal: the session works, it just will not survive a restart.
		a.logf("store session token: %v", err)
		a.m.Notice = "Signed in, but the session could not be saved: " + Describe(err)
	}
	if mustChange {
		a.m.Notice = "This account must change its password in the web UI."
	}
	a.verifySession(ctx)
}

// refreshWorkspaces refetches the list.
//
// It deliberately does not set the model's busy flag. This runs every few
// seconds underneath a user who is reading the list, and a refresh that dimmed
// the screen, moved the focus or reset the scroll position would make the
// window unusable. The only thing it changes is the data — the selection is
// tracked by key, the scroll offset belongs to the list widget, and neither is
// touched here.
func (a *App) refreshWorkspaces(ctx context.Context, manual bool) {
	if a.api == nil || a.refreshing {
		return
	}
	if a.m.State != StateWorkspaces {
		return
	}
	a.refreshing = true
	if manual {
		a.nextRefresh = time.Time{}
	}

	api := a.api
	namespace := kwclient.AllNamespaces
	if a.profile != nil && a.profile.Namespace != "" {
		namespace = a.profile.Namespace
	}

	a.background(func() func() {
		opCtx, cancel := context.WithTimeout(ctx, listTimeout)
		defer cancel()
		workspaces, err := api.ListWorkspaces(opCtx, namespace)
		now := time.Now()
		return func() {
			a.refreshing = false
			if a.m.State != StateWorkspaces {
				return
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// This is the path an expired token arrives on most often:
				// the user leaves the window open overnight. Model.Fail
				// routes it back to the login screen.
				a.m.Fail(err)
				return
			}
			a.m.WorkspacesLoaded(workspaces, now)
		}
	})
}

// refreshImages fetches the image catalog, which supplies the default path for
// a non-VM workspace's browser URL. A failure is logged and otherwise ignored:
// the workspace root is a usable fallback and an error banner about a catalog
// the user has never heard of would be noise.
func (a *App) refreshImages(ctx context.Context) {
	if a.api == nil {
		return
	}
	api := a.api
	a.background(func() func() {
		opCtx, cancel := context.WithTimeout(ctx, listTimeout)
		defer cancel()
		images, err := api.ListImages(opCtx)
		return func() {
			if err != nil {
				a.logf("list images: %v", err)
				return
			}
			a.m.ImagesLoaded(images)
		}
	})
}

// resolve fills in the workspace an intent is about.
//
// Some intents name one (the Open button knows which row it is under) and some
// do not: pressing Enter in the filter box means "open whatever is selected",
// and the filter box is laid out before the list that resolves the selection.
// Rather than make the screens carry the selection forward, an unnamed
// workspace is looked up here.
func (a *App) resolve(ws kwclient.Workspace) (kwclient.Workspace, bool) {
	if ws.Name != "" {
		return ws, true
	}
	return a.m.SelectedWorkspace()
}

// activate opens a workspace in-app: a display session for a VM, an integrated
// terminal for container and scratch workspaces. Workspace types the client
// cannot render yet fall back to the browser rather than pretending to.
func (a *App) activate(ctx context.Context, ws kwclient.Workspace) {
	switch {
	case !ws.Running():
		a.m.Notice = ""
		a.m.Err = fmt.Sprintf("%s is %s and cannot be opened yet.", ws.Name, StatusText(ws))
	case ws.IsVM(), ws.Type == kwclient.WorkspaceTypeContainer, ws.Type == kwclient.WorkspaceTypeScratch:
		a.m.Open(ws)
	default:
		// A workspace the client cannot open in-app yet, so it still gets the
		// browser: the grant flow hands the user's session to their browser,
		// which is the honest option for a surface we do not render.
		a.openInBrowser(ctx, ws)
	}
}

// openWeb spawns the embedded-webview child process (the `web` child binary,
// or the `web` subcommand of a developer copy) for a non-VM workspace. Track B
// of integrated-container-workspaces-plan.md: the child scopes the browser
// engine's cgo/windowing requirements away from the shell, which therefore
// stays cgo-free and single-threaded (SDL). A spawn failure is reported like
// any in-app failure — never silently falls back.
func (a *App) openWeb(ctx context.Context, ws kwclient.Workspace) {
	if !ws.Running() {
		a.m.Notice = ""
		a.m.Err = fmt.Sprintf("%s is %s and cannot be opened yet.", ws.Name, StatusText(ws))
		return
	}
	profile := ""
	if a.profile != nil {
		profile = a.profile.Name
	}
	if err := a.opts.OpenWeb(profile, ws.Namespace, ws.Name); err != nil {
		a.m.Notice = ""
		a.m.Err = fmt.Sprintf("Web view could not be started (%v). The browser path still works.", err)
		return
	}
	a.m.Err = ""
	a.m.Notice = "Opened " + ws.Name + " in the web view."
}

// openInBrowser opens a container workspace's web UI in the user's browser.
//
// The browser has never seen this client's session — the client logged in over
// the loopback flow or with --token — and the proxy only trusts its kw-session
// cookie, so opening the /proxy/ URL bare would land on a 401. The server
// closes the gap: the client POSTs /auth/browser-session/grant and gets a
// single-use code plus the /auth/browser-session?code=... URL that redeems it
// into the same cookie a login would set. Opening that URL is one navigation,
// and the browser lands on the workspace signed in.
func (a *App) openInBrowser(ctx context.Context, ws kwclient.Workspace) {
	if a.api == nil {
		return
	}
	if !ws.Running() {
		a.m.Notice = ""
		a.m.Err = fmt.Sprintf("%s is %s and cannot be opened yet.", ws.Name, StatusText(ws))
		return
	}

	api := a.api
	redirect := a.api.WorkspacePath(ws, a.imageFor(ws))

	a.m.Working("Opening " + ws.Name + " in your browser")
	opCtx, cancel := context.WithTimeout(ctx, listTimeout)
	a.cancelInFlight()
	a.cancelPending = cancel
	a.background(func() func() {
		grant, err := api.GrantBrowserSession(opCtx, redirect)
		return func() {
			cancel()
			a.cancelPending = nil
			if a.m.State != StateWorkspaces {
				return
			}
			a.m.Done()
			if err != nil {
				a.m.Err = Describe(err)
				return
			}
			if err := a.opts.OpenBrowser(grant.URL); err != nil {
				a.m.Err = "No browser could be opened. Visit " + grant.URL
				return
			}
			a.m.Err = ""
			a.m.Notice = "Opened " + ws.Name + " in your browser."
		}
	})
}

// setStopped starts or stops a workspace and refreshes the list so the new
// state shows up under the user.
func (a *App) setStopped(ctx context.Context, ws kwclient.Workspace, stop bool) {
	if a.api == nil {
		return
	}
	api := a.api
	what := "Starting"
	if stop {
		what = "Stopping"
	}
	a.m.Working(what + " " + ws.Name)
	opCtx, cancel := context.WithTimeout(ctx, listTimeout)
	a.cancelInFlight()
	a.cancelPending = cancel
	a.background(func() func() {
		var err error
		if stop {
			_, err = api.StopWorkspace(opCtx, ws.Namespace, ws.Name)
		} else {
			_, err = api.StartWorkspace(opCtx, ws.Namespace, ws.Name)
		}
		return func() {
			cancel()
			a.cancelPending = nil
			if a.m.State != StateWorkspaces {
				return
			}
			a.m.Done()
			if err != nil {
				a.m.Err = Describe(err)
				return
			}
			// The Info modal, when it is up, is about the workspace that just
			// changed; close it rather than leave a stale sheet in the way.
			a.m.CloseInfo()
			done := "Started"
			if stop {
				done = "Stopped"
			}
			a.m.Notice = fmt.Sprintf("%s %s.", done, ws.Name)
			a.refreshWorkspaces(ctx, true)
		}
	})
}

// signOut forgets the session but keeps the profile, so signing back in does
// not mean retyping the server address.
func (a *App) signOut() {
	a.cancelInFlight()
	if a.api != nil {
		a.api.SetToken("")
	}
	if err := a.opts.Store.Forget(a.profile); err != nil {
		a.logf("forget token: %v", err)
	}
	a.passwordField.Clear()
	a.m.SignOut("You are signed out.")
}

// rememberProfile updates the in-memory profile for the current instance.
func (a *App) rememberProfile(server string, insecure bool, email string) {
	if a.profile == nil || a.profile.Server != server {
		a.profile = &config.Profile{
			Name:   config.ProfileNameForServer(server),
			Server: server,
		}
	}
	a.profile.Server = server
	a.profile.InsecureSkipVerify = insecure
	if email != "" {
		a.profile.Email = email
	}
}

// post schedules fn to run on the loop's goroutine. It is for callbacks that
// fire on somebody else's goroutine and need to change the model.
func (a *App) post(fn func()) {
	select {
	case a.results <- fn:
	case <-a.done:
	}
}

// NormaliseServer turns what a user typed into a base URL the client accepts.
//
// A scheme is assumed rather than demanded — nobody types "https://" into a
// field that already says "workspaces.example.com" — and https is the
// assumption because a session token is a bearer credential and defaulting to
// cleartext would hand it to the network.
func NormaliseServer(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	return strings.TrimRight(s, "/")
}

// spawnWeb is the default [Options.OpenWeb]: run a `web` child process where
// the browser engine (cgo + a second windowing stack) can live safely off the
// SDL main thread. The preferred child is the standalone embedded-webview
// binary, kube-workspaces-web, shipped next to this executable; a developer
// copy built from source has no such sibling and falls back to this same
// binary's `web` subcommand. Same spawn model as openBrowser — argv elements
// only, never a shell. The profile pin is forwarded as --profile so the child
// opens the instance the shell is showing.
func spawnWeb(profile, namespace, name string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return spawnWebExe(exe, profile, namespace, name)
}

// spawnWebExe is spawnWeb against a caller-supplied shell path, for tests that
// stage a pretend installation.
func spawnWebExe(exe, profile, namespace, name string) error {
	positional := []string{namespace + "/" + name}
	var flags []string
	if profile != "" {
		flags = []string{"--profile", profile}
	}
	if child := webChildPath(exe); child != "" {
		cmd := exec.Command(child, append(flags, positional...)...)
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start web child: %w", err)
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	cmd := exec.Command(exe, append([]string{"web"}, append(flags, positional...)...)...)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start web subcommand: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// webChildPath returns the path of the standalone embedded-webview child
// binary (kube-workspaces-web) sitting next to exe, or "" when there is none.
// Releases ship the child beside the cgo-free shell so webview support can
// carry cgo without making the shell; `go run`/`go build` developer copies
// have only the one binary and fall back to the `web` subcommand.
func webChildPath(exe string) string {
	if exe == "" {
		return ""
	}
	name := "kube-workspaces-web"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	child := filepath.Join(filepath.Dir(exe), name)
	if fi, err := os.Stat(child); err == nil && !fi.IsDir() {
		return child
	}
	return ""
}

// openBrowser is the default [Options.OpenBrowser].
//
// The URL is passed as its own argv element and never through a shell, so
// nothing in it can be taken for a command. It mirrors the launcher in
// internal/kwclient, which this package cannot reach because that one is
// unexported and rightly so.
func openBrowser(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("refusing to open %q", rawURL)
	}

	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{rawURL}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}
	default:
		name, args = "xdg-open", []string{rawURL}
	}

	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s is not on PATH", name)
	}
	cmd := exec.Command(path, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	// Reap the child: both launchers exit as soon as the browser has the URL.
	go func() { _ = cmd.Wait() }()
	return nil
}

// The focus identifiers of every control in the shell. They are constants in
// one place so that a screen cannot accidentally reuse another's, which would
// make Tab jump between screens' worth of state.
const (
	idServer   ui.FocusID = "server"
	idInsecure ui.FocusID = "insecure"
	idConnect  ui.FocusID = "connect"

	idEmail    ui.FocusID = "email"
	idPassword ui.FocusID = "password"
	idSignIn   ui.FocusID = "sign-in"
	idBrowser  ui.FocusID = "sign-in-browser"
	idCancel   ui.FocusID = "cancel"
	idBack     ui.FocusID = "back"

	idFilter        ui.FocusID = "filter"
	idList          ui.FocusID = "workspaces"
	idOpen          ui.FocusID = "open"
	idOpenInBrowser ui.FocusID = "open-in-browser"
	idConsole       ui.FocusID = "console"
	idStop          ui.FocusID = "stop"
	idRefresh       ui.FocusID = "refresh"
	idSignOut       ui.FocusID = "sign-out"

	idInfo      ui.FocusID = "info"
	idInfoClose ui.FocusID = "info-close"
	idInfoStart ui.FocusID = "info-start"
	idInfoStop  ui.FocusID = "info-stop"

	idSettings     ui.FocusID = "settings"
	idStyleBubbly  ui.FocusID = "style-bubbly"
	idStyleRetro   ui.FocusID = "style-retro"
	idStyleClean   ui.FocusID = "style-clean"
	idModeDark     ui.FocusID = "mode-dark"
	idModeLight    ui.FocusID = "mode-light"
	idSettingsDone ui.FocusID = "settings-done"
)
