// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build windows && cgo

package web

/*
#cgo LDFLAGS: -luser32 -lgdi32
#include <windows.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
extern void goWebToolbarAction(uintptr_t,int);
typedef struct {
 HWND win,web,bar,handle,identity,buttons[7];
 WNDPROC original;uintptr_t token;
 BOOL full,pin,drag;LONG_PTR style;RECT saved;
 int offset,dragx;ULONGLONG until;wchar_t *details,*pinText,*unpinText,*fullscreenText,*windowedText;
} KWToolbar;
static int kw_min(int a,int b){return a<b?a:b;}
static int kw_max(int a,int b){return a>b?a:b;}
static wchar_t *kw_wide(const char *s){int n=MultiByteToWideChar(CP_UTF8,0,s,-1,NULL,0);wchar_t *r=calloc(n,sizeof(wchar_t));MultiByteToWideChar(CP_UTF8,0,s,-1,r,n);return r;}
static int kw_scale(KWToolbar *t,int n){HDC dc=GetDC(t->win);int dpi=GetDeviceCaps(dc,LOGPIXELSX);ReleaseDC(t->win,dc);return MulDiv(n,dpi,96);}
static void kw_reveal(KWToolbar *t){t->until=GetTickCount64()+3000;ShowWindow(t->bar,SW_SHOW);ShowWindow(t->handle,SW_HIDE);}
// The webview_widget child is ours to place: webview_go refills the whole
// client area on WM_SIZE, so this runs after the original window procedure.
// The reveal handle keeps the visibility kw_reveal and the idle timer give it:
// the strip and the handle are never on screen at the same time.
static void kw_layout(KWToolbar *t){RECT r;GetClientRect(t->win,&r);
 int bh=kw_scale(t,44),w=r.right,h=r.bottom,bw=t->full?kw_min(w,kw_scale(t,1040)):w,left=t->full?kw_max(0,kw_min((w-bw)/2+t->offset,w-bw)):0;
 SetWindowPos(t->web,NULL,0,t->full?0:bh,w,kw_max(1,h-(t->full?0:bh)),SWP_NOZORDER|SWP_NOACTIVATE);
 SetWindowPos(t->bar,HWND_TOP,left,0,bw,bh,SWP_NOACTIVATE);
 SetWindowPos(t->handle,HWND_TOP,kw_max(0,(w-kw_scale(t,100))/2),0,kw_scale(t,100),kw_scale(t,22),SWP_NOACTIVATE);
 int x=bw-kw_scale(t,6);for(int a=6;a>=1;a--){if(!t->buttons[a])continue;int width=kw_scale(t,a==3?112:90);x-=width;MoveWindow(t->buttons[a],x,kw_scale(t,7),width,kw_scale(t,30),TRUE);x-=kw_scale(t,4);}
 MoveWindow(t->identity,kw_scale(t,24),kw_scale(t,13),kw_max(0,x-kw_scale(t,28)),kw_scale(t,24),TRUE);
}
static void kw_action(KWToolbar *t,int a){
 if(a==1){t->full=!t->full;
  if(t->full){t->style=GetWindowLongPtrW(t->win,GWL_STYLE);GetWindowRect(t->win,&t->saved);
   MONITORINFO mi={sizeof(mi)};GetMonitorInfoW(MonitorFromWindow(t->win,MONITOR_DEFAULTTONEAREST),&mi);
   SetWindowLongPtrW(t->win,GWL_STYLE,WS_POPUP|WS_VISIBLE);
   SetWindowPos(t->win,HWND_TOP,mi.rcMonitor.left,mi.rcMonitor.top,mi.rcMonitor.right-mi.rcMonitor.left,mi.rcMonitor.bottom-mi.rcMonitor.top,SWP_FRAMECHANGED);}
  else{SetWindowLongPtrW(t->win,GWL_STYLE,t->style);
   SetWindowPos(t->win,NULL,t->saved.left,t->saved.top,t->saved.right-t->saved.left,t->saved.bottom-t->saved.top,SWP_FRAMECHANGED|SWP_NOZORDER);}
  SetWindowTextW(t->buttons[1],t->full?t->windowedText:t->fullscreenText);kw_layout(t);kw_reveal(t);}
 else if(a==4)MessageBoxW(t->win,t->details,L"Connection",MB_OK|MB_ICONINFORMATION);
 else if(a==5){t->pin=!t->pin;SetWindowTextW(t->buttons[5],t->pin?t->unpinText:t->pinText);kw_reveal(t);}
 else if(a==7)kw_reveal(t);
 else goWebToolbarAction(t->token,a);
}
// WebView2 owns its own accelerator pipeline, so the window hotkeys are
// registered for the process rather than intercepted from the key stream.
static void kw_hotkeys(KWToolbar *t,BOOL on){
 if(on){RegisterHotKey(t->win,12305,MOD_NOREPEAT,VK_F11);RegisterHotKey(t->win,12306,MOD_CONTROL|MOD_ALT|MOD_SHIFT|MOD_NOREPEAT,'T');RegisterHotKey(t->win,12307,MOD_CONTROL|MOD_ALT|MOD_NOREPEAT,'Q');}
 else{UnregisterHotKey(t->win,12305);UnregisterHotKey(t->win,12306);UnregisterHotKey(t->win,12307);}
}
static LRESULT CALLBACK kw_bar_proc(HWND hwnd,UINT msg,WPARAM wp,LPARAM lp){KWToolbar *t=(KWToolbar*)GetWindowLongPtrW(hwnd,GWLP_USERDATA);if(!t)return DefWindowProcW(hwnd,msg,wp,lp);
 if(msg==WM_COMMAND&&HIWORD(wp)==BN_CLICKED){kw_action(t,LOWORD(wp));return 0;}
 if(msg==WM_LBUTTONDOWN&&t->full){t->drag=TRUE;t->dragx=(short)LOWORD(lp);SetCapture(hwnd);return 0;}
 if(msg==WM_MOUSEMOVE&&t->drag){POINT p;GetCursorPos(&p);ScreenToClient(t->win,&p);
  RECT r;GetWindowRect(t->bar,&r);POINT start={r.left,r.top};ScreenToClient(t->win,&start);
  t->offset+=p.x-start.x-t->dragx;kw_layout(t);kw_reveal(t);return 0;}
 if(msg==WM_LBUTTONUP&&t->drag){t->drag=FALSE;ReleaseCapture();return 0;}
 return DefWindowProcW(hwnd,msg,wp,lp);
}
static LRESULT CALLBACK kw_window_proc(HWND hwnd,UINT msg,WPARAM wp,LPARAM lp){KWToolbar *t=GetPropW(hwnd,L"KW_TOOLBAR");if(!t)return DefWindowProcW(hwnd,msg,wp,lp);
 if(msg==WM_COMMAND&&LOWORD(wp)==7){kw_action(t,7);return 0;}
 if(msg==WM_HOTKEY){if(wp==12305)kw_action(t,1);else if(wp==12307)kw_action(t,6);else{kw_reveal(t);SetFocus(t->buttons[1]);}return 0;}
 if(msg==WM_TIMER&&wp==12304){POINT p;GetCursorPos(&p);RECT r;GetWindowRect(t->bar,&r);
  BOOL hover=PtInRect(&r,p);RECT hr;GetWindowRect(t->handle,&hr);if(PtInRect(&hr,p))hover=TRUE;
  HWND f=GetFocus();BOOL focused=f==t->bar||IsChild(t->bar,f);
  if(hover||focused)t->until=GetTickCount64()+3000;
  if(t->full&&!t->pin&&!t->drag&&!focused&&!hover&&GetTickCount64()>t->until){ShowWindow(t->bar,SW_HIDE);ShowWindow(t->handle,SW_SHOW);}return 0;}
 LRESULT result=CallWindowProcW(t->original,hwnd,msg,wp,lp);
 if(msg==WM_SIZE||msg==WM_DPICHANGED)kw_layout(t);
 return result;
}
static KWToolbar *kw_attach(void *win,uintptr_t token,const char *labels,const char *details){
 HWND window=win;HWND web=FindWindowExW(window,NULL,L"webview_widget",NULL);if(!web)return NULL;
 KWToolbar *t=calloc(1,sizeof(*t));t->win=window;t->web=web;t->token=token;t->details=kw_wide(details);
 t->bar=CreateWindowExW(WS_EX_CONTROLPARENT,L"STATIC",L"",WS_CHILD|WS_VISIBLE,0,0,0,0,window,NULL,GetModuleHandleW(NULL),NULL);
 SetWindowLongPtrW(t->bar,GWLP_USERDATA,(LONG_PTR)t);SetWindowLongPtrW(t->bar,GWLP_WNDPROC,(LONG_PTR)kw_bar_proc);
 char *copy=_strdup(labels);char *parts[10];int n=0;parts[n++]=copy;
 for(char *p=copy;*p&&n<10;p++)if(*p=='\n'){*p=0;parts[n++]=p+1;}
 // The alternate titles the chrome needs once it has relabelled a button in
 // place. The fallbacks only matter if a caller sends a short label list.
 char *pinText=n>5&&parts[5][0]?parts[5]:"Pin",*unpinText=n>8&&parts[8][0]?parts[8]:"Unpin";
 char *fullscreenText=n>1&&parts[1][0]?parts[1]:"Fullscreen",*windowedText=n>9&&parts[9][0]?parts[9]:"Windowed";
 char *tools=n>7&&parts[7][0]?parts[7]:"Tools";
 wchar_t *toolswide=kw_wide(tools);
 t->pinText=kw_wide(pinText);t->unpinText=kw_wide(unpinText);t->fullscreenText=kw_wide(fullscreenText);t->windowedText=kw_wide(windowedText);
 wchar_t *title=kw_wide(parts[0]);t->identity=CreateWindowExW(0,L"STATIC",title,WS_CHILD|WS_VISIBLE|SS_LEFTNOWORDWRAP|SS_ENDELLIPSIS,0,0,0,0,t->bar,NULL,NULL,NULL);free(title);
 for(int a=1;a<=6;a++){if(a>=n||!parts[a][0])continue;
  wchar_t *text=kw_wide(parts[a]);t->buttons[a]=CreateWindowExW(0,L"BUTTON",text,WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON,0,0,0,0,t->bar,(HMENU)(intptr_t)a,NULL,NULL);
  SendMessageW(t->buttons[a],WM_SETFONT,(WPARAM)GetStockObject(DEFAULT_GUI_FONT),TRUE);free(text);}
 free(copy);
 t->handle=CreateWindowExW(0,L"BUTTON",toolswide,WS_CHILD|WS_TABSTOP,0,0,0,0,window,(HMENU)7,NULL,NULL);free(toolswide);
 SendMessageW(t->handle,WM_SETFONT,(WPARAM)GetStockObject(DEFAULT_GUI_FONT),TRUE);
 SetPropW(window,L"KW_TOOLBAR",t);t->original=(WNDPROC)SetWindowLongPtrW(window,GWLP_WNDPROC,(LONG_PTR)kw_window_proc);
 SetTimer(window,12304,100,NULL);kw_hotkeys(t,TRUE);kw_layout(t);kw_reveal(t);ShowWindow(t->bar,SW_SHOW);return t;
}
static void kw_detach(KWToolbar *t){if(!t)return;
 if(IsWindow(t->win)){kw_hotkeys(t,FALSE);KillTimer(t->win,12304);SetWindowLongPtrW(t->win,GWLP_WNDPROC,(LONG_PTR)t->original);RemovePropW(t->win,L"KW_TOOLBAR");DestroyWindow(t->bar);DestroyWindow(t->handle);}
 free(t->details);free(t->pinText);free(t->unpinText);free(t->fullscreenText);free(t->windowedText);free(t);
}
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
		return nil, fmt.Errorf("web toolbar: WebView2 widget unavailable")
	}
	return func() { C.kw_detach(t) }, nil
}
