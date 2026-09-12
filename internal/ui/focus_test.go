// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

func TestFocusRingMovesThroughTheRegisteredOrder(t *testing.T) {
	var ring FocusRing
	register := func(ids ...FocusID) {
		ring.Begin()
		for _, id := range ids {
			ring.Register(id)
		}
	}

	register("a", "b", "c")
	if ring.Focus() != NoFocus {
		t.Fatal("registering gave something the focus by itself")
	}

	// A forward move with nothing focused lands on the first entry; a
	// backward one on the last. That is what Tab into a fresh screen does.
	ring.Move(1)
	if ring.Focus() != "a" {
		t.Fatalf("first Tab focused %q, want \"a\"", ring.Focus())
	}
	ring.Move(1)
	ring.Move(1)
	if ring.Focus() != "c" {
		t.Fatalf("three Tabs reached %q, want \"c\"", ring.Focus())
	}
	ring.Move(1)
	if ring.Focus() != "a" {
		t.Fatalf("Tab did not wrap; it reached %q", ring.Focus())
	}
	ring.Move(-1)
	if ring.Focus() != "c" {
		t.Fatalf("Shift-Tab did not wrap backwards; it reached %q", ring.Focus())
	}

	// The focus is by identity, so the order changing underneath it does not
	// move it: this is what stops a control appearing above the field the
	// user is typing in from stealing the keyboard.
	register("x", "a", "y", "c")
	if ring.Focus() != "c" {
		t.Fatalf("re-registering moved the focus to %q", ring.Focus())
	}
	ring.Move(1)
	if ring.Focus() != "x" {
		t.Fatalf("after the order changed, Tab reached %q, want \"x\"", ring.Focus())
	}

	// A focused widget that is no longer on screen cannot be traversed from.
	ring.Set("gone")
	ring.Move(1)
	if ring.Focus() != "x" {
		t.Fatalf("Tab from a stale focus reached %q, want the first entry", ring.Focus())
	}

	ring.Clear()
	if ring.Focus() != NoFocus || len(ring.Order()) != 0 {
		t.Fatal("Clear left state behind")
	}
	// Moving through an empty ring must not panic or invent a focus.
	ring.Move(1)
	if ring.Focus() != NoFocus {
		t.Fatal("an empty ring produced a focus")
	}
}

func TestFocusRingNormalise(t *testing.T) {
	var ring FocusRing
	ring.Begin()
	ring.Register("a")
	ring.Register("b")

	if !ring.Normalise() || ring.Focus() != "a" {
		t.Fatalf("Normalise focused %q, want \"a\"", ring.Focus())
	}
	if ring.Normalise() {
		t.Fatal("Normalise reported a change when the focus was already valid")
	}

	ring.Set("vanished")
	if !ring.Normalise() || ring.Focus() != "a" {
		t.Fatal("Normalise did not rescue a stale focus")
	}

	// Nothing registered: there is nothing to focus and nothing to report.
	ring.Begin()
	if ring.Normalise() {
		t.Fatal("Normalise invented a focus for an empty screen")
	}
	if ring.Register(NoFocus); len(ring.Order()) != 0 {
		t.Fatal("the empty id was registered as a stop")
	}
}

// TestTabTraversalThroughAContext checks the whole path: widgets register
// themselves as they are laid out, so the traversal order is the layout order,
// and Tab is resolved at the end of the frame once the order is complete.
func TestTabTraversalThroughAContext(t *testing.T) {
	h := newHarness(400, 300)
	email := &TextInput{ID: "email"}
	password := &TextInput{ID: "password"}
	signIn := &Button{ID: "sign-in", Text: "Sign in"}

	body := func(ctx *Context) {
		email.Layout(ctx, Rect{X: 10, Y: 10, W: 300, H: 30})
		password.Layout(ctx, Rect{X: 10, Y: 50, W: 300, H: 30})
		signIn.Layout(ctx, Rect{X: 10, Y: 90, W: 120, H: 30})
	}

	h.frame(nil, body)
	if h.ctx.Focus().Focus() != "email" {
		t.Fatalf("the first frame focused %q, want the first field", h.ctx.Focus().Focus())
	}

	want := []FocusID{"password", "sign-in", "email", "password"}
	for i, id := range want {
		h.frame([]Event{keyDown(keysym.KeyTab, keysym.ModNone)}, body)
		if got := h.ctx.Focus().Focus(); got != id {
			t.Fatalf("Tab %d focused %q, want %q", i+1, got, id)
		}
	}

	for _, id := range []FocusID{"email", "sign-in", "password"} {
		h.frame([]Event{keyDown(keysym.KeyTab, keysym.ModShift)}, body)
		if got := h.ctx.Focus().Focus(); got != id {
			t.Fatalf("Shift-Tab focused %q, want %q", got, id)
		}
	}

	// Typing goes to the focused field and nowhere else.
	h.ctx.Focus().Set("email")
	h.frame([]Event{runeDown('a', keysym.ModNone)}, body)
	if email.Value() != "a" || password.Value() != "" {
		t.Fatalf("typing reached the wrong field: email=%q password=%q", email.Value(), password.Value())
	}
}

func TestPointerTakesTheFocus(t *testing.T) {
	h := newHarness(400, 300)
	email := &TextInput{ID: "email"}
	password := &TextInput{ID: "password"}
	body := func(ctx *Context) {
		email.Layout(ctx, Rect{X: 10, Y: 10, W: 300, H: 30})
		password.Layout(ctx, Rect{X: 10, Y: 50, W: 300, H: 30})
	}

	h.frame(nil, body)
	h.frame([]Event{pointer(50, 60, true)}, body)
	if h.ctx.Focus().Focus() != "password" {
		t.Fatalf("clicking the second field focused %q", h.ctx.Focus().Focus())
	}

	// Clicking the background leaves the focus where it was, rather than
	// dropping it and leaving the screen with no keyboard interface.
	h.frame([]Event{pointer(380, 280, true)}, body)
	if h.ctx.Focus().Focus() != "password" {
		t.Fatalf("clicking the background moved the focus to %q", h.ctx.Focus().Focus())
	}
}

func TestContextRepaintRequests(t *testing.T) {
	h := newHarness(100, 100)
	h.frame(nil, func(*Context) {})
	if h.ctx.NeedsRepaint() {
		t.Fatal("an empty frame asked to be drawn again")
	}

	h.frame(nil, func(ctx *Context) { ctx.Repaint() })
	if !h.ctx.NeedsRepaint() {
		t.Fatal("Repaint was not recorded")
	}

	h.frame(nil, func(ctx *Context) {
		ctx.RepaintAfter(500)
		ctx.RepaintAfter(100)
		ctx.RepaintAfter(900)
	})
	if d, ok := h.ctx.RepaintDelay(); !ok || d != 100 {
		t.Fatalf("RepaintDelay = (%v, %t), want the shortest request", d, ok)
	}
}
