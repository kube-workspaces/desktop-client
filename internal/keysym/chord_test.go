// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package keysym

import (
	"errors"
	"strings"
	"testing"
)

func TestParseChord(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantMods []Key
		wantKey  Key
	}{
		{"ctrl-alt-del", "ctrl-alt-del", []Key{KeyControlL, KeyAltL}, KeyDelete},
		{"ctrl-alt-delete", "ctrl-alt-delete", []Key{KeyControlL, KeyAltL}, KeyDelete},
		{"canonical spelling", "control_l-alt_l-delete", []Key{KeyControlL, KeyAltL}, KeyDelete},
		{"ctrl-alt-f2", "ctrl-alt-f2", []Key{KeyControlL, KeyAltL}, KeyF2},
		{"ctrl-alt-f12", "ctrl-alt-f12", []Key{KeyControlL, KeyAltL}, KeyF12},
		{"alt-tab", "alt-tab", []Key{KeyAltL}, KeyTab},
		{"alt-f4", "alt-f4", []Key{KeyAltL}, KeyF4},
		{"bare super", "super", nil, KeySuperL},
		{"bare f1", "f1", nil, KeyF1},
		{"uppercase", "CTRL-ALT-DEL", []Key{KeyControlL, KeyAltL}, KeyDelete},
		{"mixed case", "Ctrl-Alt-Del", []Key{KeyControlL, KeyAltL}, KeyDelete},
		{"surrounding space", "  alt-tab  ", []Key{KeyAltL}, KeyTab},
		{"inner space", "ctrl - alt - del", []Key{KeyControlL, KeyAltL}, KeyDelete},
		{"right hand modifier", "control_r-alt_r-del", []Key{KeyControlR, KeyAltR}, KeyDelete},
		{"win alias", "win-tab", []Key{KeySuperL}, KeyTab},
		{"shift-insert", "shift-insert", []Key{KeyShiftL}, KeyInsert},
		{"three modifiers", "ctrl-shift-alt-escape", []Key{KeyControlL, KeyShiftL, KeyAltL}, KeyEscape},
		{"altgr modifier", "altgr-f1", []Key{KeyAltGr}, KeyF1},
		{"keypad key", "ctrl-kp_enter", []Key{KeyControlL}, KeyKPEnter},
		{"capslock as modifier", "capslock-f1", []Key{KeyCapsLock}, KeyF1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseChord(tt.in)
			if err != nil {
				t.Fatalf("ParseChord(%q) error = %v", tt.in, err)
			}
			if got.Key != tt.wantKey {
				t.Errorf("ParseChord(%q).Key = %v, want %v", tt.in, got.Key, tt.wantKey)
			}
			if len(got.Mods) != len(tt.wantMods) {
				t.Fatalf("ParseChord(%q).Mods = %v, want %v", tt.in, got.Mods, tt.wantMods)
			}
			for i := range got.Mods {
				if got.Mods[i] != tt.wantMods[i] {
					t.Errorf("ParseChord(%q).Mods[%d] = %v, want %v", tt.in, i, got.Mods[i], tt.wantMods[i])
				}
			}
		})
	}
}

func TestParseChordErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"only spaces", "   "},
		{"unknown key", "ctrl-alt-wibble"},
		{"unknown modifier", "wibble-del"},
		{"key used as modifier", "f1-del"},
		{"tab is not a modifier", "tab-del"},
		{"letters are not named keys", "ctrl-c"},
		{"trailing separator", "ctrl-alt-"},
		{"leading separator", "-del"},
		{"double separator", "ctrl--del"},
		{"repeated modifier", "ctrl-ctrl-del"},
		{"repeated modifier via alias", "ctrl-control-del"},
		{"unknown is not sendable", "unknown"},
		{"unknown as key", "ctrl-unknown"},
		{"f0 out of range", "ctrl-alt-f0"},
		{"f25 out of range", "ctrl-alt-f25"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseChord(tt.in)
			if err == nil {
				t.Fatalf("ParseChord(%q) = %v, want error", tt.in, got)
			}
			if !errors.Is(err, ErrInvalidChord) {
				t.Errorf("ParseChord(%q) error = %v, want it to wrap ErrInvalidChord", tt.in, err)
			}
			if !strings.HasPrefix(err.Error(), "keysym: invalid chord") {
				t.Errorf("ParseChord(%q) error = %q, want the sentinel prefix", tt.in, err)
			}
		})
	}
}

