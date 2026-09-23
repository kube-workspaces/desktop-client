// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"

	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// Ellipsis is what [Truncate] appends to text it had to shorten. It is three
// full stops for the bitmap faces, whose ASCII repertoire folds U+2026 away
// anyway; faces that cover the real ellipsis get it (see ellipsisFor), which
// keeps the width arithmetic honest in both cases.
const Ellipsis = "..."

// ellipsisFor returns the truncation marker for f: the single character when
// the face can draw it, the three-stop ASCII spelling otherwise.
func ellipsisFor(f viewer.Font) string {
	if f.Covers != nil && f.Covers('…') {
		return "…"
	}
	return Ellipsis
}

// fold rewrites s into runes f can draw, after normalising f. Every
// measurement and drawing path goes through here, so what is measured is what
// is drawn however wide the face's repertoire is.
func fold(s string, f viewer.Font) string {
	return viewer.FoldToFace(s, f.Covers)
}

// The three faces, re-exported so layout code does not have to import
// internal/viewer to measure text.
var (
	RetroFont  = viewer.RetroFont
	BubblyFont = viewer.BubblyFont
	CleanFont  = viewer.CleanFont
)

// GlyphWidth, GlyphHeight and the two advances are the original 5x8 face's
// metrics at scale 1, re-exported for the tests and the few places that deal
// in the bitmap cell specifically. Everything that measures text that could be
// drawn by any style goes through [TextWidth], [TextHeight], [LineHeight],
// [Truncate] and [Wrap], each of which takes the font it measures against.
const (
	GlyphWidth   = viewer.GlyphWidth
	GlyphHeight  = viewer.GlyphHeight
	GlyphAdvance = viewer.GlyphAdvance
	LineAdvance  = viewer.LineAdvance
)

// normalize returns f unless it is empty, in which case it returns the retro
// face — the value a pre-theme canvas draws with.
func normalize(f viewer.Font) viewer.Font {
	if f.Glyph == nil {
		return viewer.RetroFont
	}
	return f
}

// TextWidth returns the width in pixels of s drawn at the given integer scale
// in the given font.
//
// The trailing inter-glyph gap is not part of the measurement for the bitmap
// faces: a string is as wide as its ink, so centring it does not leave it one
// gap off-centre. The clean face steps by each glyph's own advance, so there
// is no uniform gap to subtract and the width is the plain sum.
func TextWidth(s string, scale int, f viewer.Font) int {
	n := RuneCount(s)
	if n == 0 {
		return 0
	}
	f = normalize(f)
	if f.Advance != nil {
		total := 0
		for _, r := range fold(s, f) {
			total += f.Advance(r, scale)
		}
		return total
	}
	return (n*f.GlyphAdvance - (f.GlyphAdvance - f.GlyphW)) * scale
}

// TextHeight returns the height in pixels of one line of text drawn in the
// given font, including its descender row.
func TextHeight(scale int, f viewer.Font) int {
	f = normalize(f)
	if f.TextHeight != nil {
		return f.TextHeight(scale)
	}
	return f.GlyphH * scale
}

// LineHeight returns the vertical distance between the tops of two
// consecutive lines of text drawn in the given font.
func LineHeight(scale int, f viewer.Font) int {
	f = normalize(f)
	if f.LineHeight != nil {
		return f.LineHeight(scale)
	}
	return f.LineAdvance * scale
}

// RuneCount returns the number of glyph cells s occupies once folded to
// ASCII. It is not len([]rune(s)): an ellipsis folds to three cells and a tab
// to one. Faces with wider repertoires draw some of those runes narrower than
// this counts — the count is conservative, which is the safe direction for
// the layout budgets that consume it.
func RuneCount(s string) int {
	n := 0
	for range viewer.FoldToFont(s) {
		n++
	}
	return n
}

