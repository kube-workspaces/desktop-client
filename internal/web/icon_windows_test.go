// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build windows && cgo

package web

import (
	"os"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

// The test executable must link the web child's go-winres object (for example
// via -ldflags '-linkmode external -extldflags=/path/to/rsrc_windows_amd64.syso').
// It needs no WebView2 runtime: icon installation only needs a native HWND.
func TestWin32WindowIcon(t *testing.T) {
	if os.Getenv("KW_WEB_ICON_NATIVE") == "" {
		t.Skip("set KW_WEB_ICON_NATIVE=1 and link the web child's icon resources")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	instance, _, _ := procGetModuleHandleW.Call(0)
	defProc := windows.NewCallback(func(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
		r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wp, lp)
		return r
	})
	const class = "KwWindowIconTestHost"
	registerClass(t, class, defProc, instance)
	defer unregisterClass(t, class, instance)
	host := createWindow(t, class, "icon host", wsOverlappedWindow, 0, 0, 320, 240, 0, instance)
	defer destroyWindow(t, host)
	setNativeWindowIcon(hwndPtr(host))
	for _, kind := range []uintptr{0, 1} { // ICON_SMALL (caption), ICON_BIG (task switcher)
		icon, _, _ := procSendMessageW.Call(host, 0x007f, kind, 0) // WM_GETICON
		if icon == 0 {
			t.Errorf("window icon %d was not installed from the executable's resources", kind)
		}
	}
}
