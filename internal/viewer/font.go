// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

// A 5x7 bitmap font, and nothing else, lives in this file.
//
// The viewer has to draw a handful of short status strings over a frozen
// frame and it must do so without a font renderer: the client ships no font
// file, has no text shaping stack, and adding one (FreeType, a TTF parser, an
// atlas generator) for three lines of modal text would be a dependency and a
// cross-compilation problem out of all proportion to the job. A hand-authored
// bitmap table is a few hundred bytes, has no failure modes, and renders
// identically on every platform.
//
// The table covers ASCII 0x20 to 0x5F only — space, punctuation, digits and
// uppercase letters. Lowercase is folded to uppercase by [glyphFor] rather
// than doubling the table: overlay text is short, drawn large, and reads fine
// in caps, and 64 glyphs is a size a human can proofread. Anything else
// renders as a hollow box, the same convention a font uses for a missing
// glyph, so unknown input is visibly wrong rather than invisible or fatal.

const (
	// glyphWidth and glyphHeight are the cell size of one glyph in pixels,
	// before the overlay's integer scale factor is applied.
	glyphWidth  = 5
	glyphHeight = 7

	// glyphAdvance is the horizontal step from one glyph to the next: the
	// cell plus a one-pixel gap.
	glyphAdvance = glyphWidth + 1

	// lineAdvance is the vertical step from one baseline to the next.
	lineAdvance = glyphHeight + 3

	// glyphFirst and glyphLast bound the range covered by the table.
	glyphFirst = ' '
	glyphLast  = '_'
)

// glyphRow is one row of a glyph: the five low bits, most significant bit
// leftmost, so that the literals below read as the picture they draw.
type glyphRow = uint8

// glyph is one character's bitmap, top row first.
type glyph [glyphHeight]glyphRow

// missingGlyph is drawn for any rune the table does not cover.
var missingGlyph = glyph{
	0b11111,
	0b10001,
	0b10001,
	0b10001,
	0b10001,
	0b10001,
	0b11111,
}

// glyphFor returns the bitmap for r.
//
// Lowercase letters are folded to their uppercase glyph; every other rune
// outside the table's range returns [missingGlyph]. It never panics, because
// the text it renders includes server-supplied error strings and a status
// overlay that crashes the client is worse than no overlay at all.
func glyphFor(r rune) glyph {
	if r >= 'a' && r <= 'z' {
		r -= 'a' - 'A'
	}
	if r < glyphFirst || r > glyphLast {
		return missingGlyph
	}
	return glyphs[r-glyphFirst]
}

