// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

// State is which screen the shell is showing.
//
// The shell is a state machine and not a stack of dialogs: there are four
// things it can be doing, every transition between them is named below, and
// there is no way to be on two of them at once. That is worth the ceremony
// because the interesting bugs in a client like this are all "the session
// expired while a modal was open" bugs.
type State int

const (
	// StateServer asks which instance to talk to. It is the first-run screen
	// and the one a user returns to to point the client somewhere else.
	StateServer State = iota
	// StateLogin authenticates against the instance, by password or by
	// browser depending on what the instance offers.
	StateLogin
	// StateWorkspaces is the main screen: the workspace list.
	StateWorkspaces
	// StateSettings is the settings screen: appearance preferences that live
	// on this computer, not on any instance.
	StateSettings
	// StateSession means a display session is running in its own window.
	// The shell's own loop is parked for the duration and resumes on
	// StateWorkspaces.
	StateSession
)

// String implements fmt.Stringer.
func (s State) String() string {
	switch s {
	case StateServer:
		return "server"
	case StateLogin:
		return "login"
	case StateWorkspaces:
		return "workspaces"
	case StateSettings:
		return "settings"
	case StateSession:
		return "session"
	default:
		return "unknown"
	}
}

// Model is everything the shell shows and everything a transition changes.
//
// It holds no window, no client and no goroutine, which is the point: the
// state machine can be driven and asserted on in a test with no display and no
// network, and the screens below are a pure function of it.
type Model struct {
	// State is the current screen.
	State State

	// Server is the instance URL, and Insecure whether its certificate is
	// being ignored.
	Server   string
	Insecure bool

	// Auth is what the instance said about authentication, and Native whether
	// it supports the RFC 8252 browser flow. Both are nil/false until the
	// server has been probed.
	Auth   *kwclient.AuthConfig
	Native bool

	// Identity is the signed-in user.
	Identity *kwclient.Identity

	// Workspaces is the last list fetched, and Images the catalog used to
	// build browser URLs for non-VM workspaces.
	Workspaces []kwclient.Workspace
	Images     []kwclient.Image

	// Filter narrows the list. It matches name, namespace and type.
	Filter string

	// Selected is the "namespace/name" key of the selected workspace.
	//
	// A key and not an index. The list refreshes every few seconds and rows
	// appear, disappear and reorder as workspaces start and stop; an index
	// would silently move the selection onto a different workspace between
	// the user deciding to press Enter and pressing it.
	Selected string

	// Info is the workspace whose details the info modal is showing, or nil
	// when no modal is open. It is a snapshot rather than a key, so a list
	// refresh underneath the modal cannot change what it says while it is up.
	Info *kwclient.Workspace

	// Opening is the workspace whose session is being started; it is only
	// meaningful in StateSession.
	Opening kwclient.Workspace

	// Busy and BusyText describe work in flight. Busy suppresses the actions
	// that would race it rather than covering the screen with a modal.
	Busy     bool
	BusyText string

	// Err and Notice are the two message lines a screen can show. Err is a
	// failure and is red; Notice is an outcome and is not.
	Err    string
	Notice string

	// LastRefresh is when the workspace list last came back, shown so that a
	// stale list is visibly stale rather than quietly wrong.
	LastRefresh time.Time
}

// NeedServer moves to the server screen, forgetting anything learned about the
// previous instance.
func (m *Model) NeedServer(reason string) {
	m.State = StateServer
	m.Auth, m.Native = nil, false
	m.Identity = nil
	m.Workspaces, m.Images = nil, nil
	m.Info = nil
	m.Busy, m.BusyText = false, ""
	m.Err = reason
	m.Notice = ""
}

// ServerReady records a successful probe of an instance and moves to the login
// screen.
func (m *Model) ServerReady(server string, insecure bool, cfg *kwclient.AuthConfig, native bool) {
	m.Server, m.Insecure = server, insecure
	m.Auth, m.Native = cfg, native
	m.Busy, m.BusyText = false, ""
	m.Err = ""
	m.State = StateLogin

	// An instance with authentication switched off has nothing to ask for.
	// Sending the user to a login screen with no fields and no buttons would
	// be a dead end; the caller proceeds straight to the list instead.
	if cfg != nil && !cfg.Enabled {
		m.Notice = "Authentication is disabled on this instance."
	}
}

