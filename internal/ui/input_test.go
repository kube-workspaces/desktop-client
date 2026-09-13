// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

var epoch = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// pointer builds a pointer event at (x, y) with the primary button in the
// given state.
func pointer(x, y int, down bool) EventPointer {
	var b Buttons
	if down {
		b = ButtonLeft
	}
	return EventPointer{X: x, Y: y, Buttons: b}
}

func keyDown(k keysym.Key, mods keysym.Modifiers) EventKey {
	return EventKey{Key: k, Down: true, Mods: mods}
}

func runeDown(r rune, mods keysym.Modifiers) EventKey {
	return EventKey{Rune: r, Down: true, Mods: mods}
}

// text builds a composed-text event, which is what a backend that can compose
// text delivers alongside the key press that produced it.
func text(s string) EventText { return EventText{Text: s} }

// editText renders the text an Input's edit stream would insert, so a test can
// assert on the whole ordered batch rather than on one event at a time.
func editText(in Input) string {
	var out []rune
	for _, ev := range in.Edits {
		switch e := ev.(type) {
		case EventText:
			out = append(out, []rune(e.Text)...)
		case EventKey:
			if !in.ComposedText && IsTextRune(e) {
				out = append(out, e.Rune)
			}
		}
	}
	return string(out)
}

func TestFoldTracksPointerEdges(t *testing.T) {
	var in Input

	in = in.Fold(epoch, []Event{pointer(10, 20, false)})
	if !in.HasMouse || in.Mouse != (Point{X: 10, Y: 20}) {
		t.Fatalf("pointer position not tracked: %+v", in.Mouse)
	}
	if in.Pressed || in.Released || in.Down {
		t.Fatal("motion alone reported a button edge")
	}

	in = in.Fold(epoch, []Event{pointer(10, 20, true)})
	if !in.Pressed || in.Released || !in.Down {
		t.Fatalf("press not reported: pressed=%t released=%t down=%t", in.Pressed, in.Released, in.Down)
	}
	if in.PressOrigin != (Point{X: 10, Y: 20}) {
		t.Fatalf("press origin = %+v", in.PressOrigin)
	}

	// Holding and dragging: no new edge, but the position moves and the
	// origin does not.
	in = in.Fold(epoch, []Event{pointer(40, 50, true)})
	if in.Pressed || in.Released || !in.Down {
		t.Fatal("a drag produced a button edge")
	}
	if in.PressOrigin != (Point{X: 10, Y: 20}) {
		t.Fatalf("the drag moved the press origin to %+v", in.PressOrigin)
	}

	in = in.Fold(epoch, []Event{pointer(40, 50, false)})
	if !in.Released || in.Down || in.Pressed {
		t.Fatal("release not reported")
	}

	// Edges do not survive an empty batch.
	in = in.Fold(epoch, nil)
	if in.Pressed || in.Released {
		t.Fatal("a button edge survived a frame with no events")
	}
	if !in.HasMouse || in.Mouse != (Point{X: 40, Y: 50}) {
		t.Fatal("the pointer position did not survive an empty frame")
	}
}

// TestFoldReleasesTheButtonOnFocusLoss: the window manager keeps the release
// that happens after focus moves away, so without this the shell would think
// the button was still held and read the next motion as a drag.
func TestFoldReleasesTheButtonOnFocusLoss(t *testing.T) {
	in := Input{}.Fold(epoch, []Event{EventFocus{Gained: true}, pointer(5, 5, true)})
	if !in.Down {
		t.Fatal("button not held")
	}
	in = in.Fold(epoch, []Event{EventFocus{Gained: false}})
	if in.Down || in.Buttons != 0 || in.Focused {
		t.Fatalf("focus loss left the button held: down=%t buttons=%d", in.Down, in.Buttons)
	}
}

