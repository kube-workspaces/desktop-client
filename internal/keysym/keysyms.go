// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package keysym

import (
	"fmt"
	"strconv"
)

// Keysym is an X11 keysym: the 32-bit key identifier carried by the RFB
// KeyEvent message. Values in this file are taken from the X.Org
// keysymdef.h and XF86keysym.h headers, which are the canonical source.
type Keysym uint32

// NoSymbol is XK_VoidSymbol's companion: the keysym meaning "no key". It is
// returned whenever a translation fails, and must never be sent to a guest.
const NoSymbol Keysym = 0

// VoidSymbol is XK_VoidSymbol, the keysym explicitly bound to "this key does
// nothing". It is distinct from [NoSymbol], which means "no translation".
const VoidSymbol Keysym = 0xffffff

// Latin-1 keysyms.
//
// Keysyms 0x20-0xff are numerically equal to the Latin-1 (and therefore
// Unicode) code point of the character they produce, so there is no table to
// look up: see [FromRune]. Only the punctuation that a scancode-to-keysym
// layer needs to name explicitly is declared here; letters, digits and
// accented characters should go through [FromRune].
const (
	Space        Keysym = 0x0020 // XK_space
	Apostrophe   Keysym = 0x0027 // XK_apostrophe
	Plus         Keysym = 0x002b // XK_plus
	Comma        Keysym = 0x002c // XK_comma
	Minus        Keysym = 0x002d // XK_minus
	Period       Keysym = 0x002e // XK_period
	Slash        Keysym = 0x002f // XK_slash
	Semicolon    Keysym = 0x003b // XK_semicolon
	Equal        Keysym = 0x003d // XK_equal
	BracketLeft  Keysym = 0x005b // XK_bracketleft
	Backslash    Keysym = 0x005c // XK_backslash
	BracketRight Keysym = 0x005d // XK_bracketright
	Grave        Keysym = 0x0060 // XK_grave
)

// UnicodeBase is the offset applied to Unicode code points that have no
// legacy keysym of their own.
//
// X11 reserves keysyms 0x01000100-0x0110ffff to represent U+0100 through
// U+10FFFF directly, so any character outside Latin-1 becomes
// UnicodeBase+codepoint rather than needing an entry in a table. This is why
// [FromRune] computes the value instead of looking it up: the mapping is an
// algorithm, and tabulating it would be both enormous and incomplete.
const UnicodeBase Keysym = 0x01000000

// Editing and control keysyms.
//
// Note that Delete is 0xffff and not near its neighbours: X11 placed it at the
// very top of the keysym space rather than with the other TTY function keys.
const (
	BackSpace  Keysym = 0xff08 // XK_BackSpace
	Tab        Keysym = 0xff09 // XK_Tab
	Linefeed   Keysym = 0xff0a // XK_Linefeed
	Clear      Keysym = 0xff0b // XK_Clear
	Return     Keysym = 0xff0d // XK_Return
	Pause      Keysym = 0xff13 // XK_Pause
	ScrollLock Keysym = 0xff14 // XK_Scroll_Lock
	SysReq     Keysym = 0xff15 // XK_Sys_Req
	Escape     Keysym = 0xff1b // XK_Escape
	Delete     Keysym = 0xffff // XK_Delete
)

// Navigation keysyms.
//
// PageUp and PageDown are the modern spellings of XK_Prior and XK_Next; the
// two names share a value, so either is correct on the wire.
const (
	Home     Keysym = 0xff50 // XK_Home
	Left     Keysym = 0xff51 // XK_Left
	Up       Keysym = 0xff52 // XK_Up
	Right    Keysym = 0xff53 // XK_Right
	Down     Keysym = 0xff54 // XK_Down
	PageUp   Keysym = 0xff55 // XK_Page_Up (XK_Prior)
	PageDown Keysym = 0xff56 // XK_Page_Down (XK_Next)
	End      Keysym = 0xff57 // XK_End
	Begin    Keysym = 0xff58 // XK_Begin
)

