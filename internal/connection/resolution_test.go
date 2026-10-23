// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package connection

import "testing"

func TestResolutionAutoToleratesChromeAndManualKeepsGuestPixels(t *testing.T) {
	modes := [][2]int{{1280, 720}, {1920, 1080}, {2560, 1440}, {3840, 2160}}
	for _, tc := range []struct {
		w, h int
		want DisplayMode
	}{
		{1920, 980, DisplayMode{1920, 1080}},
		{3840, 2010, DisplayMode{3840, 2160}},
		{800, 600, DisplayMode{1280, 720}},
	} {
		if got, ok := ClosestMode(modes, tc.w, tc.h); !ok || got != tc.want {
			t.Fatalf("closest to %dx%d = %v/%v, want %v", tc.w, tc.h, got, ok, tc.want)
		}
	}
	manual := DisplayMode{1440, 900}
	if got, ok := ParseResolution(ResolutionAction(manual)); !ok || got != manual {
		t.Fatal("manual guest pixels lost")
	}
	for _, a := range []Action{ResolutionAuto, "resolution-0x0", "resolution-1920x1080junk", "resolution-8193x1080"} {
		if _, ok := ParseResolution(a); ok {
			t.Fatalf("invalid fixed mode accepted: %s", a)
		}
	}
	choices := ResolutionChoices([][2]int{{1280, 720}, {1280, 720}, {1440, 810}, {0, 0}})
	seen := map[DisplayMode]bool{}
	for _, c := range choices {
		if seen[c.DisplayMode] {
			t.Fatal("duplicate mode")
		}
		seen[c.DisplayMode] = true
		if c.DisplayMode == (DisplayMode{1440, 810}) && !c.Advertised {
			t.Fatal("custom guest mode lost")
		}
	}
	s := Snapshot{Surface: Desktop, State: Connected, Capabilities: Capabilities{GuestResize: true, ResolutionSelection: true}}
	for _, a := range []Action{ResolutionAuto, ResolutionAction(manual)} {
		if !s.Availability(a).Enabled {
			t.Fatal("controller cannot resize")
		}
		s.Role = Observer
		if s.Availability(a).Enabled {
			t.Fatal("observer can mutate guest")
		}
		s.Role = Exclusive
	}
}
