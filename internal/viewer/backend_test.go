// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"fmt"
	"testing"
)

func TestFitLetterbox(t *testing.T) {
	tests := []struct {
		name                   string
		srcW, srcH, dstW, dstH int
		want                   Rect
	}{
		{"exact fit", 800, 600, 800, 600, Rect{0, 0, 800, 600}},
		{"integer scale up", 640, 480, 1280, 960, Rect{0, 0, 1280, 960}},
		{"pillarbox: 4:3 guest in 16:9 window", 1024, 768, 1920, 1080, Rect{240, 0, 1440, 1080}},
		{"letterbox: 16:9 guest in 4:3 window", 1920, 1080, 1024, 768, Rect{0, 96, 1024, 576}},
		{"scale down", 1920, 1080, 960, 540, Rect{0, 0, 960, 540}},
		// An odd source and an odd destination together are where naive float
		// scaling starts producing rectangles a pixel outside the window.
		{"odd sizes", 1366, 769, 1000, 999, Rect{0, 218, 1000, 562}},
		{"very wide guest", 3840, 600, 800, 600, Rect{0, 237, 800, 125}},
		{"very tall guest", 600, 3840, 800, 600, Rect{353, 0, 93, 600}},
		{"zero source", 0, 0, 800, 600, Rect{}},
		{"zero destination", 800, 600, 0, 0, Rect{}},
		{"negative destination", 800, 600, -10, 600, Rect{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FitLetterbox(tt.srcW, tt.srcH, tt.dstW, tt.dstH)
			if got != tt.want {
				t.Fatalf("FitLetterbox(%d,%d,%d,%d) = %v, want %v",
					tt.srcW, tt.srcH, tt.dstW, tt.dstH, got, tt.want)
			}
		})
	}
}

// TestFitLetterboxStaysInsideWindow is the property that matters more than any
// individual value: a presented rectangle that pokes outside the window shows
// up as a clipped edge or a crash in the renderer, depending on the backend.
func TestFitLetterboxStaysInsideWindow(t *testing.T) {
	sizes := []int{1, 2, 3, 7, 13, 97, 640, 641, 1366, 1920, 3841}
	for _, sw := range sizes {
		for _, sh := range sizes {
			for _, dw := range sizes {
				for _, dh := range sizes {
					r := FitLetterbox(sw, sh, dw, dh)
					if r.X < 0 || r.Y < 0 || r.X+r.W > dw || r.Y+r.H > dh {
						t.Fatalf("FitLetterbox(%d,%d,%d,%d) = %v escapes %dx%d", sw, sh, dw, dh, r, dw, dh)
					}
					if r.W <= 0 || r.H <= 0 {
						t.Fatalf("FitLetterbox(%d,%d,%d,%d) = %v is empty", sw, sh, dw, dh, r)
					}
					// Centred to within the rounding of one pixel.
					if left, right := r.X, dw-(r.X+r.W); left-right > 1 || right-left > 1 {
						t.Fatalf("FitLetterbox(%d,%d,%d,%d) = %v is not centred horizontally", sw, sh, dw, dh, r)
					}
					if top, bottom := r.Y, dh-(r.Y+r.H); top-bottom > 1 || bottom-top > 1 {
						t.Fatalf("FitLetterbox(%d,%d,%d,%d) = %v is not centred vertically", sw, sh, dw, dh, r)
					}
				}
			}
		}
	}
}

func TestMapToSource(t *testing.T) {
	// A 640x480 guest presented into an 800x600 window scales by 1.25 with no
	// bars; a 1920x1080 guest in the same window is pillarboxed.
	tests := []struct {
		name         string
		present      Rect
		srcW, srcH   int
		x, y         int
		wantX, wantY int
		wantInside   bool
	}{
		{
			name: "origin", present: Rect{0, 0, 800, 600}, srcW: 640, srcH: 480,
			x: 0, y: 0, wantX: 0, wantY: 0, wantInside: true,
		},
		{
			name: "centre", present: Rect{0, 0, 800, 600}, srcW: 640, srcH: 480,
			x: 400, y: 300, wantX: 320, wantY: 240, wantInside: true,
		},
		{
			name: "bottom right pixel", present: Rect{0, 0, 800, 600}, srcW: 640, srcH: 480,
			x: 799, y: 599, wantX: 639, wantY: 479, wantInside: true,
		},
		{
			// The offset must be subtracted before scaling. Getting this the
			// wrong way round is the classic letterbox pointer bug: the cursor
			// tracks correctly in the middle and drifts at the edges.
			name: "inside a pillarboxed image", present: Rect{160, 0, 480, 600}, srcW: 640, srcH: 800,
			x: 160, y: 0, wantX: 0, wantY: 0, wantInside: true,
		},
		{
			name: "left bar clamps to column zero", present: Rect{160, 0, 480, 600}, srcW: 640, srcH: 800,
			x: 12, y: 300, wantX: 0, wantY: 400, wantInside: false,
		},
		{
			// 638, not 639: the image is displayed smaller than the guest
			// framebuffer, so the rightmost source column is not addressable
			// at all. That is a property of downscaling, not a mapping bug.
			name: "right bar clamps to the last reachable column", present: Rect{160, 0, 480, 600}, srcW: 640, srcH: 800,
			x: 700, y: 300, wantX: 638, wantY: 400, wantInside: false,
		},
		{
			name: "above the window clamps to row zero", present: Rect{0, 96, 1024, 576}, srcW: 1920, srcH: 1080,
			x: 512, y: -40, wantX: 960, wantY: 0, wantInside: false,
		},
		{
			name: "letterboxed centre", present: Rect{0, 96, 1024, 576}, srcW: 1920, srcH: 1080,
			x: 512, y: 384, wantX: 960, wantY: 960 * 1080 / 1920, wantInside: true,
		},
		{
			name: "empty present", present: Rect{}, srcW: 640, srcH: 480,
			x: 10, y: 10, wantX: 0, wantY: 0, wantInside: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, y, inside := MapToSource(tt.x, tt.y, tt.present, tt.srcW, tt.srcH)
			if x != tt.wantX || y != tt.wantY || inside != tt.wantInside {
				t.Fatalf("MapToSource(%d,%d,%v,%d,%d) = (%d,%d,%t), want (%d,%d,%t)",
					tt.x, tt.y, tt.present, tt.srcW, tt.srcH, x, y, inside, tt.wantX, tt.wantY, tt.wantInside)
			}
		})
	}
}

