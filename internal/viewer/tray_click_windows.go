// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/kube-workspaces/desktop-client/internal/tray"
)

var trayUser32 = windows.NewLazySystemDLL("user32.dll")
var trayEnumThreadWindows = trayUser32.NewProc("EnumThreadWindows")
var trayGetWindowLong = trayUser32.NewProc("GetWindowLongPtrW")
var traySetWindowLong = trayUser32.NewProc("SetWindowLongPtrW")
var trayCallWindowProc = trayUser32.NewProc("CallWindowProcW")

// SDL 3.4 sends both left release and context-menu requests to its hidden
// SDL_TRAY window (WM_USER+1). Subclass that window: consume only left
// release, leaving right-click menu handling and all menu commands to SDL.
// Find it by GWLP_USERDATA rather than depending on SDL's private struct layout.
func installTrayClicks(native unsafe.Pointer, handler tray.Handler) (func(), error) {
	var hwnd uintptr
	find := windows.NewCallback(func(window, _ uintptr) uintptr {
		data, _, _ := trayGetWindowLong.Call(window, ^uintptr(20)) // GWLP_USERDATA = -21
		if data == uintptr(native) {
			hwnd = window
			return 0
		}
		return 1
	})
	_, _, _ = trayEnumThreadWindows.Call(uintptr(windows.GetCurrentThreadId()), find, 0)
	if hwnd == 0 {
		return nil, errors.New("viewer: cannot find SDL tray window")
	}
	var previous uintptr
	callback := windows.NewCallback(func(window, message, wparam, lparam uintptr) uintptr {
		if message == 0x401 && lparam&0xffff == 0x202 { // WM_TRAYICON, WM_LBUTTONUP
			handler.Handle(tray.Action{Kind: tray.ActionToggle})
			return 0
		}
		result, _, _ := trayCallWindowProc.Call(previous, window, message, wparam, lparam)
		return result
	})
	previous, _, _ = traySetWindowLong.Call(hwnd, ^uintptr(3), callback) // GWLP_WNDPROC = -4
	if previous == 0 {
		return nil, errors.New("viewer: cannot subclass SDL tray window")
	}
	return func() { _, _, _ = traySetWindowLong.Call(hwnd, ^uintptr(3), previous) }, nil
}
