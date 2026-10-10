// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package connection describes client-owned session controls without depending
// on a windowing toolkit, workspace API or transport implementation.
package connection

import "fmt"

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
	TypeClipboard                                                        bool
}

// Snapshot is an immutable copy of the state the toolbar draws this frame.
// Each window owns its own snapshot and action handlers.
//
// Tier/Proto/Activity/Extra feed the debug card only: adapters report what
// they actually measure, never estimates. A transport that tracks nothing
// leaves HasActivity false and the card says so.
type Snapshot struct {
	Workspace    string
	Surface      Surface
	State        State
	Role         Role
	Transport    string
	Tier         string // "Tier 0" or "Tier 1", classified by the adapter
	Proto        string // wire detail, e.g. "RFB over WebSocket"
	Capabilities Capabilities
	Fullscreen   bool
	Clipboard    bool
	ResizeGuest  bool
	Muted        bool
	AudioLive    bool // negotiated/open and unmuted
	FPS, Kbps    float64
	HasMetrics   bool
	BytesIn      uint64
	BytesOut     uint64
	HasActivity  bool
	// Extra holds adapter detail rows (label, value) in display order,
	// e.g. framebuffer size, encoding mix, agent video/audio counters.
	Extra [][2]string
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
	TypeClipboard Action = "type-clipboard"
	CopySelection Action = "copy-selection"
	HostInput     Action = "host-input"
	Debug         Action = "debug"
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
	case Debug:
		// Chrome-only readout, handled inside the toolbar: always visible
		// on desktop windows, even while connecting or failed — that is
		// when connection detail is most useful.
		supported = desktop
	case Sessions, WorkspaceList:
		supported = c.Shell
	case FitDesktop, HostInput:
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
	case TypeClipboard:
		supported, needsLive, mutatesGuest = desktop && c.TypeClipboard, true, true
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
	for _, a := range []Action{WorkspaceList, HostInput, SpecialKeys, TypeClipboard, ClipboardSync, FitDesktop, GuestResize, Audio, SharedControl, Paste, CopySelection} {
		if s.Availability(a).Visible {
			out = append(out, a)
		}
	}
	return out
}

// Equal reports whether two snapshots draw identically. Extra rows compare
// element-wise: snapshots hold a slice now, so they are no longer
// comparable with ==.
func (s Snapshot) Equal(o Snapshot) bool {
	if s.Workspace != o.Workspace || s.Surface != o.Surface || s.State != o.State ||
		s.Role != o.Role || s.Transport != o.Transport || s.Tier != o.Tier ||
		s.Proto != o.Proto || s.Capabilities != o.Capabilities ||
		s.Fullscreen != o.Fullscreen || s.Clipboard != o.Clipboard ||
		s.ResizeGuest != o.ResizeGuest || s.Muted != o.Muted ||
		s.AudioLive != o.AudioLive || s.FPS != o.FPS || s.Kbps != o.Kbps ||
		s.HasMetrics != o.HasMetrics || s.BytesIn != o.BytesIn ||
		s.BytesOut != o.BytesOut || s.HasActivity != o.HasActivity ||
		len(s.Extra) != len(o.Extra) {
		return false
	}
	for i := range s.Extra {
		if s.Extra[i] != o.Extra[i] {
			return false
		}
	}
	return true
}

// FormatBytes renders a byte total for the debug card: whole B under
// 1024, then one-decimal KB/MB/GB. Cumulative counters, not a rate.
func FormatBytes(n uint64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n) / 1024.0
	for _, suffix := range []string{"KB", "MB", "GB"} {
		if value < 1024.0 || suffix == "GB" {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
		value /= 1024.0
	}
	return fmt.Sprintf("%d B", n)
}

// CloseDisposition tells the owner whether a clean window close parks or
// releases its held attachment. A zero-value window preserves close-to-park.
type CloseDisposition uint8

const (
	Park CloseDisposition = iota
	Release
)
