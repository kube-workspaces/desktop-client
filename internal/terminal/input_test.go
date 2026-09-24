// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// typed returns the bytes and quit flag for one event.
func typed(in *input, e viewer.Event) (out []byte, quit bool) {
	return in.handle(e)
}

func mustBytes(t *testing.T, in *input, e viewer.Event) []byte {
	t.Helper()
	out, quit := typed(in, e)
	if quit {
		t.Fatalf("event %+v quit the session", e)
	}
	return out
}

func key(k keysym.Key, r rune, mods keysym.Modifiers) viewer.EventKey {
	return viewer.EventKey{Key: k, Rune: r, Down: true, Mods: mods}
}

func release(k keysym.Key, r rune, mods keysym.Modifiers) viewer.EventKey {
	return viewer.EventKey{Key: k, Rune: r, Down: false, Mods: mods}
}

func TestComposedTextBecomesBytes(t *testing.T) {
	in := newInput()
	if got := mustBytes(t, in, viewer.EventText{Text: "hello world"}); string(got) != "hello world" {
		t.Fatalf("text = %q", got)
	}
}

func TestKeyEventSuppressedWhenComposed(t *testing.T) {
	// Composed backends deliver the character via EventText; the key event for
	// it must not additionally type the character.
	in := newInput()
	if got := mustBytes(t, in, key(keysym.KeyUnknown, 'a', keysym.ModNone)); got != nil {
		t.Fatalf("composed key press produced bytes %q", got)
	}
}

func TestKeyEventDecodesWithoutText(t *testing.T) {
	in := newInput()
	in.composedText = false
	if got := mustBytes(t, in, key(keysym.KeyUnknown, 'a', keysym.ModNone)); string(got) != "a" {
		t.Fatalf("bare key = %q", got)
	}
}

func TestControlChords(t *testing.T) {
	in := newInput()
	cases := []struct {
		r    rune
		want byte
	}{
		{'a', 0x01}, {'z', 0x1a}, {'A', 0x01}, {'[', 0x1b}, {']', 0x1d},
	}
	for _, tc := range cases {
		got := mustBytes(t, in, key(keysym.KeyUnknown, tc.r, keysym.ModControl))
		if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("Ctrl+%c = % x, want %#02x", tc.r, got, tc.want)
		}
	}
}

func TestMetaChord(t *testing.T) {
	in := newInput()
	if got := mustBytes(t, in, key(keysym.KeyUnknown, 'x', keysym.ModAlt)); string(got) != "\x1bx" {
		t.Fatalf("Alt+x = %q", got)
	}
}

func TestExcludedChordsProduceNothing(t *testing.T) {
	in := newInput()
	evs := []viewer.Event{
		// Ctrl+Alt is the quit chord's territory.
		key(keysym.KeyUnknown, 'x', keysym.ModControl|keysym.ModAlt),
		// AltGr is a third-level shift, not a Meta chord.
		key(keysym.KeyUnknown, 'a', keysym.ModAltGr),
		key(keysym.KeyUnknown, 'a', keysym.ModAltGr|keysym.ModShift),
		// Super is not a terminal modifier.
		key(keysym.KeyUnknown, 's', keysym.ModSuper),
		// Control plus punctuation outside the letter band is not encodable.
		key(keysym.KeyUnknown, ' ', keysym.ModControl),
	}
	for _, e := range evs {
		if got := mustBytes(t, in, e); got != nil {
			t.Fatalf("%+v produced bytes %q", e, got)
		}
	}
}

