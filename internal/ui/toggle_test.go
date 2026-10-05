// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

func TestToggleActivationAndBusyState(t *testing.T) {
	h := newHarness(300, 100)
	switcher := Toggle{ID: "startup", Label: "Start at login"}
	r := Rect{X: 10, Y: 10, W: 280, H: 44}
	changes := 0
	body := func(ctx *Context) {
		if switcher.Layout(ctx, r) {
			changes++
		}
	}
	h.frame(nil, body)
	// Both the label and the trailing switch activate the same preference.
	h.click(40, 30, body)
	if !switcher.Checked || changes != 1 {
		t.Fatal("label click did not enable the switch exactly once")
	}
	h.click(260, 30, body)
	if switcher.Checked || changes != 2 {
		t.Fatal("track click did not disable the switch exactly once")
	}
	h.frame([]Event{keyDown(keysym.KeyReturn, keysym.ModNone)}, body)
	h.frame([]Event{runeDown(' ', keysym.ModNone)}, body)
	if switcher.Checked || changes != 4 {
		t.Fatal("Enter and Space did not toggle the focused switch")
	}
	h.frame([]Event{pointer(40, 30, true)}, body)
	h.frame([]Event{pointer(299, 90, false)}, body)
	if changes != 4 {
		t.Fatal("cancelled pointer gesture toggled the switch")
	}
	switcher.Disabled, switcher.Busy = true, true
	h.click(260, 30, body)
	h.frame([]Event{keyDown(keysym.KeyReturn, keysym.ModNone)}, body)
	if switcher.Checked || changes != 4 {
		t.Fatal("busy switch accepted activation")
	}
	if _, scheduled := h.ctx.RepaintDelay(); !scheduled {
		t.Fatal("busy indicator did not schedule a repaint")
	}
}
