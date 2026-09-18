// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"os"
	"strconv"
	"strings"
)

// launchDisplayEnv is the environment variable the shell sets on the spawned
// web child with the usable bounds, "x,y,w,h" in physical screen pixels, of the
// display the shell's own window is on. When present and well-formed the webview
// opens centred on that display instead of the primary one — the same
// monitor-placement the shell gives its own and its session windows. Its value
// (the string in the shell's spawnWeb) is the only thing sent across the
// boundary, so the child, which links the browser engine via cgo and no SDL,
// does not need to ask the window system which display to choose.
//
// The constant lives here too so a standalone shell binary (which does not
// import this package) and the web child it spawns stay in agreement; the
// shell's copy in internal/shell/actions.go must stay byte-identical.
const launchDisplayEnv = "KW_WEB_LAUNCH_DISPLAY"

// launchBounds is the usable area of a display, in physical screen pixels,
// that a window should be placed on. X and Y may be negative (a monitor left
// of or above the primary); W and H are always positive when parsing succeeds.
type launchBounds struct {
	X, Y, W, H int
}

// valid reports whether b describes a real area. The shell only sends bounds
// it measured itself, but the check costs nothing and keeps a damaged env
// string from sliding through a platform shim into a nonsense SetWindowPos.
func (b launchBounds) valid() bool { return b.W > 0 && b.H > 0 }

// parseLaunchBounds decodes the "x,y,w,h" usable bounds the shell forwarded,
// returning ok=false when raw is empty, malformed, or describes no area. The
// format is deliberately free-form — spaces are tolerated — so [launchBounds].
// doesn't leak a wire protocol into the code that only has to position a
// window.
func parseLaunchBounds(raw string) (b launchBounds, ok bool) {
	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return launchBounds{}, false
	}
	vals := make([]int, 4)
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return launchBounds{}, false
		}
		vals[i] = v
	}
	b = launchBounds{X: vals[0], Y: vals[1], W: vals[2], H: vals[3]}
	if !b.valid() {
		return launchBounds{}, false
	}
	return b, true
}

// launchBoundsFromEnv reads the shell's [launchDisplayEnv] variable.
func launchBoundsFromEnv() (b launchBounds, ok bool) {
	return parseLaunchBounds(os.Getenv(launchDisplayEnv))
}

// centeredPosition returns the top-left corner of a w×h window centred inside
// the display area b, in the same screen pixel space as b.
//
// The halves round towards zero the way integer division does( — an odd
// surplus pixel of slack falls to the top and left, which is how the shell
// centres its SDL windows. The two must agree so a webview window sits exactly
// where the user expects it to: beside, not overlapping, the shell they
// launched it from.
func centeredPosition(b launchBounds, w, h int) (x, y int) {
	return b.X + (b.W-w)/2, b.Y + (b.H-h)/2
}
