// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import "testing"

func TestRaisedButtonRenderingAndCancellation(t *testing.T) {
	for _, mode := range []Mode{ModeDark, ModeLight, ModeHighContrast} {
		t.Run(mode.String(), func(t *testing.T) {
			h := newHarness(200, 100)
			h.ctx.Theme = ThemeFor(StyleClean, mode)
			th := h.ctx.Theme
			b := Button{Raised: true, Text: "Open"}
			fired := 0
			draw := func(ctx *Context) {
				ctx.Canvas.Fill(ctx.Canvas.Bounds(), th.Background)
				if b.Layout(ctx, buttonRect) {
					fired++
				}
			}
			img := h.ctx.Canvas.Image()
			h.frame(nil, draw)
			// A black shadow is invisible on the high-contrast black background.
			if mode != ModeHighContrast && img.RGBAAt(30, 44) == th.Background {
				t.Fatal("raised button has no shadow below its face")
			}
			h.frame([]Event{pointer(30, 20, false)}, draw)
			if img.RGBAAt(30, 20) != th.AccentHover {
				t.Fatal("hover face does not use the hover colour")
			}
			h.frame([]Event{pointer(30, 20, true)}, draw)
			if img.RGBAAt(30, 20) != th.AccentPressed || img.RGBAAt(30, 10) != th.Background {
				t.Fatal("held face must darken and move down")
			}
			if img.RGBAAt(30, 44) != th.AccentPressed {
				t.Fatal("pressed face must land in the former shadow")
			}
			// Visual travel must not move the activation rectangle.
			h.frame([]Event{pointer(30, 44, false)}, draw)
			if fired != 0 {
				t.Fatal("release outside original bounds activated the button")
			}
			b.Disabled = true
			h.click(30, 20, draw)
			if fired != 0 || img.RGBAAt(30, 44) != th.Background {
				t.Fatal("disabled button activated or retained raised chrome")
			}
		})
	}
}
