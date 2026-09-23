// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

// A 5x8 bitmap font, and nothing else, lives in this file.
//
// The client has to draw text — the session's status overlay, and every pixel
// of the graphical shell — and it must do so without a font renderer: it ships
// no font file, has no text shaping stack, and adding one (FreeType, a TTF
// parser, an atlas generator) would be a dependency and a cross-compilation
// problem out of all proportion to the job. A hand-authored bitmap table is a
// couple of kilobytes, has no failure modes, and renders identically on every
// platform.
//
// The table covers the whole printable ASCII range, 0x20 to 0x7E. It used to
// stop at 0x5F and fold lowercase to uppercase, which was defensible for three
// lines of modal status text and is not defensible for a workspace list:
// "cf-debian-gnome-vm-0" rendered as "CF-DEBIAN-GNOME-VM-0" is shouting, and a
// user comparing it against a name they typed has to do the fold in their
// head. Anything outside the range renders as a hollow box, the same
// convention a font uses for a missing glyph, so unknown input is visibly
// wrong rather than invisible or fatal.
//
// The cell is eight rows rather than seven so that lowercase can have real
// descenders. Rows 0 to 6 are the body, with the baseline on row 6, and row 7
// is the descender row used by g, j, p, q, y and the comma. Ascenders and
// capitals occupy rows 0 to 6; the x-height runs from row 2 to row 6.

const (
	// glyphWidth and glyphHeight are the cell size of one glyph in pixels,
	// before the caller's integer scale factor is applied.
	glyphWidth  = 5
	glyphHeight = 8

	// glyphAdvance is the horizontal step from one glyph to the next: the
	// cell plus a one-pixel gap.
	glyphAdvance = glyphWidth + 1

	// lineAdvance is the vertical step from one baseline to the next.
	lineAdvance = glyphHeight + 3

	// glyphFirst and glyphLast bound the range covered by the table.
	glyphFirst = ' '
	glyphLast  = '~'
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
	0b00000,
}

