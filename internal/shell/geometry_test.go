// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/chrome"
	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

type geometryBackend struct {
	*fakeBackend
	rect        viewer.WindowRect
	unavailable bool
}

func (b *geometryBackend) Position() (int, int, bool) {
	return b.rect.X, b.rect.Y, !b.unavailable
}

func (b *geometryBackend) WindowSize() (int, int, bool) {
	return b.rect.Width, b.rect.Height, !b.unavailable
}

type geometryWindow struct {
	fakeLiveWindow
	be viewer.Backend
}

func (w *geometryWindow) WindowBackend() viewer.Backend { return w.be }

func TestGeometryUsesWindowUnitsAndKeepsNormalRect(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()
	r.be.resize(2400, 1600) // Retina drawable, not the window-manager units.
	be := &geometryBackend{fakeBackend: r.be, rect: viewer.WindowRect{X: -1200, Y: 0, Width: 1200, Height: 800}}
	r.app.be = be
	r.app.flushGeometry()
	saved := r.store.settings
	if saved.WindowWidth != 1200 || saved.WindowHeight != 800 || saved.WindowX != -1200 || saved.WindowY != 0 || !saved.WindowPositioned {
		t.Fatalf("wrong stored rectangle: %+v", saved)
	}
	be.unavailable = true // Fullscreen/minimized/maximized cannot replace it.
	r.app.flushGeometry()
	if r.store.settings.WindowWidth != saved.WindowWidth || r.store.settings.WindowX != saved.WindowX {
		t.Fatalf("transient state replaced normal geometry: %+v", r.store.settings)
	}
}

func TestDisplayGeometryPersistsOnParkWithoutToolbarSizeDrift(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()
	ws := kwclient.Workspace{Namespace: "workspaces", Name: "desktop", Type: "vm"}
	rect := viewer.WindowRect{X: 0, Y: 40, Width: 1200, Height: 840}
	be := &geometryBackend{fakeBackend: r.be, rect: rect}
	w := &geometryWindow{be: chrome.New(be)}
	r.app.live[ws.Key()] = &liveEntry{key: ws.Key(), ws: ws, window: w}
	r.app.parkLive(context.Background(), ws.Key())
	want := config.WindowRect{X: rect.X, Y: rect.Y, Width: rect.Width, Height: rect.Height}
	if got := r.store.settings.WindowGeometry[ws.Key()]; got != want {
		t.Fatalf("native window rectangle = %+v, want %+v", got, want)
	}
	// Appearance saves and a console/observer seat must preserve the display.
	be.rect.Height = 600
	r.app.rememberDisplayGeometry(&liveEntry{key: ws.Key() + "#serial", ws: ws, window: w})
	r.app.rememberDisplayGeometry(&liveEntry{key: ws.Key(), ws: ws, observer: true, window: w})
	r.app.saveSettings()
	r.settle()
	if got := r.store.settings.WindowGeometry[ws.Key()]; got != want {
		t.Fatalf("another save clobbered display rectangle: %+v", got)
	}
}

func TestTransitionGeometryUsesCurrentRectThenFreshStore(t *testing.T) {
	current := viewer.WindowRect{X: -1000, Y: 40, Width: 900, Height: 700}
	later := viewer.WindowRect{X: 100, Y: 80, Width: 1100, Height: 850}
	w := &agentLiveWindow{geometry: &current, handle: &agentHandle{opts: SessionOptions{
		RestoreGeometry: func(string) (viewer.WindowRect, bool) { return later, true },
	}}}
	opts := w.transitionOptions()
	if got, _ := opts.RestoreGeometry("workspace"); got != current {
		t.Fatalf("transition rectangle = %+v, want %+v", got, current)
	}
	if got, _ := opts.RestoreGeometry("workspace"); got != later {
		t.Fatalf("parked reopen rectangle = %+v, want fresh %+v", got, later)
	}
}