func TestFoldAccumulatesWheelAndKeys(t *testing.T) {
	in := Input{}.Fold(epoch, []Event{
		EventWheel{DY: 1},
		EventWheel{DY: 2, DX: -1},
		keyDown(keysym.KeyF5, keysym.ModNone),
		EventKey{Key: keysym.KeyF5, Down: false},
		runeDown('r', keysym.ModControl),
	})
	if in.Wheel != (Point{X: -1, Y: 3}) {
		t.Fatalf("wheel = %+v, want {-1 3}", in.Wheel)
	}
	if len(in.Keys) != 2 {
		t.Fatalf("collected %d key presses, want 2 (releases are dropped)", len(in.Keys))
	}

	// The next frame starts clean, and does not share storage with the last.
	previous := in
	next := in.Fold(epoch, nil)
	if next.Wheel != (Point{}) || len(next.Keys) != 0 {
		t.Fatal("wheel or keys survived an empty frame")
	}
	if len(previous.Keys) != 2 {
		t.Fatal("folding clobbered the previous frame's keys")
	}
}

func TestFoldTracksQuitAndResize(t *testing.T) {
	in := Input{}.Fold(epoch, []Event{EventResize{W: 800, H: 600}})
	if !in.Resized || in.Size != (Point{X: 800, Y: 600}) {
		t.Fatalf("resize not tracked: %+v", in)
	}
	in = in.Fold(epoch, nil)
	if in.Resized {
		t.Fatal("the resize edge survived an empty frame")
	}
	if in.Size != (Point{X: 800, Y: 600}) {
		t.Fatal("the size did not survive an empty frame")
	}

	in = in.Fold(epoch, []Event{EventQuit{}})
	if !in.Quit {
		t.Fatal("quit not tracked")
	}
	if !in.Fold(epoch, nil).Quit {
		t.Fatal("quit must be sticky; dropping it would ignore the user's request")
	}
}

func TestHitTestHelpers(t *testing.T) {
	r := Rect{X: 10, Y: 10, W: 20, H: 20}

	in := Input{}.Fold(epoch, []Event{pointer(15, 15, true)})
	if !in.Hovering(r) || !in.PressedIn(r) || !in.Holding(r) {
		t.Fatal("a press inside the rectangle was not detected")
	}
	if in.ClickedIn(r) {
		t.Fatal("a press alone is not a click")
	}

	in = in.Fold(epoch, []Event{pointer(15, 16, false)})
	if !in.ClickedIn(r) {
		t.Fatal("press and release inside was not a click")
	}

	// Press inside, release outside: the user changed their mind, and every
	// toolkit on every desktop lets them.
	in = Input{}.Fold(epoch, []Event{pointer(15, 15, true)})
	in = in.Fold(epoch, []Event{pointer(100, 100, false)})
	if in.ClickedIn(r) {
		t.Fatal("a release outside the rectangle counted as a click")
	}

	// Press outside, release inside: equally not a click.
	in = Input{}.Fold(epoch, []Event{pointer(100, 100, true)})
	in = in.Fold(epoch, []Event{pointer(15, 15, false)})
	if in.ClickedIn(r) {
		t.Fatal("a press outside the rectangle counted as a click")
	}

	// Nothing hovers until the pointer has been seen at all.
	if (Input{}).Hovering(r) {
		t.Fatal("an unseen pointer hovers")
	}
}

func TestChordMatchingIgnoresLockModifiers(t *testing.T) {
	in := Input{}.Fold(epoch, []Event{keyDown(keysym.KeyF5, keysym.ModCapsLock|keysym.ModNumLock)})
	if !in.KeyPressed(keysym.KeyF5) {
		t.Fatal("Caps Lock stopped F5 matching; it is a keyboard state, not part of a shortcut")
	}

	in = Input{}.Fold(epoch, []Event{keyDown(keysym.KeyTab, keysym.ModShift)})
	if in.KeyPressed(keysym.KeyTab) {
		t.Fatal("Shift-Tab matched a bare Tab")
	}
	if !in.Chord(keysym.ModShift, keysym.KeyTab) {
		t.Fatal("Shift-Tab did not match itself")
	}

	in = Input{}.Fold(epoch, []Event{runeDown('R', keysym.ModControl)})
	if !in.RuneChord(keysym.ModControl, 'r') {
		t.Fatal("Ctrl-R did not match case-insensitively")
	}
	if in.RuneChord(keysym.ModNone, 'r') {
		t.Fatal("Ctrl-R matched a bare r")
	}
	if in.KeyPressed(keysym.KeyUnknown) {
		t.Fatal("KeyUnknown must never match")
	}
}