// glyphFor returns the bitmap for r.
//
// Every rune outside the table's range returns [missingGlyph]. It never
// panics, because the text it renders includes server-supplied error strings
// and workspace names chosen by somebody else, and a status overlay that
// crashes the client is worse than no overlay at all.
func glyphFor(r rune) glyph {
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
		0b00000,
	},
	{ // "
		0b01010,
		0b01010,
		0b01010,
		0b00000,
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
		0b00000,
	},
	{ // $
		0b00100,
		0b01111,
		0b10100,
		0b01110,
		0b00101,
		0b11110,
		0b00100,
		0b00000,
	},
	{ // %
		0b11000,
		0b11001,
		0b00010,
		0b00100,
		0b01000,
		0b10011,
		0b00011,
		0b00000,
	},
	{ // &
		0b01100,
		0b10010,
		0b10100,
		0b01000,
		0b10101,
		0b10010,
		0b01101,
		0b00000,
	},
	{ // '
		0b00100,
		0b00100,
		0b01000,
		0b00000,
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
		0b00000,
	},
	{ // )
		0b01000,
		0b00100,
		0b00010,
		0b00010,
		0b00010,
		0b00100,
		0b01000,
		0b00000,
	},
	{ // *
		0b00000,
		0b00100,
		0b10101,
		0b01110,
		0b10101,
		0b00100,
		0b00000,
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
		0b00000,
	},
	{ // ,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b01100,
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
		0b00000,
	},
	{ // /
		0b00001,
		0b00010,
		0b00010,
		0b00100,
		0b01000,
		0b01000,
		0b10000,
		0b00000,
	},
	{ // 0
		0b01110,
		0b10001,
		0b10011,
		0b10101,
		0b11001,
		0b10001,
		0b01110,
		0b00000,
	},
	{ // 1
		0b00100,
		0b01100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b01110,
		0b00000,
	},
	{ // 2
		0b01110,
		0b10001,
		0b00001,
		0b00010,
		0b00100,
		0b01000,
		0b11111,
		0b00000,
	},
	{ // 3
		0b11111,
		0b00010,
		0b00100,
		0b00010,
		0b00001,
		0b10001,
		0b01110,
		0b00000,
	},
	{ // 4
		0b00010,
		0b00110,
		0b01010,
		0b10010,
		0b11111,
		0b00010,
		0b00010,
		0b00000,
	},
	{ // 5
		0b11111,
		0b10000,
		0b11110,
		0b00001,
		0b00001,
		0b10001,
		0b01110,
		0b00000,
	},
	{ // 6
		0b00110,
		0b01000,
		0b10000,
		0b11110,
		0b10001,
		0b10001,
		0b01110,
		0b00000,
	},
	{ // 7
		0b11111,
		0b00001,
		0b00010,
		0b00100,
		0b01000,
		0b01000,
		0b01000,
		0b00000,
	},
	{ // 8
		0b01110,
		0b10001,
		0b10001,
		0b01110,
		0b10001,
		0b10001,
		0b01110,
		0b00000,
	},
	{ // 9
		0b01110,
		0b10001,
		0b10001,
		0b01111,
		0b00001,
		0b00010,
		0b01100,
		0b00000,
	},
	{ // :
		0b00000,
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
		0b00000,
	},
	{ // =
		0b00000,
		0b00000,
		0b11111,
		0b00000,
		0b11111,
		0b00000,
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
		0b00000,
	},
	{ // ?
		0b01110,
		0b10001,
		0b00001,
		0b00010,
		0b00100,
		0b00000,
		0b00100,
		0b00000,
	},
	{ // @
		0b01110,
		0b10001,
		0b00001,
		0b01101,
		0b10101,
		0b10101,
		0b01110,
		0b00000,
	},
	{ // A
		0b01110,
		0b10001,
		0b10001,
		0b11111,
		0b10001,
		0b10001,
		0b10001,
		0b00000,
	},
	{ // B
		0b11110,
		0b10001,
		0b10001,
		0b11110,
		0b10001,
		0b10001,
		0b11110,
		0b00000,
	},
	{ // C
		0b01110,
		0b10001,
		0b10000,
		0b10000,
		0b10000,
		0b10001,
		0b01110,
		0b00000,
	},
	{ // D
		0b11100,
		0b10010,
		0b10001,
		0b10001,
		0b10001,
		0b10010,
		0b11100,
		0b00000,
	},
	{ // E
		0b11111,
		0b10000,
		0b10000,
		0b11110,
		0b10000,
		0b10000,
		0b11111,
		0b00000,
	},
	{ // F
		0b11111,
		0b10000,
		0b10000,
		0b11110,
		0b10000,
		0b10000,
		0b10000,
		0b00000,
	},
	{ // G
		0b01110,
		0b10001,
		0b10000,
		0b10111,
		0b10001,
		0b10001,
		0b01111,
		0b00000,
	},
	{ // H
		0b10001,
		0b10001,
		0b10001,
		0b11111,
		0b10001,
		0b10001,
		0b10001,
		0b00000,
	},
	{ // I
		0b01110,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b01110,
		0b00000,
	},
	{ // J
		0b00111,
		0b00010,
		0b00010,
		0b00010,
		0b00010,
		0b10010,
		0b01100,
		0b00000,
	},
	{ // K
		0b10001,
		0b10010,
		0b10100,
		0b11000,
		0b10100,
		0b10010,
		0b10001,
		0b00000,
	},
	{ // L
		0b10000,
		0b10000,
		0b10000,
		0b10000,
		0b10000,
		0b10000,
		0b11111,
		0b00000,
	},
	{ // M
		0b10001,
		0b11011,
		0b10101,
		0b10101,
		0b10001,
		0b10001,
		0b10001,
		0b00000,
	},
	{ // N
		0b10001,
		0b10001,
		0b11001,
		0b10101,
		0b10011,
		0b10001,
		0b10001,
		0b00000,
	},
	{ // O
		0b01110,
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b01110,
		0b00000,
	},
	{ // P
		0b11110,
		0b10001,
		0b10001,
		0b11110,
		0b10000,
		0b10000,
		0b10000,
		0b00000,
	},
	{ // Q
		0b01110,
		0b10001,
		0b10001,
		0b10001,
		0b10101,
		0b10010,
		0b01101,
		0b00000,
	},
	{ // R
		0b11110,
		0b10001,
		0b10001,
		0b11110,
		0b10100,
		0b10010,
		0b10001,
		0b00000,
	},
	{ // S
		0b01111,
		0b10000,
		0b10000,
		0b01110,
		0b00001,
		0b00001,
		0b11110,
		0b00000,
	},
	{ // T
		0b11111,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00000,
	},
	{ // U
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b01110,
		0b00000,
	},
	{ // V
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b01010,
		0b00100,
		0b00000,
	},
	{ // W
		0b10001,
		0b10001,
		0b10001,
		0b10101,
		0b10101,
		0b11011,
		0b10001,
		0b00000,
	},
	{ // X
		0b10001,
		0b10001,
		0b01010,
		0b00100,
		0b01010,
		0b10001,
		0b10001,
		0b00000,
	},
	{ // Y
		0b10001,
		0b10001,
		0b01010,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00000,
	},
	{ // Z
		0b11111,
		0b00001,
		0b00010,
		0b00100,
		0b01000,
		0b10000,
		0b11111,
		0b00000,
	},
	{ // [
		0b01110,
		0b01000,
		0b01000,
		0b01000,
		0b01000,
		0b01000,
		0b01110,
		0b00000,
	},
	{ // backslash
		0b10000,
		0b01000,
		0b01000,
		0b00100,
		0b00010,
		0b00010,
		0b00001,
		0b00000,
	},
	{ // ]
		0b01110,
		0b00010,
		0b00010,
		0b00010,
		0b00010,
		0b00010,
		0b01110,
		0b00000,
	},
	{ // ^
		0b00100,
		0b01010,
		0b10001,
		0b00000,
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
		0b00000,
		0b11111,
	},
	{ // `
		0b01000,
		0b00100,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
		0b00000,
	},
	{ // a
		0b00000,
		0b00000,
		0b01110,
		0b00001,
		0b01111,
		0b10001,
		0b01111,
		0b00000,
	},
	{ // b
		0b10000,
		0b10000,
		0b11110,
		0b10001,
		0b10001,
		0b10001,
		0b11110,
		0b00000,
	},
	{ // c
		0b00000,
		0b00000,
		0b01111,
		0b10000,
		0b10000,
		0b10000,
		0b01111,
		0b00000,
	},
	{ // d
		0b00001,
		0b00001,
		0b01111,
		0b10001,
		0b10001,
		0b10001,
		0b01111,
		0b00000,
	},
	{ // e
		0b00000,
		0b00000,
		0b01110,
		0b10001,
		0b11111,
		0b10000,
		0b01110,
		0b00000,
	},
	{ // f
		0b00110,
		0b01001,
		0b01000,
		0b11110,
		0b01000,
		0b01000,
		0b01000,
		0b00000,
	},
	{ // g
		0b00000,
		0b00000,
		0b01111,
		0b10001,
		0b10001,
		0b01111,
		0b00001,
		0b01110,
	},
	{ // h
		0b10000,
		0b10000,
		0b11110,
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b00000,
	},
	{ // i
		0b00100,
		0b00000,
		0b01100,
		0b00100,
		0b00100,
		0b00100,
		0b01110,
		0b00000,
	},
	{ // j
		0b00010,
		0b00000,
		0b00110,
		0b00010,
		0b00010,
		0b00010,
		0b10010,
		0b01100,
	},
	{ // k
		0b10000,
		0b10000,
		0b10010,
		0b10100,
		0b11000,
		0b10100,
		0b10010,
		0b00000,
	},
	{ // l
		0b01100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b01110,
		0b00000,
	},
	{ // m
		0b00000,
		0b00000,
		0b11010,
		0b10101,
		0b10101,
		0b10101,
		0b10101,
		0b00000,
	},
	{ // n
		0b00000,
		0b00000,
		0b11110,
		0b10001,
		0b10001,
		0b10001,
		0b10001,
		0b00000,
	},
	{ // o
		0b00000,
		0b00000,
		0b01110,
		0b10001,
		0b10001,
		0b10001,
		0b01110,
		0b00000,
	},
	{ // p
		0b00000,
		0b00000,
		0b11110,
		0b10001,
		0b10001,
		0b11110,
		0b10000,
		0b10000,
	},
	{ // q
		0b00000,
		0b00000,
		0b01111,
		0b10001,
		0b10001,
		0b01111,
		0b00001,
		0b00001,
	},
	{ // r
		0b00000,
		0b00000,
		0b10110,
		0b11000,
		0b10000,
		0b10000,
		0b10000,
		0b00000,
	},
	{ // s
		0b00000,
		0b00000,
		0b01111,
		0b10000,
		0b01110,
		0b00001,
		0b11110,
		0b00000,
	},
	{ // t
		0b01000,
		0b01000,
		0b11110,
		0b01000,
		0b01000,
		0b01001,
		0b00110,
		0b00000,
	},
	{ // u
		0b00000,
		0b00000,
		0b10001,
		0b10001,
		0b10001,
		0b10011,
		0b01101,
		0b00000,
	},
	{ // v
		0b00000,
		0b00000,
		0b10001,
		0b10001,
		0b10001,
		0b01010,
		0b00100,
		0b00000,
	},
	{ // w
		0b00000,
		0b00000,
		0b10001,
		0b10001,
		0b10101,
		0b10101,
		0b01010,
		0b00000,
	},
	{ // x
		0b00000,
		0b00000,
		0b10001,
		0b01010,
		0b00100,
		0b01010,
		0b10001,
		0b00000,
	},
	{ // y
		0b00000,
		0b00000,
		0b10001,
		0b10001,
		0b10001,
		0b01111,
		0b00001,
		0b01110,
	},
	{ // z
		0b00000,
		0b00000,
		0b11111,
		0b00010,
		0b00100,
		0b01000,
		0b11111,
		0b00000,
	},
	{ // {
		0b00011,
		0b00100,
		0b00100,
		0b01000,
		0b00100,
		0b00100,
		0b00011,
		0b00000,
	},
	{ // |
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00100,
		0b00000,
	},
	{ // }
		0b11000,
		0b00100,
		0b00100,
		0b00010,
		0b00100,
		0b00100,
		0b11000,
		0b00000,
	},
	{ // ~
		0b00000,
		0b00000,
		0b01001,
		0b10101,
		0b10010,
		0b00000,
		0b00000,
		0b00000,
	},
}