// SignedIn records a verified identity and moves to the workspace list.
func (m *Model) SignedIn(identity *kwclient.Identity) {
	m.Identity = identity
	m.Busy, m.BusyText = false, ""
	m.Err = ""
	m.Notice = ""
	m.State = StateWorkspaces
}

// SignOut clears the session and returns to the login screen.
//
// reason is shown there. It is a parameter rather than a constant because the
// same transition serves the user pressing "Sign out", a token that expired
// while the window was closed, and a 401 from a refresh that happened while
// the user was reading the list — and those need to say different things.
func (m *Model) SignOut(reason string) {
	m.Identity = nil
	m.Workspaces, m.Images = nil, nil
	m.Selected = ""
	m.Info = nil
	m.Busy, m.BusyText = false, ""
	m.Err = ""
	m.Notice = reason
	m.State = StateLogin
}

// Expired is [Model.SignOut] for a session the server has stopped accepting.
func (m *Model) Expired() {
	m.SignOut("")
	m.Err = "Your session has expired. Please sign in again."
}

// Fail records an error against the current screen, routing an expired session
// back to the login screen.
//
// Routing here rather than at each call site is what makes "never a silent
// failure" enforceable: every network result in the shell ends up in this one
// function, so there is one place to check that a 401 is handled and one place
// that decides what the user is told.
func (m *Model) Fail(err error) {
	if err == nil {
		return
	}
	m.Busy, m.BusyText = false, ""
	if errors.Is(err, kwclient.ErrUnauthorized) && m.State != StateLogin && m.State != StateServer {
		m.Expired()
		return
	}
	m.Err = Describe(err)
	m.Notice = ""
}

// Working marks the model busy with a description of what for.
func (m *Model) Working(what string) {
	m.Busy, m.BusyText = true, what
	m.Err = ""
	m.Notice = ""
}

// Done clears the busy state.
func (m *Model) Done() {
	m.Busy, m.BusyText = false, ""
}

// WorkspacesLoaded replaces the list.
//
// The selection is preserved by key, and falls back to the first connectable
// workspace when the selected one has gone. That fallback matters: a user
// whose workspace was deleted underneath them should find the cursor somewhere
// sensible, not on nothing.
func (m *Model) WorkspacesLoaded(workspaces []kwclient.Workspace, at time.Time) {
	m.Workspaces = sortWorkspaces(workspaces)
	m.LastRefresh = at
	m.Done()
	m.Err = ""

	if m.Selected != "" {
		for i := range m.Workspaces {
			if m.Workspaces[i].Key() == m.Selected {
				return
			}
		}
	}
	m.Selected = ""
	for i := range m.Workspaces {
		if m.Workspaces[i].Running() {
			m.Selected = m.Workspaces[i].Key()
			return
		}
	}
	if len(m.Workspaces) > 0 {
		m.Selected = m.Workspaces[0].Key()
	}
}

// ImagesLoaded records the image catalog. A failure to fetch it is not worth
// reporting: it only supplies the default path for a browser URL, and the
// workspace root is a reasonable fallback.
func (m *Model) ImagesLoaded(images []kwclient.Image) { m.Images = images }

// Open moves to the session screen for ws.
func (m *Model) Open(ws kwclient.Workspace) {
	m.Opening = ws
	m.Selected = ws.Key()
	m.Err, m.Notice = "", ""
	m.Busy, m.BusyText = false, ""
	m.State = StateSession
}

// ShowInfo opens the info modal for ws. The workspace is snapshotted, so a
// refresh that reorders or replaces the list underneath the modal does not
// change the details it is showing.
func (m *Model) ShowInfo(ws kwclient.Workspace) {
	m.Info = &ws
	m.Err, m.Notice = "", ""
}

