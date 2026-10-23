// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"image/color"
	"math"
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
	ButtonIconExpand
	ButtonIconCollapse
	ButtonIconSliders
	ButtonIconScale
	ButtonIconResolution
	ButtonIconBug
	ButtonIconPin
	ButtonIconPower
	ButtonIconKeyboard
	ButtonIconClipboard
	ButtonIconSpeaker
	ButtonIconSpeakerMuted
	ButtonIconResize
	ButtonIconPointer
	ButtonIconFit
	ButtonIconFill
	ButtonIconSnap
	ButtonIconCenter
	ButtonIconZoomIn
	ButtonIconZoomOut
	ButtonIconArrowLeft
	ButtonIconArrowRight
	ButtonIconArrowUp
	ButtonIconArrowDown
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
	case ButtonIconExpand:
		arm, inset := max(2*s, w*3/10), 2*s
		c.Fill(Rect{X: x + inset, Y: y + inset, W: arm, H: t}, ink)
		c.Fill(Rect{X: x + inset, Y: y + inset, W: t, H: arm}, ink)
		c.Fill(Rect{X: x + w - inset - arm, Y: y + inset, W: arm, H: t}, ink)
		c.Fill(Rect{X: x + w - inset - t, Y: y + inset, W: t, H: arm}, ink)
		c.Fill(Rect{X: x + inset, Y: y + h - inset - t, W: arm, H: t}, ink)
		c.Fill(Rect{X: x + inset, Y: y + h - inset - arm, W: t, H: arm}, ink)
		c.Fill(Rect{X: x + w - inset - arm, Y: y + h - inset - t, W: arm, H: t}, ink)
		c.Fill(Rect{X: x + w - inset - t, Y: y + h - inset - arm, W: t, H: arm}, ink)
	case ButtonIconCollapse:
		arm, inset := max(2*s, w*3/10), w/4
		c.Fill(Rect{X: x + inset, Y: y + inset, W: arm, H: t}, ink)
		c.Fill(Rect{X: x + inset, Y: y + inset, W: t, H: arm}, ink)
		c.Fill(Rect{X: x + w - inset - arm, Y: y + inset, W: arm, H: t}, ink)
		c.Fill(Rect{X: x + w - inset - t, Y: y + inset, W: t, H: arm}, ink)
		c.Fill(Rect{X: x + inset, Y: y + h - inset - t, W: arm, H: t}, ink)
		c.Fill(Rect{X: x + inset, Y: y + h - inset - arm, W: t, H: arm}, ink)
		c.Fill(Rect{X: x + w - inset - arm, Y: y + h - inset - t, W: arm, H: t}, ink)
		c.Fill(Rect{X: x + w - inset - t, Y: y + h - inset - arm, W: t, H: arm}, ink)
	case ButtonIconSliders:
		for i := range 3 {
			cy := y + h*(2*i+1)/6
			c.Line(x+2*s, cy, x+w-2*s, cy, t, ink)
			kx := x + w*(i*3+2)/10
			c.FillRounded(Rect{X: kx - t, Y: cy - h/8, W: 2 * t, H: h / 4}, t, ink)
		}
	case ButtonIconScale:
		d := w * 3 / 5
		c.StrokeRounded(Rect{X: x, Y: y, W: d, H: d}, d/2, t, ink)
		c.Line(x+d-t/2, y+d-t/2, x+w-t, y+h-t, t, ink)
	case ButtonIconResolution:
		screen := Rect{X: x, Y: y, W: w, H: h * 3 / 4}
		c.StrokeRounded(screen, max(1, w/8), t, ink)
		c.Line(x+w/3, y+2*t, x+w/3, y+screen.H-2*t, max(1, t/2), ink)
		c.Line(x+w*2/3, y+2*t, x+w*2/3, y+screen.H-2*t, max(1, t/2), ink)
		c.Line(x+2*t, y+screen.H/2, x+w-2*t, y+screen.H/2, max(1, t/2), ink)
	case ButtonIconBug:
		body := Rect{X: x + w/5, Y: y + h/4, W: w * 3 / 5, H: h * 3 / 5}
		c.FillRounded(body, w/6, ink)
		c.Line(x+w/4, y+h*3/4, x+w/8, y+h-t, t, ink)
		c.Line(x+w*3/4, y+h*3/4, x+w*7/8, y+h-t, t, ink)
		c.Line(x+w/3, y+h/4, x+w/4, y+2*s, t, ink)
		c.Line(x+w*2/3, y+h/4, x+w*3/4, y+2*s, t, ink)
		c.Line(x+w/5, y+h/2, x, y+h*2/5, t, ink)
		c.Line(x+w*4/5, y+h/2, x+w, y+h*2/5, t, ink)
	case ButtonIconPin:
		cx := x + w/2
		c.FillRounded(Rect{X: x + w/3, Y: y + 2*s, W: w / 3, H: h / 5}, max(1, w/10), ink)
		c.Line(cx, y+2*s+h/5, cx, y+h*3/5, t, ink)
		c.Line(x+w/5, y+h*3/5, x+w*4/5, y+h*3/5, t, ink)
		c.Line(cx, y+h*3/5, cx, y+h-2*s, t, ink)
	case ButtonIconPower:
		cx, cy := x+w/2, y+h/2
		radius, gap := float64(w/2-2*s), 0.55
		steps := 18
		for i := range steps {
			a0 := -math.Pi/2 + gap + (2*math.Pi-2*gap)*float64(i)/float64(steps)
			a1 := -math.Pi/2 + gap + (2*math.Pi-2*gap)*float64(i+1)/float64(steps)
			c.Line(
				cx+int(math.Cos(a0)*radius), cy+int(math.Sin(a0)*radius),
				cx+int(math.Cos(a1)*radius), cy+int(math.Sin(a1)*radius),
				t, ink,
			)
		}
		c.Line(cx, y+2*s, cx, cy-max(1, h/6), t, ink)
	case ButtonIconKeyboard:
		c.StrokeRounded(r, max(1, w/8), t, ink)
		pad := max(2, w/7)
		for row := range 2 {
			ky := y + pad + row*(h-3*pad)/2
			for col := range 4 {
				kx := x + pad + col*(w-2*pad)/4
				c.Fill(Rect{X: kx, Y: ky, W: max(1, w/10), H: max(1, h/12)}, ink)
			}
		}
		c.Fill(Rect{X: x + pad, Y: y + h - pad - max(1, h/12), W: w - 2*pad, H: max(1, h/12)}, ink)
	case ButtonIconClipboard:
		c.StrokeRounded(Rect{X: x + w/6, Y: y + h/6, W: w * 2 / 3, H: h * 4 / 5}, max(1, w/8), t, ink)
		c.FillRounded(Rect{X: x + w/3, Y: y + 2*s, W: w / 3, H: h / 6}, max(1, w/14), ink)
	case ButtonIconSpeaker, ButtonIconSpeakerMuted:
		box := Rect{X: x + w/6, Y: y + h*2/5, W: w / 4, H: h / 5}
		c.FillRounded(box, max(1, w/16), ink)
		for i := range h * 3 / 5 {
			d := i - h*3/10
			if d < 0 {
				d = -d
			}
			span := (h*3/10 - d) * w / 4 / max(1, h*3/10)
			if span > 0 {
				c.Fill(Rect{X: x + w/4, Y: y + h/5 + i, W: span, H: 1}, ink)
			}
		}
		if icon == ButtonIconSpeakerMuted {
			c.Line(x+w/2, y+h/5, x+w*5/6, y+h*4/5, t, ink)
		} else {
			c.Line(x+w/2, y+h/3, x+w*3/5, y+h/2, t, ink)
			c.Line(x+w*3/5, y+h/2, x+w/2, y+h*2/3, t, ink)
			c.Line(x+w*2/3, y+h/4, x+w*3/4, y+h/2, t, ink)
			c.Line(x+w*3/4, y+h/2, x+w*2/3, y+h*3/4, t, ink)
		}
	case ButtonIconResize:
		screen := Rect{X: x, Y: y, W: w, H: h * 3 / 4}
		c.StrokeRounded(screen, max(1, w/8), t, ink)
		cx, cy := x+w/2, y+screen.H/2
		c.Line(cx, cy, x+3*s, y+3*s, t, ink)
		c.Line(cx, cy, x+w-3*s, y+screen.H-3*s, t, ink)
		c.Fill(Rect{X: x + 2*s, Y: y + 2*s, W: 2 * t, H: 2 * t}, ink)
		c.Fill(Rect{X: x + w - 2*s - 2*t, Y: y + screen.H - 2*s - 2*t, W: 2 * t, H: 2 * t}, ink)
	case ButtonIconPointer:
		c.FillRounded(Rect{X: x + w/3, Y: y + 2*s, W: w / 5, H: h / 2}, max(1, w/10), ink)
		c.FillRounded(Rect{X: x + w/6, Y: y + h/2, W: w * 2 / 3, H: h / 2}, max(1, w/8), ink)
	case ButtonIconFit:
		c.StrokeRounded(r, max(1, w/8), t, ink)
		inner := Rect{X: x + w/4, Y: y + h/4, W: w / 2, H: h / 2}
		c.StrokeRounded(inner, max(1, w/12), t, ink)
	case ButtonIconFill:
		c.StrokeRounded(r, max(1, w/8), t, ink)
		c.Fill(Rect{X: x + w/4, Y: y + h/4, W: w / 2, H: h / 2}, ink)
	case ButtonIconSnap:
		c.StrokeRounded(r, max(1, w/8), t, ink)
		c.FillRounded(Rect{X: x + w/3, Y: y + h/3, W: w * 2 / 3, H: h * 2 / 3}, max(1, w/12), ink)
	case ButtonIconCenter:
		c.StrokeRounded(r, w/2, t, ink)
		cx, cy := x+w/2, y+h/2
		c.Fill(Rect{X: cx - t/2, Y: cy - t/2, W: max(1, t), H: max(1, t)}, ink)
		c.Line(cx, y+2*s, cx, y+h/2-h/4, t, ink)
		c.Line(cx, y+h/2+h/4, cx, y+h-2*s, t, ink)
		c.Line(x+2*s, cy, x+w/2-w/4, cy, t, ink)
		c.Line(x+w/2+w/4, cy, x+w-2*s, cy, t, ink)
	case ButtonIconZoomIn, ButtonIconZoomOut:
		d := w * 3 / 5
		c.StrokeRounded(Rect{X: x, Y: y, W: d, H: d}, d/2, t, ink)
		c.Line(x+d-t/2, y+d-t/2, x+w-t, y+h-t, t, ink)
		cx, cy := x+d/2, y+d/2
		c.Line(cx-w/7, cy, cx+w/7, cy, t, ink)
		if icon == ButtonIconZoomIn {
			c.Line(cx, cy-w/7, cx, cy+w/7, t, ink)
		}
	case ButtonIconArrowLeft:
		drawArrowGlyph(c, r, ink, t, -1, 0)
	case ButtonIconArrowRight:
		drawArrowGlyph(c, r, ink, t, 1, 0)
	case ButtonIconArrowUp:
		drawArrowGlyph(c, r, ink, t, 0, -1)
	case ButtonIconArrowDown:
		drawArrowGlyph(c, r, ink, t, 0, 1)
	}
}