// TestChordSequenceOrder is the important one: it pins the modifiers-down,
// key-down, key-up, modifiers-up-in-reverse contract the protocol needs.
func TestChordSequence(t *testing.T) {
	tests := []struct {
		name  string
		chord Chord
		want  []KeyAction
	}{
		{
			name:  "ctrl-alt-del",
			chord: ChordCtrlAltDel,
			want: []KeyAction{
				{ControlL, true},
				{AltL, true},
				{Delete, true},
				{Delete, false},
				{AltL, false}, // reverse order: Alt released before Ctrl
				{ControlL, false},
			},
		},
		{
			name:  "alt-tab",
			chord: ChordAltTab,
			want: []KeyAction{
				{AltL, true},
				{Tab, true},
				{Tab, false},
				{AltL, false},
			},
		},
		{
			name:  "bare super has no modifiers",
			chord: ChordSuper,
			want: []KeyAction{
				{SuperL, true},
				{SuperL, false},
			},
		},
		{
			name:  "three modifiers unwind as a stack",
			chord: Chord{Mods: []Key{KeyControlL, KeyShiftL, KeyAltL}, Key: KeyEscape},
			want: []KeyAction{
				{ControlL, true},
				{ShiftL, true},
				{AltL, true},
				{Escape, true},
				{Escape, false},
				{AltL, false},
				{ShiftL, false},
				{ControlL, false},
			},
		},
		{
			name:  "key with no keysym is skipped",
			chord: Chord{Mods: []Key{KeyControlL}, Key: KeyUnknown},
			want: []KeyAction{
				{ControlL, true},
				{ControlL, false},
			},
		},
		{
			name:  "empty chord yields nothing",
			chord: Chord{},
			want:  []KeyAction{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.chord.Sequence()
			assertActions(t, got, tt.want)
		})
	}
}

// TestChordSequenceInvariants checks the ordering rule structurally, over
// every common chord, rather than against a hand-written expectation.
func TestChordSequenceInvariants(t *testing.T) {
	for name, c := range CommonChords() {
		t.Run(name, func(t *testing.T) {
			seq := c.Sequence()
			n := len(c.Mods)
			if len(seq) != 2*n+2 {
				t.Fatalf("Sequence() has %d actions, want %d", len(seq), 2*n+2)
			}

			// Modifiers down, in order.
			for i, m := range c.Mods {
				if seq[i] != (KeyAction{m.Keysym(), true}) {
					t.Errorf("seq[%d] = %v, want %v down", i, seq[i], m)
				}
			}

			// Key down then up, in the middle.
			keySym := c.Key.Keysym()
			if seq[n] != (KeyAction{keySym, true}) {
				t.Errorf("seq[%d] = %v, want %v down", n, seq[n], c.Key)
			}
			if seq[n+1] != (KeyAction{keySym, false}) {
				t.Errorf("seq[%d] = %v, want %v up", n+1, seq[n+1], c.Key)
			}

			// Modifiers up, in reverse order.
			for i := range c.Mods {
				m := c.Mods[n-1-i]
				at := n + 2 + i
				if seq[at] != (KeyAction{m.Keysym(), false}) {
					t.Errorf("seq[%d] = %v, want %v up", at, seq[at], m)
				}
			}

			// Every press must have a matching release and vice versa.
			balance := map[Keysym]int{}
			for _, a := range seq {
				if a.Down {
					balance[a.Sym]++
				} else {
					balance[a.Sym]--
				}
			}
			for sym, b := range balance {
				if b != 0 {
					t.Errorf("keysym %v is unbalanced by %d; it would be left stuck", sym, b)
				}
			}
		})
	}
}