// Truncate shortens s so that it fits in maxWidth pixels at the given scale in
// the given font, appending [Ellipsis] when it had to cut.
//
// A workspace list is full of names that are almost identical up to their last
// few characters, so this is deliberately a tail truncation with a visible
// marker rather than a silent clip at the edge of a clipping rectangle: the
// user can see that there is more name than is being shown.
func Truncate(s string, scale int, f viewer.Font, maxWidth int) string {
	if scale <= 0 || maxWidth <= 0 {
		return ""
	}
	f = normalize(f)
	folded := fold(s, f)
	if TextWidth(folded, scale, f) <= maxWidth {
		return folded
	}
	runes := []rune(folded)
	marker := ellipsisFor(f)
	ellipsisW := TextWidth(marker, scale, f)
	// Not even the marker fits: show as much of the head as there is room
	// for, which is more useful than showing nothing at all.
	if ellipsisW > maxWidth {
		for n := len(runes); n > 0; n-- {
			if TextWidth(string(runes[:n]), scale, f) <= maxWidth {
				return string(runes[:n])
			}
		}
		return ""
	}
	// The width of a joined string is not the sum of the two widths: there is
	// an inter-glyph gap between them, and forgetting it puts every truncated
	// label one gap over its budget. A proportional face has no such gap; its
	// advances already carry the spacing.
	gap := 0
	if f.Advance == nil {
		gap = (f.GlyphAdvance - f.GlyphW) * scale
	}
	for n := len(runes); n > 0; n-- {
		if TextWidth(string(runes[:n]), scale, f)+gap+ellipsisW <= maxWidth {
			return string(runes[:n]) + marker
		}
	}
	return marker
}

// Wrap breaks s into lines that each fit in maxWidth pixels when drawn in the
// given font at the given scale.
//
// It wraps on spaces and breaks a word that is too long to fit on a line of
// its own, because the strings it wraps are error messages from a server and
// one of those is eventually going to be a 200-character URL with no spaces
// in it. Fitting is measured with [TextWidth], so it stays exact whether the
// font's glyphs are all one width (the bitmap faces) or not (the clean face).
func Wrap(s string, scale int, f viewer.Font, maxWidth int) []string {
	if scale <= 0 || maxWidth <= 0 {
		return nil
	}
	f = normalize(f)
	folded := fold(s, f)

	var lines []string
	for _, paragraph := range strings.Split(folded, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		// Joining words with a space does not cost just the space's own
		// width: the gap the ends of two runs normally lose is restored
		// between them, so "on the" costs both words plus the space plus two
		// inter-glyph gaps. Tracking the joined line exactly is what keeps
		// the fit invariant honest with a proportional font.
		sep := (f.GlyphAdvance + (f.GlyphAdvance - f.GlyphW)) * scale
		if f.Advance != nil {
			sep = f.Advance(' ', scale)
		}
		var line []string
		lineW := 0
		for _, word := range words {
			wordW := TextWidth(word, scale, f)
			if len(line) > 0 && lineW+wordW+sep > maxWidth {
				lines = append(lines, strings.Join(line, " "))
				line, lineW = nil, 0
			}
			if len(line) == 0 && wordW > maxWidth {
				lines = append(lines, splitWord(word, scale, f, maxWidth)...)
				continue
			}
			if len(line) > 0 {
				lineW += sep
			}
			line = append(line, word)
			lineW += wordW
		}
		if len(line) > 0 {
			lines = append(lines, strings.Join(line, " "))
		}
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// splitWord breaks one long word into the shortest prefix that still fits
// maxWidth, repeated across the width, so no word is ever lost or clipped.
func splitWord(word string, scale int, f viewer.Font, maxWidth int) []string {
	runes := []rune(word)
	var pieces []string
	for len(runes) > 0 {
		n := len(runes)
		for n > 1 && TextWidth(string(runes[:n]), scale, f) > maxWidth {
			n--
		}
		pieces = append(pieces, string(runes[:n]))
		runes = runes[n:]
	}
	return pieces
}
