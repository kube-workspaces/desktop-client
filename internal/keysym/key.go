// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package keysym

import (
	"fmt"
	"sort"
	"strings"
)

// Key identifies a non-text key in a backend-neutral way.
//
// The session viewer receives SDL3 scancodes and keycodes; it maps those onto
// Key values, and this package maps Key values onto keysyms. That indirection
// is the whole point: without it every keysym table would have to import an
// SDL binding, which would make this package cgo-dependent and untestable
// without a display.
//
// Key deliberately covers only keys that do not produce text. Characters —
// letters, digits, punctuation, accented and non-Latin text — are handled by
// [FromRune], because enumerating them would mean duplicating Unicode.
type Key uint16

// The complete set of keys this package knows about.
//
// Values are not stable across releases and must not be persisted; persist
// [Key.String] names instead, which are stable.
const (
	// KeyUnknown is the zero value: a key that could not be identified. Its
	// keysym is [NoSymbol] and must not be sent to a guest.
	KeyUnknown Key = iota

	// Editing and control keys.
	KeyBackSpace
	KeyTab
	KeyLinefeed
	KeyClear
	KeyReturn
	KeyPause
	KeyScrollLock
	KeySysReq
	KeyEscape
	KeyDelete
	KeyInsert
	KeyNumLock
	KeyCapsLock
	KeyPrint
	KeyMenu
	KeyHelp
	KeyBreak
	KeySelect
	KeyExecute
	KeyUndo
	KeyRedo
	KeyFind
	KeyCancel

	// Navigation keys.
	KeyHome
	KeyLeft
	KeyUp
	KeyRight
	KeyDown
	KeyPageUp
	KeyPageDown
	KeyEnd
	KeyBegin

	// Function keys.
	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
	KeyF13
	KeyF14
	KeyF15
	KeyF16
	KeyF17
	KeyF18
	KeyF19
	KeyF20
	KeyF21
	KeyF22
	KeyF23
	KeyF24

	// Modifier keys.
	KeyShiftL
	KeyShiftR
	KeyControlL
	KeyControlR
	KeyMetaL
	KeyMetaR
	KeyAltL
	KeyAltR
	KeySuperL
	KeySuperR
	KeyHyperL
	KeyHyperR
	KeyAltGr
	KeyModeSwitch
	KeyShiftLock

	// Keypad keys.
	KeyKP0
	KeyKP1
	KeyKP2
	KeyKP3
	KeyKP4
	KeyKP5
	KeyKP6
	KeyKP7
	KeyKP8
	KeyKP9
	KeyKPSpace
	KeyKPTab
	KeyKPEnter
	KeyKPF1
	KeyKPF2
	KeyKPF3
	KeyKPF4
	KeyKPHome
	KeyKPLeft
	KeyKPUp
	KeyKPRight
	KeyKPDown
	KeyKPPageUp
	KeyKPPageDown
	KeyKPEnd
	KeyKPBegin
	KeyKPInsert
	KeyKPDelete
	KeyKPEqual
	KeyKPMultiply
	KeyKPAdd
	KeyKPSeparator
	KeyKPSubtract
	KeyKPDecimal
	KeyKPDivide

	// Media, browser and power keys. These map to XF86 vendor keysyms and are
	// only meaningful if something in the guest listens for them.
	KeyAudioLowerVolume
	KeyAudioRaiseVolume
	KeyAudioMute
	KeyAudioPlay
	KeyAudioStop
	KeyAudioPrev
	KeyAudioNext
	KeyBrightnessUp
	KeyBrightnessDown
	KeyBrowserHome
	KeyBrowserSearch
	KeyBrowserBack
	KeyBrowserForward
	KeyBrowserRefresh
	KeyBrowserFavorites
	KeyMail
	KeyCalculator
	KeyExplorer
	KeyPowerOff
	KeyWakeUp
	KeySleep

	// numKeys is the number of defined Key values. It must stay last.
	numKeys
)

// keyEntry is one row of the single source of truth for Key metadata. Keeping
// the name and the keysym together in one table is what stops the two from
// drifting apart as keys are added.
type keyEntry struct {
	name string
	sym  Keysym
}