func TestChordString(t *testing.T) {
	tests := []struct {
		chord Chord
		want  string
	}{
		{ChordCtrlAltDel, "control_l-alt_l-delete"},
		{ChordAltTab, "alt_l-tab"},
		{ChordAltF4, "alt_l-f4"},
		{ChordSuper, "super_l"},
		{ChordCtrlEscape, "control_l-escape"},
		{ChordCtrlAltF1, "control_l-alt_l-f1"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.chord.String(); got != tt.want {
				t.Errorf("Chord.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestChordStringRoundTrip asserts Chord.String emits something ParseChord
// accepts, for every common chord.
func TestChordStringRoundTrip(t *testing.T) {
	for name, c := range CommonChords() {
		s := c.String()
		got, err := ParseChord(s)
		if err != nil {
			t.Errorf("%s: ParseChord(%q) error = %v", name, s, err)
			continue
		}
		assertActions(t, got.Sequence(), c.Sequence())
	}
}

func TestCommonChords(t *testing.T) {
	m := CommonChords()

	// The named chords a VDI client must offer.
	for _, name := range []string{"ctrl-alt-del", "alt-tab", "alt-f4", "super", "ctrl-alt-f1", "ctrl-alt-f12"} {
		if _, ok := m[name]; !ok {
			t.Errorf("CommonChords() is missing %q", name)
		}
	}
	if _, ok := m["ctrl-alt-f13"]; ok {
		t.Error("CommonChords() should stop at f12")
	}

	// Every name must parse back to the same chord.
	for name, c := range m {
		parsed, err := ParseChord(name)
		if err != nil {
			t.Errorf("ParseChord(%q) error = %v", name, err)
			continue
		}
		assertActions(t, parsed.Sequence(), c.Sequence())
	}

	// The returned map and its chords must be copies.
	m["ctrl-alt-del"] = Chord{}
	if _, ok := CommonChords()["ctrl-alt-del"]; !ok {
		t.Error("CommonChords() shares its map between calls")
	}
	mods := CommonChords()["ctrl-alt-del"].Mods
	mods[0] = KeyF1
	if CommonChords()["ctrl-alt-del"].Mods[0] != KeyControlL {
		t.Error("CommonChords() shares Mods backing arrays between calls")
	}

	names := CommonChordNames()
	if len(names) != len(CommonChords()) {
		t.Errorf("CommonChordNames() len = %d, want %d", len(names), len(CommonChords()))
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("CommonChordNames() is not sorted: %q before %q", names[i-1], names[i])
		}
	}
}

func TestCtrlAltF(t *testing.T) {
	for n := 1; n <= 24; n++ {
		c, ok := CtrlAltF(n)
		if !ok {
			t.Fatalf("CtrlAltF(%d) not ok", n)
		}
		if c.Key.Keysym() != FunctionKeysym(n) {
			t.Errorf("CtrlAltF(%d).Key = %v, want f%d", n, c.Key, n)
		}
		if len(c.Mods) != 2 || c.Mods[0] != KeyControlL || c.Mods[1] != KeyAltL {
			t.Errorf("CtrlAltF(%d).Mods = %v, want [control_l alt_l]", n, c.Mods)
		}
	}
	for _, n := range []int{0, -1, 25} {
		if _, ok := CtrlAltF(n); ok {
			t.Errorf("CtrlAltF(%d) ok = true, want false", n)
		}
	}
}

func TestMustParseChord(t *testing.T) {
	if got := MustParseChord("ctrl-alt-del").String(); got != "control_l-alt_l-delete" {
		t.Errorf("MustParseChord(...) = %q", got)
	}

	defer func() {
		if recover() == nil {
			t.Error("MustParseChord did not panic on invalid input")
		}
	}()
	MustParseChord("ctrl-alt-wibble")
}

func TestKeyActionString(t *testing.T) {
	if got := (KeyAction{ControlL, true}).String(); got != "control_l↓" {
		t.Errorf("KeyAction.String() = %q", got)
	}
	if got := (KeyAction{ControlL, false}).String(); got != "control_l↑" {
		t.Errorf("KeyAction.String() = %q", got)
	}
}

// assertActions compares two action sequences and reports a readable diff.
func assertActions(t *testing.T, got, want []KeyAction) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d actions %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("action[%d] = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}
}
