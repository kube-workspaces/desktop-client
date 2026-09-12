// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package keysym

import "unicode/utf8"

// FromRune returns the X11 keysym that produces the character r.
//
// The mapping is defined by two rules from keysymdef.h rather than by a table:
//
//  1. Code points U+0020 through U+00FF (Latin-1) are their own keysym. This
//     is why ASCII 'a' is keysym 0x61 and 'é' is keysym 0xe9.
//  2. Everything else is [UnicodeBase] + the code point, placing it in the
//     0x01000100-0x0110ffff range X11 reserves for Unicode. So '漢' (U+6F22)
//     is 0x01006f22 and '🙂' (U+1F642) is 0x0101f642.
//
// Computing rule 2 instead of tabulating it is not an optimisation: the table
// would need an entry for every assigned code point, and would go stale with
// every Unicode release.
//
// Control characters are handled specially, because they have no Latin-1
// keysym and the Unicode rule would produce values no server understands:
//
//   - '\b', '\t', '\r', '\n' and ESC map to the dedicated keysyms
//     [BackSpace], [Tab], [Return] and [Escape]. '\n' maps to [Return], not
//     [Linefeed], because the user pressed Enter and every guest binds Enter
//     to Return; [Linefeed] is a separate physical key that modern keyboards
//     do not have.
//   - DEL (0x7f) maps to [Delete], its dedicated keysym.
//   - Every other C0 control returns [NoSymbol]. There is no keysym for, say,
//     Ctrl-C as a single character: the client must send Control_L down, 'c',
//     Control_L up. See [Chord].
//
// Invalid runes — negative values, surrogates and anything above U+10FFFF —
// return [NoSymbol].
func FromRune(r rune) Keysym {
	switch r {
	case '\b':
		return BackSpace
	case '\t':
		return Tab
	case '\n', '\r':
		return Return
	case 0x1b:
		return Escape
	case 0x7f:
		return Delete
	}

	switch {
	case r < 0x20:
		// Remaining C0 controls have no keysym; see the doc comment.
		return NoSymbol
	case r <= 0xff:
		// Rule 1. Note that 0x80-0x9f are C1 controls with no standard keysym,
		// but no keyboard produces them, and passing the value through is what
		// every other VNC client does.
		return Keysym(r)
	case r > utf8.MaxRune, isSurrogate(r):
		return NoSymbol
	default:
		// Rule 2.
		return UnicodeBase + Keysym(r)
	}
}

// FromString returns the keysym sequence for every character in s, skipping
// characters that have no keysym. It is the convenience used for pasting text
// or replaying a scripted login into a guest.
func FromString(s string) []Keysym {
	syms := make([]Keysym, 0, len(s))
	for _, r := range s {
		if sym := FromRune(r); sym != NoSymbol {
			syms = append(syms, sym)
		}
	}
	return syms
}

// isSurrogate reports whether r is a UTF-16 surrogate half. Surrogates are not
// characters, so they have no keysym; Go yields them only from corrupt input.
func isSurrogate(r rune) bool { return r >= 0xd800 && r <= 0xdfff }
