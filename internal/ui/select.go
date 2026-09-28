// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"image/color"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// SelectableStyle configures [SelectableText]. The zero value is body text in
// the theme's text colour, wrapped to the rectangle.
type SelectableStyle struct {
	// Color is the ink. Unset means the theme's body text colour.
	Color color.RGBA
	// Scale is the integer glyph scale. Zero means the theme's body scale.
	Scale int
}

// SelectableText is read-only text the user can select and copy: the sign-in
// URL, an error strip, the installed version.
//
// It is a struct rather than a function because the selection has to survive
// a frame. Selection addresses the text as drawn (folded to the face, wrapped
// to the rectangle), so [SelectableText.SelectedText] carries what the user
// saw — including the ASCII foldings — rather than the model's original.
// Copy and select-all go through the context's clipboard; without one they
// are silent no-ops, exactly like a text field's.
type SelectableText struct {
	// ID identifies the text to the focus ring. Without one it can still be
	// selected with the pointer, but Ctrl-C has nowhere to be delivered.
	ID FocusID
	// Scale overrides the theme's body scale.
	Scale int

	raw    string
	folded []rune
	anchor int
	cursor int
	// dragging tracks a press-drag-release gesture across frames.
	dragging bool
	// lastPress records when and where the previous press landed, so that a
	// second press soon after and nearby counts as a double-click (word
	// select) and a third as a triple-click (select all).
	lastPress   time.Time
	lastPressAt Point
	clicks      int
}

// SelectedText returns the selected runes as drawn.
func (t *SelectableText) SelectedText() string {
	lo, hi := t.range_()
	return string(t.folded[lo:hi])
}

// HasSelection reports whether a non-empty range is selected.
func (t *SelectableText) HasSelection() bool {
	lo, hi := t.range_()
	return hi > lo
}

// SelectAll selects the whole text.
func (t *SelectableText) SelectAll() {
	t.anchor, t.cursor = 0, len(t.folded)
}

// range_ returns the selected rune interval, ordered and clamped, or two
// equal values when there is no selection.
func (t *SelectableText) range_() (int, int) {
	lo, hi := t.anchor, t.cursor
	if lo > hi {
		lo, hi = hi, lo
	}
	return clampInt(lo, 0, len(t.folded)), clampInt(hi, 0, len(t.folded))
}

// move puts the cursor delta runes away and collapses the selection.
func (t *SelectableText) move(delta int) {
	t.cursor = clampInt(t.cursor+delta, 0, len(t.folded))
	t.anchor = t.cursor
}

// extend moves the cursor and keeps the anchor where it is.
func (t *SelectableText) extend(delta int) {
	t.cursor = clampInt(t.cursor+delta, 0, len(t.folded))
}

// selectWord selects the word containing rune index i, with the same
// word characters as a text field so double-click agrees in both.
func (t *SelectableText) selectWord(i int) {
	i = clampInt(i, 0, len(t.folded))
	t.anchor, t.cursor = i, i
	if i < len(t.folded) && isWordChar(t.folded[i]) {
		for t.anchor > 0 && isWordChar(t.folded[t.anchor-1]) {
			t.anchor--
		}
		for t.cursor < len(t.folded) && isWordChar(t.folded[t.cursor]) {
			t.cursor++
		}
		return
	}
	for t.anchor > 0 && !isWordChar(t.folded[t.anchor-1]) {
		t.anchor--
	}
	for t.cursor < len(t.folded) && !isWordChar(t.folded[t.cursor]) {
		t.cursor++
	}
}

// selWord is one word in a laid-out line: its text and its rune offset in the
// folded whole, so a pointer position maps back to a selection index and a
// selection maps forward to highlight rectangles.
type selWord struct {
	start int
	text  string
}

// selLine is one laid-out line: words joined with single spaces for display,
// with the folded offsets that joinery hides.
type selLine struct {
	words []selWord
	text  string
	// start and end bound the line in folded coordinates: the first word's
	// start to the last word's end. The single spaces the display joins with
	// belong to no word; a click on one rounds to the nearer word end.
	start, end int
}