func TestIsTextRune(t *testing.T) {
	tests := []struct {
		name string
		e    EventKey
		want bool
	}{
		{"a letter", runeDown('a', keysym.ModNone), true},
		{"a shifted letter", runeDown('A', keysym.ModShift), true},
		{"a space", runeDown(' ', keysym.ModNone), true},
		{"an AltGr character", runeDown('€', keysym.ModAltGr), true},
		{"a control chord", runeDown('v', keysym.ModControl), false},
		{"an alt chord", runeDown('f', keysym.ModAlt), false},
		{"a named key", keyDown(keysym.KeyReturn, keysym.ModNone), false},
		{"a control character", runeDown(0x01, keysym.ModNone), false},
		{"delete", runeDown(0x7f, keysym.ModNone), false},
		{"nothing at all", EventKey{Down: true}, false},
	}
	for _, tt := range tests {
		if got := IsTextRune(tt.e); got != tt.want {
			t.Errorf("IsTextRune(%s) = %t, want %t", tt.name, got, tt.want)
		}
	}
}

// TestFoldKeepsTextAndKeysInOrder is the reason Edits exists at all. A batch
// is one drain of the window's queue, so it can hold several keystrokes, and
// applying all the keys before all the text would move the cursor before the
// characters that were typed in front of it.
func TestFoldKeepsTextAndKeysInOrder(t *testing.T) {
	in := Input{}.Fold(epoch, []Event{
		runeDown('a', keysym.ModNone), text("a"),
		keyDown(keysym.KeyHome, keysym.ModNone),
		runeDown('b', keysym.ModNone), text("b"),
	})
	if len(in.Edits) != 5 {
		t.Fatalf("collected %d edits, want 5", len(in.Edits))
	}
	want := []bool{false, true, false, false, true}
	for i, ev := range in.Edits {
		_, isText := ev.(EventText)
		if isText != want[i] {
			t.Fatalf("edit %d is text=%t, want %t; the stream was reordered", i, isText, want[i])
		}
	}
	// The key view is unchanged: chord queries still see every press,
	// including the ones whose character came from a text event.
	if len(in.Keys) != 3 {
		t.Fatalf("collected %d key presses, want 3", len(in.Keys))
	}
	if !in.RuneChord(keysym.ModNone, 'a') {
		t.Fatal("a character key press vanished from Keys; Space would stop activating buttons")
	}
}

// TestFoldLatchesComposedText covers the flag that stops a character being
// inserted twice, and the dead-key case that makes it a latch rather than a
// per-batch check.
func TestFoldLatchesComposedText(t *testing.T) {
	var in Input
	if in.ComposedText {
		t.Fatal("a fresh input claims the backend composes text")
	}

	// A backend that only ever sends key events: the character in the key
	// event is all there is, so it is text.
	in = in.Fold(epoch, []Event{runeDown('x', keysym.ModNone)})
	if in.ComposedText {
		t.Fatal("a key event set the composed-text latch")
	}
	if got := editText(in); got != "x" {
		t.Fatalf("a key-only backend typed %q, want %q", got, "x")
	}

	// The first text event proves the backend composes, in the same batch as
	// the key press it belongs to. Only the text counts, or the user gets
	// two colons.
	in = in.Fold(epoch, []Event{runeDown(';', keysym.ModShift), text(":")})
	if !in.ComposedText {
		t.Fatal("a text event did not set the composed-text latch")
	}
	if got := editText(in); got != ":" {
		t.Fatalf("shift and the ; key typed %q, want %q", got, ":")
	}

	// A dead key: a key press with a printable character and no text at all,
	// because the platform is waiting for the next keystroke. The latch is
	// what stops the bare accent being typed.
	in = in.Fold(epoch, []Event{runeDown('^', keysym.ModNone)})
	if got := editText(in); got != "" {
		t.Fatalf("a dead key typed %q, want nothing", got)
	}
	if !in.ComposedText {
		t.Fatal("the latch cleared; it must survive a batch with no text in it")
	}

	// ...and the composed result arrives on the next keystroke.
	in = in.Fold(epoch, []Event{runeDown('e', keysym.ModNone), text("ê")})
	if got := editText(in); got != "ê" {
		t.Fatalf("the composed character came out as %q", got)
	}
}

