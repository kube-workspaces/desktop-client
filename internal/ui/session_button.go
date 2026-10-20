// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"image/color"
	"strconv"
)

// ButtonIcon is a software-rendered action glyph.
type ButtonIcon int

const (
	ButtonIconNone ButtonIcon = iota
	ButtonIconFocus
	ButtonIconClose
	// ButtonIconPlay is the filled triangle of a start/open action.
	ButtonIconPlay
	// ButtonIconMonitor is a screen on a stand: open the graphical display.
	ButtonIconMonitor
	// ButtonIconWindow is a title-bar window: open the embedded web view.
	ButtonIconWindow
	// ButtonIconGlobe is the crossed circle of a system-browser hand-off.
	ButtonIconGlobe
	// ButtonIconEye is the lens of the shared-display observer.
	ButtonIconEye
	// ButtonIconTerminal is a console box with a prompt, for the text
	// consoles (integrated console and serial).
	ButtonIconTerminal
	// ButtonIconKey is the ring-and-teeth key of the SSH bridge.
	ButtonIconKey
	// ButtonIconUser is the head-and-shoulders of a sign-in action.
	ButtonIconUser
	// ButtonIconStop is the filled square of a stop action.
	ButtonIconStop
	// ButtonIconInfo is the dotted "i" of the details action.
	ButtonIconInfo
	// ButtonIconClock is the waiting face of a workspace that is not running.
	ButtonIconClock
)

func drawButtonIcon(c *Canvas, r Rect, icon ButtonIcon, ink color.RGBA) {
	s := max(1, r.W/10)
	// t is the stroke weight shared by the outlined glyphs: two tenths of the
	// icon stays crisp at the small menu size and scales with the HiDPI theme.
	t := max(2, 2*s)
	x, y, w, h := r.X, r.Y, r.W, r.H
	switch icon {
	case ButtonIconFocus:
		// Four corner brackets: "this window, here" without pretending to
		// be a pointer, and legible at every scale.
		arm := max(2*s, r.W*3/10)
		top, bottom := r.Y, r.Y+r.H-s
		left, right := r.X, r.X+r.W-s
		c.Fill(Rect{X: left, Y: top, W: arm, H: s}, ink)
		c.Fill(Rect{X: left, Y: top, W: s, H: arm}, ink)
		c.Fill(Rect{X: right + s - arm, Y: top, W: arm, H: s}, ink)
		c.Fill(Rect{X: right, Y: top, W: s, H: arm}, ink)
		c.Fill(Rect{X: left, Y: bottom, W: arm, H: s}, ink)
		c.Fill(Rect{X: left, Y: bottom + s - arm, W: s, H: arm}, ink)
		c.Fill(Rect{X: right + s - arm, Y: bottom, W: arm, H: s}, ink)
		c.Fill(Rect{X: right, Y: bottom + s - arm, W: s, H: arm}, ink)
	case ButtonIconClose:
		// Square pixels along both diagonals keep the cross crisp at every scale.
		for i := 2 * s; i < r.W-2*s; i++ {
			c.Fill(Rect{X: r.X + i, Y: r.Y + i, W: s, H: s}, ink)
			c.Fill(Rect{X: r.X + r.W - i - s, Y: r.Y + i, W: s, H: s}, ink)
		}
	case ButtonIconPlay:
		// Rows swept from the left edge to a right apex: the canvas has no
		// polygon fill, and one-pixel rows stay sharp at every scale.
		for i := range h {
			d := 2*i - h
			if d < 0 {
				d = -d
			}
			if span := w * (h - d) / h; span > 0 {
				c.Fill(Rect{X: x, Y: y + i, W: span, H: 1}, ink)
			}
		}
	case ButtonIconMonitor:
		screen := Rect{X: x, Y: y, W: w, H: h * 3 / 5}
		c.StrokeRounded(screen, max(1, w/7), t, ink)
		c.Line(x+w/2, y+screen.H, x+w/2, y+h-t, t, ink)
		c.Line(x+w/4, y+h-t, x+w*3/4, y+h-t, t, ink)
	case ButtonIconWindow:
		c.StrokeRounded(r, max(1, w/7), t, ink)
		c.Fill(Rect{X: x + t, Y: y + t, W: w - 2*t, H: max(2, h/5)}, ink)
	case ButtonIconGlobe:
		c.StrokeRounded(r, w/2, t, ink)
		c.Line(x, y+h/2, x+w, y+h/2, t, ink)
		c.Line(x+w/2, y, x+w/2, y+h, t, ink)
	case ButtonIconEye:
		lens := Rect{X: x, Y: y + h/4, W: w, H: h / 2}
		c.StrokeRounded(lens, lens.H/2, t, ink)
		pupil := max(2, w/4)
		c.FillRounded(Rect{X: x + (w-pupil)/2, Y: y + (h-pupil)/2, W: pupil, H: pupil}, pupil/2, ink)
	case ButtonIconTerminal:
		c.StrokeRounded(r, max(1, w/7), t, ink)
		pad, kw := max(2, w/6), max(3, w/4)
		c.Line(x+pad, y+h/3, x+pad+kw, y+h/2, t, ink)
		c.Line(x+pad+kw, y+h/2, x+pad, y+h*2/3, t, ink)
		c.Fill(Rect{X: x + w - pad - kw/2, Y: y + h*2/3 - t/2, W: kw / 2, H: t}, ink)
	case ButtonIconKey:
		d := w / 2
		c.StrokeRounded(Rect{X: x, Y: y + (h-d)/2, W: d, H: d}, d/2, t, ink)
		cy := y + h/2
		c.Line(x+d, cy, x+w-t/2, cy, t, ink)
		c.Line(x+d+(w-d)/2, cy, x+d+(w-d)/2, cy+h/4, t, ink)
		c.Line(x+d+(w-d)*4/5, cy, x+d+(w-d)*4/5, cy+h/4, t, ink)
	case ButtonIconUser:
		head := Rect{X: x + w/3, Y: y, W: w / 3, H: h / 3}
		c.FillRounded(head, w/6, ink)
		c.FillRounded(Rect{X: x + w/6, Y: y + h/2, W: w * 2 / 3, H: h / 2}, w/4, ink)
	case ButtonIconStop:
		c.FillRounded(Rect{X: x + w/6, Y: y + h/6, W: w * 2 / 3, H: h * 2 / 3}, max(1, w/8), ink)
	case ButtonIconInfo:
		c.StrokeRounded(r, w/2, t, ink)
		d := max(2, w/5)
		c.FillRounded(Rect{X: x + (w-d)/2, Y: y + h/5, W: d, H: d}, d/2, ink)
		stem := max(2, t)
		c.Fill(Rect{X: x + (w-stem)/2, Y: y + h*2/5, W: stem, H: h * 2 / 5}, ink)
	case ButtonIconClock:
		c.StrokeRounded(r, w/2, t, ink)
		cx, cy := x+w/2, y+h/2
		c.Line(cx, cy, cx, cy-h/3, t, ink)
		c.Line(cx, cy, cx+w/4, cy, t, ink)
	}
}

