// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package tray is the system-tray menu's pure-Go model: which workspaces are
// listed, which open modes each one offers, and the actions a menu click
// produces.
//
// It imports nothing but the standard library, so the menu rules are unit
// tested without a display. The native menu itself lives in
// internal/viewer (tray.go), which is the only place an SDL tray object may
// exist; the shell owns the policy (which workspaces, what a click does) and
// drives both through the [Backend] and [Handler] seams.
package tray

import "sort"

// Kind names the workspace type as the tray cares about it: which open
// modes apply. It mirrors kwclient.WorkspaceType without importing the
// client, so this package stays UI- and network-free.
type Kind int

const (
	// KindVM is a VM-backed workspace: display plus out-of-band consoles.
	KindVM Kind = iota
	// KindContainer is a container workspace: web app first.
	KindContainer
	// KindScratch is a scratch workspace: like a container here.
	KindScratch
	// KindOther is anything the client does not recognise. It gets the
	// browser, which is the honest option for a surface we do not render.
	KindOther
)

// Mode is one way of opening a workspace from the tray.
type Mode int

const (
	// ModeDisplay opens the VM display (Tier 1 when advertised, Tier 0
	// otherwise — the session dialler decides, not the tray).
	ModeDisplay Mode = iota
	// ModeSerial opens the VM's serial console seat.
	ModeSerial
	// ModeSSH opens the VM's SSH console seat (via the credential form).
	ModeSSH
	// ModeWeb opens a container/scratch workspace in the embedded webview.
	ModeWeb
	// ModeTerminal opens a container/scratch workspace's integrated
	// terminal over /exec.
	ModeTerminal
	// ModeBrowser opens a workspace in the system browser via the
	// browser-session grant.
	ModeBrowser
)

// ModesFor lists the open modes a workspace kind offers, in the order the
// quick-pick window shows them: the primary surface first.
func ModesFor(k Kind) []Mode {
	switch k {
	case KindVM:
		return []Mode{ModeDisplay, ModeSerial, ModeSSH}
	case KindContainer, KindScratch:
		return []Mode{ModeWeb, ModeTerminal, ModeBrowser}
	default:
		return []Mode{ModeBrowser}
	}
}

// Target is one running workspace as the tray menu shows it.
type Target struct {
	// Key is "namespace/name", the identity the shell resolves at click
	// time. The menu never carries the workspace itself: the list may
	// refresh between the menu being built and the click landing.
	Key string
	// Label is the human-readable row text.
	Label string
	// Kind selects the open modes.
	Kind Kind
	// Tier1 records the capability advert, for the pick window's hint.
	Tier1 bool
}

// ActionKind names what a tray menu click asks for.
type ActionKind int

const (
	// ActionOpen asks for the workspace with the accompanying key: focus
	// its live window, open it directly, or offer the quick-pick window,
	// per the shell's policy.
	ActionOpen ActionKind = iota
	// ActionShow asks for the shell's main window: show it if minimized
	// to the tray, and raise it either way.
	ActionShow
	// ActionAbout asks for the About panel.
	ActionAbout
	// ActionQuit asks the application to quit.
	ActionQuit
)

// Action is one tray menu click, delivered to the shell.
type Action struct {
	Kind ActionKind
	// Key names the workspace for ActionOpen; it is empty otherwise.
	Key string
}

// Handler receives tray menu clicks.
//
// It is called on the tray's thread, not the shell's loop goroutine, so an
// implementation must be safe for concurrent use — queue the action and wake
// the loop (see viewer.Backend.Wake), never touch the model directly.
type Handler interface {
	Handle(Action)
}

// Backend is the native tray menu behind the shell.
//
// Every method runs on the loop's goroutine, which owns every window and
// the tray with them.
type Backend interface {
	// Update rebuilds the Workspaces submenu from the current running
	// set. An empty slice renders the disabled placeholder row.
	Update(targets []Target)
	// Pump runs tray housekeeping that needs the loop's thread (the
	// Linux AppIndicator driver dispatches through it). It is cheap and
	// idempotent; the shell calls it once per loop iteration.
	Pump()
	// Close destroys the tray and everything under it. It is safe to
	// call on a backend that never produced a tray, and safe to call
	// twice.
	Close()
}

// MaxWorkspaces bounds the submenu: native menus degrade with hundreds of
// rows, and the shell list (with its filter) stays the place to find a
// workspace precisely.
const MaxWorkspaces = 50

// MenuItem is one row of the Workspaces submenu.
type MenuItem struct {
	// Label is the row text.
	Label string
	// Key is the workspace key for ActionOpen; empty for the placeholder
	// and overflow rows, which are disabled and carry no action.
	Key string
	// Disabled renders the row unclickable.
	Disabled bool
}

// MenuItems builds the Workspaces submenu rows from the running set: the
// targets in stable key order, capped at [MaxWorkspaces] with an overflow
// row, or the single disabled placeholder when there is nothing running.
func MenuItems(targets []Target) []MenuItem {
	if len(targets) == 0 {
		return []MenuItem{{Label: "(no running workspaces)", Disabled: true}}
	}
	sorted := make([]Target, len(targets))
	copy(sorted, targets)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	n := min(len(sorted), MaxWorkspaces)
	out := make([]MenuItem, 0, n+1)
	for _, t := range sorted[:n] {
		out = append(out, MenuItem{Label: t.Label, Key: t.Key})
	}
	if len(sorted) > n {
		out = append(out, MenuItem{
			Label:    "(and more — open the shell)",
			Disabled: true,
		})
	}
	return out
}
