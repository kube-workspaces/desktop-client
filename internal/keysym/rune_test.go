// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package keysym

import "testing"

func TestFromRune(t *testing.T) {
	tests := []struct {
		name string
		in   rune
		want Keysym
	}{
		// ASCII letters, digits and punctuation are their own keysym.
		{"lower a", 'a', 0x0061},
		{"upper A", 'A', 0x0041},
		{"lower z", 'z', 0x007a},
		{"digit 0", '0', 0x0030},
		{"digit 9", '9', 0x0039},
		{"space", ' ', 0x0020},
		{"space constant", ' ', Space},
		{"exclam", '!', 0x0021},
		{"slash", '/', Slash},
		{"backslash", '\\', Backslash},
		{"grave", '`', Grave},
		{"tilde", '~', 0x007e},
		{"bracketleft", '[', BracketLeft},
		{"equal", '=', Equal},

		// Latin-1 supplement: still a direct mapping.
		{"e acute", 'é', 0x00e9},
		{"a diaeresis", 'ä', 0x00e4},
		{"sharp s", 'ß', 0x00df},
		{"nbsp", '\u00a0', 0x00a0},
		{"y diaeresis, last latin-1", 'ÿ', 0x00ff},

		// Beyond Latin-1: UnicodeBase + code point.
		{"latin extended a", 'Ā', UnicodeBase + 0x0100},
		{"euro sign", '€', UnicodeBase + 0x20ac},
		{"cjk han", '漢', UnicodeBase + 0x6f22},
		{"cjk zhong", '中', UnicodeBase + 0x4e2d},
		{"hiragana a", 'あ', UnicodeBase + 0x3042},
		{"cyrillic zhe", 'ж', UnicodeBase + 0x0436},
		{"emoji slightly smiling", '🙂', UnicodeBase + 0x1f642},
		{"emoji rocket", '🚀', UnicodeBase + 0x1f680},
		{"max rune", 0x10ffff, UnicodeBase + 0x10ffff},

		// Control characters with dedicated keysyms.
		{"backspace", '\b', BackSpace},
		{"tab", '\t', Tab},
		{"carriage return", '\r', Return},
		{"line feed maps to Return, not Linefeed", '\n', Return},
		{"escape", 0x1b, Escape},
		{"del", 0x7f, Delete},

		// Control characters without one.
		{"nul", 0x00, NoSymbol},
		{"ctrl-c", 0x03, NoSymbol},
		{"vertical tab", 0x0b, NoSymbol},
		{"unit separator", 0x1f, NoSymbol},

		// Invalid runes.
		{"negative", -1, NoSymbol},
		{"surrogate low half", 0xd800, NoSymbol},
		{"surrogate high half", 0xdfff, NoSymbol},
		{"above max rune", 0x110000, NoSymbol},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FromRune(tt.in); got != tt.want {
				t.Errorf("FromRune(%q) = %#x, want %#x", tt.in, uint32(got), uint32(tt.want))
			}
		})
	}
}

// TestFromRuneLatin1Rule asserts the whole 0x20-0xff block obeys the identity
// rule, rather than trusting the handful of samples above.
func TestFromRuneLatin1Rule(t *testing.T) {
	for r := rune(0x20); r <= 0xff; r++ {
		if r == 0x7f {
			// DEL is the documented exception: it has a dedicated keysym.
			if got := FromRune(r); got != Delete {
				t.Errorf("FromRune(0x7f) = %#x, want Delete %#x", uint32(got), uint32(Delete))
			}
			continue
		}
		if got := FromRune(r); got != Keysym(r) {
			t.Errorf("FromRune(%#x) = %#x, want %#x", r, uint32(got), uint32(r))
		}
	}
}

// TestFromRuneUnicodeRule asserts the 0x01000000 offset across the whole
// non-Latin-1 range, skipping surrogates.
func TestFromRuneUnicodeRule(t *testing.T) {
	for r := rune(0x100); r <= 0x10ffff; r += 0x137 {
		if isSurrogate(r) {
			continue
		}
		want := UnicodeBase + Keysym(r)
		if got := FromRune(r); got != want {
			t.Errorf("FromRune(%#x) = %#x, want %#x", r, uint32(got), uint32(want))
		}
		if !want.IsUnicode() {
			t.Errorf("Keysym(%#x).IsUnicode() = false, want true", uint32(want))
		}
	}
}

func TestKeysymRuneRoundTrip(t *testing.T) {
	runes := []rune{'a', 'Z', '0', ' ', '~', 'é', 'ÿ', 'Ā', '€', '漢', '🙂', 0x10ffff}
	for _, r := range runes {
		sym := FromRune(r)
		if got := sym.Rune(); got != r {
			t.Errorf("FromRune(%q).Rune() = %q (%#x), want %q", r, got, got, r)
		}
	}
}

func TestKeysymRuneNoCharacter(t *testing.T) {
	for _, sym := range []Keysym{NoSymbol, F1, ControlL, KPEnter, Delete, Escape, AudioMute} {
		if got := sym.Rune(); got != 0 {
			t.Errorf("Keysym(%#x).Rune() = %q, want 0", uint32(sym), got)
		}
	}
}

func TestFromString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []Keysym
	}{
		{"empty", "", nil},
		{"ascii", "Hi!", []Keysym{'H', 'i', '!'}},
		{"mixed scripts", "aé漢", []Keysym{'a', 0xe9, UnicodeBase + 0x6f22}},
		{"newline becomes Return", "a\nb", []Keysym{'a', Return, 'b'}},
		{"tab kept", "a\tb", []Keysym{'a', Tab, 'b'}},
		{"unmapped control dropped", "a\x03b", []Keysym{'a', 'b'}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromString(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("FromString(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("FromString(%q)[%d] = %#x, want %#x", tt.in, i, uint32(got[i]), uint32(tt.want[i]))
				}
			}
		})
	}
}