// Miscellaneous function keysyms.
const (
	Select  Keysym = 0xff60 // XK_Select
	Print   Keysym = 0xff61 // XK_Print
	Execute Keysym = 0xff62 // XK_Execute
	Insert  Keysym = 0xff63 // XK_Insert
	Undo    Keysym = 0xff65 // XK_Undo
	Redo    Keysym = 0xff66 // XK_Redo
	Menu    Keysym = 0xff67 // XK_Menu
	Find    Keysym = 0xff68 // XK_Find
	Cancel  Keysym = 0xff69 // XK_Cancel
	Help    Keysym = 0xff6a // XK_Help
	Break   Keysym = 0xff6b // XK_Break
	NumLock Keysym = 0xff7f // XK_Num_Lock
)

// Keypad keysyms.
//
// A VDI client should prefer these over the equivalent Latin-1 keysyms when
// the physical keypad was used: guests distinguish KP_Enter from Return, and
// applications such as spreadsheets and terminal emulators behave differently
// for each.
//
// KPEqual sits at 0xffbd, out of sequence with the rest of the block; that is
// how keysymdef.h defines it.
const (
	KPSpace     Keysym = 0xff80 // XK_KP_Space
	KPTab       Keysym = 0xff89 // XK_KP_Tab
	KPEnter     Keysym = 0xff8d // XK_KP_Enter
	KPF1        Keysym = 0xff91 // XK_KP_F1
	KPF2        Keysym = 0xff92 // XK_KP_F2
	KPF3        Keysym = 0xff93 // XK_KP_F3
	KPF4        Keysym = 0xff94 // XK_KP_F4
	KPHome      Keysym = 0xff95 // XK_KP_Home
	KPLeft      Keysym = 0xff96 // XK_KP_Left
	KPUp        Keysym = 0xff97 // XK_KP_Up
	KPRight     Keysym = 0xff98 // XK_KP_Right
	KPDown      Keysym = 0xff99 // XK_KP_Down
	KPPageUp    Keysym = 0xff9a // XK_KP_Page_Up (XK_KP_Prior)
	KPPageDown  Keysym = 0xff9b // XK_KP_Page_Down (XK_KP_Next)
	KPEnd       Keysym = 0xff9c // XK_KP_End
	KPBegin     Keysym = 0xff9d // XK_KP_Begin
	KPInsert    Keysym = 0xff9e // XK_KP_Insert
	KPDelete    Keysym = 0xff9f // XK_KP_Delete
	KPMultiply  Keysym = 0xffaa // XK_KP_Multiply
	KPAdd       Keysym = 0xffab // XK_KP_Add
	KPSeparator Keysym = 0xffac // XK_KP_Separator
	KPSubtract  Keysym = 0xffad // XK_KP_Subtract
	KPDecimal   Keysym = 0xffae // XK_KP_Decimal
	KPDivide    Keysym = 0xffaf // XK_KP_Divide
	KPEqual     Keysym = 0xffbd // XK_KP_Equal
	KP0         Keysym = 0xffb0 // XK_KP_0
	KP1         Keysym = 0xffb1 // XK_KP_1
	KP2         Keysym = 0xffb2 // XK_KP_2
	KP3         Keysym = 0xffb3 // XK_KP_3
	KP4         Keysym = 0xffb4 // XK_KP_4
	KP5         Keysym = 0xffb5 // XK_KP_5
	KP6         Keysym = 0xffb6 // XK_KP_6
	KP7         Keysym = 0xffb7 // XK_KP_7
	KP8         Keysym = 0xffb8 // XK_KP_8
	KP9         Keysym = 0xffb9 // XK_KP_9
)

