// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build windows && cgo

package web

/*
#cgo LDFLAGS: -luser32
#include <windows.h>
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// centerOnLaunchDisplay moves the freshly created webview window so its centre
// lands in the middle of the launch display — the monitor the shell's own
// window is on, which the shell measured and handed over in [launchDisplayEnv]
// as the usable bounds in physical screen pixels. Without the move a webview
// always opens on the primary display, so on a two-monitor desk the browser
// window would appear on the wrong screen while the shell sat on the other.
//
// The positioning happens before [webview.Run] enters its message loop, so the
// user never sees the window flash up centred on the primary display and then
// jump. The process is per-monitor-DPI-aware (webview_go's Win32 engine calls
// enable_dpi_awareness at start-up), which is exactly what makes the
// coordinates agree: SetWindowPos takes physical pixels in a DPI-aware
// process, the client area of the window is sized at 1280×800 screen pixels by
// [web_cgo.go]'s SetSize, and the shell's usable bounds are the same physical
// space. The WEBVIEW2 window is a child of the top-level browser window, so
// moving the parent moves the engine with it.
//
// It is best-effort for the same reason every other placement in this project
// is: keep the current window size (SWP_NOSIZE) and z-order, and never
// activate or steal focus from the shell.
func centerOnLaunchDisplay(win unsafe.Pointer, b launchBounds) {
	hwnd := C.HWND(win)
	if hwnd == nil {
		return
	}
	var r C.RECT
	if C.GetWindowRect(hwnd, &r) == 0 {
		return
	}
	w := int(r.right - r.left)
	h := int(r.bottom - r.top)
	if w <= 0 || h <= 0 {
		return
	}
	x, y := centeredPosition(b, w, h)
	C.SetWindowPos(hwnd, C.HWND(nil), C.int(x), C.int(y), 0, 0,
		C.UINT(C.SWP_NOSIZE|C.SWP_NOZORDER|C.SWP_NOACTIVATE))
}

var _ = fmt.Sprintf
