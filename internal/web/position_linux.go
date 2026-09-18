// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build linux && cgo

package web

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>
*/
import "C"
import "unsafe"

// centerOnLaunchDisplay moves the freshly created GTK window so its centre
// lands in the middle of the launch display — the monitor the shell is on,
// whose usable physical-pixel bounds it forwarded in [launchDisplayEnv].
//
// SDL's usable bounds and the webview child's own size are physical screen
// pixels, but GTK's move API is in logical pixels. GTK keeps a per-window
// scale factor (1 on a 100% desk, 2 on a 200% one), so the physical
// top-left is divided back by that factor before gtk_window_move. The
// division drops a scale-unit the way GTK itself does, which on a mixed-DPI
// desk costs at most a pixel. The window was already shown by the cgo
// constructor, so the move is visible from the start rather than flashing
// up centred on the primary display first.
func centerOnLaunchDisplay(win unsafe.Pointer, b launchBounds) {
	if win == nil {
		return
	}
	widget := (*C.GtkWidget)(unsafe.Pointer(win))
	scale := C.gtk_widget_get_scale_factor(widget)
	if scale <= 0 {
		scale = 1
	}
	var w, h C.gint
	C.gtk_window_get_size((*C.GtkWindow)(unsafe.Pointer(widget)), &w, &h)
	if w <= 0 || h <= 0 {
		return
	}
	x, y := centeredPosition(b, int(w)*int(scale), int(h)*int(scale))
	C.gtk_window_move((*C.GtkWindow)(unsafe.Pointer(widget)), C.gint(x)/scale, C.gint(y)/scale)
}