// Function keysyms F1 through F24. The block is contiguous from 0xffbe, so
// F(n) == F1 + n - 1 for n in 1..24.
const (
	F1  Keysym = 0xffbe // XK_F1
	F2  Keysym = 0xffbf // XK_F2
	F3  Keysym = 0xffc0 // XK_F3
	F4  Keysym = 0xffc1 // XK_F4
	F5  Keysym = 0xffc2 // XK_F5
	F6  Keysym = 0xffc3 // XK_F6
	F7  Keysym = 0xffc4 // XK_F7
	F8  Keysym = 0xffc5 // XK_F8
	F9  Keysym = 0xffc6 // XK_F9
	F10 Keysym = 0xffc7 // XK_F10
	F11 Keysym = 0xffc8 // XK_F11
	F12 Keysym = 0xffc9 // XK_F12
	F13 Keysym = 0xffca // XK_F13
	F14 Keysym = 0xffcb // XK_F14
	F15 Keysym = 0xffcc // XK_F15
	F16 Keysym = 0xffcd // XK_F16
	F17 Keysym = 0xffce // XK_F17
	F18 Keysym = 0xffcf // XK_F18
	F19 Keysym = 0xffd0 // XK_F19
	F20 Keysym = 0xffd1 // XK_F20
	F21 Keysym = 0xffd2 // XK_F21
	F22 Keysym = 0xffd3 // XK_F22
	F23 Keysym = 0xffd4 // XK_F23
	F24 Keysym = 0xffd5 // XK_F24
)

// Modifier keysyms.
//
// Left and right variants are distinct keysyms and must stay that way: guests
// bind them separately (AltGr is the right Alt on many layouts, and Windows
// treats Control_R differently in some IMEs). A client that collapses them
// will produce subtly wrong input.
const (
	ShiftL    Keysym = 0xffe1 // XK_Shift_L
	ShiftR    Keysym = 0xffe2 // XK_Shift_R
	ControlL  Keysym = 0xffe3 // XK_Control_L
	ControlR  Keysym = 0xffe4 // XK_Control_R
	CapsLock  Keysym = 0xffe5 // XK_Caps_Lock
	ShiftLock Keysym = 0xffe6 // XK_Shift_Lock
	MetaL     Keysym = 0xffe7 // XK_Meta_L
	MetaR     Keysym = 0xffe8 // XK_Meta_R
	AltL      Keysym = 0xffe9 // XK_Alt_L
	AltR      Keysym = 0xffea // XK_Alt_R
	SuperL    Keysym = 0xffeb // XK_Super_L
	SuperR    Keysym = 0xffec // XK_Super_R
	HyperL    Keysym = 0xffed // XK_Hyper_L
	HyperR    Keysym = 0xffee // XK_Hyper_R

	// ISOLevel3Shift is AltGr, the third-level shift used by most non-US
	// layouts. It lives in the 0xfe.. ISO block, not with the modifiers above.
	ISOLevel3Shift Keysym = 0xfe03 // XK_ISO_Level3_Shift

	// ModeSwitch is XK_Mode_switch, the group shift. Some servers and layouts
	// use it where others use ISOLevel3Shift.
	ModeSwitch Keysym = 0xff7e // XK_Mode_switch
)

// AltGr is the conventional name for [ISOLevel3Shift].
const AltGr = ISOLevel3Shift

