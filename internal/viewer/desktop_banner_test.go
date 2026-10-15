// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"image"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/i18n"
)

// deskInput is a capable guest whose reported input desktop can be set, so the
// blocked-input banner can be driven without a live agent session.
type deskInput struct {
	recordInput
	required bool
	reason   string
}

func (d *deskInput) InputAvailable() bool            { return true }
func (d *deskInput) ResizeAvailable() bool           { return false }
func (d *deskInput) DesktopRequired() (bool, string) { return d.required, d.reason }

func bootBannerWindow(required bool, reason string) (*fakeBackend, *deskInput, *Tier1Sink, *tier1Window) {
	be := newFakeBackend(1280, 800)
	inp := &deskInput{required: required, reason: reason}
	sink := &Tier1Sink{}
	w := &tier1Window{be: be, inp: inp, sink: sink, winW: 1280, winH: 800,
		opts: Tier1Config{Transport: "Agent", NoResize: true}}
	w.opts.applyDefaults()
	return be, inp, sink, w
}

func lastOverlay(be *fakeBackend) Overlay {
	return be.overlays[len(be.overlays)-1]
}

// An elevated (higher-integrity) foreground window keeps the agent streaming
// but blocks injection. The live window must annotate that rather than leave a
// dead keyboard unexplained, and clear when the desktop is no longer blocked.
func TestTier1DesktopBannerShowsWhenInputBlocked(t *testing.T) {
	t.Parallel()
	be, inp, sink, w := bootBannerWindow(true, "higher-integrity-window")
	sink.Video(image.NewRGBA(image.Rect(0, 0, 1280, 800)))
	if err := w.presentFrame(time.Now()); err != nil {
		t.Fatal(err)
	}

	ov := lastOverlay(be)
	if ov.Dim != 0 {
		t.Fatalf("input banner must not dim the live frame: %+v", ov)
	}
	if ov.Rect.Empty() || ov.Rect.Y != bannerTopMargin {
		t.Fatalf("banner not drawn at the top: %+v", ov)
	}
	if len(be.ovUploads) == 0 {
		t.Fatal("banner pixels were not uploaded")
	}

	inp.required = false
	if err := w.presentFrame(time.Now()); err != nil {
		t.Fatal(err)
	}
	if ov := lastOverlay(be); !ov.Empty() {
		t.Fatalf("cleared desktop still shows an overlay: %+v", ov)
	}
}

// The status plate and the banner share one overlay texture. Switching from a
// dimming plate to the banner must re-upload rather than trust the stale
// raster, even though both are rendered for the same window size.
func TestTier1DesktopBannerReplacesStatusPlate(t *testing.T) {
	t.Parallel()
	be, _, sink, w := bootBannerWindow(true, "higher-integrity-window")
	sink.Video(image.NewRGBA(image.Rect(0, 0, 1280, 800)))

	sink.reconnecting.Store(true)
	if err := w.presentFrame(time.Now()); err != nil {
		t.Fatal(err)
	}
	uploads := len(be.ovUploads)
	if ov := lastOverlay(be); ov.Dim == 0 {
		t.Fatalf("a reconnecting plate must dim the frame: %+v", ov)
	}

	sink.reconnecting.Store(false)
	if err := w.presentFrame(time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(be.ovUploads) <= uploads {
		t.Fatal("banner was not re-uploaded after the plate")
	}
	if ov := lastOverlay(be); ov.Dim != 0 {
		t.Fatalf("banner must not dim the frame: %+v", ov)
	}
}

// Without a desktop reporter the live frame carries no banner, so a non-agent
// guest and a guest that has not reported are unaffected.
func TestTier1DesktopBannerAbsentWithoutReporter(t *testing.T) {
	t.Parallel()
	be := newFakeBackend(1280, 800)
	sink := &Tier1Sink{}
	w := &tier1Window{be: be, inp: &recordInput{}, sink: sink, winW: 1280, winH: 800,
		opts: Tier1Config{Transport: "Agent", NoResize: true}}
	w.opts.applyDefaults()
	sink.Video(image.NewRGBA(image.Rect(0, 0, 1280, 800)))
	if err := w.presentFrame(time.Now()); err != nil {
		t.Fatal(err)
	}
	if ov := lastOverlay(be); !ov.Empty() {
		t.Fatalf("no reporter must draw no banner: %+v", ov)
	}
}

func TestDesktopBannerLines(t *testing.T) {
	t.Parallel()
	if got := desktopBannerLines("protected-desktop"); len(got) != 1 || got[0] != i18n.Get("viewer.secureDesktop") {
		t.Fatalf("protected-desktop lines = %q", got)
	}
	for _, reason := range []string{"higher-integrity-window", ""} {
		got := desktopBannerLines(reason)
		if len(got) != 2 || got[0] != i18n.Get("viewer.desktopBlocked") || got[1] != i18n.Get("viewer.desktopBlockedHint") {
			t.Fatalf("reason %q lines = %q", reason, got)
		}
	}
}
