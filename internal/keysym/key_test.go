// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package keysym

import (
	"strings"
	"testing"
)

func TestKeyKeysym(t *testing.T) {
	tests := []struct {
		key  Key
		want Keysym
	}{
		// Unknown.
		{KeyUnknown, NoSymbol},

		// Editing and control.
		{KeyBackSpace, 0xff08},
		{KeyTab, 0xff09},
		{KeyLinefeed, 0xff0a},
		{KeyClear, 0xff0b},
		{KeyReturn, 0xff0d},
		{KeyPause, 0xff13},
		{KeyScrollLock, 0xff14},
		{KeySysReq, 0xff15},
		{KeyEscape, 0xff1b},
		{KeyDelete, 0xffff},
		{KeyInsert, 0xff63},
		{KeyNumLock, 0xff7f},
		{KeyCapsLock, 0xffe5},
		{KeyMenu, 0xff67},
		{KeyPrint, 0xff61},

		// Navigation.
		{KeyHome, 0xff50},
		{KeyLeft, 0xff51},
		{KeyUp, 0xff52},
		{KeyRight, 0xff53},
		{KeyDown, 0xff54},
		{KeyPageUp, 0xff55},
		{KeyPageDown, 0xff56},
		{KeyEnd, 0xff57},
		{KeyBegin, 0xff58},

		// Function keys, including the block boundaries.
		{KeyF1, 0xffbe},
		{KeyF2, 0xffbf},
		{KeyF10, 0xffc7},
		{KeyF12, 0xffc9},
		{KeyF13, 0xffca},
		{KeyF24, 0xffd5},

		// Modifiers.
		{KeyShiftL, 0xffe1},
		{KeyShiftR, 0xffe2},
		{KeyControlL, 0xffe3},
		{KeyControlR, 0xffe4},
		{KeyMetaL, 0xffe7},
		{KeyMetaR, 0xffe8},
		{KeyAltL, 0xffe9},
		{KeyAltR, 0xffea},
		{KeySuperL, 0xffeb},
		{KeySuperR, 0xffec},
		{KeyHyperL, 0xffed},
		{KeyHyperR, 0xffee},
		{KeyAltGr, 0xfe03},
		{KeyModeSwitch, 0xff7e},
		{KeyShiftLock, 0xffe6},

		// Keypad.
		{KeyKP0, 0xffb0},
		{KeyKP9, 0xffb9},
		{KeyKPSpace, 0xff80},
		{KeyKPTab, 0xff89},
		{KeyKPEnter, 0xff8d},
		{KeyKPF1, 0xff91},
		{KeyKPF4, 0xff94},
		{KeyKPHome, 0xff95},
		{KeyKPDelete, 0xff9f},
		{KeyKPEqual, 0xffbd},
		{KeyKPMultiply, 0xffaa},
		{KeyKPAdd, 0xffab},
		{KeyKPSeparator, 0xffac},
		{KeyKPSubtract, 0xffad},
		{KeyKPDecimal, 0xffae},
		{KeyKPDivide, 0xffaf},

		// Media / browser (XF86 vendor keysyms).
		{KeyAudioMute, 0x1008ff12},
		{KeyAudioRaiseVolume, 0x1008ff13},
		{KeyBrowserBack, 0x1008ff26},
		{KeyBrightnessUp, 0x1008ff02},
		{KeySleep, 0x1008ff2f},
	}

	for _, tt := range tests {
		t.Run(tt.key.String(), func(t *testing.T) {
			if got := tt.key.Keysym(); got != tt.want {
				t.Errorf("%v.Keysym() = %#x, want %#x", tt.key, uint32(got), uint32(tt.want))
			}
		})
	}
}

// TestFunctionKeyBlockIsContiguous checks the arithmetic shortcut used by
// FunctionKeysym against the individually declared constants.
func TestFunctionKeyBlockIsContiguous(t *testing.T) {
	for n := 1; n <= 24; n++ {
		k, ok := FunctionKey(n)
		if !ok {
			t.Fatalf("FunctionKey(%d) not ok", n)
		}
		want := Keysym(0xffbe + n - 1)
		if got := k.Keysym(); got != want {
			t.Errorf("FunctionKey(%d).Keysym() = %#x, want %#x", n, uint32(got), uint32(want))
		}
		if got := FunctionKeysym(n); got != want {
			t.Errorf("FunctionKeysym(%d) = %#x, want %#x", n, uint32(got), uint32(want))
		}
	}
	for _, n := range []int{-1, 0, 25, 100} {
		if got := FunctionKeysym(n); got != NoSymbol {
			t.Errorf("FunctionKeysym(%d) = %#x, want NoSymbol", n, uint32(got))
		}
		if _, ok := FunctionKey(n); ok {
			t.Errorf("FunctionKey(%d) ok = true, want false", n)
		}
	}
}