func TestFoldDropsTextFromCommandChords(t *testing.T) {
	tests := []struct {
		name string
		mods keysym.Modifiers
		want string
	}{
		// Ctrl-V must paste and nothing else. If the platform reports a "v"
		// alongside it, the field would paste and then type the v.
		{"control", keysym.ModControl, ""},
		{"super", keysym.ModSuper, ""},
		{"control and super", keysym.ModControl | keysym.ModSuper, ""},
		// AltGr is Ctrl+Alt on Windows and Alt on macOS, and it is how a
		// large part of the world types "@" and "€". Discarding it as a
		// shortcut would break exactly the users this path is for.
		{"altgr as control+alt", keysym.ModControl | keysym.ModAlt, "€"},
		{"altgr as alt", keysym.ModAlt, "€"},
		{"altgr proper", keysym.ModAltGr, "€"},
		{"shift", keysym.ModShift, "€"},
		{"none", keysym.ModNone, "€"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := Input{}.Fold(epoch, []Event{
				EventKey{Rune: 'v', Down: true, Mods: tt.mods},
				text("€"),
			})
			if got := editText(in); got != tt.want {
				t.Fatalf("with %s held the batch typed %q, want %q", tt.mods, got, tt.want)
			}
			if !in.ComposedText {
				t.Fatal("the latch records the backend's capability, not whether the text was kept")
			}
		})
	}
}

func TestFoldIgnoresEmptyTextButStillLatches(t *testing.T) {
	// SDL ends a composition that produced nothing with an empty commit.
	in := Input{}.Fold(epoch, []Event{text("")})
	if len(in.Edits) != 0 {
		t.Fatalf("an empty commit produced %d edits", len(in.Edits))
	}
	if !in.ComposedText {
		t.Fatal("an empty commit is still proof the backend composes text")
	}
}

func TestFoldTracksModifierState(t *testing.T) {
	in := Input{}.Fold(epoch, []Event{keyDown(keysym.KeyControlL, keysym.ModControl)})
	if in.Mods != keysym.ModControl {
		t.Fatalf("mods = %s, want control", in.Mods)
	}
	// A release updates the state too, or a chord's trailing Ctrl-up would
	// leave the shell believing Ctrl is held forever.
	in = in.Fold(epoch, []Event{EventKey{Key: keysym.KeyControlL, Down: false, Mods: keysym.ModNone}})
	if in.Mods != keysym.ModNone {
		t.Fatalf("mods after the release = %s, want none", in.Mods)
	}

	// Focus loss eats the release, so the state is cleared outright.
	in = in.Fold(epoch, []Event{keyDown(keysym.KeyControlL, keysym.ModControl)})
	in = in.Fold(epoch, []Event{EventFocus{Gained: false}})
	if in.Mods != keysym.ModNone {
		t.Fatalf("focus loss left %s held", in.Mods)
	}
	// ...and text typed after coming back is text, not a shortcut.
	in = in.Fold(epoch, []Event{EventFocus{Gained: true}, text("a")})
	if got := editText(in); got != "a" {
		t.Fatalf("after focus returned the batch typed %q, want %q", got, "a")
	}
}

func TestIdle(t *testing.T) {
	var zero Input
	if !zero.Idle() {
		t.Fatal("a fresh input is not idle")
	}
	in := zero.Fold(epoch, []Event{pointer(1, 1, false)})
	if !in.Idle() {
		t.Fatal("bare pointer motion should not force a repaint")
	}
	if wheel := zero.Fold(epoch, []Event{EventWheel{DY: 1}}); wheel.Idle() {
		t.Fatal("a wheel click is not idle")
	}
	// An IME commit accepted by clicking a candidate changes the text with no
	// key press behind it. A frame skipped here leaves the field stale.
	if typed := zero.Fold(epoch, []Event{text("漢字")}); typed.Idle() {
		t.Fatal("committed text is not idle")
	}
}
