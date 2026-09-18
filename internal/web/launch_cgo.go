// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build cgo

// The launch-display environment plumbing belongs to the web child only.
// launchBoundsFromEnv is invoked solely by [Run] in web_cgo.go (there is no
// equivalent in the cgo-free web_nocgo.go build), so keeping it and the
// constant in this cgo-gated file means the cgo-free build — which is what
// the shell ships and what lint builds with CGO_ENABLED=0 — has no unreferenced
// webview-only symbols, instead of carrying an otherwise-unused wrapper.
package web

import "os"

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

// launchBoundsFromEnv reads the shell's [launchDisplayEnv] variable.
func launchBoundsFromEnv() (b launchBounds, ok bool) {
	return parseLaunchBounds(os.Getenv(launchDisplayEnv))
}
