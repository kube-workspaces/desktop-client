// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

func r(x, y, w, h int) rfb.Rect {
	return rfb.Rect{X: uint16(x), Y: uint16(y), Width: uint16(w), Height: uint16(h)}
}

// covers reports whether every pixel of want is inside some rectangle of got.
// Losing a damaged pixel leaves stale content on screen, which is the one
// failure mode the planner must never have.
func covers(got, want []rfb.Rect) bool {
	for _, w := range want {
		for y := int(w.Y); y < int(w.Y)+int(w.Height); y++ {
			for x := int(w.X); x < int(w.X)+int(w.Width); x++ {
				found := false
				for _, g := range got {
					if x >= int(g.X) && x < int(g.X)+int(g.Width) &&
						y >= int(g.Y) && y < int(g.Y)+int(g.Height) {
						found = true
						break
					}
				}
				if !found {
					return false
				}
			}
		}
	}
	return true
}

func totalArea(rects []rfb.Rect) int {
	n := 0
	for _, rc := range rects {
		n += rc.Area()
	}
	return n
}

func TestPlanUploads(t *testing.T) {
	const w, h = 800, 600

	tests := []struct {
		name   string
		damage []rfb.Rect
		want   []rfb.Rect
	}{
		{
			name:   "single rect passes through",
			damage: []rfb.Rect{r(10, 10, 100, 50)},
			want:   []rfb.Rect{r(10, 10, 100, 50)},
		},
		{
			name:   "duplicates collapse",
			damage: []rfb.Rect{r(10, 10, 100, 50), r(10, 10, 100, 50)},
			want:   []rfb.Rect{r(10, 10, 100, 50)},
		},
		{
			name:   "contained rect is absorbed",
			damage: []rfb.Rect{r(0, 0, 200, 200), r(50, 50, 10, 10)},
			want:   []rfb.Rect{r(0, 0, 200, 200)},
		},
		{
			name:   "adjacent rects merge when the union costs no more",
			damage: []rfb.Rect{r(0, 0, 100, 100), r(100, 0, 100, 100)},
			want:   []rfb.Rect{r(0, 0, 200, 100)},
		},
		{
			// Two small far-apart rectangles: merging them would upload most
			// of the screen to save one call, which is a bad trade.
			name:   "distant rects stay separate",
			damage: []rfb.Rect{r(0, 0, 20, 20), r(700, 500, 20, 20)},
			want:   []rfb.Rect{r(0, 0, 20, 20), r(700, 500, 20, 20)},
		},
		{
			name:   "out of bounds damage is clipped",
			damage: []rfb.Rect{r(700, 550, 400, 400)},
			want:   []rfb.Rect{r(700, 550, 100, 50)},
		},
		{
			name:   "empty rects are dropped",
			damage: []rfb.Rect{r(10, 10, 0, 50), r(10, 10, 100, 0), r(20, 20, 30, 30)},
			want:   []rfb.Rect{r(20, 20, 30, 30)},
		},
		{
			name:   "fully out of bounds damage disappears",
			damage: []rfb.Rect{r(900, 700, 10, 10)},
			want:   nil,
		},
		{
			name:   "no damage",
			damage: nil,
			want:   nil,
		},
		{
			// Past three quarters of the screen, one full-frame upload beats
			// any arrangement of partial ones.
			name:   "large damage becomes a full frame",
			damage: []rfb.Rect{r(0, 0, 800, 500)},
			want:   []rfb.Rect{r(0, 0, 800, 600)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planUploads(tt.damage, w, h, 0)
			if !rectsEqual(got, tt.want) {
				t.Fatalf("planUploads(%v) = %v, want %v", tt.damage, got, tt.want)
			}
			if !covers(got, clipAll(tt.damage, w, h)) {
				t.Fatalf("planUploads(%v) = %v, which does not cover the damage", tt.damage, got)
			}
		})
	}
}

