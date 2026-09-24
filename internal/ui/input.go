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
	//
	// It is the right field for asking whether a key or a chord was pressed.
	// It is the wrong field for asking what the user typed; see Edits.
	Keys []EventKey

	// Edits is the same batch as a text field must apply it: the key presses
	// of Keys interleaved, in arrival order, with the [EventText] commits
	// that landed between them.
	//
	// The two views exist because the two questions are different. A chord
	// query ("was F5 pressed?") does not care about order and does not care
	// about text at all, so Keys stays a plain slice of key events. Editing
	// does care about both: "a", Home, "b" and Home, "a", "b" leave different
	// text in the field, and a batch really can hold all three — one poll
	// drains everything the window queued, which at sixty frames a second is
	// however many keystrokes a fast typist managed in sixteen milliseconds.
	// Folding the text into a second, separate slice and applying it after
	// the keys would silently reorder exactly that case.
	Edits []Event

	// ComposedText latches the first time the backend delivers an
	// [EventText], and never clears.
	//
	// It is how a text field knows which of the two descriptions of a
	// keystroke to believe. A backend that composes text sends both: a key
	// event naming the physical key (which the guest needs) and a text event
	// carrying the character (which is the only one that is right for dead
	// keys, IME commits, AltGr and every shifted symbol). Inserting from both
	// types every character twice.
	//
	// The latch, rather than a per-batch check, is what makes dead keys work.
	// Pressing the dead key produces a key event and no text at all — the
	// platform is waiting for the second keystroke — so "insert from the key
	// event when this batch brought no text" would type a bare accent that
	// the user is still in the middle of composing.
	//
	// Backends that do not report composed text — test doubles, and any
	// future backend on a platform without a composition API — never set it,
	// and their key events are read as characters as before.
	ComposedText bool

	// Mods is the modifier state as of the most recent key event. It is what
	// tells a text commit apart from the side effect of a shortcut; see
	// [Input.Fold].
	Mods keysym.Modifiers

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

	// SystemThemeChanged is an edge reporting that the platform reported a
	// change to its colour scheme in this batch. It describes the change, not
	// the value: a shell following the platform asks its backend for the
	// current scheme when it acts on it, so the choice is made against the
	// freshest value the platform has rather than one that is a frame stale.
	SystemThemeChanged bool
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
	out.SystemThemeChanged = false
	out.Keys = nil
	out.Edits = nil

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
				// Same argument for the keyboard: the Ctrl-up that lands
				// after focus moved away is never delivered, and a window
				// that came back believing Ctrl was held would read the next
				// keystroke as a shortcut and refuse to type it.
				out.Mods = keysym.ModNone
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
			// Modifier state is tracked from releases too, or a chord's
			// trailing Ctrl-up would leave Ctrl held forever.
			out.Mods = e.Mods
			if e.Down {
				out.Keys = append(out.Keys, e)
				out.Edits = append(out.Edits, e)
			}

		case EventText:
			// The latch records the capability, not the commit, so it is set
			// before the filtering below: a text event that turns out to be
			// the side effect of a shortcut still proves the backend composes
			// text.
			out.ComposedText = true
			if e.Text != "" && !commandChord(out.Mods) {
				out.Edits = append(out.Edits, e)
			}

		case EventClipboard:
			out.ClipboardChanged = true

		case EventSystemTheme:
			out.SystemThemeChanged = true
		}
	}
	return out
}

// Idle reports whether the batch contained nothing worth repainting for.
//
// The shell uses it to decide whether to draw at all: a frame with no input
// and no data change would be pixel-identical to the one already on screen.
// It tests Edits rather than Keys because Edits is the superset: an IME commit
// accepted by clicking a candidate changes the text with no key press behind
// it at all, and a frame that skipped it would leave the field showing the
// text from before.
func (in Input) Idle() bool {
	return !in.Pressed && !in.Released && !in.Resized && !in.Quit &&
		in.Wheel == (Point{}) && len(in.Edits) == 0 && !in.SystemThemeChanged
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
//
// It applies only to backends that do not compose text; see
// [Input.ComposedText]. When one does, the character in a key event is the
// backend's best guess from a keycode, and the text event is the platform's
// actual answer.
func IsTextRune(e EventKey) bool {
	if e.Key != keysym.KeyUnknown || e.Rune == 0 {
		return false
	}
	if e.Mods.HasAny(keysym.ModControl | keysym.ModAlt | keysym.ModSuper) {
		return false
	}
	return e.Rune >= 0x20 && e.Rune != 0x7f
}

// commandChord reports whether mods mean the user is pressing a shortcut
// rather than typing, so that any text the platform reports alongside it is a
// side effect to be discarded. Without it, Ctrl-V would both paste the
// clipboard and type a "v".
//
// Alt deliberately does not count on its own, and neither does Ctrl+Alt.
// AltGr is reported as Ctrl+Alt on Windows and as Alt on macOS, and AltGr is
// how a large part of the world types "@", "€", "\" and every accented
// letter. Treating those as commands would drop the characters this code
// exists to deliver. Plain command chords do not produce text on any platform
// the client runs on, so nothing is lost by being narrow here.
func commandChord(m keysym.Modifiers) bool {
	if m.Has(keysym.ModSuper) {
		return true
	}
	return m.Has(keysym.ModControl) && !m.Has(keysym.ModAlt)
}