// Media, browser and power keysyms, from XF86keysym.h.
//
// These are vendor keysyms in the 0x1008ff.. block rather than core X11
// keysyms. Guests only act on them if a desktop environment is listening, so
// they are best-effort: forwarding them is harmless when nothing is bound.
const (
	AudioLowerVolume Keysym = 0x1008ff11 // XF86XK_AudioLowerVolume
	AudioMute        Keysym = 0x1008ff12 // XF86XK_AudioMute
	AudioRaiseVolume Keysym = 0x1008ff13 // XF86XK_AudioRaiseVolume
	AudioPlay        Keysym = 0x1008ff14 // XF86XK_AudioPlay
	AudioStop        Keysym = 0x1008ff15 // XF86XK_AudioStop
	AudioPrev        Keysym = 0x1008ff16 // XF86XK_AudioPrev
	AudioNext        Keysym = 0x1008ff17 // XF86XK_AudioNext

	BrightnessUp   Keysym = 0x1008ff02 // XF86XK_MonBrightnessUp
	BrightnessDown Keysym = 0x1008ff03 // XF86XK_MonBrightnessDown

	BrowserHome      Keysym = 0x1008ff18 // XF86XK_HomePage
	BrowserSearch    Keysym = 0x1008ff1b // XF86XK_Search
	BrowserBack      Keysym = 0x1008ff26 // XF86XK_Back
	BrowserForward   Keysym = 0x1008ff27 // XF86XK_Forward
	BrowserRefresh   Keysym = 0x1008ff29 // XF86XK_Refresh
	BrowserFavorites Keysym = 0x1008ff30 // XF86XK_Favorites

	Mail       Keysym = 0x1008ff19 // XF86XK_Mail
	Calculator Keysym = 0x1008ff1d // XF86XK_Calculator
	Explorer   Keysym = 0x1008ff5d // XF86XK_Explorer

	PowerOff Keysym = 0x1008ff2a // XF86XK_PowerOff
	WakeUp   Keysym = 0x1008ff2b // XF86XK_WakeUp
	Sleep    Keysym = 0x1008ff2f // XF86XK_Sleep
)

// FunctionKeysym returns the keysym for function key n, for n in 1..24.
// It returns [NoSymbol] for any n outside that range.
//
// The F1..F24 keysyms are contiguous, so this is arithmetic rather than a
// lookup. It exists mainly for Ctrl-Alt-Fn, where the n comes from a loop or
// from parsed configuration.
func FunctionKeysym(n int) Keysym {
	if n < 1 || n > 24 {
		return NoSymbol
	}
	return F1 + Keysym(n-1)
}

// IsModifier reports whether s is a modifier keysym, that is, one whose press
// changes the meaning of other keys rather than producing input itself.
//
// Lock keys (Caps Lock, Num Lock, Shift Lock) count as modifiers here because
// they too must be released to leave the guest in a sane state.
func (s Keysym) IsModifier() bool {
	switch s {
	case ShiftL, ShiftR, ControlL, ControlR, MetaL, MetaR,
		AltL, AltR, SuperL, SuperR, HyperL, HyperR,
		ISOLevel3Shift, ModeSwitch, CapsLock, ShiftLock, NumLock:
		return true
	default:
		return false
	}
}

// IsUnicode reports whether s is a Unicode-mapped keysym, that is, one in the
// 0x01000100-0x0110ffff range produced by the [UnicodeBase] rule.
func (s Keysym) IsUnicode() bool {
	return s >= UnicodeBase+0x100 && s <= UnicodeBase+0x10ffff
}

// String returns a human-readable rendering of the keysym, for logs and test
// failures. Named keys use their stable [Key] name ("control_l", "kp_enter"),
// characters are shown as a quoted rune, and anything unrecognised falls back
// to hexadecimal.
func (s Keysym) String() string {
	if s == NoSymbol {
		return "NoSymbol"
	}
	if k, ok := keyForSym[s]; ok {
		return k.String()
	}
	if r := s.Rune(); r != 0 {
		return strconv.QuoteRune(r)
	}
	return fmt.Sprintf("Keysym(%#x)", uint32(s))
}

// Rune returns the character the keysym produces, or 0 if it does not produce
// one. It is the inverse of [FromRune] for every keysym that has a character.
func (s Keysym) Rune() rune {
	switch {
	case s >= 0x20 && s <= 0x7e, s >= 0xa0 && s <= 0xff:
		// Latin-1 keysyms are their own code point.
		return rune(s)
	case s.IsUnicode():
		return rune(s - UnicodeBase)
	default:
		return 0
	}
}
