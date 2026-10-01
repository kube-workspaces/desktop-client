// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"math"
	"time"
)

// RefreshButton is a quiet circular-arrow action that rotates while refreshing.
// Even a fast request gets one visible turn, without delaying its result.
type RefreshButton struct {
	ID         FocusID
	Started    time.Time
	Refreshing bool
}

// Layout draws the icon and retains ordinary button mouse and keyboard behavior.
func (b RefreshButton) Layout(ctx *Context, r Rect) bool {
	button := Button{ID: b.ID, Variant: ButtonQuiet}
	activated := button.Layout(ctx, r)
	const turn = 600 * time.Millisecond
	elapsed := max(time.Duration(0), ctx.Input.Now.Sub(b.Started))
	angle := 0.0
	ink := ctx.Theme.Text
	if !b.Started.IsZero() && (b.Refreshing || elapsed < turn) {
		angle = 2 * math.Pi * float64(elapsed%turn) / float64(turn)
		ink = ctx.Theme.Accent
		ctx.RepaintAfter(time.Second / 30)
	}

	// Rasterise a rounded stroke with coverage antialiasing so the small icon
	// stays clean at rest and at arbitrary rotation angles on every UI scale.
	size := float64(min(r.W, r.H)) * 0.52
	radius := size / 2
	if radius <= 0 {
		return activated
	}
	cx, cy := float64(r.X)+float64(r.W)/2, float64(r.Y)+float64(r.H)/2
	type point struct{ x, y float64 }
	points := make([]point, 0, 51)
	const steps = 48
	start := -math.Pi / 2
	end := start + 5*math.Pi/3
	for i := 0; i <= steps; i++ {
		a := start + (end-start)*float64(i)/steps + angle
		points = append(points, point{cx + radius*math.Cos(a), cy + radius*math.Sin(a)})
	}
	arcEnd := points[len(points)-1]
	a := end + angle
	// The arrow follows the clockwise tangent at the end of the arc.
	tx, ty := -math.Sin(a), math.Cos(a)
	nx, ny := math.Cos(a), math.Sin(a)
	head := size * 0.26
	// Carry the tip beyond the curve so the inner wing does not merge into
	// the circular stroke. A wider head keeps both wings distinct at small sizes.
	tip := point{arcEnd.x + head*0.8*tx, arcEnd.y + head*0.8*ty}
	left := point{tip.x - head*tx + head*0.8*nx, tip.y - head*ty + head*0.8*ny}
	right := point{tip.x - head*tx - head*0.8*nx, tip.y - head*ty - head*0.8*ny}
	points = append(points, tip, left, tip, right)
	stroke := math.Max(1.5, size*0.095)
	ctx.Canvas.PushClip(r)
	defer ctx.Canvas.PopClip()
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			distance := math.Inf(1)
			for i := 1; i < len(points); i++ {
				p, q := points[i-1], points[i]
				dx, dy := q.x-p.x, q.y-p.y
				t := max(0.0, min(1.0, ((px-p.x)*dx+(py-p.y)*dy)/(dx*dx+dy*dy)))
				distance = math.Min(distance, math.Hypot(px-p.x-t*dx, py-p.y-t*dy))
			}
			coverage := max(0.0, min(1.0, stroke/2+0.5-distance))
			if coverage > 0 {
				ctx.Canvas.plot(x, y, ink, scaleAlpha(ink.A, coverage))
			}
		}
	}
	return activated
}
