// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"image/color"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

// Toggle is a labelled switch. The entire row is a click target; keyboard
// activation follows buttons. Checked changes only when activation succeeds.
type Toggle struct {
	ID       FocusID
	Label    string
	State    string
	Checked  bool
	Disabled bool
	Busy     bool
}

// Layout draws a soft settings row with a trailing capsule switch.
func (t *Toggle) Layout(ctx *Context, r Rect) bool {
	th := ctx.Theme
	focused := false
	if !t.Disabled {
		focused = ctx.register(t.ID, r)
	}
	hovered := !t.Disabled && ctx.Input.Hovering(r)
	toggled := !t.Disabled && (ctx.Input.ClickedIn(r) || focused &&
		(ctx.Input.KeyPressed(keysym.KeyReturn) || ctx.Input.KeyPressed(keysym.KeyKPEnter) || ctx.Input.RuneChord(keysym.ModNone, ' ')))
	if toggled {
		t.Checked = !t.Checked
	}
	radius := max(th.Radius, th.ControlHeight/3)
	fill, border := th.Surface, th.Border
	if hovered {
		fill, border = th.SurfaceAlt, th.BorderStrong
	}
	ctx.Canvas.FillRounded(r, radius, fill)
	ctx.Canvas.StrokeRounded(r, radius, th.BorderWidth, border)
	if focused {
		ctx.Canvas.StrokeRounded(r, radius, th.FocusWidth, th.Focus)
	}
	inner := InsetXY(r, th.Pad, 0)
	h := min(inner.H, max(18, th.ControlHeight*3/5))
	w := h * 7 / 4
	track, labels := CutRight(inner, w)
	track.Y += (track.H - h) / 2
	track.H = h
	stateWidth := min(labels.W/2, TextWidth(t.State, th.Small, th.Font)+2*th.Gap)
	state, label := CutRight(labels, stateWidth)
	ink := th.Text
	if t.Disabled {
		ink = th.TextDisabled
	}
	Label(ctx, label, t.Label, LabelStyle{Color: ink, Middle: true})
	Label(ctx, state, t.State, LabelStyle{Color: th.TextMuted, Scale: th.Small, Middle: true, Align: AlignCenter})
	trackFill, trackBorder := th.BorderStrong, th.BorderStrong
	if t.Checked {
		trackFill, trackBorder = th.Accent, th.Accent
		if hovered {
			trackFill = th.AccentHover
		}
	}
	if t.Disabled {
		trackFill, trackBorder = th.SurfaceAlt, th.Border
	}
	ctx.Canvas.FillRounded(track, h/2, trackFill)
	ctx.Canvas.StrokeRounded(track, h/2, th.BorderWidth, trackBorder)
	pad := max(2, h/8)
	knob := Rect{X: track.X + pad, Y: track.Y + pad, W: h - 2*pad, H: h - 2*pad}
	if t.Checked {
		knob.X = track.X + track.W - pad - knob.W
	}
	ctx.Canvas.FillRounded(Rect{X: knob.X, Y: knob.Y + max(1, th.Body/2), W: knob.W, H: knob.H}, knob.H/2, buttonShadow)
	knobInk := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	if t.Disabled {
		knobInk = th.TextDisabled
	}
	ctx.Canvas.FillRounded(knob, knob.H/2, knobInk)
	if t.Busy {
		Spinner(ctx, Inset(knob, pad), th.TextMuted)
	}
	return toggled
}
