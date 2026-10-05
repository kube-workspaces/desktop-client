// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import "math"

// CloseButton is a circular, softly highlighted button with a font-independent X.
// It shares ordinary button hover, press, focus and keyboard activation.
type CloseButton struct {
	ID FocusID
}

func (b CloseButton) Layout(ctx *Context, r Rect) bool {
	button := Button{ID: b.ID, Variant: ButtonSecondary, Smooth: true, Round: true}
	activated := button.Layout(ctx, r)
	ink := ctx.Theme.Text
	size := float64(min(r.W, r.H))
	if size <= 0 {
		return activated
	}
	cx, cy := float64(r.X)+float64(r.W)/2, float64(r.Y)+float64(r.H)/2
	half, stroke := size*0.16, math.Max(1.5, size*0.055)
	ctx.Canvas.PushClip(r)
	defer ctx.Canvas.PopClip()
	// Coverage-rasterised round strokes keep the small X smooth at all scales.
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			px, py := float64(x)+0.5-cx, float64(y)+0.5-cy
			distance := math.Inf(1)
			for _, direction := range []float64{-1, 1} {
				t := max(-half, min(half, (px+direction*py)/2))
				distance = math.Min(distance, math.Hypot(px-t, py-direction*t))
			}
			coverage := max(0.0, min(1.0, stroke/2+0.5-distance))
			if coverage > 0 {
				ctx.Canvas.plot(x, y, ink, scaleAlpha(ink.A, coverage))
			}
		}
	}
	return activated
}
