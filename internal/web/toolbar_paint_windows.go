// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build windows && cgo

package web

/*
#cgo LDFLAGS: -lgdi32
#include <windows.h>
#include <stdint.h>
static void kw_toolbar_pixels(uintptr_t dc, int w, int h, void *pixels) {
 BITMAPINFO info = {0};
 info.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
 info.bmiHeader.biWidth = w; info.bmiHeader.biHeight = -h;
 info.bmiHeader.biPlanes = 1; info.bmiHeader.biBitCount = 32;
 info.bmiHeader.biCompression = BI_RGB;
 SetDIBitsToDevice((HDC)dc, 0, 0, w, h, 0, 0, 0, h, pixels, &info, DIB_RGB_COLORS);
}
*/
import "C"

import (
	"image"
	"runtime/cgo"
	unicode16 "unicode/utf16"
	"unsafe"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"golang.org/x/sys/windows/registry"
)

type toolbarPainter struct {
	theme *ui.Theme
	scale float64
}

func windowsToolbarPainter() *toolbarPainter {
	style, mode, scale := ui.StyleBubbly, ui.ModeSystem, 0.0
	if cfg, err := config.Load(); err == nil {
		if s, ok := ui.ParseStyle(cfg.Settings.Style); ok {
			style = s
		}
		if m, ok := ui.ParseMode(cfg.Settings.Mode); ok {
			mode = m
		}
		scale = cfg.Settings.UIScale
	}
	if mode == ui.ModeSystem {
		mode = ui.ModeDark // Same fallback as the VM toolbar for an unknown host theme.
		key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
		if err == nil {
			if light, _, err := key.GetIntegerValue("AppsUseLightTheme"); err == nil && light != 0 {
				mode = ui.ModeLight
			}
			_ = key.Close()
		}
	}
	return &toolbarPainter{theme: ui.ThemeFor(style, mode), scale: scale}
}

// raster uses the very same palette, fonts and Button/Label widgets as the VM
// toolbar. Native BUTTONs still own keyboard, mouse, accessibility and actions;
// only their painting is replaced, so Windows' white theme cannot leak through.
func (p *toolbarPainter) raster(text string, kind, state, w, h, dpi int) *image.RGBA {
	scale := p.scale
	if scale <= 0 {
		scale = float64(dpi) / 96
	}
	th := p.theme.Scaled(scale)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	canvas := ui.NewCanvas(img)
	r := ui.Rect{W: w, H: h}
	canvas.Fill(r, th.Surface)
	ctx := ui.NewContext(th)
	in := ui.Input{}
	if state&(0x0001|0x0040) != 0 { // ODS_SELECTED / hover
		in.HasMouse, in.Mouse = true, ui.Point{X: w / 2, Y: h / 2}
		in.Down, in.PressOrigin = state&0x0001 != 0, in.Mouse
	}
	ctx.Begin(canvas, in)
	switch {
	case kind < 0: // The strip itself.
		canvas.StrokeRounded(r, th.Radius, th.BorderWidth, th.Border)
	case kind == 0 || kind == 7: // Identity / fullscreen reveal handle.
		align := ui.AlignLeft
		if kind == 7 {
			align = ui.AlignCenter
		}
		ui.Label(ctx, r, text, ui.LabelStyle{Scale: th.Small, Align: align, Middle: true})
	default:
		b := ui.Button{ID: "web-toolbar", Text: text, Variant: ui.ButtonSecondary, Scale: th.Small, Disabled: state&0x0004 != 0}
		if kind == 6 {
			b.Variant = ui.ButtonDanger
		}
		if state&0x0010 != 0 { // ODS_FOCUS
			ctx.Focus().Set(b.ID)
		}
		b.Layout(ctx, r)
	}
	ctx.End()
	return img
}

//export goWebToolbarPaint
func goWebToolbarPaint(handle C.uintptr_t, dc C.uintptr_t, w, h C.int, label *C.uint16_t, length, kind, state, dpi C.int) {
	if w <= 0 || h <= 0 {
		return
	}
	text := string(unicode16.Decode(unsafe.Slice((*uint16)(unsafe.Pointer(label)), int(length))))
	p := cgo.Handle(handle).Value().(*toolbarPainter)
	img := p.raster(text, int(kind), int(state), int(w), int(h), int(dpi))
	// A top-down Win32 DIB is BGRA, while the shared software canvas is RGBA.
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+2] = img.Pix[i+2], img.Pix[i]
	}
	C.kw_toolbar_pixels(dc, w, h, unsafe.Pointer(&img.Pix[0]))
}
