// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/connection"
)

func TestPresentationGeometryAndPointerShareTransform(t *testing.T) {
	for _, test := range []struct {
		name             string
		view             presentation
		want             Rect
		originX, originY int
	}{
		{"fit", presentation{}, Rect{Y: 75, W: 800, H: 450}, 0, 0},
		{"actual", presentation{mode: "zoom", zoom: 100}, Rect{X: -560, Y: -240, W: 1920, H: 1080}, 560, 240},
		{"zoom", presentation{mode: "zoom", zoom: 200}, Rect{X: -1520, Y: -780, W: 3840, H: 2160}, 760, 390},
		{"fill", presentation{mode: "fill"}, Rect{X: -133, W: 1067, H: 600}, 239, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := test.view.rectangle(1920, 1080, 800, 600)
			if r != test.want {
				t.Fatalf("rectangle=%v want=%v", r, test.want)
			}
			x, y, _ := MapToSource(max(0, r.X), max(0, r.Y), r, 1920, 1080)
			if x != test.originX || y != test.originY {
				t.Fatalf("visible origin maps to %d,%d want %d,%d", x, y, test.originX, test.originY)
			}
			x, y, inside := MapToSource(400, 300, r, 1920, 1080)
			if !inside || x < 958 || x > 961 || y != 540 {
				t.Fatalf("centre maps to %d,%d inside=%v", x, y, inside)
			}
		})
	}
}

func TestPresentationPanReachesEveryEdgeAndClampsOnResize(t *testing.T) {
	p := presentation{mode: "zoom", zoom: 100}
	for range 30 {
		p.action(connection.PanLeft, 1920, 1080, 800, 600)
		p.action(connection.PanUp, 1920, 1080, 800, 600)
		p.rectangle(1920, 1080, 800, 600)
	}
	r := p.rectangle(1920, 1080, 800, 600)
	if r.X != 0 || r.Y != 0 {
		t.Fatalf("could not reach top-left: %v", r)
	}
	for range 30 {
		p.action(connection.PanRight, 1920, 1080, 800, 600)
		p.action(connection.PanDown, 1920, 1080, 800, 600)
		p.rectangle(1920, 1080, 800, 600)
	}
	r = p.rectangle(1920, 1080, 800, 600)
	x, y, inside := MapToSource(799, 599, r, 1920, 1080)
	if !inside || x != 1919 || y != 1079 {
		t.Fatalf("could not reach bottom-right: rect=%v pointer=%d,%d", r, x, y)
	}
	// Moving to a larger/HiDPI drawable removes overflow and recentres.
	r = p.rectangle(1920, 1080, 3840, 2160)
	if r != (Rect{X: 960, Y: 540, W: 1920, H: 1080}) || p.panX != 0 || p.panY != 0 {
		t.Fatalf("stale pan survived drawable resize: %v %+v", r, p)
	}
}

func TestPresentationActionsDoNotResizeGuestInEitherViewer(t *testing.T) {
	v := &Viewer{be: newFakeBackend(800, 600), winW: 800, winH: 600, haveFrame: true, frameW: 1920, frameH: 1080}
	w := &tier1Window{be: newFakeBackend(800, 600), inp: &recordInput{}, winW: 800, winH: 600, haveFrame: true, texW: 1920, texH: 1080}
	v.cfg.applyDefaults()
	w.opts.applyDefaults()
	for _, a := range []connection.Action{connection.ActualPixels, connection.ZoomIn, connection.PanLeft, connection.FillDesktop, connection.FitDesktop} {
		if err := v.connectionAction(a); err != nil {
			t.Fatal(err)
		}
		if err := w.connectionAction(a); err != nil {
			t.Fatal(err)
		}
		if v.present != w.present || !v.resizeDisabled || !w.opts.NoResize || v.resizePending || w.resizePending {
			t.Fatalf("action %s changed guest resize or diverged: rfb=%v premium=%v", a, v.present, w.present)
		}
	}
	// Observer geometry is local too: no guest resize capability is needed.
	for _, a := range []connection.Action{connection.ActualPixels, connection.ZoomIn, connection.FillDesktop} {
		s := connection.Snapshot{Surface: connection.Desktop, Role: connection.Observer, State: connection.Reconnecting}
		if !s.Availability(a).Enabled {
			t.Fatalf("local observer action disabled: %s", a)
		}
	}
	// A resize and click in the same event batch must use the new transform.
	w.view = presentation{mode: "zoom", zoom: 100}
	if err := w.handleEvent(time.Now(), EventResize{W: 1000, H: 700}); err != nil {
		t.Fatal(err)
	}
	if w.present != (Rect{X: -460, Y: -190, W: 1920, H: 1080}) {
		t.Fatalf("stale pointer transform: %v", w.present)
	}
}