// keyTable is indexed by Key. Using explicit indices rather than positional
// literals means adding a Key in the middle of the const block cannot silently
// shift every name onto the wrong key.
var keyTable = [numKeys]keyEntry{
	KeyUnknown: {"unknown", NoSymbol},

	KeyBackSpace:  {"backspace", BackSpace},
	KeyTab:        {"tab", Tab},
	KeyLinefeed:   {"linefeed", Linefeed},
	KeyClear:      {"clear", Clear},
	KeyReturn:     {"return", Return},
	KeyPause:      {"pause", Pause},
	KeyScrollLock: {"scroll_lock", ScrollLock},
	KeySysReq:     {"sys_req", SysReq},
	KeyEscape:     {"escape", Escape},
	KeyDelete:     {"delete", Delete},
	KeyInsert:     {"insert", Insert},
	KeyNumLock:    {"num_lock", NumLock},
	KeyCapsLock:   {"caps_lock", CapsLock},
	KeyPrint:      {"print", Print},
	KeyMenu:       {"menu", Menu},
	KeyHelp:       {"help", Help},
	KeyBreak:      {"break", Break},
	KeySelect:     {"select", Select},
	KeyExecute:    {"execute", Execute},
	KeyUndo:       {"undo", Undo},
	KeyRedo:       {"redo", Redo},
	KeyFind:       {"find", Find},
	KeyCancel:     {"cancel", Cancel},

	KeyHome:     {"home", Home},
	KeyLeft:     {"left", Left},
	KeyUp:       {"up", Up},
	KeyRight:    {"right", Right},
	KeyDown:     {"down", Down},
	KeyPageUp:   {"page_up", PageUp},
	KeyPageDown: {"page_down", PageDown},
	KeyEnd:      {"end", End},
	KeyBegin:    {"begin", Begin},

	KeyF1:  {"f1", F1},
	KeyF2:  {"f2", F2},
	KeyF3:  {"f3", F3},
	KeyF4:  {"f4", F4},
	KeyF5:  {"f5", F5},
	KeyF6:  {"f6", F6},
	KeyF7:  {"f7", F7},
	KeyF8:  {"f8", F8},
	KeyF9:  {"f9", F9},
	KeyF10: {"f10", F10},
	KeyF11: {"f11", F11},
	KeyF12: {"f12", F12},
	KeyF13: {"f13", F13},
	KeyF14: {"f14", F14},
	KeyF15: {"f15", F15},
	KeyF16: {"f16", F16},
	KeyF17: {"f17", F17},
	KeyF18: {"f18", F18},
	KeyF19: {"f19", F19},
	KeyF20: {"f20", F20},
	KeyF21: {"f21", F21},
	KeyF22: {"f22", F22},
	KeyF23: {"f23", F23},
	KeyF24: {"f24", F24},

	KeyShiftL:     {"shift_l", ShiftL},
	KeyShiftR:     {"shift_r", ShiftR},
	KeyControlL:   {"control_l", ControlL},
	KeyControlR:   {"control_r", ControlR},
	KeyMetaL:      {"meta_l", MetaL},
	KeyMetaR:      {"meta_r", MetaR},
	KeyAltL:       {"alt_l", AltL},
	KeyAltR:       {"alt_r", AltR},
	KeySuperL:     {"super_l", SuperL},
	KeySuperR:     {"super_r", SuperR},
	KeyHyperL:     {"hyper_l", HyperL},
	KeyHyperR:     {"hyper_r", HyperR},
	KeyAltGr:      {"altgr", ISOLevel3Shift},
	KeyModeSwitch: {"mode_switch", ModeSwitch},
	KeyShiftLock:  {"shift_lock", ShiftLock},

	KeyKP0:         {"kp_0", KP0},
	KeyKP1:         {"kp_1", KP1},
	KeyKP2:         {"kp_2", KP2},
	KeyKP3:         {"kp_3", KP3},
	KeyKP4:         {"kp_4", KP4},
	KeyKP5:         {"kp_5", KP5},
	KeyKP6:         {"kp_6", KP6},
	KeyKP7:         {"kp_7", KP7},
	KeyKP8:         {"kp_8", KP8},
	KeyKP9:         {"kp_9", KP9},
	KeyKPSpace:     {"kp_space", KPSpace},
	KeyKPTab:       {"kp_tab", KPTab},
	KeyKPEnter:     {"kp_enter", KPEnter},
	KeyKPF1:        {"kp_f1", KPF1},
	KeyKPF2:        {"kp_f2", KPF2},
	KeyKPF3:        {"kp_f3", KPF3},
	KeyKPF4:        {"kp_f4", KPF4},
	KeyKPHome:      {"kp_home", KPHome},
	KeyKPLeft:      {"kp_left", KPLeft},
	KeyKPUp:        {"kp_up", KPUp},
	KeyKPRight:     {"kp_right", KPRight},
	KeyKPDown:      {"kp_down", KPDown},
	KeyKPPageUp:    {"kp_page_up", KPPageUp},
	KeyKPPageDown:  {"kp_page_down", KPPageDown},
	KeyKPEnd:       {"kp_end", KPEnd},
	KeyKPBegin:     {"kp_begin", KPBegin},
	KeyKPInsert:    {"kp_insert", KPInsert},
	KeyKPDelete:    {"kp_delete", KPDelete},
	KeyKPEqual:     {"kp_equal", KPEqual},
	KeyKPMultiply:  {"kp_multiply", KPMultiply},
	KeyKPAdd:       {"kp_add", KPAdd},
	KeyKPSeparator: {"kp_separator", KPSeparator},
	KeyKPSubtract:  {"kp_subtract", KPSubtract},
	KeyKPDecimal:   {"kp_decimal", KPDecimal},
	KeyKPDivide:    {"kp_divide", KPDivide},

	KeyAudioLowerVolume: {"audio_lower_volume", AudioLowerVolume},
	KeyAudioRaiseVolume: {"audio_raise_volume", AudioRaiseVolume},
	KeyAudioMute:        {"audio_mute", AudioMute},
	KeyAudioPlay:        {"audio_play", AudioPlay},
	KeyAudioStop:        {"audio_stop", AudioStop},
	KeyAudioPrev:        {"audio_prev", AudioPrev},
	KeyAudioNext:        {"audio_next", AudioNext},
	KeyBrightnessUp:     {"brightness_up", BrightnessUp},
	KeyBrightnessDown:   {"brightness_down", BrightnessDown},
	KeyBrowserHome:      {"browser_home", BrowserHome},
	KeyBrowserSearch:    {"browser_search", BrowserSearch},
	KeyBrowserBack:      {"browser_back", BrowserBack},
	KeyBrowserForward:   {"browser_forward", BrowserForward},
	KeyBrowserRefresh:   {"browser_refresh", BrowserRefresh},
	KeyBrowserFavorites: {"browser_favorites", BrowserFavorites},
	KeyMail:             {"mail", Mail},
	KeyCalculator:       {"calculator", Calculator},
	KeyExplorer:         {"explorer", Explorer},
	KeyPowerOff:         {"power_off", PowerOff},
	KeyWakeUp:           {"wake_up", WakeUp},
	KeySleep:            {"sleep", Sleep},
}

