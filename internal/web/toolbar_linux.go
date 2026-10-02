// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build linux && cgo

package web

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>
#include <stdint.h>
#include <stdlib.h>
extern void goWebToolbarAction(uintptr_t,int);
typedef struct {
 GtkWidget *window,*browser,*overlay,*bar,*handle,*first;
 uintptr_t token;
 gboolean full,pin,hover,drag;
 gint offset; gdouble dragx;
 gint64 until; guint timer;
 char *details;
} KWToolbar;
static void kw_reveal(KWToolbar *t) {
 t->until=g_get_monotonic_time()+3000000;
 gtk_widget_show(t->bar);gtk_widget_hide(t->handle);
}
static void kw_layout(KWToolbar *t) {
 gint w=gtk_widget_get_allocated_width(t->overlay),height=0;
 gtk_widget_get_preferred_height(t->bar,&height,NULL);
 gtk_widget_set_margin_top(t->browser,t->full?0:height);
 gint bw=t->full?MIN(w,1000):w;
 gint left=t->full?MAX(0,MIN((w-bw)/2+t->offset,w-bw)):0;
 gtk_widget_set_halign(t->bar,GTK_ALIGN_START);
 gtk_widget_set_size_request(t->bar,bw,-1);
 gtk_widget_set_margin_start(t->bar,left);
 kw_reveal(t);
}
static void kw_action(GtkWidget *button,gpointer data) {
 KWToolbar *t=data;int a=GPOINTER_TO_INT(g_object_get_data(G_OBJECT(button),"kw-action"));
 if(a==1){if(t->full)gtk_window_unfullscreen(GTK_WINDOW(t->window));else gtk_window_fullscreen(GTK_WINDOW(t->window));}
 else if(a==4){GtkWidget *d=gtk_message_dialog_new(GTK_WINDOW(t->window),GTK_DIALOG_DESTROY_WITH_PARENT,GTK_MESSAGE_INFO,GTK_BUTTONS_CLOSE,"%s",t->details);gtk_dialog_run(GTK_DIALOG(d));gtk_widget_destroy(d);}
 else if(a==5){t->pin=!t->pin;gtk_button_set_label(GTK_BUTTON(button),t->pin?"Unpin":"Pin");kw_reveal(t);}
 else goWebToolbarAction(t->token,a);
}
static gboolean kw_enter(GtkWidget *w,GdkEventCrossing *e,gpointer p){(void)w;(void)e;KWToolbar*t=p;t->hover=TRUE;kw_reveal(t);return FALSE;}
static gboolean kw_leave(GtkWidget *w,GdkEventCrossing *e,gpointer p){(void)w;(void)e;KWToolbar*t=p;t->hover=FALSE;t->until=g_get_monotonic_time()+3000000;return FALSE;}
static gboolean kw_grip(GtkWidget *w,GdkEventButton *e,gpointer p){(void)w;KWToolbar*t=p;if(e->button==1&&t->full){t->drag=e->type==GDK_BUTTON_PRESS;t->dragx=e->x_root;return TRUE;}return FALSE;}
static gboolean kw_motion(GtkWidget *w,GdkEventMotion *e,gpointer p){(void)w;KWToolbar*t=p;if(t->drag){t->offset+=(int)(e->x_root-t->dragx);t->dragx=e->x_root;kw_layout(t);return TRUE;}return FALSE;}
static gboolean kw_state(GtkWidget *w,GdkEventWindowState *e,gpointer p){(void)w;KWToolbar*t=p;t->full=(e->new_window_state&GDK_WINDOW_STATE_FULLSCREEN)!=0;kw_layout(t);return FALSE;}
static void kw_size(GtkWidget*w,GtkAllocation*a,gpointer p){(void)w;(void)a;KWToolbar*t=p;kw_layout(t);}
static gboolean kw_tick(gpointer p){KWToolbar*t=p;GtkWidget*f=gtk_window_get_focus(GTK_WINDOW(t->window));gboolean focused=f&&gtk_widget_is_ancestor(f,t->bar);if(t->full&&!t->pin&&!t->hover&&!t->drag&&!focused&&g_get_monotonic_time()>t->until){gtk_widget_hide(t->bar);gtk_widget_show(t->handle);}return G_SOURCE_CONTINUE;}
static gboolean kw_key(GtkWidget*w,GdkEventKey*e,gpointer p){(void)w;KWToolbar*t=p;
 if(e->keyval==GDK_KEY_F11){kw_action(t->first,t);return TRUE;}
 if((e->state&(GDK_CONTROL_MASK|GDK_MOD1_MASK))==(GDK_CONTROL_MASK|GDK_MOD1_MASK)&&(e->keyval==GDK_KEY_q||e->keyval==GDK_KEY_Q)){goWebToolbarAction(t->token,6);return TRUE;}
 if((e->state&(GDK_CONTROL_MASK|GDK_MOD1_MASK|GDK_SHIFT_MASK))==(GDK_CONTROL_MASK|GDK_MOD1_MASK|GDK_SHIFT_MASK)&&(e->keyval==GDK_KEY_t||e->keyval==GDK_KEY_T)){kw_reveal(t);gtk_widget_grab_focus(t->first);return TRUE;}
 return FALSE;
}
static KWToolbar* kw_attach(void *window,uintptr_t token,const char*labels,const char*details){
 KWToolbar*t=calloc(1,sizeof(*t));t->window=window;t->token=token;t->details=g_strdup(details);
 t->browser=gtk_bin_get_child(GTK_BIN(window));if(!t->browser){g_free(t->details);free(t);return NULL;}
 g_object_ref(t->window);g_object_ref(t->browser);gtk_container_remove(GTK_CONTAINER(window),t->browser);
 t->overlay=gtk_overlay_new();gtk_container_add(GTK_CONTAINER(t->overlay),t->browser);gtk_container_add(GTK_CONTAINER(window),t->overlay);
 t->bar=gtk_event_box_new();GtkWidget*row=gtk_box_new(GTK_ORIENTATION_HORIZONTAL,6);gtk_container_add(GTK_CONTAINER(t->bar),row);
 GtkWidget*grip=gtk_event_box_new();gtk_container_add(GTK_CONTAINER(grip),gtk_label_new("::"));gtk_box_pack_start(GTK_BOX(row),grip,FALSE,FALSE,6);
 gtk_widget_add_events(grip,GDK_BUTTON_PRESS_MASK|GDK_BUTTON_RELEASE_MASK|GDK_POINTER_MOTION_MASK);
 g_signal_connect(grip,"button-press-event",G_CALLBACK(kw_grip),t);g_signal_connect(grip,"button-release-event",G_CALLBACK(kw_grip),t);g_signal_connect(grip,"motion-notify-event",G_CALLBACK(kw_motion),t);
 char**parts=g_strsplit(labels,"\n",7);GtkWidget*identity=gtk_label_new(parts[0]);gtk_label_set_ellipsize(GTK_LABEL(identity),PANGO_ELLIPSIZE_END);gtk_box_pack_start(GTK_BOX(row),identity,TRUE,TRUE,6);
 for(int a=1;a<=6;a++){if(!parts[a]||!parts[a][0])continue;GtkWidget*b=gtk_button_new_with_label(parts[a]);g_object_set_data(G_OBJECT(b),"kw-action",GINT_TO_POINTER(a));g_signal_connect(b,"clicked",G_CALLBACK(kw_action),t);gtk_box_pack_start(GTK_BOX(row),b,FALSE,FALSE,0);if(a==1)t->first=b;}
 g_strfreev(parts);gtk_widget_set_valign(t->bar,GTK_ALIGN_START);gtk_overlay_add_overlay(GTK_OVERLAY(t->overlay),t->bar);
 t->handle=gtk_button_new_with_label("Tools");gtk_widget_set_halign(t->handle,GTK_ALIGN_CENTER);gtk_widget_set_valign(t->handle,GTK_ALIGN_START);gtk_overlay_add_overlay(GTK_OVERLAY(t->overlay),t->handle);
 g_signal_connect(t->bar,"enter-notify-event",G_CALLBACK(kw_enter),t);g_signal_connect(t->bar,"leave-notify-event",G_CALLBACK(kw_leave),t);g_signal_connect(t->handle,"enter-notify-event",G_CALLBACK(kw_enter),t);
 g_signal_connect(t->window,"window-state-event",G_CALLBACK(kw_state),t);g_signal_connect(t->window,"key-press-event",G_CALLBACK(kw_key),t);g_signal_connect(t->overlay,"size-allocate",G_CALLBACK(kw_size),t);
 gtk_widget_show_all(t->overlay);gtk_widget_hide(t->handle);t->until=g_get_monotonic_time()+3000000;t->timer=g_timeout_add(100,kw_tick,t);
 return t;
}
static void kw_detach(KWToolbar*t){if(!t)return;g_source_remove(t->timer);g_signal_handlers_disconnect_by_data(t->window,t);g_signal_handlers_disconnect_by_data(t->overlay,t);g_object_unref(t->browser);g_object_unref(t->window);g_free(t->details);free(t);}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func attachNativeToolbar(window unsafe.Pointer, token uintptr, labels, details string) (func(), error) {
	l, d := C.CString(labels), C.CString(details)
	defer C.free(unsafe.Pointer(l))
	defer C.free(unsafe.Pointer(d))
	t := C.kw_attach(window, C.uintptr_t(token), l, d)
	if t == nil {
		return nil, fmt.Errorf("web toolbar: browser widget unavailable")
	}
	return func() { C.kw_detach(t) }, nil
}
