// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin && cgo

package web

import "unsafe"

// centerOnLaunchDisplay is a no-op on macOS for now. Centring the webview on
// the launch display here needs to set an [NSWindow setFrameOrigin:] at the
// right physical-coordinate moment in the Cocoa run loop, and that cannot be
// verified on any machine available to this project today (no Mac desktop; the
// position Windows/Linux legs are verified, macOS placement stays honest and
// documented in the tracker TODO as deferred). When a real Mac run is
// possible, the NSWindow comes from the same webview_go Window() the other
// legs use, and the bounds come from [launchDisplayEnv] exactly as it does
// here.
//
// Keeping the file (rather than build-tagging the call site) means the
// cgo+browser build has exactly one positional seam, [centerOnLaunchDisplay],
// that the Windows and GTK legs implement; the macOS build is not a special
// case in web_cgo.go.
func centerOnLaunchDisplay(_ unsafe.Pointer, _ launchBounds) {}
