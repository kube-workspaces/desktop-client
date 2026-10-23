// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"math"

	"github.com/kube-workspaces/desktop-client/internal/connection"
)

// snapWindow shrinks or expands the content viewport to the whole displayed
// desktop, retaining the current scale and leaving the guest resolution alone.
// SetSize takes window coordinates; presentation rectangles use drawable pixels.
// Chrome adds its own height after that conversion.
func (p *presentation) snapWindow(be Backend, srcW, srcH, dstW, dstH int) error {
	r := p.rectangle(srcW, srcH, dstW, dstH)
	if r.Empty() || be.Fullscreen() {
		return nil
	}
	// Fill rounds outward; an already snug viewport may therefore differ by
	// one pixel. Do not grow it by that rounding pixel on every Snap click.
	if p.modeName() == "fill" && r.W-dstW <= 1 && r.H-dstH <= 1 {
		p.panX, p.panY = 0, 0
		return nil
	}
	scale := be.ScaleFactor()
	if scale <= 0 {
		scale = 1
	}
	w := max(1, int(math.Round(float64(r.W)/scale)))
	h := max(1, int(math.Round(float64(r.H)/scale)))
	if err := be.SetSize(w, h); err != nil {
		return err
	}
	p.panX, p.panY = 0, 0
	return nil
}

// presentation is client-only geometry in drawable pixels. The exact same
// rectangle drives rendering and MapToSource, including cropped/negative edges.
// Its zero value preserves the established aspect-preserving fit policy.
type presentation struct {
	mode       string
	zoom       int
	panX, panY int
}

func (p *presentation) modeName() string {
	if p.mode == "" {
		return "fit"
	}
	return p.mode
}

func (p *presentation) rectangle(srcW, srcH, dstW, dstH int) Rect {
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return Rect{}
	}
	var w, h int
	switch p.modeName() {
	case "zoom":
		w, h = max(1, srcW*p.zoom/100), max(1, srcH*p.zoom/100)
	case "fill":
		// Round up so fill never leaves a one-pixel uncovered edge. Keep the
		// source aspect ratio; fill crops instead of stretching the desktop.
		w, h = dstW, (srcH*dstW+srcW-1)/srcW
		if h < dstH {
			w, h = (srcW*dstH+srcH-1)/srcH, dstH
		}
	default:
		p.panX, p.panY = 0, 0
		return FitLetterbox(srcW, srcH, dstW, dstH)
	}
	x, y := (dstW-w)/2, (dstH-h)/2
	// Pan is a displacement from centre, clamped so it never exposes bars
	// along an overflowing axis. Smaller images remain centred.
	if w > dstW {
		x = clamp(x+p.panX, dstW-w, 0)
		p.panX = x - (dstW-w)/2
	} else {
		p.panX = 0
	}
	if h > dstH {
		y = clamp(y+p.panY, dstH-h, 0)
		p.panY = y - (dstH-h)/2
	} else {
		p.panY = 0
	}
	return Rect{X: x, Y: y, W: w, H: h}
}

// action returns whether this is a local presentation command. Selecting a
// scale policy opts out of automatic guest resizing; panning does not change it.
func (p *presentation) action(a connection.Action, srcW, srcH, dstW, dstH int) bool {
	switch a {
	case connection.FitDesktop:
		*p = presentation{}
	case connection.ActualPixels:
		*p = presentation{mode: "zoom", zoom: 100}
	case connection.FillDesktop:
		*p = presentation{mode: "fill"}
	case connection.ZoomIn, connection.ZoomOut:
		r := p.rectangle(srcW, srcH, dstW, dstH)
		zoom := 100
		if p.modeName() == "zoom" {
			zoom = p.zoom
		} else if srcW > 0 && r.W > 0 {
			zoom = r.W * 100 / srcW
		}
		if a == connection.ZoomIn {
			zoom = (zoom/25 + 1) * 25
		} else {
			zoom = ((zoom - 1) / 25) * 25
		}
		p.mode, p.zoom = "zoom", clamp(zoom, 25, 400)
	case connection.PanLeft:
		p.panX += max(1, dstW/4)
	case connection.PanRight:
		p.panX -= max(1, dstW/4)
	case connection.PanUp:
		p.panY += max(1, dstH/4)
	case connection.PanDown:
		p.panY -= max(1, dstH/4)
	case connection.CenterDesktop:
		p.panX, p.panY = 0, 0
	default:
		return false
	}
	return true
}

func selectsPresentation(a connection.Action) bool {
	return a == connection.FitDesktop || a == connection.ActualPixels || a == connection.FillDesktop || a == connection.ZoomIn || a == connection.ZoomOut
}
