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
}
