// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build linux && cgo

package web

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>
static gboolean kw_geometry_transition(void *win){return g_object_get_data(G_OBJECT(win),"kw-geometry-transition")!=NULL;}
*/
import "C"
import (
	"unsafe"

	"github.com/kube-workspaces/desktop-client/internal/config"
)

func nativeGeometry(win unsafe.Pointer) (config.WindowRect, bool) {
	w := (*C.GtkWindow)(win)
	if C.kw_geometry_transition(win) != 0 {
		return config.WindowRect{}, false
	}
	gw := C.gtk_widget_get_window((*C.GtkWidget)(win))
	if gw == nil || C.gtk_widget_get_mapped((*C.GtkWidget)(win)) == 0 {
		return config.WindowRect{}, false
	}
	state := C.gdk_window_get_state(gw)
	if state&(C.GDK_WINDOW_STATE_FULLSCREEN|C.GDK_WINDOW_STATE_MAXIMIZED|C.GDK_WINDOW_STATE_ICONIFIED) != 0 {
		return config.WindowRect{}, false
	}
	var x, y, width, height C.gint
	C.gtk_window_get_position(w, &x, &y)
	C.gtk_window_get_size(w, &width, &height)
	r := config.WindowRect{X: int(x), Y: int(y), Width: int(width), Height: int(height)}
	return r, r.Valid()
}

func restoreNativeGeometry(win unsafe.Pointer, r config.WindowRect) {
	display := C.gtk_widget_get_display((*C.GtkWidget)(win))
	current := C.gdk_display_get_monitor_at_window(display, C.gtk_widget_get_window((*C.GtkWidget)(win)))
	var frame C.GdkRectangle
	C.gdk_window_get_frame_extents(C.gtk_widget_get_window((*C.GtkWidget)(win)), &frame)
	var width, height C.gint
	C.gtk_window_get_size((*C.GtkWindow)(win), &width, &height)
	// GTK positions the decorated frame but sizes the client area. Reserve the
	// current decorations when fitting that client size into a monitor workarea.
	borderW, borderH := max(0, int(frame.width-width)), max(0, int(frame.height-height))
	var monitors []config.WindowRect
	fallback := 0
	for i := C.int(0); i < C.gdk_display_get_n_monitors(display); i++ {
		m := C.gdk_display_get_monitor(display, i)
		if m == current {
			fallback = int(i)
		}
		var b C.GdkRectangle
		C.gdk_monitor_get_workarea(m, &b)
		monitors = append(monitors, config.WindowRect{X: int(b.x), Y: int(b.y), Width: int(b.width) - borderW, Height: int(b.height) - borderH})
	}
	if fitted, ok := fitGeometry(r, monitors, fallback); ok {
		C.gtk_window_resize((*C.GtkWindow)(win), C.gint(fitted.Width), C.gint(fitted.Height))
		C.gtk_window_move((*C.GtkWindow)(win), C.gint(fitted.X), C.gint(fitted.Y))
	}
}

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