// SessionsButton pairs stacked windows with an accent count badge, matching
// the quiet icon-only Add workspace action beside it.
type SessionsButton struct {
	ID    FocusID
	Count int
}

func (b SessionsButton) Layout(ctx *Context, r Rect) bool {
	button := Button{ID: b.ID, Variant: ButtonQuiet}
	activated := button.Layout(ctx, r)
	size := min(r.W, r.H)
	if size <= 0 {
		return activated
	}
	th := ctx.Theme
	ctx.Canvas.PushClip(r)
	defer ctx.Canvas.PopClip()
	s := max(1, size/20)

	// The stacked windows sit low and left, leaving the top-right corner to
	// the badge so the two read as one glyph rather than as a pile-up.
	back := Rect{X: r.X + size*10/100, Y: r.Y + size*30/100, W: size * 48 / 100, H: size * 48 / 100}
	front := Rect{X: back.X + size*12/100, Y: back.Y + size*14/100, W: back.W, H: back.H}
	ctx.Canvas.StrokeRounded(back, 2*s, s, th.Text)
	ctx.Canvas.FillRounded(front, 2*s, th.Background)
	ctx.Canvas.StrokeRounded(front, 2*s, s, th.Text)

	if b.Count > 0 {
		text := strconv.Itoa(b.Count)
		if b.Count > 99 {
			text = "99+"
		}
		// Inset past the focus ring so a focused Sessions button still
		// shows its whole outline.
		inset := th.FocusWidth + 1
		h := TextHeight(th.Small, th.Font) + 2*s
		w := max(h, TextWidth(text, th.Small, th.Font)+3*s)
		badge := Rect{X: r.X + r.W - w - inset, Y: r.Y + inset, W: w, H: h}
		ctx.Canvas.FillRounded(badge, h/2, th.Accent)
		Label(ctx, badge, text, LabelStyle{Color: th.TextOnAccent, Scale: th.Small, Align: AlignCenter, Middle: true})
	}
	return activated
}