// TestMapToSourceAlwaysInBounds guards the invariant the RFB layer depends on:
// the mapped coordinate is cast to uint16 and sent to the guest, so a value
// outside the framebuffer would be silently wrapped rather than rejected.
func TestMapToSourceAlwaysInBounds(t *testing.T) {
	srcs := [][2]int{{640, 480}, {1920, 1080}, {1366, 769}, {1, 1}, {3, 7}}
	wins := [][2]int{{800, 600}, {1024, 768}, {1920, 1080}, {101, 97}}
	for _, src := range srcs {
		for _, win := range wins {
			present := FitLetterbox(src[0], src[1], win[0], win[1])
			for y := -20; y < win[1]+20; y += 7 {
				for x := -20; x < win[0]+20; x += 7 {
					sx, sy, _ := MapToSource(x, y, present, src[0], src[1])
					if sx < 0 || sx >= src[0] || sy < 0 || sy >= src[1] {
						t.Fatalf("MapToSource(%d,%d) with src %v win %v = (%d,%d), out of bounds",
							x, y, src, win, sx, sy)
					}
				}
			}
		}
	}
}

// TestMapToSourceRoundTrip checks that the mapping is monotonic and that the
// whole source range is reachable when scaling up, i.e. that no guest column
// is impossible to click on.
func TestMapToSourceRoundTrip(t *testing.T) {
	const srcW, srcH = 320, 200
	present := FitLetterbox(srcW, srcH, 1280, 800)

	seen := make(map[int]bool, srcW)
	prev := -1
	for x := present.X; x < present.X+present.W; x++ {
		sx, _, inside := MapToSource(x, present.Y, present, srcW, srcH)
		if !inside {
			t.Fatalf("x=%d should be inside %v", x, present)
		}
		if sx < prev {
			t.Fatalf("mapping is not monotonic at x=%d: %d after %d", x, sx, prev)
		}
		prev = sx
		seen[sx] = true
	}
	if len(seen) != srcW {
		t.Fatalf("only %d of %d source columns are reachable", len(seen), srcW)
	}
}

func TestRectHelpers(t *testing.T) {
	r := Rect{X: 10, Y: 20, W: 30, H: 40}
	if r.Empty() {
		t.Fatal("Rect with positive size reported empty")
	}
	if got := r.Area(); got != 1200 {
		t.Fatalf("Area = %d, want 1200", got)
	}
	if !r.Contains(10, 20) || !r.Contains(39, 59) {
		t.Fatal("Contains rejected a corner that is inside")
	}
	if r.Contains(9, 20) || r.Contains(40, 59) || r.Contains(10, 60) {
		t.Fatal("Contains accepted a point that is outside")
	}
	if got, want := r.String(), "30x40+10+20"; got != want {
		t.Fatalf("String = %q, want %q", got, want)
	}
	for _, empty := range []Rect{{}, {W: 0, H: 5}, {W: 5, H: 0}, {W: -1, H: -1}} {
		if !empty.Empty() || empty.Area() != 0 {
			t.Fatalf("%v should be empty with zero area", empty)
		}
	}
}

func TestParseScaleQuality(t *testing.T) {
	for _, q := range ScaleQualities() {
		got, err := ParseScaleQuality(" " + string(q) + " ")
		if err != nil || got != q {
			t.Fatalf("ParseScaleQuality(%q) = (%q, %v), want (%q, nil)", q, got, err, q)
		}
	}
	if got, err := ParseScaleQuality("LINEAR"); err != nil || got != ScaleLinear {
		t.Fatalf("ParseScaleQuality is not case-insensitive: (%q, %v)", got, err)
	}
	if _, err := ParseScaleQuality("bicubic"); err == nil {
		t.Fatal("ParseScaleQuality accepted an unknown filter")
	}
}

func TestButtonsHas(t *testing.T) {
	b := ButtonLeft | ButtonRight
	if !b.Has(ButtonLeft) || !b.Has(ButtonRight) || !b.Has(ButtonLeft|ButtonRight) {
		t.Fatal("Has rejected a set bit")
	}
	if b.Has(ButtonMiddle) {
		t.Fatal("Has accepted a clear bit")
	}
}

// TestEventsImplementEvent keeps the closed event set honest: a new type that
// forgets the marker method would silently never be deliverable.
func TestEventsImplementEvent(t *testing.T) {
	events := []Event{
		EventQuit{},
		EventKey{},
		EventPointer{},
		EventWheel{},
		EventResize{},
		EventFocus{},
		EventClipboard{},
	}
	if len(events) != 7 {
		t.Fatalf("unexpected event count %d", len(events))
	}
	for _, e := range events {
		if _, ok := e.(Event); !ok {
			t.Fatalf("%s does not implement Event", fmt.Sprintf("%T", e))
		}
	}
}
