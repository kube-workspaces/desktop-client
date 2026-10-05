// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego/objc"
	"github.com/kube-workspaces/desktop-client/internal/tray"
)

type cocoaTrayClick struct {
	item, menu objc.ID
	handler    tray.Handler
}

// All access happens on the Cocoa/SDL main thread.
var cocoaTrayClicks = make(map[objc.ID]cocoaTrayClick)

var cocoaTrayClickClass = sync.OnceValues(func() (objc.Class, error) {
	return objc.RegisterClass("KWTrayClickTarget", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{{
		Cmd: objc.RegisterName("click:"),
		Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
			click, ok := cocoaTrayClicks[self]
			if !ok {
				return
			}
			app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
			event := app.Send(objc.RegisterName("currentEvent"))
			if event.Send(objc.RegisterName("type")) == 4 { // NSEventTypeRightMouseUp
				click.item.Send(objc.RegisterName("popUpStatusItemMenu:"), click.menu)
			} else {
				click.handler.Handle(tray.Action{Kind: tray.ActionToggle})
			}
		},
	}})
})

func installTrayClicks(native unsafe.Pointer, handler tray.Handler) (func(), error) {
	class, err := cocoaTrayClickClass()
	if err != nil {
		return nil, err
	}
	// SDL 3.4's Cocoa SDL_Tray begins with NSStatusBar*, NSStatusItem*.
	// This adapter is tied to the bundled SDL version, like the Win32
	// WM_TRAYICON adapter; review it when upgrading go-sdl3/binsdl.
	prefix := (*struct{ bar, item objc.ID })(native)
	item := prefix.item
	menu := item.Send(objc.RegisterName("menu"))
	menu.Send(objc.RegisterName("retain"))
	button := item.Send(objc.RegisterName("button"))
	target := objc.ID(class).Send(objc.RegisterName("new"))
	cocoaTrayClicks[target] = cocoaTrayClick{item: item, menu: menu, handler: handler}
	// An attached menu intercepts clicks before the button's action. Keep
	// SDL's menu, but present it explicitly only for right-button releases.
	item.Send(objc.RegisterName("setMenu:"), objc.ID(0))
	button.Send(objc.RegisterName("setTarget:"), target)
	button.Send(objc.RegisterName("setAction:"), objc.RegisterName("click:"))
	button.Send(objc.RegisterName("sendActionOn:"), uintptr(1<<2|1<<4))
	return func() {
		button.Send(objc.RegisterName("setTarget:"), objc.ID(0))
		button.Send(objc.RegisterName("setAction:"), objc.SEL(0))
		item.Send(objc.RegisterName("setMenu:"), menu)
		menu.Send(objc.RegisterName("release"))
		delete(cocoaTrayClicks, target)
		target.Send(objc.RegisterName("release"))
	}, nil
}
