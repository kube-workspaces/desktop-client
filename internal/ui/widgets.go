// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"image/color"
	"math"
	"time"
	"unicode"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// Align is the horizontal placement of text within its rectangle.
type Align int

// The supported alignments.
const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// LabelStyle configures [Label]. The zero value is body text, left-aligned,
// drawn from the top of the rectangle.
type LabelStyle struct {
	// Color is the ink. Unset means the theme's body text colour.
	Color color.RGBA
	// Scale is the integer glyph scale. Zero means the theme's body scale.
	Scale int
	// Align places the text horizontally.
	Align Align
	// Middle centres the text vertically in the rectangle instead of drawing
	// from its top edge. It is what every label inside a control wants.
	Middle bool
	// Wrap lays the text out over as many lines as fit. Without it the text
	// is truncated with an ellipsis to one line.
	Wrap bool
}

// Label draws text inside r.
//
// Text that does not fit is truncated with an ellipsis rather than clipped,
// unless Wrap is set. See [Truncate] for why.
func Label(ctx *Context, r Rect, text string, style LabelStyle) {
	if r.W <= 0 || r.H <= 0 || text == "" {
		return
	}
	scale := orInt(style.Scale, ctx.Theme.Body)
	col := or(style.Color, ctx.Theme.Text)

	lines := []string{Truncate(text, scale, ctx.Theme.Font, r.W)}
	if style.Wrap {
		lines = Wrap(text, scale, ctx.Theme.Font, r.W)
	}

	lineH := LineHeight(scale, ctx.Theme.Font)
	blockH := len(lines)*lineH - (lineH - TextHeight(scale, ctx.Theme.Font))
	y := r.Y
	if style.Middle {
		y = r.Y + (r.H-blockH)/2
	}

	ctx.Canvas.PushClip(r)
	defer ctx.Canvas.PopClip()
	for _, line := range lines {
		x := r.X
		switch style.Align {
		case AlignCenter:
			x = r.X + (r.W-TextWidth(line, scale, ctx.Theme.Font))/2
		case AlignRight:
			x = r.X + r.W - TextWidth(line, scale, ctx.Theme.Font)
		case AlignLeft:
		}
		ctx.Canvas.Text(line, x, y, scale, col)
		y += lineH
	}
}

// ButtonVariant selects a button's weight.
type ButtonVariant int

// The button variants. There are four because an interface needs exactly one
// primary action per screen, a quiet way to offer the rest, and a visibly
// different treatment for anything destructive.
const (
	// ButtonPrimary is the screen's main action: filled with the accent.
	ButtonPrimary ButtonVariant = iota
	// ButtonSecondary is an ordinary action: an outlined surface.
	ButtonSecondary
	// ButtonQuiet is a tertiary action: text with no chrome until hovered.
	ButtonQuiet
	// ButtonDanger is destructive: sign out, remove, disconnect.
	ButtonDanger
)

// Button is a clickable command.
//
// It is a struct rather than a function call with eight arguments because the
// caller keeps one per screen and only its Text and Disabled change; the ID is
// what ties it to the focus ring across frames.
type Button struct {
	// ID identifies the button to the focus ring. A button with no ID is not
	// focusable and can only be clicked.
	ID FocusID
	// Text is the label.
	Text string
	// Variant selects the weight.
	Variant ButtonVariant
	// Disabled greys the button out and stops it activating.
	Disabled bool
	// Scale overrides the theme's body scale.
	Scale int
}

// Width returns the button's intrinsic width: its label plus symmetric
// padding. A row of buttons sized this way stays legible when the labels
// change length, which they do as soon as the interface is translated or a
// state name changes.
func (b *Button) Width(ctx *Context) int {
	scale := orInt(b.Scale, ctx.Theme.Body)
	return TextWidth(b.Text, scale, ctx.Theme.Font) + 2*ctx.Theme.Pad
}

// Layout draws the button in r and reports whether it was activated.
//
// Activation is a complete click inside r, or Enter or Space while the button
// has the keyboard. Space as well as Enter because a button is a button on
// every desktop, and a user who has tabbed to one should not have to guess.
func (b *Button) Layout(ctx *Context, r Rect) bool {
	th := ctx.Theme
	focused := false
	if b.ID != NoFocus && !b.Disabled {
		focused = ctx.register(b.ID, r)
	}

	hovered := !b.Disabled && ctx.Input.Hovering(r)
	held := !b.Disabled && ctx.Input.Holding(r) && hovered

	fill, ink, border := b.colors(th, hovered, held)
	if b.Disabled {
		fill, ink, border = th.Surface, th.TextDisabled, th.Border
	}

	if fill != Transparent {
		ctx.Canvas.FillRounded(r, th.Radius, fill)
	}
	if border != Transparent {
		ctx.Canvas.StrokeRounded(r, th.Radius, th.BorderWidth, border)
	}
	if focused {
		ctx.Canvas.StrokeRounded(r, th.Radius, th.FocusWidth, th.Focus)
	}

	Label(ctx, InsetXY(r, th.Gap, 0), b.Text, LabelStyle{
		Color:  ink,
		Scale:  orInt(b.Scale, th.Body),
		Align:  AlignCenter,
		Middle: true,
	})

	if b.Disabled {
		return false
	}
	if ctx.Input.ClickedIn(r) {
		return true
	}
	return focused && (ctx.Input.KeyPressed(keysym.KeyReturn) ||
		ctx.Input.KeyPressed(keysym.KeyKPEnter) ||
		ctx.Input.RuneChord(keysym.ModNone, ' '))
}