func TestPresentationZoomIsBoundedAndFitResetsPan(t *testing.T) {
	p := presentation{mode: "zoom", zoom: 100}
	p.action(connection.ZoomIn, 1023, 767, 800, 600)
	p.action(connection.ZoomIn, 1023, 767, 800, 600)
	p.action(connection.ZoomIn, 1023, 767, 800, 600)
	if p.zoom != 175 {
		t.Fatalf("odd framebuffer rounding stalled zoom: %d", p.zoom)
	}
	for range 30 {
		p.action(connection.ZoomIn, 1920, 1080, 800, 600)
	}
	if p.zoom != 400 {
		t.Fatalf("unbounded zoom: %d", p.zoom)
	}
	for range 30 {
		p.action(connection.ZoomOut, 1920, 1080, 800, 600)
	}
	if p.zoom != 25 {
		t.Fatalf("invalid minimum zoom: %d", p.zoom)
	}
	p.panX, p.panY = 999, -999
	p.action(connection.FitDesktop, 1920, 1080, 800, 600)
	if p != (presentation{}) {
		t.Fatalf("fit retained crop state: %+v", p)
	}
}

func TestSnapFitsWindowWithoutResizingGuestInEitherViewer(t *testing.T) {
	for _, test := range []struct {
		name          string
		view          presentation
		width, height int
	}{
		{"fit", presentation{}, 800, 450},
		{"actual", presentation{mode: "zoom", zoom: 100, panX: 100}, 1920, 1080},
		{"zoom", presentation{mode: "zoom", zoom: 50}, 960, 540},
	} {
		t.Run(test.name, func(t *testing.T) {
			v := &Viewer{be: newFakeBackend(800, 600), winW: 800, winH: 600, haveFrame: true, frameW: 1920, frameH: 1080, view: test.view, resizePending: true}
			w := &tier1Window{be: newFakeBackend(800, 600), inp: &recordInput{}, winW: 800, winH: 600, haveFrame: true, texW: 1920, texH: 1080, view: test.view, resizePending: true}
			v.cfg.applyDefaults()
			w.opts.applyDefaults()
			if err := v.connectionAction(connection.SnapDesktop); err != nil {
				t.Fatal(err)
			}
			if err := w.connectionAction(connection.SnapDesktop); err != nil {
				t.Fatal(err)
			}
			want := Rect{W: test.width, H: test.height}
			if v.present != want || w.present != want || v.winW != want.W || v.winH != want.H || w.winW != want.W || w.winH != want.H {
				t.Fatalf("snap: rfb=%v premium=%v want=%v", v.present, w.present, want)
			}
			if !v.resizeDisabled || !w.opts.NoResize || v.resizePending || w.resizePending {
				t.Fatal("snap left guest resize enabled or pending")
			}
			if v.view.mode != test.view.mode || v.view.zoom != test.view.zoom || v.view.panX != 0 || w.view != v.view {
				t.Fatalf("snap changed scale or retained pan: %+v %+v", v.view, w.view)
			}
		})
	}
}

func TestSnapFromFillIsStableDespiteOutwardPixelRounding(t *testing.T) {
	be := newFakeBackend(800, 600)
	p := presentation{mode: "fill"}
	for range 3 {
		w, h := be.Size()
		if err := p.snapWindow(be, 1920, 1080, w, h); err != nil {
			t.Fatal(err)
		}
		w, h = be.Size()
		if w != 1067 || h != 600 {
			t.Fatalf("snap fill window=%dx%d", w, h)
		}
		r := p.rectangle(1920, 1080, w, h)
		if r.X != 0 || r.Y != 0 || r.W != w || r.H < h || r.H > h+1 {
			t.Fatalf("fill retained bars or cropping: %v", r)
		}
	}
}

type snapScaledBackend struct {
	*fakeBackend
	scale float64
}

func (b *snapScaledBackend) ScaleFactor() float64 { return b.scale }
func (b *snapScaledBackend) SetSize(w, h int) error {
	return b.fakeBackend.SetSize(int(float64(w)*b.scale), int(float64(h)*b.scale))
}

func TestSnapConvertsDrawablePixelsToWindowCoordinates(t *testing.T) {
	for _, scale := range []float64{1, 1.5, 2} {
		be := &snapScaledBackend{fakeBackend: newFakeBackend(2400, 1800), scale: scale}
		p := presentation{mode: "zoom", zoom: 100}
		if err := p.snapWindow(be, 1920, 1080, 2400, 1800); err != nil {
			t.Fatal(err)
		}
		if w, h := be.Size(); w != 1920 || h != 1080 {
			t.Fatalf("scale=%v: drawable=%dx%d", scale, w, h)
		}
	}
	s := connection.Snapshot{Surface: connection.Desktop, Fullscreen: true}
	if a := s.Availability(connection.SnapDesktop); !a.Visible || a.Enabled {
		t.Fatalf("fullscreen snap availability: %+v", a)
	}
}
