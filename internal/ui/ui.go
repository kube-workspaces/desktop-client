// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package ui is the client's widget layer: a small immediate-mode toolkit that
// rasterises into an [image.RGBA] in software.
//
// # Why software, and why no toolkit
//
// The shell is a launcher. It draws a list, a few text fields and a handful of
// buttons, and it repaints only when input arrives or the workspace list
// changes — not sixty times a second. Rasterising a 1280x800 surface in pure Go
// costs a couple of milliseconds on that schedule, which is free, and it buys
// three things that matter more than the milliseconds:
//
//   - No dependency. A Go GUI toolkit is a large, fast-moving surface to pin,
//     audit and cross-compile for six targets; this package is the standard
//     library plus the bitmap font in internal/viewer.
//   - Shared SDL. The session viewer and the shell each open an SDL window
//     on the same main OS thread. Drawing the shell into its own image
//     surface means the two can coexist in one process without competing
//     for the same window or a second windowing toolkit.
//   - Testability. Every widget is a function from an input batch and a
//     rectangle to pixels in a buffer, so the tests need neither a display nor
//     a golden-image harness.
//
// # Immediate mode, with retained state where it belongs
//
// Widgets are laid out and drawn in one call per frame, which keeps the screen
// code a straight-line description of what is on screen. The state that cannot
// be recomputed each frame — the text in a field, where its cursor is, which
// row of a list is selected, which widget has the keyboard — is held in
// ordinary structs the caller owns ([TextInput], [List]) or in the [Context]
// ([FocusRing]). That split is deliberate: state the user can see is worth
// naming and testing, and state the layout implies is not worth storing.
//
// # Events
//
// The package deliberately does not define its own event types. It consumes
// the backend-neutral ones from internal/viewer, aliased below so call sites
// stay short. A second, parallel event vocabulary in the same binary would be
// a translation layer to keep in sync for no benefit.
package ui

import (
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// The backend-neutral event set, aliased from internal/viewer.
//
// These are aliases, not definitions: a [viewer.Backend] can be polled
// straight into this package with no conversion, and a widget and the session
// viewer describe the same key press with the same type.
type (
	// Event is one input or window event; see [viewer.Event].
	Event = viewer.Event
	// EventKey is a key press or release; see [viewer.EventKey].
	EventKey = viewer.EventKey
	// EventText is composed text the user committed; see [viewer.EventText].
	EventText = viewer.EventText
	// EventPointer is an absolute pointer position plus the buttons held.
	EventPointer = viewer.EventPointer
	// EventWheel is a scroll wheel movement in whole clicks.
	EventWheel = viewer.EventWheel
	// EventResize reports a new drawable size in pixels.
	EventResize = viewer.EventResize
	// EventFocus reports a change in keyboard focus.
	EventFocus = viewer.EventFocus
	// EventQuit reports that the user asked to close the window.
	EventQuit = viewer.EventQuit
	// EventClipboard reports that the host clipboard may have changed.
	EventClipboard = viewer.EventClipboard

	// Buttons is a pointer button mask; see [viewer.Buttons].
	Buttons = viewer.Buttons

	// Rect is an axis-aligned rectangle in integer pixels; see [viewer.Rect].
	Rect = viewer.Rect
)

// The pointer buttons, re-exported so that a caller of this package does not
// have to import internal/viewer for a button mask.
const (
	ButtonLeft   = viewer.ButtonLeft
	ButtonMiddle = viewer.ButtonMiddle
	ButtonRight  = viewer.ButtonRight
)

// Point is a position in canvas pixels.
type Point struct {
	X, Y int
}

// In reports whether p lies inside r.
func (p Point) In(r Rect) bool { return r.Contains(p.X, p.Y) }

// Add returns the sum of two points.
func (p Point) Add(q Point) Point { return Point{p.X + q.X, p.Y + q.Y} }