func drawArrowGlyph(c *Canvas, r Rect, ink color.RGBA, t, dx, dy int) {
	x, y, w, h := r.X, r.Y, r.W, r.H
	pad := max(2, w/6)
	cx, cy := x+w/2, y+h/2
	switch {
	case dx < 0:
		c.Line(x+pad, cy, x+w-pad, cy, t, ink)
		c.Line(x+pad, cy, x+pad+w/4, cy-h/6, t, ink)
		c.Line(x+pad, cy, x+pad+w/4, cy+h/6, t, ink)
	case dx > 0:
		c.Line(x+pad, cy, x+w-pad, cy, t, ink)
		c.Line(x+w-pad, cy, x+w-pad-w/4, cy-h/6, t, ink)
		c.Line(x+w-pad, cy, x+w-pad-w/4, cy+h/6, t, ink)
	case dy < 0:
		c.Line(cx, y+pad, cx, y+h-pad, t, ink)
		c.Line(cx, y+pad, cx-w/6, y+pad+h/4, t, ink)
		c.Line(cx, y+pad, cx+w/6, y+pad+h/4, t, ink)
	case dy > 0:
		c.Line(cx, y+pad, cx, y+h-pad, t, ink)
		c.Line(cx, y+h-pad, cx-w/6, y+h-pad-h/4, t, ink)
		c.Line(cx, y+h-pad, cx+w/6, y+h-pad-h/4, t, ink)
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