// asciiFolds maps the few non-ASCII characters the client's own strings use
// onto something the table can draw. It is applied before layout, so an
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

// foldToFont rewrites s into runes the bitmap font can draw, collapsing
// anything that is not printable (tabs, newlines, control characters from a
// server's error text) into single spaces.
func foldToFont(s string) string {
	return foldToFace(s, asciiCovers)
}

// foldToFace rewrites s into runes covers can draw. A rune the face lacks but
// the legibility table knows becomes its ASCII equivalent; anything else
// unprintable becomes a space, and anything merely uncovered is left alone to
// render as the missing-glyph box. A nil covers means the ASCII repertoire,
// which is what callers with no face get.
func foldToFace(s string, covers func(rune) bool) string {
	if covers == nil {
		covers = asciiCovers
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if covers(r) {
			out = append(out, r)
			continue
		}
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

// The exported view of the font.
//
// internal/ui rasterises the shell's widgets with this same table. The client
// draws text in exactly two places — the session's status overlay and the
// shell — and shipping two hand-authored bitmap fonts in one binary would let
// them drift apart glyph by glyph. Exporting the table rather than copying it
// keeps one source of truth, and costs this package nothing: the accessors
// below add no state and no behaviour.

// Glyph is one character's bitmap, top row first. Each row holds [GlyphWidth]
// bits with the most significant bit leftmost, so bit (GlyphWidth-1-col) of
// row y is the pixel at (col, y).
type Glyph = glyph

// Font metrics, in unscaled glyph pixels.
const (
	// GlyphWidth and GlyphHeight are the size of one glyph cell.
	GlyphWidth  = glyphWidth
	GlyphHeight = glyphHeight
	// GlyphAdvance is the horizontal step from one glyph to the next.
	GlyphAdvance = glyphAdvance
	// LineAdvance is the vertical step from one line of text to the next.
	LineAdvance = lineAdvance
	// Baseline is the row the body of a glyph sits on; rows below it are the
	// descender.
	Baseline = 6
)

// GlyphFor returns the bitmap for r, or the missing-glyph box for a rune the
// font does not cover. It never panics.
func GlyphFor(r rune) Glyph { return glyphFor(r) }

// FoldToFont rewrites s into runes the bitmap font can draw: typographic
// punctuation is replaced by its ASCII equivalent and control characters
// become spaces. Runes it cannot fold are left alone and render as the
// missing-glyph box.
func FoldToFont(s string) string { return foldToFont(s) }

// FoldToFace rewrites s into runes covers can draw, with the same legibility
// table and control handling as [FoldToFont]. Faces with wider repertoires
// keep what they can draw: only genuinely uncovered runes fall back to ASCII
// or, failing that, to the missing-glyph box.
func FoldToFace(s string, covers func(rune) bool) string { return foldToFace(s, covers) }
