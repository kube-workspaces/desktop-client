// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"

	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// Ellipsis is what [Truncate] appends to text it had to shorten. It is three
// full stops rather than U+2026 because the font is ASCII and the fold would
// produce these three characters anyway; spelling it out keeps the width
// arithmetic honest.
const Ellipsis = "..."

// GlyphWidth, GlyphHeight and the two advances are the font's metrics at scale
// 1, re-exported so that layout code does not have to import internal/viewer
// to work out how big a label will be.
const (
	GlyphWidth   = viewer.GlyphWidth
	GlyphHeight  = viewer.GlyphHeight
	GlyphAdvance = viewer.GlyphAdvance
	LineAdvance  = viewer.LineAdvance
)

// TextWidth returns the width in pixels of s drawn at the given integer scale.
//
// The trailing inter-glyph gap is not part of the measurement: a string is as
// wide as its ink, so centring it does not leave it one gap off-centre.
func TextWidth(s string, scale int) int {
	n := RuneCount(s)
	if n == 0 {
		return 0
	}
	return (n*GlyphAdvance - (GlyphAdvance - GlyphWidth)) * scale
}

// TextHeight returns the height in pixels of one line of text, including the
// descender row.
func TextHeight(scale int) int { return GlyphHeight * scale }

// LineHeight returns the vertical distance between the tops of two
// consecutive lines.
func LineHeight(scale int) int { return LineAdvance * scale }

// RuneCount returns the number of glyph cells s occupies once folded to the
// font's repertoire. It is not len([]rune(s)): an ellipsis folds to three
// cells and a tab to one.
func RuneCount(s string) int {
	n := 0
	for range viewer.FoldToFont(s) {
		n++
	}
	return n
}

// Truncate shortens s so that it fits in maxWidth pixels at the given scale,
// appending [Ellipsis] when it had to cut.
//
// A workspace list is full of names that are almost identical up to their last
// few characters, so this is deliberately a tail truncation with a visible
// marker rather than a silent clip at the edge of a clipping rectangle: the
// user can see that there is more name than is being shown.
func Truncate(s string, scale, maxWidth int) string {
	if scale <= 0 || maxWidth <= 0 {
		return ""
	}
	folded := viewer.FoldToFont(s)
	if TextWidth(folded, scale) <= maxWidth {
		return folded
	}
	runes := []rune(folded)
	ellipsisW := TextWidth(Ellipsis, scale)
	// Not even the marker fits: show as much of the head as there is room
	// for, which is more useful than showing nothing at all.
	if ellipsisW > maxWidth {
		for n := len(runes); n > 0; n-- {
			if TextWidth(string(runes[:n]), scale) <= maxWidth {
				return string(runes[:n])
			}
		}
		return ""
	}
	// The width of a joined string is not the sum of the two widths: there is
	// an inter-glyph gap between them, and forgetting it puts every truncated
	// label one gap over its budget.
	gap := (GlyphAdvance - GlyphWidth) * scale
	for n := len(runes); n > 0; n-- {
		if TextWidth(string(runes[:n]), scale)+gap+ellipsisW <= maxWidth {
			return string(runes[:n]) + Ellipsis
		}
	}
	return Ellipsis
}

// Wrap breaks s into lines that each fit in maxWidth pixels.
//
// It wraps on spaces and breaks a word that is too long to fit on a line of
// its own, because the strings it wraps are error messages from a server and
// one of those is eventually going to be a 200-character URL with no spaces
// in it.
func Wrap(s string, scale, maxWidth int) []string {
	folded := viewer.FoldToFont(s)
	if scale <= 0 || maxWidth <= 0 {
		return nil
	}
	cell := GlyphAdvance * scale
	// Columns that fit, accounting for the missing trailing gap.
	cols := (maxWidth + (GlyphAdvance-GlyphWidth)*scale) / cell
	if cols < 1 {
		cols = 1
	}

	var lines []string
	for _, paragraph := range strings.Split(folded, "\n") {
		lines = append(lines, wrapParagraph(paragraph, cols)...)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// wrapParagraph is greedy word wrapping over a fixed column count, which is
// exact here because every glyph is the same width.
func wrapParagraph(s string, cols int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var (
		lines []string
		line  strings.Builder
	)
	flush := func() {
		lines = append(lines, line.String())
		line.Reset()
	}
	for _, word := range words {
		for len([]rune(word)) > cols {
			// A single word longer than the line: emit whole lines of it
			// until the remainder fits.
			if line.Len() > 0 {
				flush()
			}
			runes := []rune(word)
			lines = append(lines, string(runes[:cols]))
			word = string(runes[cols:])
		}
		switch {
		case line.Len() == 0:
			line.WriteString(word)
		case len([]rune(line.String()))+1+len([]rune(word)) <= cols:
			line.WriteByte(' ')
			line.WriteString(word)
		default:
			flush()
			line.WriteString(word)
		}
	}
	if line.Len() > 0 {
		flush()
	}
	return lines
}
