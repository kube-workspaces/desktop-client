// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

// FocusID names a focusable widget within a screen. It is a string rather than
// a generated integer so that the identity survives a widget being moved,
// added or removed: focus follows the field the user is typing in, not the
// third control in layout order.
type FocusID string

// NoFocus is the zero FocusID: nothing has the keyboard.
const NoFocus FocusID = ""

// FocusRing is the keyboard focus for one screen.
//
// It is rebuilt every frame: widgets register themselves as they are laid out,
// which makes the traversal order the layout order by construction, and means
// a control that is not on screen this frame cannot be tabbed to. The current
// focus survives the rebuild, and is only moved by [FocusRing.Move] or
// [FocusRing.Set].
//
// A VDI client has to be usable without a mouse — the whole point of it is to
// get the user into a remote desktop, and that journey should not require
// pointing at anything — so this is not an accessibility afterthought but the
// primary input path.
type FocusRing struct {
	order []FocusID
	focus FocusID
}

// Begin clears the registered order for a new frame, keeping the focus.
func (f *FocusRing) Begin() { f.order = f.order[:0] }

// Register adds id to this frame's traversal order.
//
// Registering the same id twice is the caller's bug, and it is not policed:
// the cost of the check is a map per frame, and the symptom (Tab sticking) is
// immediately obvious.
func (f *FocusRing) Register(id FocusID) {
	if id == NoFocus {
		return
	}
	f.order = append(f.order, id)
}

// Order returns this frame's traversal order. The slice aliases the ring's own
// storage and is only valid until the next [FocusRing.Begin].
func (f *FocusRing) Order() []FocusID { return f.order }

// Focus returns the focused widget, or [NoFocus].
func (f *FocusRing) Focus() FocusID { return f.focus }

// Has reports whether id currently has the keyboard.
func (f *FocusRing) Has(id FocusID) bool { return id != NoFocus && f.focus == id }

// Set moves the focus to id without regard to the traversal order, for a
// widget that was clicked or a screen that wants a specific field focused when
// it opens.
func (f *FocusRing) Set(id FocusID) { f.focus = id }

// Clear drops the focus and the registered order. A screen change calls it, so
// that focus does not land on whatever happens to share an id with a widget on
// the previous screen.
func (f *FocusRing) Clear() {
	f.focus = NoFocus
	f.order = f.order[:0]
}

// Move advances the focus by delta positions through this frame's order,
// wrapping at both ends.
//
// If nothing is focused, or the focused widget is no longer on screen, it
// lands on the first entry for a forward move and the last for a backward one,
// which is what a user pressing Tab into a fresh screen expects.
func (f *FocusRing) Move(delta int) {
	if len(f.order) == 0 || delta == 0 {
		return
	}
	i, ok := f.indexOf(f.focus)
	if !ok {
		if delta > 0 {
			f.focus = f.order[0]
		} else {
			f.focus = f.order[len(f.order)-1]
		}
		return
	}
	n := len(f.order)
	f.focus = f.order[((i+delta)%n+n)%n]
}

// Normalise points the focus at the first registered widget when it is unset
// or stale, and reports whether it changed anything.
//
// A screen with no focus has no keyboard interface at all, which is the state
// every immediate-mode UI lands in on its first frame; this is what gets it
// out again without the screen code having to say so.
func (f *FocusRing) Normalise() bool {
	if len(f.order) == 0 {
		return false
	}
	if _, ok := f.indexOf(f.focus); ok {
		return false
	}
	f.focus = f.order[0]
	return true
}

func (f *FocusRing) indexOf(id FocusID) (int, bool) {
	if id == NoFocus {
		return 0, false
	}
	for i, got := range f.order {
		if got == id {
			return i, true
		}
	}
	return 0, false
}
