// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build windows && cgo

package web

/*
#cgo LDFLAGS: -luser32
#include <windows.h>

static void kw_window_icon(void *window) {
 HINSTANCE instance = GetModuleHandleW(NULL);
 // Both executables' winres.json name the icon group "1", not numeric #1.
 // Explorer uses it automatically, but webview's window class does not.
 HICON small = (HICON)LoadImageW(instance, L"1", IMAGE_ICON,
  GetSystemMetrics(SM_CXSMICON), GetSystemMetrics(SM_CYSMICON), LR_SHARED);
 HICON large = (HICON)LoadImageW(instance, L"1", IMAGE_ICON,
  GetSystemMetrics(SM_CXICON), GetSystemMetrics(SM_CYICON), LR_SHARED);
 if (small) SendMessageW((HWND)window, WM_SETICON, ICON_SMALL, (LPARAM)small);
 if (large) SendMessageW((HWND)window, WM_SETICON, ICON_BIG, (LPARAM)large);
 // LR_SHARED icons are owned by the module and must not be destroyed.
}
*/
import "C"

import "unsafe"

func setNativeWindowIcon(win unsafe.Pointer) {
	if win != nil {
		C.kw_window_icon(win)
	}
}
