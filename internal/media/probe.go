// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package media

import "sync/atomic"

// videoLatched records that the pinned FFmpeg libraries have loaded once in
// this process. Only success is remembered: a failure can be transient (a
// scanner holding a freshly written DLL, a library still being installed), and
// latching that would downgrade Tier 1 for the whole run over something that
// resolves itself a second later.
var videoLatched atomic.Bool

// ProbeVideo reports whether this process can construct the pinned H.264
// decoder. It loads and immediately releases both libraries, doing no I/O and
// touching nothing remote, so a caller can ask the question *before* claiming a
// guest display. Without this, a machine with no FFmpeg beside its executable
// takes ownership of the display, fails to decode the first frame and hands the
// display back mid-fallback, which is what turned one missing DLL into a
// "someone else is using this display" prompt against the user's own session.
//
// A nil return is cached; a non-nil one is not, so a transient failure gets
// another chance on the next call rather than being frozen into the process.
func ProbeVideo() error {
	if videoLatched.Load() {
		return nil
	}
	codec, err := load(codecNames()...)
	if err != nil {
		return err
	}
	codec.close()
	util, err := load(utilNames()...)
	if err != nil {
		return err
	}
	util.close()
	videoLatched.Store(true)
	return nil
}
