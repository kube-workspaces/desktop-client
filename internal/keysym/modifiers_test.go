// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package keysym

import "testing"

func TestModifiersHas(t *testing.T) {
	m := ModShift | ModControl

	tests := []struct {
		name string
		of   Modifiers
		want bool
	}{
		{"shift", ModShift, true},
		{"control", ModControl, true},
		{"shift and control", ModShift | ModControl, true},
		{"none is always present", ModNone, true},
		{"alt", ModAlt, false},
		{"shift and alt", ModShift | ModAlt, false},
		{"super", ModSuper, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.Has(tt.of); got != tt.want {
				t.Errorf("(%v).Has(%v) = %v, want %v", m, tt.of, got, tt.want)
			}
		})
	}

	if !m.HasAny(ModShift | ModAlt) {
		t.Error("HasAny(shift|alt) = false, want true")
	}
	if m.HasAny(ModAlt | ModSuper) {
		t.Error("HasAny(alt|super) = true, want false")
	}
	if got := m.Count(); got != 2 {
		t.Errorf("Count() = %d, want 2", got)
	}
	if got := m.With(ModAlt); got != ModShift|ModControl|ModAlt {
		t.Errorf("With(alt) = %v", got)
	}
	if got := m.Without(ModShift); got != ModControl {
		t.Errorf("Without(shift) = %v", got)
	}
}

func TestModifiersString(t *testing.T) {
	tests := []struct {
		mods Modifiers
		want string
	}{
		{ModNone, "none"},
		{ModShift, "shift"},
		{ModControl, "control"},
		{ModAlt, "alt"},
		{ModSuper, "super"},
		{ModAltGr, "altgr"},
		{ModCapsLock, "capslock"},
		{ModNumLock, "numlock"},
		{ModShift | ModControl, "shift|control"},
		// Order is fixed by declaration, not by the order the caller OR'd them.
		{ModControl | ModShift, "shift|control"},
		{ModControl | ModAlt | ModShift, "shift|control|alt"},
		{ModShift | ModControl | ModAlt | ModSuper | ModAltGr | ModCapsLock | ModNumLock,
			"shift|control|alt|super|altgr|capslock|numlock"},
		{Modifiers(1 << 15), "none"}, // undefined bit
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.mods.String(); got != tt.want {
				t.Errorf("Modifiers(%#x).String() = %q, want %q", uint16(tt.mods), got, tt.want)
			}
		})
	}
}

