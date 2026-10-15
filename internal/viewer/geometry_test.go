// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"testing"
	"time"
)

type restoredBackend struct{ *fakeBackend }

func (b *restoredBackend) Position() (int, int, bool) { return 0, 0, true }
func (b *restoredBackend) WindowSize() (int, int, bool) {
	w, h := b.Size()
	return w, h, true
}
func (b *restoredBackend) GeometryRestored() bool { return true }

func TestFirstGuestDoesNotReplaceRestoredWindow(t *testing.T) {
	be := &restoredBackend{newFakeBackend(900, 700)}
	v := New(be, Config{})
	v.winW, v.winH = 900, 700
	v.fitWindow(1920, 1080)
	if len(be.sized) != 0 {
		t.Fatalf("guest replaced restored RFB window: %v", be.sized)
	}

	w := &tier1Window{be: be, winW: 900, winH: 700, pinned: geometryRestored(be)}
	w.opts.Transport = "Agent"
	w.fitInitialGuest(time.Now(), 1920, 1080)
	w.onGuestSize(896, 672) // Guest mode snapping must not move the host window.
	w.fitInitialGuest(time.Now(), 896, 672)
	if len(be.sized) != 0 || !w.resizePending || w.resizeW != 900 || w.resizeH != 700 {
		t.Fatalf("restored premium window lost: resizes=%v, request=%dx%d pending=%v", be.sized, w.resizeW, w.resizeH, w.resizePending)
	}
}
