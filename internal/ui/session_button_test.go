// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import "testing"

// TestSessionsButtonCountBadge: the toolbar entry is an icon-only quiet
// button like Add workspace, wearing an accent count bubble in its top-right
// corner — present exactly while a session is held, and inside the button so
// it never bleeds into the toolbar or the neighbour's hit area.
func TestSessionsButtonCountBadge(t *testing.T) {
	th := ThemeFor(StyleClean, ModeDark)
	h := newHarness(160, 80)
	h.ctx.Theme = th
	r := Rect{X: 10, Y: 10, W: th.ControlHeight, H: th.ControlHeight}

	accentIn := func() (n, topRight int) {
		img := h.ctx.Canvas.Image()
		for y := r.Y; y < r.Y+r.H; y++ {
			for x := r.X; x < r.X+r.W; x++ {
				if img.RGBAAt(x, y) != th.Accent {
					continue
				}
				n++
				if x >= r.X+r.W/2 && y < r.Y+r.H/2 {
					topRight++
				}
			}
		}
		return n, topRight
	}

	fired := 0
	draw := func(count int) func(*Context) {
		return func(ctx *Context) {
			if (SessionsButton{ID: "s", Count: count}).Layout(ctx, r) {
				fired++
			}
		}
	}

	h.frame(nil, draw(0))
	if n, _ := accentIn(); n != 0 {
		t.Fatalf("zero sessions drew %d badge pixels, want none", n)
	}

	h.frame(nil, draw(3))
	n, topRight := accentIn()
	if n == 0 {
		t.Fatal("a held session drew no count badge")
	}
	if topRight == 0 {
		t.Fatal("the count badge is not in the button's top-right corner")
	}

	h.click(r.X+r.W/2, r.Y+r.H/2, draw(3))
	if fired != 1 {
		t.Fatalf("clicking the button fired %d times, want 1", fired)
	}
}
