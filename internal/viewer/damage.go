// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import "github.com/kube-workspaces/desktop-client/internal/rfb"

// Upload planning tuning.
//
// A texture upload has a large fixed cost (a driver call, a possible pipeline
// stall) and a small per-pixel cost, so the planner trades pixels for calls:
// it is worth re-uploading some clean pixels to avoid a call, but not worth
// re-uploading the whole screen to avoid a few.
const (
	// defaultMaxUploadRects is the point past which a frame's damage list is
	// collapsed into its bounding box. A Tight-encoded frame from QEMU can
	// carry hundreds of small rectangles; issuing hundreds of uploads costs
	// far more than one larger one.
	defaultMaxUploadRects = 32

	// fullFrameNumer/fullFrameDenom express the fraction of the framebuffer
	// that, once damaged, makes a single full-frame upload the cheaper option.
	// At three quarters, the bounding box of the damage almost always covers
	// the whole screen anyway.
	fullFrameNumer = 3
	fullFrameDenom = 4
)

// box is a signed working rectangle. The protocol's uint16 fields cannot
// represent an intermediate negative value, and clipping arithmetic wants to.
type box struct {
	x0, y0, x1, y1 int // half-open: [x0,x1) x [y0,y1)
}

func boxOf(r rfb.Rect) box {
	return box{int(r.X), int(r.Y), int(r.X) + int(r.Width), int(r.Y) + int(r.Height)}
}

func (b box) empty() bool { return b.x1 <= b.x0 || b.y1 <= b.y0 }

func (b box) area() int {
	if b.empty() {
		return 0
	}
	return (b.x1 - b.x0) * (b.y1 - b.y0)
}

func (b box) clip(w, h int) box {
	if b.x0 < 0 {
		b.x0 = 0
	}
	if b.y0 < 0 {
		b.y0 = 0
	}
	if b.x1 > w {
		b.x1 = w
	}
	if b.y1 > h {
		b.y1 = h
	}
	return b
}

func (b box) union(o box) box {
	return box{min(b.x0, o.x0), min(b.y0, o.y0), max(b.x1, o.x1), max(b.y1, o.y1)}
}

func (b box) rect() rfb.Rect {
	return rfb.Rect{
		X:      uint16(b.x0),
		Y:      uint16(b.y0),
		Width:  uint16(b.x1 - b.x0),
		Height: uint16(b.y1 - b.y0),
	}
}

// planUploads turns the damage rectangles of one framebuffer update into the
// list of sub-rectangles the renderer should actually upload.
//
// It clips to the framebuffer, drops empty rectangles, merges pairs where the
// merged upload is no larger than the two separate ones (which covers the
// common cases of duplicate, nested and overlapping damage), and falls back to
// a single rectangle when the damage is either too fragmented or too large for
// partial uploads to be worthwhile.
//
// The result always covers every damaged pixel: dropping damage shows stale
// pixels, which is far worse than uploading a few clean ones.
func planUploads(damage []rfb.Rect, w, h, maxRects int) []rfb.Rect {
	if w <= 0 || h <= 0 || len(damage) == 0 {
		return nil
	}
	if maxRects <= 0 {
		maxRects = defaultMaxUploadRects
	}

	boxes := make([]box, 0, len(damage))
	for _, d := range damage {
		b := boxOf(d).clip(w, h)
		if !b.empty() {
			boxes = append(boxes, b)
		}
	}
	if len(boxes) == 0 {
		return nil
	}

	boxes = mergeBoxes(boxes)

	// Too many pieces: one bounding-box upload beats a long tail of tiny ones.
	if len(boxes) > maxRects {
		boxes = []box{boundingBox(boxes)}
	}

	total := 0
	for _, b := range boxes {
		total += b.area()
	}
	// Enough of the screen changed that partial uploads have stopped paying
	// for themselves; upload the whole frame in one call.
	if total*fullFrameDenom >= w*h*fullFrameNumer {
		boxes = []box{{0, 0, w, h}}
	}

	out := make([]rfb.Rect, 0, len(boxes))
	for _, b := range boxes {
		out = append(out, b.rect())
	}
	return out
}

// mergeBoxes repeatedly merges any pair whose union is no larger than the sum
// of the pair, which is exactly the condition under which merging cannot cost
// extra uploaded pixels. It is O(n^2) per pass over a list that is small by
// construction (planUploads collapses anything longer than maxRects), and it
// runs to a fixed point so the result does not depend on input order.
func mergeBoxes(boxes []box) []box {
	for merged := true; merged; {
		merged = false
		for i := 0; i < len(boxes); i++ {
			for j := i + 1; j < len(boxes); j++ {
				u := boxes[i].union(boxes[j])
				if u.area() > boxes[i].area()+boxes[j].area() {
					continue
				}
				boxes[i] = u
				boxes = append(boxes[:j], boxes[j+1:]...)
				merged = true
				j--
			}
		}
	}
	return boxes
}

func boundingBox(boxes []box) box {
	b := boxes[0]
	for _, o := range boxes[1:] {
		b = b.union(o)
	}
	return b
}
