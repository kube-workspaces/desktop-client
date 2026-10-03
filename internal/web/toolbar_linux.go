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
 GtkWidget *window,*browser,*overlay,*slot,*bar,*handle,*first;
 uintptr_t token;
 gboolean full,pin,hover,drag;
 gdouble dragx;
 gint top,bw,offset,placed,want,w;
 gint64 until; guint timer;
 char *details,*pinText,*unpinText,*fullscreenText,*windowedText;
} KWToolbar;
static void kw_reveal(KWToolbar *t) {
 t->until=g_get_monotonic_time()+3000000;
 gtk_widget_show(t->bar);gtk_widget_hide(t->handle);
}
// GtkOverlay positions its overlay children by halign/valign only, which cannot
// express "centred, then dragged sideways". The strip therefore rides in a box
// spanning the window and is placed with a left margin inside it, which is
// ordinary container behaviour and repaints from a real allocation.
static void kw_place(KWToolbar *t) {
 if(t->want==t->placed)return;
 t->placed=t->want;
 gtk_widget_set_margin_start(t->bar,t->placed);
}
static void kw_layout(KWToolbar *t, gint w) {
 gint height=0;
 if(w>0)t->w=w;
 gtk_widget_get_preferred_height(t->bar,&height,NULL);
 gint top=t->full?0:height;
 gint bw=t->full?MIN(t->w,1000):t->w;
 gint x=t->full?MAX(0,MIN((t->w-bw)/2+t->offset,t->w-bw)):0;
 if(top!=t->top){gtk_widget_set_margin_top(t->browser,top);t->top=top;}
 if(bw!=t->bw){gtk_widget_set_size_request(t->bar,bw,-1);t->bw=bw;}
 if(x!=t->want)t->want=x;
}
static void kw_action(GtkWidget *button,gpointer data) {
 KWToolbar *t=data;int a=GPOINTER_TO_INT(g_object_get_data(G_OBJECT(button),"kw-action"));
 if(a==1){gboolean entering=!t->full;
  if(entering)gtk_window_fullscreen(GTK_WINDOW(t->window));else gtk_window_unfullscreen(GTK_WINDOW(t->window));
  // The toggle reads the same either way, so say what pressing it again does.
  gtk_button_set_label(GTK_BUTTON(t->first),entering?t->windowedText:t->fullscreenText);}
  else if(a==4){GtkWidget *d=gtk_message_dialog_new(GTK_WINDOW(t->window),GTK_DIALOG_DESTROY_WITH_PARENT,GTK_MESSAGE_INFO,GTK_BUTTONS_CLOSE,"%s",t->details);gtk_dialog_run(GTK_DIALOG(d));gtk_widget_destroy(d);}
  else if(a==5){t->pin=!t->pin;gtk_button_set_label(GTK_BUTTON(button),t->pin?t->unpinText:t->pinText);kw_reveal(t);}
 else goWebToolbarAction(t->token,a);
}
// The reveal handle carries no action of its own: pressing it only has to put
// the strip back.
static void kw_pressed(GtkWidget *button,gpointer data){(void)button;kw_reveal((KWToolbar*)data);}
static gboolean kw_enter(GtkWidget *w,GdkEventCrossing *e,gpointer p){(void)w;(void)e;KWToolbar*t=p;t->hover=TRUE;kw_reveal(t);return FALSE;}
static gboolean kw_leave(GtkWidget *w,GdkEventCrossing *e,gpointer p){(void)w;(void)e;KWToolbar*t=p;t->hover=FALSE;t->until=g_get_monotonic_time()+3000000;return FALSE;}
static gboolean kw_grip(GtkWidget *w,GdkEventButton *e,gpointer p){(void)w;KWToolbar*t=p;if(e->button==1&&t->full){t->drag=e->type==GDK_BUTTON_PRESS;t->dragx=e->x_root;return TRUE;}return FALSE;}
static gboolean kw_motion(GtkWidget *w,GdkEventMotion *e,gpointer p){(void)w;KWToolbar*t=p;if(t->drag){t->offset+=(int)(e->x_root-t->dragx);t->dragx=e->x_root;kw_layout(t,t->w);kw_place(t);kw_reveal(t);return TRUE;}return FALSE;}
static gboolean kw_state(GtkWidget *w,GdkEventWindowState *e,gpointer p){(void)w;KWToolbar*t=p;t->full=(e->new_window_state&GDK_WINDOW_STATE_FULLSCREEN)!=0;kw_layout(t,gtk_widget_get_allocated_width(t->overlay));kw_place(t);kw_reveal(t);return FALSE;}
// "size-allocate" is emitted before the widget keeps the new allocation, so the
// width has to come from the argument or fullscreen centres against the stale one.
static void kw_size(GtkWidget*w,GtkAllocation*a,gpointer p){(void)w;KWToolbar*t=p;kw_layout(t,a->width);}
// gtk_window_get_focus answers the toplevel itself when nothing inside it
// holds focus, so the bar only counts as focused when a widget *inside* it
// does — otherwise the strip would never hide in fullscreen.
static gboolean kw_tick(gpointer p){KWToolbar*t=p;kw_place(t);GtkWidget*f=gtk_window_get_focus(GTK_WINDOW(t->window));gboolean focused=f&&f!=t->window&&gtk_widget_is_ancestor(f,t->bar);if(t->full&&!t->pin&&!t->hover&&!t->drag&&!focused&&g_get_monotonic_time()>t->until){gtk_widget_hide(t->bar);gtk_widget_show(t->handle);}return G_SOURCE_CONTINUE;}
static gboolean kw_key(GtkWidget*w,GdkEventKey*e,gpointer p){(void)w;KWToolbar*t=p;
 if(e->keyval==GDK_KEY_F11){kw_action(t->first,t);return TRUE;}
 if((e->state&(GDK_CONTROL_MASK|GDK_MOD1_MASK))==(GDK_CONTROL_MASK|GDK_MOD1_MASK)&&(e->keyval==GDK_KEY_q||e->keyval==GDK_KEY_Q)){goWebToolbarAction(t->token,6);return TRUE;}
 if((e->state&(GDK_CONTROL_MASK|GDK_MOD1_MASK|GDK_SHIFT_MASK))==(GDK_CONTROL_MASK|GDK_MOD1_MASK|GDK_SHIFT_MASK)&&(e->keyval==GDK_KEY_t||e->keyval==GDK_KEY_T)){kw_reveal(t);gtk_widget_grab_focus(t->first);return TRUE;}
 return FALSE;
}
// The bar is a plain event box, which GTK draws nothing for: over a page that
// means a transparent strip. Give it the window surface colour and a hairline
// edge so the strip reads as chrome in both windowed and fullscreen layouts.
static void kw_style(KWToolbar *t){
 GtkCssProvider *css=gtk_css_provider_new();
 gtk_css_provider_load_from_data(css,"#kw-toolbar{background-color:@theme_bg_color;background-image:none;border-bottom:1px solid alpha(@theme_fg_color,0.25);}\n",-1,NULL);
 gtk_style_context_add_provider(gtk_widget_get_style_context(t->bar),GTK_STYLE_PROVIDER(css),GTK_STYLE_PROVIDER_PRIORITY_APPLICATION);
 g_object_unref(css);
}
static KWToolbar* kw_attach(void *window,uintptr_t token,const char*labels,const char*details){
 KWToolbar*t=calloc(1,sizeof(*t));t->window=window;t->token=token;t->details=g_strdup(details);
 char**parts=g_strsplit(labels,"\n",10);
 // The alternate titles the chrome needs once it has relabelled a button in
 // place. The fallbacks only matter if a caller sends a short label list.
 t->pinText=g_strdup(parts[5]&&parts[5][0]?parts[5]:"Pin");t->unpinText=g_strdup(parts[8]&&parts[8][0]?parts[8]:"Unpin");
 t->fullscreenText=g_strdup(parts[1]&&parts[1][0]?parts[1]:"Fullscreen");t->windowedText=g_strdup(parts[9]&&parts[9][0]?parts[9]:"Windowed");
 const char *tools=(parts[7]&&parts[7][0])?parts[7]:"Tools";
 t->browser=gtk_bin_get_child(GTK_BIN(window));if(!t->browser){g_strfreev(parts);g_free(t->details);g_free(t->pinText);g_free(t->unpinText);g_free(t->fullscreenText);g_free(t->windowedText);free(t);return NULL;}
 g_object_ref(t->window);g_object_ref(t->browser);gtk_container_remove(GTK_CONTAINER(window),t->browser);
 t->overlay=gtk_overlay_new();gtk_container_add(GTK_CONTAINER(t->overlay),t->browser);gtk_container_add(GTK_CONTAINER(window),t->overlay);
 t->bar=gtk_event_box_new();gtk_widget_set_name(t->bar,"kw-toolbar");kw_style(t);
 GtkWidget*row=gtk_box_new(GTK_ORIENTATION_HORIZONTAL,6);gtk_container_add(GTK_CONTAINER(t->bar),row);
 GtkWidget*grip=gtk_event_box_new();gtk_widget_set_size_request(grip,28,-1);gtk_container_add(GTK_CONTAINER(grip),gtk_label_new("::"));gtk_box_pack_start(GTK_BOX(row),grip,FALSE,FALSE,6);
 gtk_widget_add_events(grip,GDK_BUTTON_PRESS_MASK|GDK_BUTTON_RELEASE_MASK|GDK_POINTER_MOTION_MASK);
 g_signal_connect(grip,"button-press-event",G_CALLBACK(kw_grip),t);g_signal_connect(grip,"button-release-event",G_CALLBACK(kw_grip),t);g_signal_connect(grip,"motion-notify-event",G_CALLBACK(kw_motion),t);
 GtkWidget*identity=gtk_label_new(parts[0]);gtk_label_set_ellipsize(GTK_LABEL(identity),PANGO_ELLIPSIZE_END);gtk_box_pack_start(GTK_BOX(row),identity,TRUE,TRUE,6);
 for(int a=1;a<=6;a++){if(!parts[a]||!parts[a][0])continue;GtkWidget*b=gtk_button_new_with_label(parts[a]);g_object_set_data(G_OBJECT(b),"kw-action",GINT_TO_POINTER(a));g_signal_connect(b,"clicked",G_CALLBACK(kw_action),t);gtk_box_pack_start(GTK_BOX(row),b,FALSE,FALSE,0);if(a==1)t->first=b;}
 t->slot=gtk_box_new(GTK_ORIENTATION_HORIZONTAL,0);gtk_box_pack_start(GTK_BOX(t->slot),t->bar,FALSE,FALSE,0);
 gtk_widget_set_halign(t->bar,GTK_ALIGN_START);gtk_widget_set_valign(t->bar,GTK_ALIGN_FILL);
 gtk_widget_set_valign(t->slot,GTK_ALIGN_START);gtk_widget_set_halign(t->slot,GTK_ALIGN_FILL);
 gtk_overlay_add_overlay(GTK_OVERLAY(t->overlay),t->slot);
 // Read the handle's label out of parts before the split goes away.
 t->handle=gtk_button_new_with_label(tools);g_strfreev(parts);gtk_widget_set_halign(t->handle,GTK_ALIGN_CENTER);gtk_widget_set_valign(t->handle,GTK_ALIGN_START);gtk_overlay_add_overlay(GTK_OVERLAY(t->overlay),t->handle);
 g_signal_connect(t->bar,"enter-notify-event",G_CALLBACK(kw_enter),t);g_signal_connect(t->bar,"leave-notify-event",G_CALLBACK(kw_leave),t);g_signal_connect(t->handle,"enter-notify-event",G_CALLBACK(kw_enter),t);
 // A pointer with no hover to reveal from — a trackpad tap — still gets the
 // strip back by pressing the handle, as the other two platforms do.
 g_signal_connect(t->handle,"clicked",G_CALLBACK(kw_pressed),t);
 g_signal_connect(t->window,"window-state-event",G_CALLBACK(kw_state),t);g_signal_connect(t->window,"key-press-event",G_CALLBACK(kw_key),t);g_signal_connect(t->overlay,"size-allocate",G_CALLBACK(kw_size),t);
 gtk_widget_show_all(t->overlay);gtk_widget_hide(t->handle);t->until=g_get_monotonic_time()+3000000;t->timer=g_timeout_add(100,kw_tick,t);
 return t;
}
static void kw_detach(KWToolbar*t){if(!t)return;g_source_remove(t->timer);g_signal_handlers_disconnect_by_data(t->window,t);g_signal_handlers_disconnect_by_data(t->overlay,t);g_object_unref(t->browser);g_object_unref(t->window);g_free(t->details);g_free(t->pinText);g_free(t->unpinText);g_free(t->fullscreenText);g_free(t->windowedText);free(t);}
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
