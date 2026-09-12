// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

// Input is the pointer, keyboard and window activity a frame is drawn against.
//
// It has two kinds of field. Some are *states* that persist until something
// changes them — where the pointer is, which buttons are held, whether the
// window has keyboard focus. The rest are *edges* that describe only what
// happened since the last frame: a press, a release, wheel clicks, key
// presses. [Input.Fold] preserves the first kind and recomputes the second, so
// a caller keeps one Input for the life of the window and feeds each batch of
// backend events through it.
type Input struct {
	// Now is when the frame is being drawn. It drives the busy indicator and
	// the double-click interval, and it is a field rather than a call to
	// time.Now so that tests are not racing a clock.
	Now time.Time

	// Mouse is the pointer position in canvas pixels, and HasMouse reports
	// whether the pointer has ever been seen. Until it has, nothing hovers:
	// a window that opens under the pointer must not light up a button the
	// user has not moved to.
	Mouse    Point
	HasMouse bool

	// Buttons is the full button mask; Down is the primary button alone,
	// which is all the widgets care about.
	Buttons Buttons
	Down    bool

	// Pressed and Released are the primary button's edges within this batch.
	// PressOrigin is where the current (or most recent) press started, so a
	// click can be required to begin and end on the same control.
	Pressed     bool
	Released    bool
	PressOrigin Point

	// Wheel is the wheel movement in whole clicks, positive Y being away from
	// the user.
	Wheel Point

	// Keys are the key presses in this batch, in order, including auto-repeat.
	// Releases are dropped: no widget here acts on one, and keeping them would
	// make every consumer filter.
	Keys []EventKey

	// Focused reports whether the window has keyboard focus.
	Focused bool

	// Resized reports that the drawable size changed in this batch, and Size
	// is the new size.
	Resized bool
	Size    Point

	// Quit reports that the user asked to close the window. It is sticky:
	// once seen it stays set, because losing it would mean ignoring the
	// request.
	Quit bool

	// ClipboardChanged reports that the host clipboard may have changed.
	ClipboardChanged bool
}

// Fold returns in updated with a batch of backend events applied.
//
// The receiver is not modified, and the returned value does not share the
// receiver's key slice, so an Input may be kept and compared across frames.
func (in Input) Fold(now time.Time, events []Event) Input {
	out := in
	out.Now = now
	out.Pressed, out.Released = false, false
	out.Wheel = Point{}
	out.Resized = false
	out.ClipboardChanged = false
	out.Keys = nil

	for _, ev := range events {
		switch e := ev.(type) {
		case EventQuit:
			out.Quit = true

		case EventResize:
			out.Resized = true
			out.Size = Point{X: e.W, Y: e.H}

		case EventFocus:
			out.Focused = e.Gained
			if !e.Gained {
				// A window manager keeps the button and key releases that
				// arrive after focus has moved away. Without this the shell
				// would believe the primary button is still held and the next
				// pointer motion would read as a drag.
				out.Down = false
				out.Buttons = 0
			}

		case EventPointer:
			out.Mouse = Point{X: e.X, Y: e.Y}
			out.HasMouse = true
			out.Buttons = e.Buttons
			down := e.Buttons.Has(ButtonLeft)
			switch {
			case down && !out.Down:
				out.Pressed = true
				out.PressOrigin = out.Mouse
			case !down && out.Down:
				out.Released = true
			}
			out.Down = down

		case EventWheel:
			out.Wheel.X += e.DX
			out.Wheel.Y += e.DY

		case EventKey:
			if e.Down {
				out.Keys = append(out.Keys, e)
			}

		case EventClipboard:
			out.ClipboardChanged = true
		}
	}
	return out
}

// Idle reports whether the batch contained nothing worth repainting for.
//
// The shell uses it to decide whether to draw at all: a frame with no input
// and no data change would be pixel-identical to the one already on screen.
func (in Input) Idle() bool {
	return !in.Pressed && !in.Released && !in.Resized && !in.Quit &&
		in.Wheel == (Point{}) && len(in.Keys) == 0
}

// Hovering reports whether the pointer is inside r.
func (in Input) Hovering(r Rect) bool {
	return in.HasMouse && in.Mouse.In(r)
}

// Holding reports whether the primary button is down and was pressed inside r.
// It is the "armed" state a button draws while the user holds it.
func (in Input) Holding(r Rect) bool {
	return in.Down && in.PressOrigin.In(r)
}

// PressedIn reports whether the primary button went down inside r this frame.
func (in Input) PressedIn(r Rect) bool {
	return in.Pressed && in.Mouse.In(r)
}

// ClickedIn reports whether a complete click — press and release — happened
// inside r.
//
// Requiring both ends is not pedantry: it is what lets a user who pressed the
// wrong button slide off it and let go without firing the action, which is the
// behaviour every desktop toolkit has and every user relies on without
// noticing.
func (in Input) ClickedIn(r Rect) bool {
	return in.Released && in.Mouse.In(r) && in.PressOrigin.In(r)
}

// KeyPressed reports whether key k was pressed in this batch with none of
// shift, control, alt or super held.
func (in Input) KeyPressed(k keysym.Key) bool {
	return in.Chord(keysym.ModNone, k)
}

// Chord reports whether key k was pressed with exactly the modifiers in mods.
//
// "Exactly" ignores the lock modifiers: Caps Lock and Num Lock are states of
// the keyboard, not part of a shortcut, and a user with Caps Lock on still
// expects F5 to refresh.
func (in Input) Chord(mods keysym.Modifiers, k keysym.Key) bool {
	if k == keysym.KeyUnknown {
		return false
	}
	for _, e := range in.Keys {
		if e.Key == k && effectiveMods(e.Mods) == mods {
			return true
		}
	}
	return false
}

// RuneChord reports whether the character r was typed with exactly the
// modifiers in mods. It is how Ctrl-R is matched: a letter arrives as a rune,
// not as a [keysym.Key].
func (in Input) RuneChord(mods keysym.Modifiers, r rune) bool {
	for _, e := range in.Keys {
		if e.Key != keysym.KeyUnknown {
			continue
		}
		if lowerASCII(e.Rune) == lowerASCII(r) && effectiveMods(e.Mods) == mods {
			return true
		}
	}
	return false
}

// effectiveMods drops the lock modifiers from a modifier set.
func effectiveMods(m keysym.Modifiers) keysym.Modifiers {
	return m.Without(keysym.ModCapsLock | keysym.ModNumLock)
}

// lowerASCII lowercases an ASCII letter and leaves everything else alone.
// Shortcut letters are ASCII by construction, so this avoids dragging in a
// Unicode case table for four comparisons.
func lowerASCII(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

// IsTextRune reports whether a key event carries a character a text field
// should insert.
//
// Control and Alt chords are excluded because they are commands — Ctrl-V is
// not the letter v — and so are the C0 range and DEL, which reach a field as
// named keys instead.
func IsTextRune(e EventKey) bool {
	if e.Key != keysym.KeyUnknown || e.Rune == 0 {
		return false
	}
	if e.Mods.HasAny(keysym.ModControl | keysym.ModAlt | keysym.ModSuper) {
		return false
	}
	return e.Rune >= 0x20 && e.Rune != 0x7f
}