func TestNativeGeometryRoundTripThroughChrome(t *testing.T) {
	if os.Getenv("KW_NATIVE_MEDIA_TEST") != "1" {
		t.Skip("set KW_NATIVE_MEDIA_TEST=1 for SDL dummy-window acceptance")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	t.Setenv("SDL_VIDEODRIVER", "dummy")
	t.Setenv("SDL_AUDIODRIVER", "dummy")
	want := viewer.WindowRect{Width: 800, Height: 600}
	rect := want
	for i := 0; i < 3; i++ {
		be := viewer.NewSDLBackend()
		be.RestoreGeometry(rect)
		decorated := chrome.New(be)
		if err := decorated.Open(viewer.WindowOptions{Title: "geometry", Width: 1280, Height: 800}); err != nil {
			t.Fatal(err)
		}
		got, ok := viewer.WindowRectangle(decorated)
		decorated.Close()
		if !ok || got != want {
			t.Fatalf("round trip %d: got %+v (valid=%v), want %+v", i, got, ok, want)
		}
		rect = got
	}
}

// TestResizeRecordsGeometry: a window resize updates the cached size and,
// once due, persists it with the settings.
func TestResizeRecordsGeometry(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()

	r.be.resize(1600, 900)
	r.be.send(viewer.EventResize{W: 1600, H: 900})
	r.step()
	r.settle()

	if r.app.geomW != 1600 || r.app.geomH != 900 {
		t.Fatalf("geometry = %dx%d, want 1600x900", r.app.geomW, r.app.geomH)
	}
	if r.store.settings.WindowWidth != 1600 || r.store.settings.WindowHeight != 900 {
		t.Fatalf("stored geometry = %dx%d, want 1600x900",
			r.store.settings.WindowWidth, r.store.settings.WindowHeight)
	}
}

// TestResizeBurstsSaveOnce: a drag's worth of resizes inside the interval
// writes once, not once per frame.
func TestResizeBurstsSaveOnce(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()
	saves := r.store.saves

	for i := 0; i < 10; i++ {
		r.be.resize(1600+i, 900)
		r.be.send(viewer.EventResize{W: 1600 + i, H: 900})
		r.step()
	}
	r.settle()

	if got := r.store.saves - saves; got != 1 {
		t.Fatalf("saves = %d, want 1 for a single burst", got)
	}
	if r.app.geomW != 1609 {
		t.Fatalf("geometry = %d, want the last size 1609", r.app.geomW)
	}
}

// TestFlushGeometryWritesFinalSize: the exit flush persists the size even
// when the last resize came too soon after a write to be due.
func TestFlushGeometryWritesFinalSize(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()

	r.be.resize(1600, 900)
	r.be.send(viewer.EventResize{W: 1600, H: 900})
	r.step()
	r.settle()

	r.be.resize(1920, 1080)
	r.app.flushGeometry()

	if r.store.settings.WindowWidth != 1920 || r.store.settings.WindowHeight != 1080 {
		t.Fatalf("stored geometry = %dx%d, want 1920x1080",
			r.store.settings.WindowWidth, r.store.settings.WindowHeight)
	}
}

// TestGeometrySeededFromLaunchSize: an appearance save before any resize
// keeps the launch size instead of clearing the stored geometry.
func TestGeometrySeededFromLaunchSize(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()

	if r.app.geomW != DefaultWidth || r.app.geomH != DefaultHeight {
		t.Fatalf("geometry = %dx%d, want launch size %dx%d",
			r.app.geomW, r.app.geomH, DefaultWidth, DefaultHeight)
	}
	r.app.saveSettings()
	r.settle()

	if r.store.settings.WindowWidth != DefaultWidth || r.store.settings.WindowHeight != DefaultHeight {
		t.Fatalf("stored geometry = %dx%d, want launch size",
			r.store.settings.WindowWidth, r.store.settings.WindowHeight)
	}
}

// TestRecordGeometryIgnoresZeroSize: a minimized (zero-size) window never
// clobbers the stored arrangement.
func TestRecordGeometryIgnoresZeroSize(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()

	r.be.resize(0, 0)
	r.app.recordGeometry(time.Now())

	if r.app.geomW != DefaultWidth || r.app.geomH != DefaultHeight {
		t.Fatalf("geometry = %dx%d, want launch size kept", r.app.geomW, r.app.geomH)
	}
}
