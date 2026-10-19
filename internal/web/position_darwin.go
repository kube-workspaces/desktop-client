// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin && cgo

package web

/*
#cgo LDFLAGS: -framework Cocoa
int kw_cocoa_geometry(void*,double*);
int kw_cocoa_monitor_count(void);
void kw_cocoa_monitor(int,double*);
int kw_cocoa_current_monitor(void*);
void kw_cocoa_restore(void*,double,double,double,double);
*/
import "C"

import (
	"unsafe"

	"github.com/kube-workspaces/desktop-client/internal/config"
)

// Cocoa frame origins and dimensions are points, not backing pixels. Keep the
// native bottom-left origin in the child record rather than mixing SDL units.
func nativeGeometry(win unsafe.Pointer) (config.WindowRect, bool) {
	var b [4]C.double
	if C.kw_cocoa_geometry(win, &b[0]) == 0 {
		return config.WindowRect{}, false
	}
	r := config.WindowRect{X: int(b[0]), Y: int(b[1]), Width: int(b[2]), Height: int(b[3])}
	return r, r.Valid()
}

func restoreNativeGeometry(win unsafe.Pointer, r config.WindowRect) {
	var monitors []config.WindowRect
	for i := C.int(0); i < C.kw_cocoa_monitor_count(); i++ {
		var b [4]C.double
		C.kw_cocoa_monitor(i, &b[0])
		monitors = append(monitors, config.WindowRect{X: int(b[0]), Y: int(b[1]), Width: int(b[2]), Height: int(b[3])})
	}
	if fitted, ok := fitGeometry(r, monitors, int(C.kw_cocoa_current_monitor(win))); ok {
		C.kw_cocoa_restore(win, C.double(fitted.X), C.double(fitted.Y), C.double(fitted.Width), C.double(fitted.Height))
	}
}

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