// TestKeyNameRoundTrip iterates every defined Key rather than listing them, so
// that a new key with a typo'd or missing name fails here.
func TestKeyNameRoundTrip(t *testing.T) {
	for _, k := range AllKeys() {
		name := k.String()
		if name == "" {
			t.Errorf("Key(%d).String() is empty", uint16(k))
			continue
		}
		if strings.HasPrefix(name, "Key(") {
			t.Errorf("Key(%d) has no entry in keyTable", uint16(k))
			continue
		}
		if name != strings.ToLower(name) {
			t.Errorf("Key(%d).String() = %q, want lowercase", uint16(k), name)
		}
		if strings.ContainsAny(name, " -") {
			// "-" is the chord separator, so it must never appear in a name.
			t.Errorf("Key(%d).String() = %q, must not contain a space or %q", uint16(k), name, "-")
		}
		got, ok := ParseKey(name)
		if !ok {
			t.Errorf("ParseKey(%q) not ok", name)
			continue
		}
		if got != k {
			t.Errorf("ParseKey(%q) = %v (%d), want %v (%d)", name, got, uint16(got), k, uint16(k))
		}
	}
}

// TestKeyNamesAreUnique asserts the canonical name mapping is a bijection: no
// two keys share a name, and no name resolves to two keys. This is what stops
// a copy-paste slip in keyTable from silently shadowing a key.
func TestKeyNamesAreUnique(t *testing.T) {
	byName := make(map[string]Key, numKeys)
	byKey := make(map[Key]string, numKeys)

	for _, k := range AllKeys() {
		name := keyTable[k].name
		if prev, dup := byName[name]; dup {
			t.Errorf("name %q used by both %d and %d", name, uint16(prev), uint16(k))
		}
		byName[name] = k

		if prev, dup := byKey[k]; dup {
			t.Errorf("Key %d has two names: %q and %q", uint16(k), prev, name)
		}
		byKey[k] = name
	}

	if len(byName) != int(numKeys) {
		t.Errorf("got %d distinct names for %d keys", len(byName), numKeys)
	}
	if len(byKey) != int(numKeys) {
		t.Errorf("got %d distinct keys for %d keys", len(byKey), numKeys)
	}
	if got := len(KeyNames()); got != int(numKeys) {
		t.Errorf("KeyNames() returned %d names, want %d", got, numKeys)
	}
}

// TestKeysymsAreUnique asserts no two keys claim the same keysym, which would
// make Keysym.String ambiguous and usually means a wrong constant.
func TestKeysymsAreUnique(t *testing.T) {
	seen := make(map[Keysym]Key, numKeys)
	for _, k := range AllKeys() {
		sym := k.Keysym()
		if sym == NoSymbol {
			continue
		}
		if prev, dup := seen[sym]; dup {
			t.Errorf("keysym %#x claimed by both %v and %v", uint32(sym), prev, k)
		}
		seen[sym] = k
	}
}

