// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package connection describes client-owned session controls without depending
// on a windowing toolkit, workspace API or transport implementation.
package connection

// Surface is the connection view, independent of the workspace's CR type.
type Surface string

const (
	Desktop Surface = "desktop"
	Console Surface = "console"
	Serial  Surface = "serial"
	SSH     Surface = "ssh"
	Web     Surface = "web"
)

// State is the latest observed connection state. Web hosts should use Loading
// or Ready rather than claiming a loaded page is a healthy remote transport.
type State string

const (
	Connecting   State = "connecting"
	Connected    State = "connected"
	Reconnecting State = "reconnecting"
	Busy         State = "busy"
	Failed       State = "failed"
	Loading      State = "loading"
	Ready        State = "ready"
)

// Role is authoritative display ownership, not the user's platform role.
type Role string

const (
	Exclusive  Role = "exclusive"
	Controller Role = "controller"
	Observer   Role = "observer"
)

// Capabilities are advertised by the live adapter, not inferred from VM type.
type Capabilities struct {
	Shell, SpecialKeys, ClipboardSync, GuestResize, Audio, SharedControl bool
	Paste, CopySelection                                                 bool
}

// Snapshot is an immutable copy of the state the toolbar draws this frame.
// Each window owns its own snapshot and action handlers.
type Snapshot struct {
	Workspace    string
	Surface      Surface
	State        State
	Role         Role
	Transport    string
	Capabilities Capabilities
	Fullscreen   bool
	Clipboard    bool
	ResizeGuest  bool
	Muted        bool
}

// Action identifies a command shared by toolbar items and hotkeys.
type Action string

const (
	Fullscreen    Action = "fullscreen"
	Sessions      Action = "sessions"
	WorkspaceList Action = "workspace-list"
	Disconnect    Action = "disconnect"
	SpecialKeys   Action = "special-keys"
	ClipboardSync Action = "clipboard-sync"
	FitDesktop    Action = "fit-desktop"
	GuestResize   Action = "guest-resize"
	Audio         Action = "audio"
	SharedControl Action = "shared-control"
	Paste         Action = "paste"
	CopySelection Action = "copy-selection"
)

// Availability distinguishes an unsupported action from a temporarily
// disabled one. Reason is a stable message key suffix for the presentation.
type Availability struct {
	Visible bool
	Enabled bool
	Reason  string
}

// Availability resolves an action against this frame's capabilities and role.
// Adapters must recheck before execution; this is a UX gate, not authorization.
func (s Snapshot) Availability(a Action) Availability {
	c := s.Capabilities
	desktop := s.Surface == Desktop
	terminal := s.Surface == Console || s.Surface == Serial || s.Surface == SSH
	supported := false
	needsLive, mutatesGuest := false, false
	switch a {
	case Fullscreen, Disconnect:
		supported = true
	case Sessions, WorkspaceList:
		supported = c.Shell
	case FitDesktop:
		supported = desktop
	case SpecialKeys:
		supported, needsLive, mutatesGuest = desktop && c.SpecialKeys, true, true
	case ClipboardSync:
		supported, needsLive, mutatesGuest = desktop && c.ClipboardSync, true, true
	case GuestResize:
		supported, needsLive, mutatesGuest = desktop && c.GuestResize, true, true
	case Audio:
		supported, needsLive = desktop && c.Audio, true
	case SharedControl:
		supported, needsLive = desktop && c.SharedControl && (s.Role == Observer || s.Role == Controller), true
	case Paste:
		supported, needsLive, mutatesGuest = terminal && c.Paste, true, true
	case CopySelection:
		supported = terminal && c.CopySelection
	}
	if !supported {
		return Availability{}
	}
	if mutatesGuest && s.Role == Observer {
		return Availability{Visible: true, Reason: "viewOnly"}
	}
	if needsLive && s.State != Connected {
		return Availability{Visible: true, Reason: string(s.State)}
	}
	return Availability{Visible: true, Enabled: true}
}

// Tools is the stable menu order across surfaces, omitting unsupported tools.
func (s Snapshot) Tools() []Action {
	var out []Action
	for _, a := range []Action{WorkspaceList, SpecialKeys, ClipboardSync, FitDesktop, GuestResize, Audio, SharedControl, Paste, CopySelection} {
		if s.Availability(a).Visible {
			out = append(out, a)
		}
	}
	return out
}

// CloseDisposition tells the owner whether a clean window close parks or
// releases its held attachment. A zero-value window preserves close-to-park.
type CloseDisposition uint8

const (
	Park CloseDisposition = iota
	Release
)