// keyAliases are additional spellings accepted by [ParseKey] but never
// produced by [Key.String]. They exist so that hand-written configuration can
// say "ctrl-alt-del" instead of "control_l-alt_l-delete".
//
// Aliases are intentionally kept out of keyTable: the canonical names there
// must remain a one-to-one mapping, which is what the round-trip tests assert.
//
// Note that the unqualified modifier names resolve to the left-hand key. That
// is the convention every VNC and RDP client uses, and it is what a guest
// expects when a user asks for "ctrl".
var keyAliases = map[string]Key{
	"ctrl":    KeyControlL,
	"control": KeyControlL,
	"lctrl":   KeyControlL,
	"rctrl":   KeyControlR,

	"alt":  KeyAltL,
	"lalt": KeyAltL,
	"ralt": KeyAltR,

	"shift":  KeyShiftL,
	"lshift": KeyShiftL,
	"rshift": KeyShiftR,

	"super":   KeySuperL,
	"win":     KeySuperL,
	"windows": KeySuperL,
	"cmd":     KeySuperL,
	"command": KeySuperL,
	"lsuper":  KeySuperL,
	"rsuper":  KeySuperR,

	"meta":  KeyMetaL,
	"lmeta": KeyMetaL,
	"rmeta": KeyMetaR,

	"hyper": KeyHyperL,

	"iso_level3_shift": KeyAltGr,
	"alt_gr":           KeyAltGr,
	"altright":         KeyAltR,

	"esc":        KeyEscape,
	"del":        KeyDelete,
	"ins":        KeyInsert,
	"bs":         KeyBackSpace,
	"back_space": KeyBackSpace,
	"enter":      KeyReturn,
	"ret":        KeyReturn,
	"pgup":       KeyPageUp,
	"pageup":     KeyPageUp,
	"prior":      KeyPageUp,
	"pgdn":       KeyPageDown,
	"pgdown":     KeyPageDown,
	"pagedown":   KeyPageDown,
	"next":       KeyPageDown,

	"capslock":    KeyCapsLock,
	"numlock":     KeyNumLock,
	"scrolllock":  KeyScrollLock,
	"scroll":      KeyScrollLock,
	"sysrq":       KeySysReq,
	"printscreen": KeyPrint,
	"prtsc":       KeyPrint,

	"kpenter": KeyKPEnter,
	"kpplus":  KeyKPAdd,
	"kpminus": KeyKPSubtract,
}