// wrapSelect lays text out into lines that fit width, recording every word's
// folded offset. It mirrors [Wrap] — greedy space packing, overlong words
// broken mid-word — so a selectable strip breaks where a label would; the
// offsets are what [Wrap] does not keep and selection needs.
func wrapSelect(text string, scale int, f viewer.Font, width int) []selLine {
	folded := []rune(fold(text, f))
	// The separator is Wrap's, verbatim — not the drawing step — so the two
	// break lines identically (see the comment where Wrap computes it).
	// Hit-testing below still walks the drawing steps via advanceAt.
	sep := (f.GlyphAdvance + (f.GlyphAdvance - f.GlyphW)) * scale
	if f.Advance != nil {
		sep = f.Advance(' ', scale)
	}
	wordW := func(s string) int { return TextWidth(s, scale, f) }

	// Split into words, keeping each word's folded offset. A run of spaces
	// is one separator, as in [Wrap].
	var words []selWord
	for i := 0; i < len(folded); {
		if folded[i] == ' ' {
			i++
			continue
		}
		j := i
		for j < len(folded) && folded[j] != ' ' {
			j++
		}
		words = append(words, selWord{start: i, text: string(folded[i:j])})
		i = j
	}

	var lines []selLine
	var cur []selWord
	curW := 0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		lines = append(lines, makeSelLine(cur))
		cur, curW = nil, 0
	}
	for _, w := range words {
		wW := wordW(w.text)
		if len(cur) > 0 && curW+wW+sep > width {
			flush()
		}
		if len(cur) == 0 && wW > width {
			// An overlong word breaks mid-word, like [splitWord]: each
			// piece takes its own line and the next word starts fresh.
			runes := []rune(w.text)
			off := w.start
			for len(runes) > 0 {
				n := len(runes)
				for n > 1 && wordW(string(runes[:n])) > width {
					n--
				}
				lines = append(lines, makeSelLine([]selWord{{start: off, text: string(runes[:n])}}))
				off += n
				runes = runes[n:]
			}
			continue
		}
		if len(cur) > 0 {
			curW += sep
		}
		cur = append(cur, w)
		curW += wW
	}
	flush()
	if len(lines) == 0 {
		return []selLine{{}}
	}
	return lines
}

// makeSelLine joins words with single spaces for display, recording the
// folded span the joinery covers.
func makeSelLine(words []selWord) selLine {
	var b []rune
	for k, w := range words {
		if k > 0 {
			b = append(b, ' ')
		}
		b = append(b, []rune(w.text)...)
	}
	return selLine{
		words: words,
		text:  string(b),
		start: words[0].start,
		end:   words[len(words)-1].start + len([]rune(words[len(words)-1].text)),
	}
}

// SelectHeight returns the pixel height text needs when wrapped to width in
// the given font at the given scale: the text block without surrounding
// padding, so a caller can add its own.
func SelectHeight(text string, scale int, f viewer.Font, width int) int {
	if text == "" || width <= 0 || scale <= 0 {
		return 0
	}
	lines := wrapSelect(text, scale, f, width)
	lineH, textH := LineHeight(scale, f), TextHeight(scale, f)
	return len(lines)*lineH - (lineH - textH)
}

// Layout draws text in r and handles selection. The text wraps within r;
// unlike [Label] it never truncates, because a truncated copy is a corrupted
// one (a pasted URL with an ellipsis in the middle does not resolve).
func (t *SelectableText) Layout(ctx *Context, r Rect, text string, style SelectableStyle) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	if text != t.raw {
		t.raw = text
		t.folded = []rune(fold(text, ctx.Theme.Font))
		t.anchor, t.cursor = 0, 0
		t.dragging = false
	}
	if len(t.folded) == 0 {
		return
	}
	th := ctx.Theme
	scale := orInt(style.Scale, orInt(t.Scale, th.Body))
	col := or(style.Color, th.Text)
	focused := false
	if t.ID != NoFocus {
		focused = ctx.register(t.ID, r)
	}

	lines := wrapSelect(text, scale, th.Font, r.W)
	lineH := LineHeight(scale, th.Font)

	if focused {
		for _, e := range ctx.Input.Keys {
			t.handleKey(ctx, e)
		}
	}
	// Pointer selection does not depend on focus: a press starts a gesture
	// whether or not the widget holds the keyboard.
	if ctx.Input.PressedIn(r) {
		t.handlePress(ctx, r, scale, lines, lineH)
	} else if t.dragging {
		if ctx.Input.Down {
			t.cursor = t.indexAt(ctx, r, scale, lines, lineH)
		} else {
			t.dragging = false
		}
	}
	if ctx.Input.Released {
		t.dragging = false
	}

	ctx.Canvas.PushClip(r)
	defer ctx.Canvas.PopClip()
	lo, hi := t.range_()
	y := r.Y
	for _, ln := range lines {
		if hi > lo {
			t.highlight(ctx, ln, lo, hi, r.X, y, scale)
		}
		ctx.Canvas.Text(ln.text, r.X, y, scale, col)
		y += lineH
	}
}