// TestPlanUploadsCollapsesFragmentedDamage covers the Tight case: QEMU splits
// an update into many small rectangles, and issuing one driver call per
// rectangle costs far more than the pixels saved.
func TestPlanUploadsCollapsesFragmentedDamage(t *testing.T) {
	const w, h = 1920, 1080
	var damage []rfb.Rect
	for i := 0; i < 64; i++ {
		damage = append(damage, r(i*16, i*8, 8, 8))
	}
	got := planUploads(damage, w, h, 32)
	if len(got) != 1 {
		t.Fatalf("planUploads returned %d rects, want 1 bounding box: %v", len(got), got)
	}
	if !covers(got, damage) {
		t.Fatalf("bounding box %v does not cover the damage", got)
	}
	if got[0].Width > uint16(w) || got[0].Height > uint16(h) {
		t.Fatalf("bounding box %v exceeds the framebuffer", got[0])
	}
}

// TestPlanUploadsKeepsSmallScatteredDamage is the other half of the same
// trade: a handful of small updates (a blinking cursor, a clock) must stay
// small. Uploading the full frame for them is the ~8x regression that
// damage-rect uploading exists to avoid.
func TestPlanUploadsKeepsSmallScatteredDamage(t *testing.T) {
	const w, h = 1920, 1080
	damage := []rfb.Rect{
		r(10, 10, 8, 16),
		r(1800, 20, 60, 20),
		r(900, 1000, 40, 20),
	}
	got := planUploads(damage, w, h, 32)
	if len(got) != 3 {
		t.Fatalf("planUploads merged scattered damage: %v", got)
	}
	if area, full := totalArea(got), w*h; area*100 > full {
		t.Fatalf("uploading %d px for %d px of damage is too much", area, totalArea(damage))
	}
}

// TestPlanUploadsNeverGrowsTheUpload is the planner's core invariant: merging
// is only allowed when it does not increase the number of pixels uploaded,
// except for the two deliberate collapses, which are bounded by the frame.
func TestPlanUploadsNeverGrowsTheUpload(t *testing.T) {
	const w, h = 640, 480
	cases := [][]rfb.Rect{
		{r(0, 0, 10, 10), r(5, 5, 10, 10)},
		{r(0, 0, 320, 240), r(320, 240, 320, 240)},
		{r(100, 100, 1, 1), r(101, 100, 1, 1), r(102, 100, 1, 1)},
		{r(0, 0, 640, 1), r(0, 479, 640, 1)},
	}
	for _, damage := range cases {
		got := planUploads(damage, w, h, 32)
		if !covers(got, damage) {
			t.Fatalf("planUploads(%v) = %v does not cover the damage", damage, got)
		}
		if area := totalArea(got); area > w*h {
			t.Fatalf("planUploads(%v) = %v uploads %d px, more than the whole frame", damage, got, area)
		}
	}
}

func TestPlanUploadsRejectsEmptyFramebuffer(t *testing.T) {
	if got := planUploads([]rfb.Rect{r(0, 0, 10, 10)}, 0, 0, 32); got != nil {
		t.Fatalf("planUploads with an empty framebuffer returned %v", got)
	}
	if got := planUploads([]rfb.Rect{r(0, 0, 10, 10)}, -5, 10, 32); got != nil {
		t.Fatalf("planUploads with a negative width returned %v", got)
	}
}

// TestMergeBoxesIsOrderIndependent: damage arrives in whatever order the
// decoder produced it, and the plan should not depend on that.
func TestMergeBoxesIsOrderIndependent(t *testing.T) {
	forward := []rfb.Rect{r(0, 0, 100, 100), r(100, 0, 100, 100), r(200, 0, 100, 100)}
	reverse := []rfb.Rect{r(200, 0, 100, 100), r(100, 0, 100, 100), r(0, 0, 100, 100)}
	a := planUploads(forward, 800, 600, 32)
	b := planUploads(reverse, 800, 600, 32)
	if !rectsEqual(a, b) {
		t.Fatalf("plan depends on input order: %v vs %v", a, b)
	}
	if len(a) != 1 || a[0] != r(0, 0, 300, 100) {
		t.Fatalf("three abutting rects should merge into one strip, got %v", a)
	}
}

func clipAll(rects []rfb.Rect, w, h int) []rfb.Rect {
	out := make([]rfb.Rect, 0, len(rects))
	for _, rc := range rects {
		b := boxOf(rc).clip(w, h)
		if !b.empty() {
			out = append(out, b.rect())
		}
	}
	return out
}

func rectsEqual(a, b []rfb.Rect) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