// colors picks the fill, ink and border for a variant and interaction state.
func (b *Button) colors(th *Theme, hovered, held bool) (fill, ink, border color.RGBA) {
	switch b.Variant {
	case ButtonPrimary:
		fill = th.Accent
		if hovered {
			fill = th.AccentHover
		}
		if held {
			fill = th.Accent
		}
		return fill, th.TextOnAccent, Transparent

	case ButtonDanger:
		fill = th.Surface
		if hovered {
			fill = th.DangerSurface
		}
		return fill, th.Danger, th.Border

	case ButtonQuiet:
		fill = Transparent
		if hovered {
			fill = th.SurfaceAlt
		}
		ink = th.TextMuted
		if hovered {
			ink = th.Text
		}
		return fill, ink, Transparent

	default: // ButtonSecondary
		fill = th.SurfaceAlt
		border = th.Border
		if hovered {
			border = th.BorderStrong
		}
		return fill, th.Text, border
	}
}

// cursorBlink is the period of the text cursor. Half a second on, half off is
// the convention every platform settled on; matching it means the cursor does
// not read as an animation.
const cursorBlink = 530 * time.Millisecond

// TextInput is a single-line editable field.
//
// It holds its text as runes rather than a string because every operation it
// performs — insert at the cursor, delete before it, move it — is by character
// position, and doing that on a UTF-8 string means re-scanning it on every
// keystroke and getting the boundaries wrong once.
//
// Selection is the range between anchor (the fixed end) and cursor (the moving
// end). When the two are equal there is no selection. Shift-arrow extends,
// plain movement collapses, and every editing operation acts on the selection
// first. Copy/cut go through the context's clipboard; without one they are
// silent no-ops.
type TextInput struct {
	// ID identifies the field to the focus ring.
	ID FocusID
	// Placeholder is shown, muted, while the field is empty.
	Placeholder string
	// Password masks the text.
	Password bool
	// MaxLen bounds the text in runes. Zero means unbounded.
	MaxLen int
	// Scale overrides the theme's body scale.
	Scale int

	text   []rune
	cursor int
	anchor int
	// offset is the first visible rune, so that a cursor past the right edge
	// scrolls the field rather than disappearing.
	offset int
	// dragging tracks a press-drag-release gesture across frames: set on a
	// single press inside the field, cleared on release.
	dragging bool
	// lastPress records when and where the previous press landed, so that a
	// second press soon after and nearby counts as a double-click (word
	// select) and a third as a triple-click (select all).
	lastPress   time.Time
	lastPressAt Point
	clicks      int
}

// Value returns the field's text.
func (t *TextInput) Value() string { return string(t.text) }

// SetValue replaces the text and puts the cursor at the end, which is where a
// user who has just been given a prefilled value wants it.
func (t *TextInput) SetValue(s string) {
	t.text = []rune(s)
	if t.MaxLen > 0 && len(t.text) > t.MaxLen {
		t.text = t.text[:t.MaxLen]
	}
	t.cursor = len(t.text)
	t.anchor = t.cursor
	t.offset = 0
	t.dragging = false
}

// Len returns the length of the text in runes.
func (t *TextInput) Len() int { return len(t.text) }

// Cursor returns the cursor's rune index.
func (t *TextInput) Cursor() int { return t.cursor }

// SetCursor moves the cursor, clamped to the text. The selection collapses:
// a programmatic placement is a new starting point, not an extension.
func (t *TextInput) SetCursor(i int) {
	t.cursor = clampInt(i, 0, len(t.text))
	t.anchor = t.cursor
}

// HasSelection reports whether the field holds a non-empty selection.
func (t *TextInput) HasSelection() bool {
	lo, hi := t.SelectionRange()
	return hi > lo
}

// SelectionRange returns the selected rune interval, ordered and clamped, or
// two equal values when there is no selection.
func (t *TextInput) SelectionRange() (int, int) {
	lo, hi := t.anchor, t.cursor
	if lo > hi {
		lo, hi = hi, lo
	}
	return clampInt(lo, 0, len(t.text)), clampInt(hi, 0, len(t.text))
}

// SelectedText returns the selected runes. For a password field this is the
// real text, not the mask: the user selected what they typed, and copy must
// carry that rather than a row of asterisks.
func (t *TextInput) SelectedText() string {
	lo, hi := t.SelectionRange()
	return string(t.text[lo:hi])
}

// SelectAll selects the whole field.
func (t *TextInput) SelectAll() {
	t.anchor = 0
	t.cursor = len(t.text)
}

// DeleteSelection removes the selected runes, leaves the cursor at the lower
// end, and reports whether it removed anything.
func (t *TextInput) DeleteSelection() bool {
	lo, hi := t.SelectionRange()
	if hi == lo {
		return false
	}
	t.text = append(t.text[:lo], t.text[hi:]...)
	t.cursor, t.anchor = lo, lo
	return true
}

// extend moves the cursor by delta runes and keeps the anchor where it is, so
// the selection grows or shrinks around the fixed end.
func (t *TextInput) extend(delta int) {
	t.cursor = clampInt(t.cursor+delta, 0, len(t.text))
}

// extendToWord moves the cursor by one word in the given direction (-1 or +1)
// and keeps the anchor where it is.
func (t *TextInput) extendToWord(dir int) {
	if dir < 0 {
		t.cursor = wordStart(t.text, t.cursor)
	} else {
		t.cursor = wordEnd(t.text, t.cursor)
	}
}

// selectWord selects the word containing rune index i, or the separator run
// under the pointer when i is not on a word character. Unlike wordStart and
// wordEnd — which skip over separators because Ctrl-arrow navigation must —
// this expands strictly around the click: double-clicking "bar" in "foo bar"
// selects "bar", not the previous word along with it.
func (t *TextInput) selectWord(i int) {
	i = clampInt(i, 0, len(t.text))
	t.anchor, t.cursor = i, i
	if i < len(t.text) && isWordChar(t.text[i]) {
		for t.anchor > 0 && isWordChar(t.text[t.anchor-1]) {
			t.anchor--
		}
		for t.cursor < len(t.text) && isWordChar(t.text[t.cursor]) {
			t.cursor++
		}
		return
	}
	for t.anchor > 0 && !isWordChar(t.text[t.anchor-1]) {
		t.anchor--
	}
	for t.cursor < len(t.text) && !isWordChar(t.text[t.cursor]) {
		t.cursor++
	}
}