func TestModifierOf(t *testing.T) {
	tests := []struct {
		key   Key
		want  Modifiers
		wantB bool
	}{
		{KeyShiftL, ModShift, true},
		{KeyShiftR, ModShift, true},
		{KeyShiftLock, ModShift, true},
		{KeyControlL, ModControl, true},
		{KeyControlR, ModControl, true},
		{KeyAltL, ModAlt, true},
		{KeyAltR, ModAlt, true},
		{KeyMetaL, ModAlt, true},
		{KeyMetaR, ModAlt, true},
		{KeySuperL, ModSuper, true},
		{KeySuperR, ModSuper, true},
		{KeyHyperL, ModSuper, true},
		{KeyHyperR, ModSuper, true},
		{KeyAltGr, ModAltGr, true},
		{KeyModeSwitch, ModAltGr, true},
		{KeyCapsLock, ModCapsLock, true},
		{KeyNumLock, ModNumLock, true},

		{KeyUnknown, ModNone, false},
		{KeyF1, ModNone, false},
		{KeyTab, ModNone, false},
		{KeyKPEnter, ModNone, false},
		{KeyScrollLock, ModNone, false},
	}
	for _, tt := range tests {
		t.Run(tt.key.String(), func(t *testing.T) {
			got, ok := ModifierOf(tt.key)
			if ok != tt.wantB {
				t.Fatalf("ModifierOf(%v) ok = %v, want %v", tt.key, ok, tt.wantB)
			}
			if got != tt.want {
				t.Errorf("ModifierOf(%v) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestModifiersOf(t *testing.T) {
	tests := []struct {
		name string
		keys []Key
		want Modifiers
	}{
		{"none", nil, ModNone},
		{"ctrl-alt", []Key{KeyControlL, KeyAltL}, ModControl | ModAlt},
		{"left and right collapse", []Key{KeyShiftL, KeyShiftR}, ModShift},
		{"non-modifiers ignored", []Key{KeyControlL, KeyF1, KeyDelete}, ModControl},
		{"all", []Key{KeyShiftL, KeyControlL, KeyAltL, KeySuperL, KeyAltGr, KeyCapsLock, KeyNumLock},
			ModShift | ModControl | ModAlt | ModSuper | ModAltGr | ModCapsLock | ModNumLock},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ModifiersOf(tt.keys...); got != tt.want {
				t.Errorf("ModifiersOf(%v) = %v, want %v", tt.keys, got, tt.want)
			}
		})
	}
}

func TestModifiersKeys(t *testing.T) {
	m := ModControl | ModShift | ModAlt
	got := m.Keys()
	want := []Key{KeyShiftL, KeyControlL, KeyAltL}
	if len(got) != len(want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("Keys()[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	syms := m.Keysyms()
	wantSyms := []Keysym{ShiftL, ControlL, AltL}
	if len(syms) != len(wantSyms) {
		t.Fatalf("Keysyms() = %v, want %v", syms, wantSyms)
	}
	for i := range syms {
		if syms[i] != wantSyms[i] {
			t.Errorf("Keysyms()[%d] = %v, want %v", i, syms[i], wantSyms[i])
		}
	}

	if got := ModNone.Keys(); len(got) != 0 {
		t.Errorf("ModNone.Keys() = %v, want empty", got)
	}
}

// TestModifierRoundTrip asserts every bit has both a name and a key, so adding
// a bit without updating the tables fails here.
func TestModifierTablesAreComplete(t *testing.T) {
	if len(modNames) != len(modKeys) {
		t.Fatalf("modNames has %d entries, modKeys has %d", len(modNames), len(modKeys))
	}
	for i := range modNames {
		if modNames[i].bit != modKeys[i].bit {
			t.Errorf("table row %d disagrees: %v vs %v", i, modNames[i].bit, modKeys[i].bit)
		}
		k := modKeys[i].key
		if !k.IsModifier() {
			t.Errorf("modKeys[%d] = %v, which is not a modifier key", i, k)
		}
		bit, ok := ModifierOf(k)
		if !ok || bit != modKeys[i].bit {
			t.Errorf("ModifierOf(%v) = %v/%v, want %v", k, bit, ok, modKeys[i].bit)
		}
	}
}

func TestTrackerTracksOnlyModifiers(t *testing.T) {
	var tr Tracker

	if changed := tr.Track(FromRune('a'), true); changed {
		t.Error("Track('a', down) reported a change; only modifiers should be tracked")
	}
	if changed := tr.Track(F1, true); changed {
		t.Error("Track(F1, down) reported a change")
	}
	if changed := tr.Track(NoSymbol, true); changed {
		t.Error("Track(NoSymbol, down) reported a change")
	}
	if tr.Len() != 0 {
		t.Errorf("Len() = %d, want 0", tr.Len())
	}

	if changed := tr.Track(ControlL, true); !changed {
		t.Error("Track(ControlL, down) did not report a change")
	}
	if !tr.IsHeld(ControlL) {
		t.Error("IsHeld(ControlL) = false after press")
	}
	if tr.IsHeld(ControlR) {
		t.Error("IsHeld(ControlR) = true; left and right must stay distinct")
	}
}

func TestTrackerAutoRepeatDoesNotDuplicate(t *testing.T) {
	var tr Tracker
	tr.Track(ControlL, true)
	if changed := tr.Track(ControlL, true); changed {
		t.Error("repeated press reported a change; auto-repeat must not push a duplicate")
	}
	if tr.Len() != 1 {
		t.Fatalf("Len() = %d after auto-repeat, want 1", tr.Len())
	}
	if got := tr.ReleaseAll(); len(got) != 1 {
		t.Errorf("ReleaseAll() = %v, want exactly one release", got)
	}
}

func TestTrackerReleaseOfUnheldKey(t *testing.T) {
	var tr Tracker
	if changed := tr.Track(AltL, false); changed {
		t.Error("releasing an unheld modifier reported a change")
	}
	if tr.Len() != 0 {
		t.Errorf("Len() = %d, want 0", tr.Len())
	}
}

// TestTrackerStuckModifierOnFocusLoss is the scenario this type exists for:
// the user presses Alt-Tab, the host window manager steals focus, and the
// client never sees the Alt or Tab release. Without ReleaseAll the guest would
// believe Alt is still down forever.
func TestTrackerReleaseAllAfterStuckModifiers(t *testing.T) {
	var tr Tracker

	// User holds Ctrl, then Alt, then strikes Tab. Tab is not a modifier and
	// is therefore not tracked.
	tr.Track(ControlL, true)
	tr.Track(AltL, true)
	tr.Track(Tab, true)
	tr.Track(Tab, false)

	// Focus is lost here; the key-ups for Ctrl and Alt never arrive.
	if got, want := tr.Len(), 2; got != want {
		t.Fatalf("Len() = %d, want %d", got, want)
	}
	if got, want := tr.Modifiers(), ModControl|ModAlt; got != want {
		t.Errorf("Modifiers() = %v, want %v", got, want)
	}

	// Releases must unwind the press order: Alt first, then Ctrl.
	got := tr.ReleaseAll()
	want := []KeyAction{
		{AltL, false},
		{ControlL, false},
	}
	assertActions(t, got, want)

	// And the tracker must now be empty, so a second call is a no-op.
	if tr.Len() != 0 {
		t.Errorf("Len() = %d after ReleaseAll, want 0", tr.Len())
	}
	if tr.Modifiers() != ModNone {
		t.Errorf("Modifiers() = %v after ReleaseAll, want none", tr.Modifiers())
	}
	if second := tr.ReleaseAll(); second != nil {
		t.Errorf("second ReleaseAll() = %v, want nil", second)
	}
}

func TestTrackerReleaseAllOrderIsLIFO(t *testing.T) {
	var tr Tracker
	press := []Keysym{ShiftL, ControlL, AltL, SuperL, ISOLevel3Shift}
	for _, sym := range press {
		tr.Track(sym, true)
	}

	got := tr.ReleaseAll()
	if len(got) != len(press) {
		t.Fatalf("ReleaseAll() = %v, want %d actions", got, len(press))
	}
	for i, a := range got {
		want := press[len(press)-1-i]
		if a.Sym != want {
			t.Errorf("ReleaseAll()[%d] = %v, want %v", i, a.Sym, want)
		}
		if a.Down {
			t.Errorf("ReleaseAll()[%d] is a press, want a release", i)
		}
	}
}

func TestTrackerPartialRelease(t *testing.T) {
	var tr Tracker
	tr.Track(ControlL, true)
	tr.Track(ShiftL, true)
	tr.Track(AltL, true)

	// The middle modifier is released normally.
	if changed := tr.Track(ShiftL, false); !changed {
		t.Error("releasing a held modifier did not report a change")
	}
	if tr.IsHeld(ShiftL) {
		t.Error("IsHeld(ShiftL) = true after release")
	}

	// The remaining two must still unwind in press order.
	assertActions(t, tr.ReleaseAll(), []KeyAction{
		{AltL, false},
		{ControlL, false},
	})
}

func TestTrackerTrackKeyAndAction(t *testing.T) {
	var tr Tracker

	tr.TrackKey(KeyControlL, true)
	tr.TrackKey(KeyF1, true) // not a modifier, ignored
	if tr.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", tr.Len())
	}
	tr.TrackKey(KeyControlL, false)
	if tr.Len() != 0 {
		t.Fatalf("Len() = %d after release, want 0", tr.Len())
	}

	// Feeding a chord's own sequence back in leaves the tracker empty, because
	// every press in a chord has a matching release.
	for _, a := range ChordCtrlAltDel.Sequence() {
		tr.TrackAction(a)
	}
	if got := tr.Len(); got != 0 {
		t.Errorf("Len() = %d after a self-balanced chord, want 0", got)
	}
}

// TestTrackerChordOverlappingHeldModifier pins a subtle but correct behaviour:
// the Tracker models what the *guest* believes is held, not what the user is
// physically holding.
//
// If the user is holding Ctrl when the client injects Ctrl-Alt-Del, the
// chord's trailing Ctrl-up really does tell the guest that Ctrl is now up. The
// tracker must agree, otherwise ReleaseAll would later emit a second Ctrl-up
// for a key the guest already considers released.
func TestTrackerChordOverlappingHeldModifier(t *testing.T) {
	var tr Tracker
	tr.Track(ControlL, true) // user physically holds Ctrl

	for _, a := range ChordCtrlAltDel.Sequence() {
		tr.TrackAction(a)
	}

	if tr.IsHeld(ControlL) {
		t.Error("IsHeld(ControlL) = true, but the chord told the guest Ctrl was released")
	}
	if got := tr.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if got := tr.ReleaseAll(); got != nil {
		t.Errorf("ReleaseAll() = %v, want nil: a duplicate release would be wrong", got)
	}
}

func TestTrackerHeldIsACopy(t *testing.T) {
	var tr Tracker
	tr.Track(ControlL, true)
	tr.Track(AltL, true)

	held := tr.Held()
	if len(held) != 2 || held[0] != ControlL || held[1] != AltL {
		t.Fatalf("Held() = %v, want [control_l alt_l]", held)
	}
	held[0] = ShiftL
	if tr.Held()[0] != ControlL {
		t.Error("Held() exposes the tracker's backing array")
	}
}

func TestTrackerReset(t *testing.T) {
	var tr Tracker
	tr.Track(ControlL, true)
	tr.Track(AltL, true)
	tr.Reset()
	if tr.Len() != 0 {
		t.Errorf("Len() = %d after Reset, want 0", tr.Len())
	}
	if got := tr.ReleaseAll(); got != nil {
		t.Errorf("ReleaseAll() after Reset = %v, want nil", got)
	}
}

func TestTrackerZeroValueIsUsable(t *testing.T) {
	var tr Tracker
	if tr.Len() != 0 || tr.Modifiers() != ModNone || tr.ReleaseAll() != nil {
		t.Error("the zero Tracker is not usable")
	}
	if tr.IsHeld(ControlL) {
		t.Error("the zero Tracker reports a held key")
	}
}

func TestTrackerLockKeys(t *testing.T) {
	// Lock keys are tracked too: a stuck Caps Lock is just as disruptive as a
	// stuck Shift.
	var tr Tracker
	tr.Track(CapsLock, true)
	tr.Track(NumLock, true)
	if got, want := tr.Modifiers(), ModCapsLock|ModNumLock; got != want {
		t.Errorf("Modifiers() = %v, want %v", got, want)
	}
	assertActions(t, tr.ReleaseAll(), []KeyAction{
		{NumLock, false},
		{CapsLock, false},
	})
}