// CloseInfo dismisses the info modal, returning to the list.
func (m *Model) CloseInfo() { m.Info = nil }

// SessionEnded returns from a session to the workspace list.
//
// It returns to the list whatever happened, including on a failure. The window
// belongs to the shell and a session that could not start is not a reason to
// leave the user looking at nothing — which is exactly what the connect
// subcommand does, because there it is the whole process.
func (m *Model) SessionEnded(err error) {
	ws := m.Opening
	m.Opening = kwclient.Workspace{}
	m.State = StateWorkspaces
	m.Busy, m.BusyText = false, ""

	switch {
	case err == nil:
		m.Err, m.Notice = "", "Disconnected from "+ws.Key()+"."
	case errors.Is(err, kwclient.ErrUnauthorized):
		m.Expired()
	default:
		m.Notice = ""
		m.Err = ws.Key() + ": " + Describe(err)
	}
}

// Filtered returns the workspaces matching [Model.Filter].
func (m *Model) Filtered() []kwclient.Workspace {
	needle := strings.ToLower(strings.TrimSpace(m.Filter))
	if needle == "" {
		return m.Workspaces
	}
	out := make([]kwclient.Workspace, 0, len(m.Workspaces))
	for _, ws := range m.Workspaces {
		if matches(ws, needle) {
			out = append(out, ws)
		}
	}
	return out
}

// SelectedIndex returns the position of the selected workspace within rows, or
// -1. It is how the model's key-based selection is handed to the index-based
// list widget.
func SelectedIndex(rows []kwclient.Workspace, key string) int {
	for i := range rows {
		if rows[i].Key() == key {
			return i
		}
	}
	return -1
}

// SelectedWorkspace returns the selected workspace.
func (m *Model) SelectedWorkspace() (kwclient.Workspace, bool) {
	for i := range m.Workspaces {
		if m.Workspaces[i].Key() == m.Selected {
			return m.Workspaces[i], true
		}
	}
	return kwclient.Workspace{}, false
}

// CanUseLocalAuth reports whether the instance accepts email and password.
func (m *Model) CanUseLocalAuth() bool {
	return m.Auth != nil && m.Auth.Enabled && m.Auth.LocalAuth.Enabled
}

// CanUseBrowserAuth reports whether the RFC 8252 browser flow is available.
func (m *Model) CanUseBrowserAuth() bool {
	return m.Auth != nil && m.Auth.Enabled && m.Native
}

// AuthDisabled reports that the instance does not authenticate at all, so the
// login screen has nothing to offer and the list is reachable directly.
func (m *Model) AuthDisabled() bool { return m.Auth != nil && !m.Auth.Enabled }

// matches reports whether a workspace matches a lowercased search term. Name,
// namespace and type are all searchable because all three are things a user
// might have in mind, and a filter that only matched one of them would be a
// guessing game.
func matches(ws kwclient.Workspace, needle string) bool {
	return strings.Contains(strings.ToLower(ws.Name), needle) ||
		strings.Contains(strings.ToLower(ws.Namespace), needle) ||
		strings.Contains(strings.ToLower(string(ws.Type)), needle)
}

// sortWorkspaces orders the list the way the user reads it: the ones that can
// be connected to first, then alphabetically. It matches the `list`
// subcommand, so the two never disagree about what is at the top.
func sortWorkspaces(workspaces []kwclient.Workspace) []kwclient.Workspace {
	out := make([]kwclient.Workspace, len(workspaces))
	copy(out, workspaces)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Running() != b.Running() {
			return a.Running()
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}

// StatusText renders a workspace's state the way the web UI and the `list`
// subcommand do. There is no phase field on the API, so the state is derived:
// stopped wins, then readiness, and anything else is still coming up.
func StatusText(ws kwclient.Workspace) string {
	switch {
	case ws.Stopped:
		return "stopped"
	case ws.ReadyReplicas > 0:
		return "running"
	case ws.ContainerState != nil && ws.ContainerState.Reason != "":
		return "starting: " + ws.ContainerState.Reason
	default:
		return "starting"
	}
}
