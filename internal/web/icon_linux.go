// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build linux && cgo

package web

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>

static void kw_window_icon(void *window, const guchar *png, gsize size) {
 GdkPixbufLoader *loader = gdk_pixbuf_loader_new();
 GError *error = NULL;
 gboolean written = gdk_pixbuf_loader_write(loader, png, size, &error);
 if (error) { g_error_free(error); error = NULL; }
 gboolean closed = gdk_pixbuf_loader_close(loader, &error);
 if (written && closed) {
  GdkPixbuf *icon = gdk_pixbuf_loader_get_pixbuf(loader);
  if (icon) gtk_window_set_icon(GTK_WINDOW(window), icon);
 }
 if (error) g_error_free(error);
 // GtkWindow keeps its own reference to the pixbuf.
 g_object_unref(loader);
}
*/
import "C"

import (
	"unsafe"

	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

func setNativeWindowIcon(win unsafe.Pointer) {
	png := viewer.AppIcon()
	if win != nil && len(png) > 0 {
		// The loader consumes the bytes synchronously; GTK retains only the
		// decoded pixbuf, never a pointer into Go memory.
		C.kw_window_icon(win, (*C.guchar)(unsafe.Pointer(&png[0])), C.gsize(len(png)))
	}
}