func TestParseKey(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  Key
		wantB bool
	}{
		{"canonical f1", "f1", KeyF1, true},
		{"canonical kp_enter", "kp_enter", KeyKPEnter, true},
		{"canonical control_l", "control_l", KeyControlL, true},
		{"canonical page_up", "page_up", KeyPageUp, true},
		{"uppercase", "F12", KeyF12, true},
		{"mixed case", "Kp_Enter", KeyKPEnter, true},
		{"surrounding space", "  left  ", KeyLeft, true},

		// Aliases.
		{"alias ctrl", "ctrl", KeyControlL, true},
		{"alias control", "control", KeyControlL, true},
		{"alias rctrl", "rctrl", KeyControlR, true},
		{"alias alt", "alt", KeyAltL, true},
		{"alias shift", "shift", KeyShiftL, true},
		{"alias super", "super", KeySuperL, true},
		{"alias win", "win", KeySuperL, true},
		{"alias cmd", "cmd", KeySuperL, true},
		{"alias meta", "meta", KeyMetaL, true},
		{"alias esc", "esc", KeyEscape, true},
		{"alias del", "del", KeyDelete, true},
		{"alias enter", "enter", KeyReturn, true},
		{"alias pgup", "pgup", KeyPageUp, true},
		{"alias pgdn", "pgdn", KeyPageDown, true},
		{"alias sysrq", "sysrq", KeySysReq, true},
		{"alias iso_level3_shift", "iso_level3_shift", KeyAltGr, true},

		// Unknown.
		{"empty", "", KeyUnknown, false},
		{"nonsense", "wibble", KeyUnknown, false},
		{"a letter is not a named key", "a", KeyUnknown, false},
		{"f0 out of range", "f0", KeyUnknown, false},
		{"f25 out of range", "f25", KeyUnknown, false},
		{"hyphenated is not a key", "page-up", KeyUnknown, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseKey(tt.in)
			if ok != tt.wantB {
				t.Fatalf("ParseKey(%q) ok = %v, want %v", tt.in, ok, tt.wantB)
			}
			if got != tt.want {
				t.Errorf("ParseKey(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestKeyAliasesResolve makes sure every alias points at a real key, so a
// typo in keyAliases cannot ship as a silently dead name.
func TestKeyAliasesResolve(t *testing.T) {
	for alias, k := range keyAliases {
		if alias != strings.ToLower(alias) {
			t.Errorf("alias %q is not lowercase", alias)
		}
		if strings.ContainsAny(alias, " -") {
			t.Errorf("alias %q must not contain a space or %q", alias, "-")
		}
		if k == KeyUnknown || k >= numKeys {
			t.Errorf("alias %q maps to invalid key %d", alias, uint16(k))
			continue
		}
		if k.Keysym() == NoSymbol {
			t.Errorf("alias %q maps to %v, which has no keysym", alias, k)
		}
		if _, canonical := byCanonicalName(alias); canonical {
			t.Errorf("alias %q shadows a canonical name", alias)
		}
	}
}

// byCanonicalName reports whether name is a canonical key name (as opposed to
// an alias).
func byCanonicalName(name string) (Key, bool) {
	for i, e := range keyTable {
		if e.name == name {
			return Key(i), true
		}
	}
	return KeyUnknown, false
}

func TestKeyStringOutOfRange(t *testing.T) {
	k := Key(60000)
	if got := k.String(); got != "Key(60000)" {
		t.Errorf("Key(60000).String() = %q, want %q", got, "Key(60000)")
	}
	if got := k.Keysym(); got != NoSymbol {
		t.Errorf("Key(60000).Keysym() = %#x, want NoSymbol", uint32(got))
	}
}

func TestKeyIsModifier(t *testing.T) {
	mods := []Key{
		KeyShiftL, KeyShiftR, KeyControlL, KeyControlR, KeyMetaL, KeyMetaR,
		KeyAltL, KeyAltR, KeySuperL, KeySuperR, KeyHyperL, KeyHyperR,
		KeyAltGr, KeyModeSwitch, KeyCapsLock, KeyNumLock, KeyShiftLock,
	}
	for _, k := range mods {
		if !k.IsModifier() {
			t.Errorf("%v.IsModifier() = false, want true", k)
		}
	}
	for _, k := range []Key{KeyUnknown, KeyF1, KeyLeft, KeyKPEnter, KeyDelete, KeyTab, KeyAudioMute} {
		if k.IsModifier() {
			t.Errorf("%v.IsModifier() = true, want false", k)
		}
	}
}

func TestKeyIsKeypad(t *testing.T) {
	for _, k := range []Key{KeyKP0, KeyKP9, KeyKPEnter, KeyKPDivide, KeyKPEqual, KeyKPPageUp} {
		if !k.IsKeypad() {
			t.Errorf("%v.IsKeypad() = false, want true", k)
		}
	}
	for _, k := range []Key{KeyUnknown, KeyF1, KeyReturn, KeyPageUp, KeyControlL, KeyAudioMute} {
		if k.IsKeypad() {
			t.Errorf("%v.IsKeypad() = true, want false", k)
		}
	}
}

func TestKeysymString(t *testing.T) {
	tests := []struct {
		sym  Keysym
		want string
	}{
		{NoSymbol, "NoSymbol"},
		{F1, "f1"},
		{ControlL, "control_l"},
		{KPEnter, "kp_enter"},
		{Delete, "delete"},
		{ISOLevel3Shift, "altgr"},
		{FromRune('a'), `'a'`},
		{FromRune('é'), `'é'`},
		{FromRune('漢'), `'漢'`},
		{Space, `' '`},
		{Keysym(0xfe99), "Keysym(0xfe99)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.sym.String(); got != tt.want {
				t.Errorf("Keysym(%#x).String() = %q, want %q", uint32(tt.sym), got, tt.want)
			}
		})
	}
}

func TestKeysymIsUnicode(t *testing.T) {
	tests := []struct {
		sym  Keysym
		want bool
	}{
		{FromRune('a'), false},
		{FromRune('é'), false},
		{FromRune('Ā'), true},
		{FromRune('漢'), true},
		{FromRune('🙂'), true},
		{UnicodeBase, false},
		{UnicodeBase + 0xff, false},
		{UnicodeBase + 0x100, true},
		{UnicodeBase + 0x10ffff, true},
		{UnicodeBase + 0x110000, false},
		{F1, false},
		{NoSymbol, false},
	}
	for _, tt := range tests {
		if got := tt.sym.IsUnicode(); got != tt.want {
			t.Errorf("Keysym(%#x).IsUnicode() = %v, want %v", uint32(tt.sym), got, tt.want)
		}
	}
}

// TestAllKeysReturnsCopy guards the accessors that hand out slices.
func TestAllKeysReturnsCopy(t *testing.T) {
	a := AllKeys()
	if len(a) != int(numKeys) {
		t.Fatalf("AllKeys() len = %d, want %d", len(a), numKeys)
	}
	a[0] = KeyF1
	if AllKeys()[0] != KeyUnknown {
		t.Error("AllKeys() shares state between calls")
	}

	n := KeyNames()
	n[0] = "mutated"
	if KeyNames()[0] == "mutated" {
		t.Error("KeyNames() shares state between calls")
	}
}