// glyphs is the table, indexed by rune minus [glyphFirst].
var glyphs = [glyphLast - glyphFirst + 1]glyph{
	{ // space
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
	},
	{ // !
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00000,
		0b00100,
	},
	{ // "
		0b01010,
		0b01010,
		0b01010,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
	},
	{ // #
		0b01010,
		0b01010,
		0b11111,
		0b01010,
		0b11111,
		0b01010,
		0b01010,
	},
	{ // $
		0b00100,
		0b01111,
		0b10100,
		0b01110,
		0b00101,
		0b11110,
		0b00100,
	},
	{ // %
		0b11000,
		0b11001,
		0b00010,
		0b00100,
		0b01000,
		0b10011,
		0b00011,
	},
	{ // &
		0b01100,
		0b10010,
		0b10100,
		0b01000,
		0b10101,
		0b10010,
		0b01101,
	},
	{ // '
		0b00100,
		0b00100,
		0b01000,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
	},
	{ // (
		0b00010,
		0b00100,
		0b01000,
		0b01000,
		0b01000,
		0b00100,
		0b00010,
	},
	{ // )
		0b01000,
		0b00100,
		0b00010,
		0b00010,
		0b00010,
		0b00100,
		0b01000,
	},
	{ // *
		0b00000,
		0b00100,
		0b10101,
		0b01110,
		0b10101,
		0b00100,
		0b00000,
	},
	{ // +
		0b00000,
		0b00100,
		0b00100,
		0b11111,
		0b00100,
		0b00100,
		0b00000,
	},
	{ // ,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b00110,
		0b00100,
		0b01000,
	},
	{ // -
		0b00000,
		0b00000,
		0b00000,
		0b11111,
		0b00000,
		0b00000,
		0b00000,
	},
	{ // .
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b01100,
		0b01100,
	},
	{ // /
		0b00001,
		0b00010,
		0b00010,
		0b00100,
		0b01000,
		0b01000,
		0b10000,
	},
	{ // 0
		0b01110,
		0b10001,
		0b10011,
		0b10101,
		0b11001,
		0b10001,
		0b01110,
	},
	{ // 1
		0b00100,
		0b01100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b01110,
	},
	{ // 2
		0b01110,
		0b10001,
		0b00001,
		0b00010,
		0b00100,
		0b01000,
		0b11111,
	},
	{ // 3
		0b11111,
		0b00010,
		0b00100,
		0b00010,
		0b00001,
		0b10001,
		0b01110,
	},
	{ // 4
		0b00010,
		0b00110,
		0b01010,
		0b10010,
		0b11111,
		0b00010,
		0b00010,
	},
	{ // 5
		0b11111,
		0b10000,
		0b11110,
		0b00001,
		0b00001,
		0b10001,
		0b01110,
	},
	{ // 6
		0b00110,
		0b01000,
		0b10000,
		0b11110,
		0b10001,
		0b10001,
		0b01110,
	},
	{ // 7
		0b11111,
		0b00001,
		0b00010,
		0b00100,
		0b01000,
		0b01000,
		0b01000,
	},
	{ // 8
		0b01110,
		0b10001,
		0b10001,
		0b01110,
		0b10001,
		0b10001,
		0b01110,
	},
	{ // 9
		0b01110,
		0b10001,
		0b10001,
		0b01111,
		0b00001,
		0b00010,
		0b01100,
	},
	{ // :
		0b00000,
		0b01100,
		0b01100,
		0b00000,
		0b01100,
		0b01100,
		0b00000,
	},
	{ // ;
		0b00000,
		0b01100,
		0b01100,
		0b00000,
		0b01100,
		0b00100,
		0b01000,
	},
	{ // <
		0b00010,
		0b00100,
		0b01000,
		0b10000,
		0b01000,
		0b00100,
		0b00010,
	},
	{ // =
		0b00000,
		0b00000,
		0b11111,
		0b00000,
		0b11111,
		0b00000,
		0b00000,
	},
	{ // >
		0b01000,
		0b00100,
		0b00010,
		0b00001,
		0b00010,
		0b00100,
		0b01000,
	},
	{ // ?
		0b01110,
		0b10001,
		0b00001,
		0b00010,
		0b00100,
		0b00000,
		0b00100,
	},
	{ // @
		0b01110,
		0b10001,
		0b00001,
		0b01101,
		0b10101,
		0b10101,
		0b01110,
	},
	{ // A
		0b01110,
		0b10001,
		0b10001,
		0b11111,
		0b10001,
		0b10001,
		0b10001,
	},
	{ // B
		0b11110,
		0b10001,
		0b10001,
		0b11110,
		0b10001,
		0b10001,
		0b11110,
	},
	{ // C
		0b01110,
		0b10001,
		0b10000,
		0b10000,
		0b10000,
		0b10001,
		0b01110,
	},
	{ // D
		0b11100,
		0b10010,
		0b10001,
		0b10001,
		0b10001,
		0b10010,
		0b11100,
	},
	{ // E
		0b11111,
		0b10000,
		0b10000,
		0b11110,
		0b10000,
		0b10000,
		0b11111,
	},
	{ // F
		0b11111,
		0b10000,
		0b10000,
		0b11110,
		0b10000,
		0b10000,
		0b10000,
	},
	{ // G
		0b01110,
		0b10001,
		0b10000,
		0b10111,
		0b10001,
		0b10001,
		0b01111,
	},
	{ // H
		0b10001,
		0b10001,
		0b10001,
		0b11111,
		0b10001,
		0b10001,
		0b10001,
	},
	{ // I
		0b01110,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b01110,
	},
	{ // J
		0b00111,
		0b00010,
		0b00010,
		0b00010,
		0b00010,
		0b10010,
		0b01100,
	},
	{ // K
		0b10001,
		0b10010,
		0b10100,
		0b11000,
		0b10100,
		0b10010,
		0b10001,
	},
	{ // L
		0b10000,
		0b10000,
		0b10000,
		0b10000,
		0b10000,
		0b10000,
		0b11111,
	},
	{ // M
		0b10001,
		0b11011,
		0b10101,
		0b10101,
		0b10001,
		0b10001,
		0b10001,
	},
	{ // N
		0b10001,
		0b10001,
		0b11001,
		0b10101,
		0b10011,
		0b10001,
		0b10001,
	},
	{ // O
		0b01110,
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b01110,
	},
	{ // P
		0b11110,
		0b10001,
		0b10001,
		0b11110,
		0b10000,
		0b10000,
		0b10000,
	},
	{ // Q
		0b01110,
		0b10001,
		0b10001,
		0b10001,
		0b10101,
		0b10010,
		0b01101,
	},
	{ // R
		0b11110,
		0b10001,
		0b10001,
		0b11110,
		0b10100,
		0b10010,
		0b10001,
	},
	{ // S
		0b01111,
		0b10000,
		0b10000,
		0b01110,
		0b00001,
		0b00001,
		0b11110,
	},
	{ // T
		0b11111,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
	},
	{ // U
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b01110,
	},
	{ // V
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b01010,
		0b00100,
	},
	{ // W
		0b10001,
		0b10001,
		0b10001,
		0b10101,
		0b10101,
		0b11011,
		0b10001,
	},
	{ // X
		0b10001,
		0b10001,
		0b01010,
		0b00100,
		0b01010,
		0b10001,
		0b10001,
	},
	{ // Y
		0b10001,
		0b10001,
		0b01010,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
	},
	{ // Z
		0b11111,
		0b00001,
		0b00010,
		0b00100,
		0b01000,
		0b10000,
		0b11111,
	},
	{ // [
		0b01110,
		0b01000,
		0b01000,
		0b01000,
		0b01000,
		0b01000,
		0b01110,
	},
	{ // \
		0b10000,
		0b01000,
		0b01000,
		0b00100,
		0b00010,
		0b00010,
		0b00001,
	},
	{ // ]
		0b01110,
		0b00010,
		0b00010,
		0b00010,
		0b00010,
		0b00010,
		0b01110,
	},
	{ // ^
		0b00100,
		0b01010,
		0b10001,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
	},
	{ // _
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b11111,
	},
}

// asciiFolds maps the few non-ASCII characters the client's own status strings
// use onto something the table can draw. It is applied before layout, so an
// ellipsis becomes three dots rather than a missing-glyph box.
//
// Runes that are not listed here and not in the table still render — as the
// missing-glyph box — so this is a legibility table, not a validation one.
var asciiFolds = map[rune]string{
	'…': "...",
	'—': "-",
	'–': "-",
	'‑': "-",
	'·': "-",
	'’': "'",
	'‘': "'",
	'“': `"`,
	'”': `"`,
	'×': "x",
}

// foldToFont rewrites s into runes the font can draw, collapsing anything that
// is not printable (tabs, newlines, control characters from a server's error
// text) into single spaces.
func foldToFont(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if sub, ok := asciiFolds[r]; ok {
			out = append(out, []rune(sub)...)
			continue
		}
		if r < ' ' || r == 0x7f {
			r = ' '
		}
		out = append(out, r)
	}
	return string(out)
}