// isWordChar reports whether r belongs inside a word for navigation and
// double-click selection. Letters, digits and the underscore hang together;
// everything else — spaces, slashes, dots, colons — is a boundary. That is
// what makes double-clicking a URL select one segment of it rather than the
// whole line.
func isWordChar(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// wordStart returns the start of the word containing or preceding i.
func wordStart(text []rune, i int) int {
	i = clampInt(i, 0, len(text))
	for i > 0 && !isWordChar(text[i-1]) {
		i--
	}
	for i > 0 && isWordChar(text[i-1]) {
		i--
	}
	return i
}

// wordEnd returns the end of the word containing or following i.
func wordEnd(text []rune, i int) int {
	i = clampInt(i, 0, len(text))
	for i < len(text) && !isWordChar(text[i]) {
		i++
	}
	for i < len(text) && isWordChar(text[i]) {
		i++
	}
	return i
}

// Insert inserts runes at the cursor and leaves the cursor after them.
// A selection is replaced: typing over a selection is the reason it exists.
// Non-printable runes are dropped: the font cannot draw them and a field is
// not the place to discover that.
func (t *TextInput) Insert(runes ...rune) {
	t.DeleteSelection()
	for _, r := range runes {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if t.MaxLen > 0 && len(t.text) >= t.MaxLen {
			return
		}
		t.cursor = clampInt(t.cursor, 0, len(t.text))
		t.text = append(t.text, 0)
		copy(t.text[t.cursor+1:], t.text[t.cursor:])
		t.text[t.cursor] = r
		t.cursor++
	}
	t.anchor = t.cursor
}

// Backspace deletes the selection, or the rune before the cursor when there
// is none, and reports whether it deleted anything.
func (t *TextInput) Backspace() bool {
	if t.DeleteSelection() {
		return true
	}
	if t.cursor <= 0 || len(t.text) == 0 {
		return false
	}
	t.cursor = clampInt(t.cursor, 0, len(t.text))
	if t.cursor == 0 {
		return false
	}
	t.text = append(t.text[:t.cursor-1], t.text[t.cursor:]...)
	t.cursor--
	t.anchor = t.cursor
	return true
}

// DeleteForward deletes the selection, or the rune at the cursor when there
// is none, and reports whether it deleted anything.
func (t *TextInput) DeleteForward() bool {
	if t.DeleteSelection() {
		return true
	}
	if t.cursor >= len(t.text) {
		return false
	}
	t.text = append(t.text[:t.cursor], t.text[t.cursor+1:]...)
	return true
}

// MoveCursor moves the cursor by delta runes, clamped to the text, and
// collapses the selection. Clamping rather than wrapping is what makes holding
// the left arrow down settle at the start of the line instead of jumping to
// the end.
func (t *TextInput) MoveCursor(delta int) {
	t.cursor = clampInt(t.cursor+delta, 0, len(t.text))
	t.anchor = t.cursor
}

// MoveHome puts the cursor before the first rune and collapses the selection.
func (t *TextInput) MoveHome() { t.cursor, t.anchor = 0, 0 }

// MoveEnd puts the cursor after the last rune and collapses the selection.
func (t *TextInput) MoveEnd() { t.cursor, t.anchor = len(t.text), len(t.text) }

// Clear empties the field.
func (t *TextInput) Clear() {
	t.text = t.text[:0]
	t.cursor, t.anchor = 0, 0
	t.offset = 0
	t.dragging = false
}

// KillToEnd deletes the selection, or from the cursor to the end of the line
// (Ctrl-K) when there is none.
func (t *TextInput) KillToEnd() {
	if t.DeleteSelection() {
		return
	}
	t.cursor = clampInt(t.cursor, 0, len(t.text))
	t.anchor = t.cursor
	t.text = t.text[:t.cursor]
}

// HandleKey applies one key press and reports whether it was consumed.
//
// Enter is deliberately *not* consumed: it is the screen's business, and
// swallowing it here would mean every form needed a submit button.
func (t *TextInput) HandleKey(ctx *Context, e EventKey) bool {
	ctrl := e.Mods.Has(keysym.ModControl)
	shift := e.Mods.Has(keysym.ModShift)
	// Word jumps ride on Ctrl (and on Alt, which is Option on macOS), but
	// never on Ctrl+Alt together: on Windows that combination is AltGr, the
	// third-level shift a large part of the world types "@" and "€" with,
	// and stealing it for navigation would eat those characters.
	wordJump := (ctrl != e.Mods.Has(keysym.ModAlt)) && !e.Mods.Has(keysym.ModSuper)

	switch e.Key {
	case keysym.KeyBackSpace:
		t.Backspace()
		return true
	case keysym.KeyDelete:
		t.DeleteForward()
		return true
	case keysym.KeyLeft:
		if shift {
			if wordJump {
				t.extendToWord(-1)
			} else {
				t.extend(-1)
			}
		} else if wordJump {
			t.SetCursor(wordStart(t.text, t.cursor))
		} else {
			t.MoveCursor(-1)
		}
		return true
	case keysym.KeyRight:
		if shift {
			if wordJump {
				t.extendToWord(1)
			} else {
				t.extend(1)
			}
		} else if wordJump {
			t.SetCursor(wordEnd(t.text, t.cursor))
		} else {
			t.MoveCursor(1)
		}
		return true
	case keysym.KeyHome:
		if shift {
			t.cursor = 0
		} else {
			t.MoveHome()
		}
		return true
	case keysym.KeyEnd:
		if shift {
			t.cursor = len(t.text)
		} else {
			t.MoveEnd()
		}
		return true
	}

	// Selection clipboard on Ctrl or Cmd (Super), so macOS muscle memory
	// works without relearning. Alt is excluded so AltGr typing is
	// unaffected (see wordJump above); the composed-text path drops the
	// side-effect text these chords produce, so Ctrl-V pastes without also
	// typing a "v".
	if (ctrl || e.Mods.Has(keysym.ModSuper)) && !e.Mods.Has(keysym.ModAlt) {
		switch lowerASCII(e.Rune) {
		case 'a':
			t.SelectAll()
			return true
		case 'c':
			ctx.copy(t.SelectedText())
			return true
		case 'x':
			if t.HasSelection() {
				ctx.copy(t.SelectedText())
				t.DeleteSelection()
			}
			return true
		case 'v':
			t.Insert([]rune(singleLine(ctx.paste()))...)
			return true
		}
	}

	if ctrl && !e.Mods.HasAny(keysym.ModAlt|keysym.ModSuper) {
		// The readline bindings, because this is a developer tool and these
		// are muscle memory.
		switch lowerASCII(e.Rune) {
		case 'e':
			t.MoveEnd()
			return true
		case 'u':
			t.Clear()
			return true
		case 'k':
			t.KillToEnd()
			return true
		}
	}

	// The character in a key event is only text when the backend has no
	// better answer. Once it has delivered even one [EventText] it is
	// composing properly, and [TextInput.HandleText] owns insertion; reading
	// both would type every character twice.
	if !ctx.Input.ComposedText && IsTextRune(e) {
		t.Insert(e.Rune)
		return true
	}
	return false
}

// HandleText inserts one committed text event and reports whether it inserted
// anything.
//
// The whole string goes in, not its first rune: an IME commits a word at a
// time, and on some platforms a paste arrives this way too. [TextInput.Insert]
// drops what the field cannot hold — control characters, and anything past
// MaxLen.
//
// The event needs no modifier check of its own: [Input.Fold] has already
// dropped the text a command chord produced, which is the check that keeps
// Ctrl-V from pasting and typing a "v".
func (t *TextInput) HandleText(e EventText) bool {
	before := len(t.text)
	t.Insert([]rune(e.Text)...)
	return len(t.text) != before
}

// Layout draws the field in r and reports whether the user pressed Enter in
// it, which is what a form treats as "submit".
func (t *TextInput) Layout(ctx *Context, r Rect) bool {
	th := ctx.Theme
	scale := orInt(t.Scale, th.Body)
	focused := ctx.register(t.ID, r)

	if focused {
		// Edits, not Keys: a field has to apply key presses and composed text
		// in the order they arrived. Type "a", press Home, type "b" quickly
		// enough that all three land in one batch, and applying the keys
		// first would put the cursor at the start before "a" was inserted.
		for _, ev := range ctx.Input.Edits {
			switch e := ev.(type) {
			case EventKey:
				t.HandleKey(ctx, e)
			case EventText:
				t.HandleText(e)
			}
		}
		// The pointer places the cursor and, while held, extends the
		// selection. A press starts (or multi-clicks) a gesture; later frames
		// of the same hold stretch the moving end to the pointer.
		if ctx.Input.PressedIn(r) {
			t.handlePress(ctx, r, scale)
		} else if t.dragging {
			if ctx.Input.Down {
				t.cursor = t.indexFromPointer(ctx, r, scale)
			} else {
				t.dragging = false
			}
		}
		if ctx.Input.Released {
			t.dragging = false
		}
	} else {
		t.dragging = false
	}

	hovered := ctx.Input.Hovering(r)
	border := th.Border
	if hovered {
		border = th.BorderStrong
	}
	ctx.Canvas.FillRounded(r, th.Radius, th.SurfaceAlt)
	ctx.Canvas.StrokeRounded(r, th.Radius, th.BorderWidth, border)
	if focused {
		ctx.Canvas.StrokeRounded(r, th.Radius, th.FocusWidth, th.Focus)
	}

	inner := InsetXY(r, th.Gap, 0)
	if inner.W <= 0 {
		return false
	}
	ctx.Canvas.PushClip(inner)
	defer ctx.Canvas.PopClip()

	display := t.display()
	baseY := inner.Y + (inner.H-TextHeight(scale, th.Font))/2

	if len(display) == 0 && t.Placeholder != "" {
		ctx.Canvas.Text(Truncate(t.Placeholder, scale, th.Font, inner.W), inner.X, baseY, scale, th.TextDisabled)
	}

	t.scrollToCursor(inner.W, scale, th.Font)
	visible := display
	if t.offset < len(visible) {
		visible = visible[t.offset:]
	} else {
		visible = nil
	}

	// The selection highlight goes down before the text, measured from the
	// same origin in one run each so the proportional face's advances land
	// exactly. Both theme modes pair a light-on-dark or dark-on-light text
	// colour with a selected surface that keeps it legible.
	if lo, hi := t.SelectionRange(); hi > lo {
		lo = max(lo, t.offset)
		if hi > lo {
			x0 := inner.X + TextWidth(string(display[t.offset:lo]), scale, th.Font)
			x1 := inner.X + TextWidth(string(display[t.offset:hi]), scale, th.Font)
			ctx.Canvas.Fill(Rect{X: x0, Y: baseY, W: x1 - x0, H: TextHeight(scale, th.Font)}, th.SurfaceSelected)
		}
	}
	ctx.Canvas.Text(string(visible), inner.X, baseY, scale, th.Text)

	if focused {
		// Ask for exactly the frame the next blink needs, rather than a frame
		// per tick: a software repaint of the whole window sixty times a
		// second to animate one rectangle is not a trade worth making.
		ctx.RepaintAfter(untilNextBlink(ctx.Input.Now))
		if blinkOn(ctx.Input.Now) {
			cx := inner.X + TextWidth(string(display[t.offset:clampInt(t.cursor, t.offset, len(display))]), scale, th.Font)
			if t.cursor > t.offset && th.Font.Advance == nil {
				// The measured run omits the trailing inter-glyph gap, which
				// is exactly where the cursor belongs. A proportional face
				// carries its spacing in each advance and needs no gap.
				cx += (th.Font.GlyphAdvance - th.Font.GlyphW) * scale
			}
			ctx.Canvas.Fill(Rect{
				X: cx,
				Y: baseY - scale,
				W: max(1, scale),
				H: TextHeight(scale, th.Font) + 2*scale,
			}, th.Accent)
		}
	}

	return focused && (ctx.Input.KeyPressed(keysym.KeyReturn) || ctx.Input.KeyPressed(keysym.KeyKPEnter))
}

// display is the text as it should be drawn: masked for a password field.
func (t *TextInput) display() []rune {
	if !t.Password {
		return t.text
	}
	masked := make([]rune, len(t.text))
	for i := range masked {
		masked[i] = '*'
	}
	return masked
}

// scrollToCursor adjusts the first visible rune so the cursor is on screen.
// Scrolling is measured in pixels rather than cells because the clean face is
// proportional and has no glyph cell.
func (t *TextInput) scrollToCursor(width, scale int, f viewer.Font) {
	if width <= 0 {
		t.offset = 0
		return
	}
	t.cursor = clampInt(t.cursor, 0, len(t.text))
	if t.offset > t.cursor {
		t.offset = t.cursor
	}
	// The caret may not be to the right of the box: push the window forward
	// until the run between offset and the caret fits.
	for t.offset < t.cursor && TextWidth(string(t.text[t.offset:t.cursor]), scale, f) > width {
		t.offset++
	}
	// When the caret is at the end of the text, pull the window back so as
	// much of the tail as fits is visible, the opposite of the press.
	if t.cursor >= len(t.text) {
		for t.offset > 0 && TextWidth(string(t.text[t.offset-1:]), scale, f) <= width {
			t.offset--
		}
	}
	t.offset = clampInt(t.offset, 0, len(t.text))
}

// handlePress starts a pointer gesture: a single press collapses the
// selection to the clicked glyph and arms a drag, a double-click selects the
// word under the pointer (dragging extends from its start), and a
// triple-click selects the whole field.
func (t *TextInput) handlePress(ctx *Context, r Rect, scale int) {
	now, at := ctx.Input.Now, ctx.Input.Mouse
	if !now.IsZero() && !t.lastPress.IsZero() &&
		now.Sub(t.lastPress) <= doubleClickInterval &&
		abs(at.X-t.lastPressAt.X)+abs(at.Y-t.lastPressAt.Y) <= doubleClickRadius {
		t.clicks++
	} else {
		t.clicks = 1
	}
	t.lastPress, t.lastPressAt = now, at

	idx := t.indexFromPointer(ctx, r, scale)
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

// doubleClickInterval is how soon after a press a second press on the same
// spot counts as a multi-click. Half a second is the desktop convention.
const doubleClickInterval = 500 * time.Millisecond

// doubleClickRadius is how far the pointer may wander, in Manhattan canvas
// pixels, and still count as the same spot for multi-click purposes.
const doubleClickRadius = 8

// indexFromPointer returns the rune index of the glyph under the pointer,
// rounding to the nearest gap the way a caret does.
func (t *TextInput) indexFromPointer(ctx *Context, r Rect, scale int) int {
	inner := InsetXY(r, ctx.Theme.Gap, 0)
	px := ctx.Input.Mouse.X - inner.X
	if px <= 0 {
		return clampInt(t.offset, 0, len(t.text))
	}
	// Walk the advances of the visible run until the click falls in a glyph's
	// left half, rounding to the nearest gap the way a caret does.
	idx, total := len(t.text), 0
	for i := t.offset; i < len(t.text); i++ {
		total += advanceAt(ctx.Theme.Font, scale, t.text[i])
		if px <= total-advanceAt(ctx.Theme.Font, scale, t.text[i])/2 {
			return i
		}
		idx = i + 1
	}
	return idx
}

// advanceAt is the horizontal step a glyph takes at the given scale, working
// for the proportional clean face and the monospace bitmap faces alike.
func advanceAt(f viewer.Font, scale int, r rune) int {
	if f.Advance != nil {
		return f.Advance(r, scale)
	}
	return f.GlyphAdvance * scale
}

// blinkOn reports whether the text cursor is in its visible half-period.
func blinkOn(now time.Time) bool {
	if now.IsZero() {
		return true
	}
	return now.UnixNano()/int64(cursorBlink)%2 == 0
}

// untilNextBlink is how long the cursor stays as it is.
func untilNextBlink(now time.Time) time.Duration {
	if now.IsZero() {
		return cursorBlink
	}
	return cursorBlink - time.Duration(now.UnixNano()%int64(cursorBlink))
}

// singleLine collapses a pasted string to its first line and strips control
// characters, because a single-line field cannot show the rest and a stray
// newline in a server URL is invisible and maddening.
func singleLine(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' {
			break
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// Checkbox is a labelled on/off switch.
//
// It exists for exactly one setting — whether to ignore a development
// instance's self-signed certificate — and it is a checkbox rather than a
// button because that setting is a state the user needs to be able to see at a
// glance before they type a password into the instance it applies to.
type Checkbox struct {
	// ID identifies the checkbox to the focus ring.
	ID FocusID
	// Label is drawn to the right of the box.
	Label string
	// Checked is the state, and is updated in place by [Checkbox.Layout].
	Checked bool
	// Scale overrides the theme's body scale.
	Scale int
}

// Layout draws the checkbox in r and reports whether it was toggled.
func (c *Checkbox) Layout(ctx *Context, r Rect) bool {
	th := ctx.Theme
	scale := orInt(c.Scale, th.Body)
	focused := ctx.register(c.ID, r)

	toggled := ctx.Input.ClickedIn(r) ||
		(focused && (ctx.Input.RuneChord(keysym.ModNone, ' ') ||
			ctx.Input.KeyPressed(keysym.KeyReturn)))
	if toggled {
		c.Checked = !c.Checked
	}

	size := TextHeight(scale, th.Font) + 2*scale
	box := Rect{X: r.X, Y: r.Y + (r.H-size)/2, W: size, H: size}
	fill, border := th.SurfaceAlt, th.Border
	if c.Checked {
		fill, border = th.Accent, th.Accent
	}
	if ctx.Input.Hovering(r) && !c.Checked {
		border = th.BorderStrong
	}
	ctx.Canvas.FillRounded(box, th.Radius/2, fill)
	ctx.Canvas.StrokeRounded(box, th.Radius/2, th.BorderWidth, border)
	if focused {
		ctx.Canvas.StrokeRounded(box, th.Radius/2, th.FocusWidth, th.Focus)
	}
	if c.Checked {
		// A tick drawn from two strokes; the font has no dingbats and an "x"
		// in a filled box reads as "clear this", which is the opposite.
		ctx.Canvas.Line(box.X+size/4, box.Y+size/2, box.X+size/2-scale/2, box.Y+size-size/3, max(1, scale), th.TextOnAccent)
		ctx.Canvas.Line(box.X+size/2-scale/2, box.Y+size-size/3, box.X+size-size/4, box.Y+size/4, max(1, scale), th.TextOnAccent)
	}

	label, _ := CutRight(r, r.W-size-th.Gap/2)
	ink := th.TextMuted
	if focused || ctx.Input.Hovering(r) {
		ink = th.Text
	}
	Label(ctx, label, c.Label, LabelStyle{Color: ink, Scale: scale, Middle: true})
	return toggled
}

// RowState describes how one list row should be drawn.
type RowState struct {
	// Index is the row's position in the list.
	Index int
	// Selected marks the row the keyboard is on.
	Selected bool
	// Hovered marks the row under the pointer.
	Hovered bool
	// ListFocused reports whether the list itself has the keyboard, so a row
	// can show its selection more faintly when it does not.
	ListFocused bool
}

// List is a scrollable, keyboard-navigable list with a caller-supplied row
// renderer.
//
// Scrolling is by whole rows rather than by pixels. Every row is the same
// height, so row scrolling cannot leave a half-row clipped at the top edge,
// and "which rows are visible" stays a pair of integers that a test can
// assert on rather than a pixel offset that has to be divided back out.
type List struct {
	// ID identifies the list to the focus ring.
	ID FocusID
	// RowHeight overrides the theme's row height.
	RowHeight int
	// WheelRows is how many rows one wheel click scrolls. Zero means three,
	// which is what every platform's default is.
	WheelRows int

	// Selected is the index of the selected row, or -1 for none.
	Selected int
	// Offset is the index of the first visible row.
	Offset int

	// count and view are the last layout's row count and visible row count,
	// kept so that the keyboard handlers can be used between frames.
	count int
	view  int

	lastClickIndex int
	lastClickAt    time.Time

	// dragging is a thumb drag in flight: the primary button went down on the
	// thumb and is still held, so Offset follows the pointer. dragGrab is how
	// far below the thumb's top the pointer grabbed it, so the thumb tracks
	// the pointer instead of jumping its head under the cursor on pickup.
	// Both are valid only while dragging is true.
	dragging bool
	dragGrab int
}

// doubleClick is how close together two clicks on the same row have to be to
// count as an activation.
const doubleClick = 400 * time.Millisecond

// Visible returns how many rows fitted on screen at the last layout.
func (l *List) Visible() int { return l.view }

// Count returns the row count at the last layout.
func (l *List) Count() int { return l.count }

// Select moves the selection to i, clamped, and scrolls it into view.
func (l *List) Select(i int) {
	if l.count <= 0 {
		l.Selected = -1
		return
	}
	l.Selected = clampInt(i, 0, l.count-1)
	l.EnsureVisible()
}

// Scroll moves the viewport by delta rows without moving the selection, which
// is what a wheel does: the user is looking, not choosing.
func (l *List) Scroll(delta int) {
	l.Offset = clampInt(l.Offset+delta, 0, l.maxOffset())
}

// EnsureVisible scrolls the minimum distance needed to bring the selection on
// screen.
func (l *List) EnsureVisible() {
	if l.Selected < 0 || l.view <= 0 {
		return
	}
	if l.Selected < l.Offset {
		l.Offset = l.Selected
	}
	if l.Selected >= l.Offset+l.view {
		l.Offset = l.Selected - l.view + 1
	}
	l.Offset = clampInt(l.Offset, 0, l.maxOffset())
}

// HandleKey applies one key press to the selection and reports whether it was
// consumed. Enter is not consumed: activation is [List.Layout]'s result.
func (l *List) HandleKey(e EventKey) bool {
	if l.count == 0 {
		return false
	}
	page := max(1, l.view-1)
	switch e.Key {
	case keysym.KeyUp:
		l.Select(l.Selected - 1)
		return true
	case keysym.KeyDown:
		l.Select(l.Selected + 1)
		return true
	case keysym.KeyPageUp:
		l.Select(l.Selected - page)
		return true
	case keysym.KeyPageDown:
		l.Select(l.Selected + page)
		return true
	case keysym.KeyHome:
		l.Select(0)
		return true
	case keysym.KeyEnd:
		l.Select(l.count - 1)
		return true
	}
	return false
}

// Layout draws count rows inside r using row to paint each one, and reports
// whether a row was activated.
//
// Activation is Enter on the selection or a double click, never a single
// click: a single click selects. Opening a remote session is expensive and
// exclusive — the display is single-seat — so it should not be one stray click
// away.
func (l *List) Layout(ctx *Context, r Rect, count int, row func(*Context, Rect, RowState)) bool {
	th := ctx.Theme
	rowH := orInt(l.RowHeight, th.RowHeight)
	l.count = count
	l.view = 0
	if rowH > 0 && r.H > 0 {
		l.view = r.H / rowH
	}

	switch {
	case count == 0:
		l.Selected = -1
	case l.Selected < 0 || l.Selected >= count:
		l.Selected = clampInt(l.Selected, 0, count-1)
	}
	l.Offset = clampInt(l.Offset, 0, l.maxOffset())

	focused := ctx.register(l.ID, r)
	activated := false
	// sb is the scrollbar's hit band, empty while the list fits on screen. A
	// gesture that begins there belongs to the scrollbar, not to a row, so row
	// selection below refuses presses that started on it.
	sb := l.scrollbarBand(r, rowH)

	if ctx.Input.Hovering(r) && ctx.Input.Wheel.Y != 0 {
		l.Scroll(-ctx.Input.Wheel.Y * orInt(l.WheelRows, 3))
	}
	if focused {
		for _, e := range ctx.Input.Keys {
			l.HandleKey(e)
		}
		if ctx.Input.KeyPressed(keysym.KeyReturn) || ctx.Input.KeyPressed(keysym.KeyKPEnter) {
			activated = l.Selected >= 0
		}
	}

	if ctx.Input.ClickedIn(r) && rowH > 0 && !sb.Contains(ctx.Input.PressOrigin.X, ctx.Input.PressOrigin.Y) {
		if i := l.Offset + (ctx.Input.Mouse.Y-r.Y)/rowH; i >= 0 && i < count {
			l.Select(i)
			if l.lastClickIndex == i && !ctx.Input.Now.IsZero() &&
				ctx.Input.Now.Sub(l.lastClickAt) <= doubleClick {
				activated = true
			}
			l.lastClickIndex, l.lastClickAt = i, ctx.Input.Now
		}
	}

	ctx.Canvas.PushClip(r)
	defer ctx.Canvas.PopClip()
	for i := 0; i < l.view; i++ {
		index := l.Offset + i
		if index >= count {
			break
		}
		rect := Rect{X: r.X, Y: r.Y + i*rowH, W: r.W, H: rowH}
		state := RowState{
			Index:       index,
			Selected:    index == l.Selected,
			Hovered:     ctx.Input.Hovering(rect),
			ListFocused: focused,
		}
		row(ctx, rect, state)
	}

	if l.maxOffset() > 0 {
		l.drawScrollbar(ctx, r, rowH)
	}
	return activated
}

// Scrollbar geometry. The drawn thumb is scrollbarWidth pixels wide; the hit
// band is scrollbarHit, because a three-pixel strip is ungrabbable and
// dragging is the point of a scrollbar.
const (
	scrollbarWidth = 3
	scrollbarHit   = 8
)

// scrollbarBand is the right-edge strip of the list that answers to the
// scrollbar, or an empty rect when the list fits on screen and has none. It
// needs count and view from the current layout, so it is meaningful between
// Layout calls like everything else on the List.
func (l *List) scrollbarBand(r Rect, rowH int) Rect {
	if l.count <= 0 || l.view <= 0 || rowH <= 0 || l.maxOffset() <= 0 {
		return Rect{}
	}
	return Rect{X: r.X + r.W - scrollbarHit, Y: r.Y, W: scrollbarHit, H: l.view * rowH}
}

// drawScrollbar draws a thin proportional thumb on the right edge and drives
// it.
//
// The thumb is a control, not merely an indicator: pressing it drags the
// view, and pressing the track below or above it pages toward the press, as
// a mouse user expects of a scrollbar. The hit band is wider than the drawn
// thumb so the strip is grabbable, and a gesture that begins there is
// excluded from row selection by [List.Layout].
func (l *List) drawScrollbar(ctx *Context, r Rect, rowH int) {
	if l.count <= 0 || l.view <= 0 || rowH <= 0 {
		l.dragging = false
		return
	}

	maxOff := l.maxOffset()
	trackH := l.view * rowH
	thumbH := max(rowH/2, trackH*l.view/l.count)
	span := trackH - thumbH
	thumbY := r.Y
	if maxOff > 0 && span > 0 {
		thumbY += span * l.Offset / maxOff
	}
	track := Rect{X: r.X + r.W - scrollbarHit, Y: r.Y, W: scrollbarHit, H: trackH}
	thumb := Rect{X: track.X + track.W - scrollbarWidth, Y: thumbY, W: scrollbarWidth, H: thumbH}

	// A release ends any in-flight drag whether or not it lands on the bar:
	// the button going up is the gesture ending, and the next drag starts afresh.
	if ctx.Input.Released {
		l.dragging = false
	}

	switch {
	case l.dragging && ctx.Input.Down:
		// The thumb follows the pointer, pinned under wherever it was grabbed
		// and climbing nothing past either end of the track.
		top := ctx.Input.Mouse.Y - l.dragGrab - r.Y
		l.Offset = clampInt((top*maxOff+span/2)/span, 0, maxOff)

	case ctx.Input.PressedIn(track):
		switch {
		case ctx.Input.Mouse.Y < thumbY || ctx.Input.Mouse.Y >= thumbY+thumbH:
			// A press on the track pages toward the press: one page is what a
			// mouse user expects of the strip around the thumb, and it keeps
			// the anchor row on screen.
			delta := l.view - 1
			if ctx.Input.Mouse.Y < thumbY {
				delta = -delta
			}
			l.Scroll(delta)

		default:
			// Grabbing the thumb pins the pointer's offset inside it so it
			// does not jump its head under the cursor on pickup.
			l.dragging = true
			l.dragGrab = ctx.Input.Mouse.Y - thumbY
		}
	}

	col := ctx.Theme.BorderStrong
	if l.dragging || ctx.Input.Hovering(track) {
		// The same accent the rest of the UI uses for a hovered control, so
		// the bar reads as something alive and grabbable.
		col = ctx.Theme.AccentHover
	}
	if thumbH > 0 {
		ctx.Canvas.FillRounded(thumb, scrollbarWidth/2, col)
	}
}

// maxOffset is the largest first-visible-row index that still fills the view.
func (l *List) maxOffset() int {
	if l.count <= l.view {
		return 0
	}
	return l.count - l.view
}

// spinnerDots is the number of dots in the busy indicator, and spinnerStep how
// long each one is lit.
const (
	spinnerDots = 8
	spinnerStep = 110 * time.Millisecond
)

// Spinner draws an indeterminate busy indicator centred in r.
//
// It is a ring of fading dots rather than a rotating arc because the canvas
// has no rotation and a bitmap arc at eight orientations would be eight
// bitmaps. The phase comes from the frame's clock, so it also tells the shell
// to keep repainting: an indicator that has stopped moving says the process
// has hung.
func Spinner(ctx *Context, r Rect, col color.RGBA) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	ctx.RepaintAfter(spinnerStep)
	col = or(col, ctx.Theme.TextMuted)

	size := min(r.W, r.H)
	dot := max(2, size/7)
	radius := float64(size-dot)/2 - 1
	if radius <= 0 {
		return
	}
	cx := float64(r.X) + float64(r.W)/2
	cy := float64(r.Y) + float64(r.H)/2

	phase := 0
	if !ctx.Input.Now.IsZero() {
		phase = int(ctx.Input.Now.UnixNano()/int64(spinnerStep)) % spinnerDots
	}
	for i := 0; i < spinnerDots; i++ {
		angle := 2 * math.Pi * float64(i) / spinnerDots
		// The lit dot leads and the tail fades behind it, which reads as
		// motion in one direction.
		behind := (phase - i + spinnerDots) % spinnerDots
		alpha := 255 - behind*(200/spinnerDots)
		shade := col
		shade.A = scaleAlpha(col.A, float64(alpha)/255)
		ctx.Canvas.FillRounded(Rect{
			X: int(cx + radius*math.Sin(angle) - float64(dot)/2),
			Y: int(cy - radius*math.Cos(angle) - float64(dot)/2),
			W: dot,
			H: dot,
		}, dot/2, shade)
	}
}

// Panel fills a rounded surface with a border: the container every screen's
// content sits in.
func Panel(ctx *Context, r Rect) {
	ctx.Canvas.FillRounded(r, ctx.Theme.Radius, ctx.Theme.Surface)
	ctx.Canvas.StrokeRounded(r, ctx.Theme.Radius, ctx.Theme.BorderWidth, ctx.Theme.Border)
}

// Divider draws a hairline across the top of r.
func Divider(ctx *Context, r Rect) {
	ctx.Canvas.Fill(Rect{X: r.X, Y: r.Y, W: r.W, H: 1}, ctx.Theme.Border)
}

// DividerLabel draws a hairline through the middle of r with a word knocked
// out of it, for the "or" between two alternative actions.
//
// The word is knocked out rather than drawn over the line because a rule
// running through the middle of five-pixel-tall glyphs makes them unreadable.
func DividerLabel(ctx *Context, r Rect, text string) {
	th := ctx.Theme
	mid := r.Y + r.H/2
	ctx.Canvas.Fill(Rect{X: r.X, Y: mid, W: r.W, H: 1}, th.Border)
	if text == "" {
		return
	}
	w := TextWidth(text, th.Small, th.Font) + 2*th.Gap
	box := Rect{X: r.X + (r.W-w)/2, Y: r.Y, W: w, H: r.H}
	ctx.Canvas.Fill(box, th.Background)
	Label(ctx, box, text, LabelStyle{
		Color: th.TextMuted, Scale: th.Small, Align: AlignCenter, Middle: true,
	})
}

// Dot draws a small filled status marker, vertically centred in r at its left
// edge, and returns the width it consumed including the following gap.
func Dot(ctx *Context, r Rect, col color.RGBA) int {
	size := max(4, ctx.Theme.Body*4)
	ctx.Canvas.FillRounded(Rect{
		X: r.X,
		Y: r.Y + (r.H-size)/2,
		W: size,
		H: size,
	}, size/2, col)
	return size + ctx.Theme.Gap/2
}

// CheckMark draws a tick centred in r, for a status line that reports a
// confirmed or completed state. The tick is two strokes with the same geometry
// as the checkbox's check — the font has no dingbats, and an "x" reads as
// "clear this", the opposite of what the state means.
func CheckMark(ctx *Context, r Rect, col color.RGBA) {
	size := min(r.W, r.H)
	if size <= 0 {
		return
	}
	col = or(col, ctx.Theme.Success)
	stroke := max(1, ctx.Theme.Body)
	x0, y0 := r.X+(r.W-size)/2, r.Y+(r.H-size)/2
	ctx.Canvas.Line(x0+size/4, y0+size/2, x0+size/2-stroke/2, y0+size-size/3, stroke, col)
	ctx.Canvas.Line(x0+size/2-stroke/2, y0+size-size/3, x0+size-size/4, y0+size/4, stroke, col)
}

// BannerKind selects a banner's colour.
type BannerKind int

// The banner kinds.
const (
	// BannerError reports something that failed.
	BannerError BannerKind = iota
	// BannerInfo reports something that happened.
	BannerInfo
)

// Banner draws a message strip in r and returns the height it needed.
//
// It is measured and drawn in one pass because the height depends on how the
// text wraps, and a screen that had to ask twice would either lay out against
// a stale height or wrap the text twice.
func Banner(ctx *Context, r Rect, kind BannerKind, text string) int {
	if text == "" || r.W <= 0 {
		return 0
	}
	th := ctx.Theme
	fill, ink := th.SurfaceAlt, th.TextMuted
	if kind == BannerError {
		fill, ink = th.DangerSurface, th.Danger
	}

	scale := th.Body
	inner := InsetXY(r, th.Gap, th.Gap/2)
	lines := Wrap(text, scale, th.Font, inner.W)
	height := len(lines)*LineHeight(scale, th.Font) - (LineHeight(scale, th.Font) - TextHeight(scale, th.Font)) + th.Gap

	box := Rect{X: r.X, Y: r.Y, W: r.W, H: height}
	ctx.Canvas.FillRounded(box, th.Radius, fill)
	Label(ctx, InsetXY(box, th.Gap, th.Gap/2), text, LabelStyle{
		Color: ink,
		Scale: scale,
		Wrap:  true,
	})
	return height
}