func TestNamedKeys(t *testing.T) {
	in := newInput()
	cases := []struct {
		name string
		k    keysym.Key
		mods keysym.Modifiers
		want string
	}{
		{"enter", keysym.KeyReturn, keysym.ModNone, "\r"},
		{"kpend", keysym.KeyKPEnter, keysym.ModNone, "\r"},
		{"tab", keysym.KeyTab, keysym.ModNone, "\t"},
		{"shift-tab", keysym.KeyTab, keysym.ModShift, "\x1b[Z"},
		{"backspace", keysym.KeyBackSpace, keysym.ModNone, "\x7f"},
		{"escape", keysym.KeyEscape, keysym.ModNone, "\x1b"},
		{"delete", keysym.KeyDelete, keysym.ModNone, "\x1b[3~"},
		{"insert", keysym.KeyInsert, keysym.ModNone, "\x1b[2~"},
		{"left", keysym.KeyLeft, keysym.ModNone, "\x1b[D"},
		{"right", keysym.KeyRight, keysym.ModNone, "\x1b[C"},
		{"up", keysym.KeyUp, keysym.ModNone, "\x1b[A"},
		{"down", keysym.KeyDown, keysym.ModNone, "\x1b[B"},
		{"ctrl-left", keysym.KeyLeft, keysym.ModControl, "\x1b[1;5D"},
		{"ctrl-right", keysym.KeyRight, keysym.ModControl, "\x1b[1;5C"},
		{"home", keysym.KeyHome, keysym.ModNone, "\x1b[H"},
		{"ctrl-home", keysym.KeyHome, keysym.ModControl, "\x1b[1;5H"},
		{"end", keysym.KeyEnd, keysym.ModNone, "\x1b[F"},
		{"ctrl-end", keysym.KeyEnd, keysym.ModControl, "\x1b[1;5F"},
		{"page-up", keysym.KeyPageUp, keysym.ModNone, "\x1b[5~"},
		{"ctrl-page-up", keysym.KeyPageUp, keysym.ModControl, "\x1b[5;5~"},
		{"page-down", keysym.KeyPageDown, keysym.ModNone, "\x1b[6~"},
		{"ctrl-page-down", keysym.KeyPageDown, keysym.ModControl, "\x1b[6;5~"},
		{"f1", keysym.KeyF1, keysym.ModNone, "\x1bOP"},
		{"f4", keysym.KeyF4, keysym.ModNone, "\x1bOS"},
		{"f5", keysym.KeyF5, keysym.ModNone, "\x1b[15~"},
		{"f12", keysym.KeyF12, keysym.ModNone, "\x1b[24~"},
		{"kp1", keysym.KeyKP1, keysym.ModNone, "1"},
		{"kp0", keysym.KeyKP0, keysym.ModNone, "0"},
		{"kpspace", keysym.KeyKPSpace, keysym.ModNone, " "},
		{"kpadd", keysym.KeyKPAdd, keysym.ModNone, "+"},
		{"kpsub", keysym.KeyKPSubtract, keysym.ModNone, "-"},
		{"kpmul", keysym.KeyKPMultiply, keysym.ModNone, "*"},
		{"kpdiv", keysym.KeyKPDivide, keysym.ModNone, "/"},
		{"kpdec", keysym.KeyKPDecimal, keysym.ModNone, "."},
	}
	for _, tc := range cases {
		if got := mustBytes(t, in, key(tc.k, 0, tc.mods)); string(got) != tc.want {
			t.Fatalf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestMetaNamedKey(t *testing.T) {
	in := newInput()
	if got := mustBytes(t, in, key(keysym.KeyLeft, 0, keysym.ModAlt)); string(got) != "\x1b\x1b[D" {
		t.Fatalf("Alt+Left = %q", got)
	}
	if got := mustBytes(t, in, key(keysym.KeyF1, 0, keysym.ModAlt)); string(got) != "\x1b\x1bOP" {
		t.Fatalf("Alt+F1 = %q", got)
	}
}

func TestModifiedNamedKeys(t *testing.T) {
	in := newInput()
	cases := []struct {
		name string
		k    keysym.Key
		mods keysym.Modifiers
		want string
	}{
		{"shift-left", keysym.KeyLeft, keysym.ModShift, "\x1b[1;2D"},
		{"shift-right", keysym.KeyRight, keysym.ModShift, "\x1b[1;2C"},
		{"shift-up", keysym.KeyUp, keysym.ModShift, "\x1b[1;2A"},
		{"shift-down", keysym.KeyDown, keysym.ModShift, "\x1b[1;2B"},
		{"shift-home", keysym.KeyHome, keysym.ModShift, "\x1b[1;2H"},
		{"shift-end", keysym.KeyEnd, keysym.ModShift, "\x1b[1;2F"},
		{"shift-page-up", keysym.KeyPageUp, keysym.ModShift, "\x1b[5;2~"},
		{"shift-page-down", keysym.KeyPageDown, keysym.ModShift, "\x1b[6;2~"},
		{"shift-insert", keysym.KeyInsert, keysym.ModShift, "\x1b[2;2~"},
		{"shift-delete", keysym.KeyDelete, keysym.ModShift, "\x1b[3;2~"},
		{"ctrl-shift-left", keysym.KeyLeft, keysym.ModControl | keysym.ModShift, "\x1b[1;6D"},
		{"ctrl-alt-left", keysym.KeyLeft, keysym.ModControl | keysym.ModAlt, "\x1b[1;7D"},
		{"shift-f1", keysym.KeyF1, keysym.ModShift, "\x1b[1;2P"},
		{"shift-f4", keysym.KeyF4, keysym.ModShift, "\x1b[1;2S"},
		{"ctrl-f5", keysym.KeyF5, keysym.ModControl, "\x1b[15;5~"},
		{"ctrl-shift-f11", keysym.KeyF11, keysym.ModControl | keysym.ModShift, "\x1b[23;6~"},
		{"ctrl-alt-f12", keysym.KeyF12, keysym.ModControl | keysym.ModAlt, "\x1b[24;7~"},
	}
	for _, tc := range cases {
		if got := mustBytes(t, in, key(tc.k, 0, tc.mods)); string(got) != tc.want {
			t.Fatalf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAltGrStillTypes(t *testing.T) {
	// AltGr is a third-level shift that produces characters: it must never be
	// turned into a meta prefix or a modifier code, because doing so would eat
	// the very characters it exists to type.
	in := newInput()
	evs := []viewer.Event{
		key(keysym.KeyUnknown, '@', keysym.ModAltGr),
		key(keysym.KeyUnknown, '@', keysym.ModAltGr|keysym.ModAlt),
		key(keysym.KeyUnknown, 'ß', keysym.ModAltGr|keysym.ModShift),
	}
	for _, e := range evs {
		if got := mustBytes(t, in, e); got != nil {
			t.Fatalf("%+v produced bytes %q, want none", e, got)
		}
	}
	// A navigation key pressed with AltGr stays unmodified: the AltGr/Ctrl
	// bits arrive together on Windows, and Ctrl+Alt+Left must not read as a
	// modified command sequence the shell has no idea it asked for.
	if got := mustBytes(t, in, key(keysym.KeyLeft, 0, keysym.ModAltGr|keysym.ModControl|keysym.ModAlt)); string(got) != "\x1b[D" {
		t.Fatalf("AltGr+Ctrl+Alt+Left = %q, want the plain arrow", got)
	}
}

func TestModifierPressProducesNothing(t *testing.T) {
	in := newInput()
	if got := mustBytes(t, in, key(keysym.KeyShiftL, 0, keysym.ModShift)); got != nil {
		t.Fatalf("Shift press = %q", got)
	}
	if got := mustBytes(t, in, key(keysym.KeyControlL, 0, keysym.ModControl)); got != nil {
		t.Fatalf("Ctrl press = %q", got)
	}
}

func TestRepeatsForward(t *testing.T) {
	in := newInput()
	e := viewer.EventKey{Key: keysym.KeyUnknown, Rune: 'a', Down: true, Repeat: true, Mods: keysym.ModNone}
	in.composedText = false
	if got := mustBytes(t, in, e); string(got) != "a" {
		t.Fatalf("repeated 'a' = %q", got)
	}
}

func TestQuitChord(t *testing.T) {
	in := newInput()
	for _, e := range []viewer.Event{
		viewer.EventQuit{},
		key(keysym.KeyUnknown, 'q', keysym.ModControl|keysym.ModAlt),
		key(keysym.KeyUnknown, 'Q', keysym.ModControl|keysym.ModAlt|keysym.ModShift),
	} {
		if out, quit := typed(in, e); quit != true || out != nil {
			t.Fatalf("%+v = (%q, quit=%v), want (nil, true)", e, out, quit)
		}
	}
}

func TestQuitChordRepeatActsOnce(t *testing.T) {
	in := newInput()
	press := viewer.EventKey{Key: keysym.KeyUnknown, Rune: 'q', Down: true, Repeat: false, Mods: keysym.ModControl | keysym.ModAlt}
	rep := press
	rep.Repeat = true
	if out, quit := typed(in, press); !quit || out != nil {
		t.Fatalf("press = (%q, %v)", out, quit)
	}
	if out, quit := typed(in, rep); quit || out != nil {
		t.Fatalf("auto-repeat quit = (%q, %v), want (nil, false)", out, quit)
	}
}

func TestQuitChordReleaseSwallowed(t *testing.T) {
	in := newInput()
	press := key(keysym.KeyUnknown, 'q', keysym.ModControl|keysym.ModAlt)
	up := release(keysym.KeyUnknown, 'q', keysym.ModControl|keysym.ModAlt)

	if _, quit := typed(in, press); !quit {
		t.Fatal("press did not quit")
	}
	// The release of the key the shell never saw must not leak a lone 'q'.
	if out, quit := typed(in, up); quit || out != nil {
		t.Fatalf("release = (%q, %v), want (nil, false)", out, quit)
	}
	// And a second press of the same chord quits again.
	if _, quit := typed(in, press); !quit {
		t.Fatal("second press did not quit")
	}
}

func TestCustomQuitRune(t *testing.T) {
	in := newInput()
	in.quitRune = 'x'
	if out, quit := typed(in, key(keysym.KeyUnknown, 'x', keysym.ModControl|keysym.ModAlt)); !quit || out != nil {
		t.Fatalf("Ctrl+Alt+x = (%q, %v)", out, quit)
	}
	// The default 'q' no longer quits.
	if out, quit := typed(in, key(keysym.KeyUnknown, 'q', keysym.ModControl|keysym.ModAlt)); quit || out != nil {
		t.Fatalf("Ctrl+Alt+q = (%q, %v), want (nil, false)", out, quit)
	}
}

func TestEscapeReachesShell(t *testing.T) {
	// The plan's "Esc quits" was deliberately not implemented: Esc is part of
	// the shell's vocabulary (vim, nano, readline). A lone Esc key must be
	// forwarded, not treated as a quit.
	in := newInput()
	if got := mustBytes(t, in, key(keysym.KeyEscape, 0, keysym.ModNone)); string(got) != "\x1b" {
		t.Fatalf("Esc = %q, want the escape byte itself", got)
	}
}