// handleKey applies one key press: copy and select-all on Ctrl or Cmd (so
// macOS muscle memory works), caret movement otherwise. Text commits are
// ignored: the text is read-only.
func (t *SelectableText) handleKey(ctx *Context, e EventKey) {
	ctrl := e.Mods.Has(keysym.ModControl)
	shift := e.Mods.Has(keysym.ModShift)
	if (ctrl || e.Mods.Has(keysym.ModSuper)) && !e.Mods.Has(keysym.ModAlt) {
		switch lowerASCII(e.Rune) {
		case 'c':
			ctx.copy(t.SelectedText())
		case 'a':
			t.SelectAll()
		}
		return
	}
	switch e.Key {
	case keysym.KeyLeft:
		if shift {
			t.extend(-1)
		} else {
			t.move(-1)
		}
	case keysym.KeyRight:
		if shift {
			t.extend(1)
		} else {
			t.move(1)
		}
	case keysym.KeyHome:
		if shift {
			t.cursor = 0
		} else {
			t.move(-len(t.folded))
		}
	case keysym.KeyEnd:
		if shift {
			t.cursor = len(t.folded)
		} else {
			t.move(len(t.folded))
		}
	}
}

// handlePress starts a pointer gesture: a single press collapses the
// selection to the clicked rune and arms a drag, a double-click selects the
// word under the pointer, and a triple-click selects the whole text.
func (t *SelectableText) handlePress(ctx *Context, r Rect, scale int, lines []selLine, lineH int) {
	now, at := ctx.Input.Now, ctx.Input.Mouse
	if !now.IsZero() && !t.lastPress.IsZero() &&
		now.Sub(t.lastPress) <= doubleClickInterval &&
		abs(at.X-t.lastPressAt.X)+abs(at.Y-t.lastPressAt.Y) <= doubleClickRadius {
		t.clicks++
	} else {
		t.clicks = 1
	}
	t.lastPress, t.lastPressAt = now, at

	idx := t.indexAt(ctx, r, scale, lines, lineH)
	switch {
	case t.clicks >= 3:
		t.SelectAll()
		t.dragging = false
	case t.clicks == 2:
		t.selectWord(idx)
		t.dragging = true
	default:
		t.cursor, t.anchor = idx, idx
		t.dragging = true
	}
}

// indexAt returns the folded rune index under the pointer, rounding to the
// nearest gap the way a caret does.
func (t *SelectableText) indexAt(ctx *Context, r Rect, scale int, lines []selLine, lineH int) int {
	if len(lines) == 0 || lineH <= 0 {
		return 0
	}
	row := (ctx.Input.Mouse.Y - r.Y) / lineH
	row = clampInt(row, 0, len(lines)-1)
	ln := lines[row]
	px := ctx.Input.Mouse.X - r.X
	if px <= 0 {
		return clampInt(ln.start, 0, len(t.folded))
	}
	f := ctx.Theme.Font
	x := 0
	for wi, w := range ln.words {
		if wi > 0 {
			gap := advanceAt(f, scale, ' ')
			if px < x+gap {
				// Inside the inter-word gap: round to the nearer end.
				if px < x+gap/2 {
					prev := ln.words[wi-1]
					return clampInt(prev.start+len([]rune(prev.text)), 0, len(t.folded))
				}
				return clampInt(w.start, 0, len(t.folded))
			}
			x += gap
		}
		wrunes := []rune(w.text)
		for j := range wrunes {
			adv := advanceAt(f, scale, wrunes[j])
			if px <= x+adv/2 {
				return clampInt(w.start+j, 0, len(t.folded))
			}
			x += adv
		}
	}
	return clampInt(ln.end, 0, len(t.folded))
}

// highlight paints the selection over the words of one line that the
// [lo, hi) range overlaps.
func (t *SelectableText) highlight(ctx *Context, ln selLine, lo, hi, x, y, scale int) {
	f := ctx.Theme.Font
	h := TextHeight(scale, f)
	dx := 0
	for wi, w := range ln.words {
		if wi > 0 {
			dx += advanceAt(f, scale, ' ')
		}
		wrunes := []rune(w.text)
		k0 := clampInt(lo-w.start, 0, len(wrunes))
		k1 := clampInt(hi-w.start, 0, len(wrunes))
		if k1 > k0 {
			x0 := x + dx + TextWidth(string(wrunes[:k0]), scale, f)
			x1 := x + dx + TextWidth(string(wrunes[:k1]), scale, f)
			ctx.Canvas.Fill(Rect{X: x0, Y: y, W: x1 - x0, H: h}, ctx.Theme.SurfaceSelected)
		}
		dx += TextWidth(w.text, scale, f)
	}
}