// keyByName resolves canonical names and aliases. Built once at init rather
// than searched linearly because the viewer may parse bindings per keystroke.
var keyByName = func() map[string]Key {
	m := make(map[string]Key, len(keyTable)+len(keyAliases))
	for i, e := range keyTable {
		if e.name != "" {
			m[e.name] = Key(i)
		}
	}
	for name, k := range keyAliases {
		m[name] = k
	}
	return m
}()

// keyForSym is the reverse mapping used by [Keysym.String]. Lower Key values
// win, so the canonical key for a keysym is the first one declared.
var keyForSym = func() map[Keysym]Key {
	m := make(map[Keysym]Key, len(keyTable))
	for i, e := range keyTable {
		if e.sym == NoSymbol {
			continue
		}
		if _, dup := m[e.sym]; dup {
			continue
		}
		m[e.sym] = Key(i)
	}
	return m
}()

// Keysym returns the X11 keysym for k, or [NoSymbol] if k is [KeyUnknown] or
// out of range. A [NoSymbol] result must not be sent to the guest: RFB has no
// way to express "no key", and servers differ in how they mishandle it.
func (k Key) Keysym() Keysym {
	if k >= numKeys {
		return NoSymbol
	}
	return keyTable[k].sym
}

// String returns the stable lowercase name of k, such as "f1", "left",
// "kp_enter" or "control_l".
//
// These names are part of this package's contract: they appear in
// configuration files and key bindings, so they must not be renamed once
// released. Unknown values render as Key(n) rather than panicking.
func (k Key) String() string {
	if k >= numKeys || keyTable[k].name == "" {
		return fmt.Sprintf("Key(%d)", uint16(k))
	}
	return keyTable[k].name
}

// IsModifier reports whether k is a modifier or lock key.
func (k Key) IsModifier() bool { return k.Keysym().IsModifier() }

// IsKeypad reports whether k belongs to the numeric keypad. Callers need this
// because the keypad keysyms must be preferred whenever the physical keypad
// was used: guests distinguish KP_Enter from Return and KP_1 from End.
func (k Key) IsKeypad() bool { return k >= KeyKP0 && k <= KeyKPDivide }

// ParseKey resolves a key name to a [Key]. It accepts the canonical names
// returned by [Key.String] as well as the common aliases ("ctrl", "esc",
// "del", "pgup", "enter", "win", ...), and is case-insensitive.
//
// The second result reports whether the name was recognised; an unrecognised
// name yields [KeyUnknown].
//
// Only non-text keys have names. To send a character, use [FromRune].
func ParseKey(name string) (Key, bool) {
	k, ok := keyByName[strings.ToLower(strings.TrimSpace(name))]
	return k, ok
}

// KeyNames returns every canonical key name, sorted. It is intended for help
// text, shell completion and validating configuration, and returns a fresh
// slice on each call.
func KeyNames() []string {
	names := make([]string, 0, len(keyTable))
	for _, e := range keyTable {
		if e.name != "" {
			names = append(names, e.name)
		}
	}
	sort.Strings(names)
	return names
}

// AllKeys returns every defined [Key] in declaration order, including
// [KeyUnknown]. It returns a fresh slice on each call.
func AllKeys() []Key {
	keys := make([]Key, 0, numKeys)
	for i := Key(0); i < numKeys; i++ {
		keys = append(keys, i)
	}
	return keys
}

// FunctionKey returns the [Key] for function key n, for n in 1..24, and
// reports whether n was in range.
func FunctionKey(n int) (Key, bool) {
	if n < 1 || n > 24 {
		return KeyUnknown, false
	}
	return KeyF1 + Key(n-1), true
}
