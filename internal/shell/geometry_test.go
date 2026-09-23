// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

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
