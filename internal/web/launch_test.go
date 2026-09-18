// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package web

import "testing"

func TestParseLaunchBounds(t *testing.T) {
	tests := []struct {
		raw    string
		want   launchBounds
		wantOK bool
	}{
		// The shell writes the full "x,y,w,h" it measured. Spaces are
		// tolerated because the string is eyeballed in logs and the format is
		// deliberately free-form.
		{"1920,0,1920,1080", launchBounds{X: 1920, Y: 0, W: 1920, H: 1080}, true},
		// A monitor left of the primary has a negative X; Y too for one above.
		{"-1920,0,1920,1080", launchBounds{X: -1920, Y: 0, W: 1920, H: 1080}, true},
		{"-1920,-240,1920,1080", launchBounds{X: -1920, Y: -240, W: 1920, H: 1080}, true},
		// Zero or negative W/H describe no area; the shell only ever sends
		// usable bounds it measured, but a damaged env string must not slide
		// through shims into a nonsense SetWindowPos.
		{"1920,0,0,1080", launchBounds{}, false},
		{"1920,0,-1920,1080", launchBounds{}, false},
		// Empty is the normal, unset case that yields the primary-display
		// fallback.
		{"", launchBounds{}, false},
		// Malformed: wrong arity, trailing/comma junk, non-integer.
		{"1920,0,1920", launchBounds{}, false},
		{"1920,0,1920,1080,", launchBounds{}, false},
		{"a,0,1920,1080", launchBounds{}, false},
	}
	for _, tc := range tests {
		got, ok := parseLaunchBounds(tc.raw)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("parseLaunchBounds(%q) = %+v, %v; want %+v, %v",
				tc.raw, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestCenteredPosition(t *testing.T) {
	// A 1280×800 window inside a 1920×1080 display: (1920-1280)/2 = 320 per
	// side, (1080-800)/2 = 140 top and bottom.
	b := launchBounds{X: 0, Y: 0, W: 1920, H: 1080}
	if x, y := centeredPosition(b, 1280, 800); x != 320 || y != 140 {
		t.Fatalf("centeredPosition = (%d,%d); want (320,140)", x, y)
	}
	// A window bigger than a side still centres with the surplus negative —
	// exactly how the shell's integer halfing behaves, so the two agree.
	if x, y := centeredPosition(b, 2000, 1280); x != -40 || y != -100 {
		t.Fatalf("oversized centredPosition = (%d,%d); want (-40,-100)", x, y)
	}
	// The left/above monitor case must compute in its own (negative) space.
	b = launchBounds{X: -1920, Y: 0, W: 1920, H: 1080}
	if x, y := centeredPosition(b, 1280, 800); x != -1600 || y != 140 {
		t.Fatalf("negative-X centredPosition = (%d,%d); want (-1600,140)", x, y)
	}
	// Odd surplus pixel: the spare goes to the top and left (towards zero),
	// matching the shell's SDL centring so a webview sits exactly where the
	// window it was launched beside expects it.
	b = launchBounds{X: 0, Y: 0, W: 1921, H: 1081}
	if x, y := centeredPosition(b, 1280, 800); x != 320 || y != 140 {
		t.Fatalf("odd-size centredPosition = (%d,%d); want (320,140)", x, y)
	}
}
