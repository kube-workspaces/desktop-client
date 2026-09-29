// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

var linkRect = Rect{X: 10, Y: 10, W: 200, H: 30}

func TestLinkHitTesting(t *testing.T) {
	h := newHarness(300, 100)
	l := &Link{Text: "https://example.com"}

	fired := 0
	body := func(ctx *Context) {
		if l.Layout(ctx, linkRect) {
			fired++
		}
	}

	h.click(50, 20, body)
	if fired != 1 {
		t.Fatalf("a click inside the link fired %d times, want 1", fired)
	}

	// Outside on both axes, and press-inside-release-outside.
	fired = 0
	for _, p := range [][2]int{{5, 20}, {50, 5}, {215, 20}, {50, 45}} {
		h.click(p[0], p[1], body)
	}
	h.frame([]Event{pointer(50, 20, true)}, body)
	h.frame([]Event{pointer(250, 80, false)}, body)
	if fired != 0 {
		t.Fatalf("clicks outside the link fired %d times", fired)
	}
}

func TestLinkKeyboardActivation(t *testing.T) {
	h := newHarness(300, 100)
	l := &Link{ID: "go", Text: "https://example.com"}

	fired := 0
	body := func(ctx *Context) {
		if l.Layout(ctx, linkRect) {
			fired++
		}
	}

	h.frame(nil, body)
	if !h.ctx.Focused("go") {
		t.Fatal("the only focusable widget did not receive the focus")
	}

	h.frame([]Event{keyDown(keysym.KeyReturn, keysym.ModNone)}, body)
	h.frame([]Event{runeDown(' ', keysym.ModNone)}, body)
	if fired != 2 {
		t.Fatalf("Enter and Space fired %d activations, want 2", fired)
	}
}

func TestLinkWidthFitsItsLabel(t *testing.T) {
	h := newHarness(300, 100)
	l := &Link{Text: "https://example.com"}
	var w int
	h.frame(nil, func(ctx *Context) { w = l.Width(ctx) })
	if w <= 0 || w > 300 {
		t.Fatalf("link width = %d, want a positive fit", w)
	}
}
