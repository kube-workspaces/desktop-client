// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

// selBody lays sel out over r with text. The harness theme is the bubbly
// bitmap face at body scale 2, so one character is GlyphAdvance*2 = 12 px
// wide starting at r.X.
func selBody(sel *SelectableText, r Rect, text string) func(*Context) {
	return func(ctx *Context) { sel.Layout(ctx, r, text, SelectableStyle{}) }
}

var selRect = Rect{X: 10, Y: 10, W: 380, H: 60}

func TestSelectableDragSelectsRange(t *testing.T) {
	h := newHarness(400, 100)
	sel := &SelectableText{ID: "sel"}
	body := selBody(sel, selRect, "hello world")
	h.frame(nil, body)

	// Press inside 'l' (index 2), drag to the space (index 5), release.
	h.frame([]Event{pointer(10+2*12+1, 20, true)}, body)
	h.frame([]Event{pointer(10+5*12+1, 20, true)}, body)
	h.frame([]Event{pointer(10+5*12+1, 20, false)}, body)
	if got := sel.SelectedText(); got != "llo" {
		t.Fatalf("drag selected %q, want %q", got, "llo")
	}
}

func TestSelectableDoubleClickSelectsWord(t *testing.T) {
	h := newHarness(400, 100)
	sel := &SelectableText{ID: "sel"}
	body := selBody(sel, selRect, "hello world")
	h.frame(nil, body)

	// Two quick clicks on 'w' (index 6) select "world".
	h.click(10+6*12+6, 20, body)
	h.click(10+6*12+6, 20, body)
	if got := sel.SelectedText(); got != "world" {
		t.Fatalf("double-click selected %q, want %q", got, "world")
	}
}

func TestSelectableTripleClickSelectsAll(t *testing.T) {
	h := newHarness(400, 100)
	sel := &SelectableText{ID: "sel"}
	body := selBody(sel, selRect, "hello world")
	h.frame(nil, body)

	h.click(30, 20, body)
	h.click(30, 20, body)
	h.click(30, 20, body)
	if got := sel.SelectedText(); got != "hello world" {
		t.Fatalf("triple-click selected %q, want everything", got)
	}
}

func TestSelectableCopyKeys(t *testing.T) {
	h := newHarness(400, 100)
	var copied string
	h.ctx.SetClipboard = func(s string) error { copied = s; return nil }
	sel := &SelectableText{ID: "sel"}
	body := selBody(sel, selRect, "hello world")
	h.frame(nil, body)

	// Copy with no selection must not wipe the clipboard.
	h.frame([]Event{runeDown('c', keysym.ModControl)}, body)
	if copied != "" {
		t.Fatalf("copy with no selection wrote %q to the clipboard", copied)
	}

	h.click(10+6*12+6, 20, body)
	h.click(10+6*12+6, 20, body)
	h.frame([]Event{runeDown('c', keysym.ModControl)}, body)
	if copied != "world" {
		t.Fatalf("copy put %q on the clipboard, want %q", copied, "world")
	}

	h.frame([]Event{runeDown('a', keysym.ModControl)}, body)
	if !sel.HasSelection() || sel.SelectedText() != "hello world" {
		t.Fatalf("Ctrl-A selected %q", sel.SelectedText())
	}
}

func TestSelectableWrapsAndCopiesWholeURL(t *testing.T) {
	h := newHarness(200, 120)
	sel := &SelectableText{ID: "sel"}
	const url = "https://instance.example.com:8443/auth/callback?code=abc123"
	r := Rect{X: 10, Y: 10, W: 180, H: 100}
	body := selBody(sel, r, url)
	h.frame(nil, body)

	// A narrow rect wraps the URL over several lines; triple-click still
	// copies it whole, with no wrapped newline smuggled in.
	h.click(30, 20, body)
	h.click(30, 20, body)
	h.click(30, 20, body)
	if got := sel.SelectedText(); got != url {
		t.Fatalf("wrapped triple-click copied %q", got)
	}
	var copied string
	h.ctx.SetClipboard = func(s string) error { copied = s; return nil }
	h.frame([]Event{runeDown('c', keysym.ModControl)}, body)
	if copied != url {
		t.Fatalf("copy put %q on the clipboard", copied)
	}
}

func TestSelectableHeightMatchesBanner(t *testing.T) {
	// A screen reserves Banner height and then draws BannerSelectable into
	// it: the two must agree, or the strip overflows its reservation.
	for _, text := range []string{
		"short",
		"connection refused: dial tcp 10.0.0.4:443: connect: no route to host",
		"https://instance.example.com:8443/auth/callback?code=abc123&state=xyz",
	} {
		for _, width := range []int{400, 180, 90} {
			for _, style := range []Style{StyleBubbly, StyleClean} {
				r := Rect{X: 0, Y: 0, W: width, H: 600}
				h := newHarness(width, 600)
				h.ctx.Theme = ThemeFor(style, ModeDark)
				var plain, selectable int
				h.ctx.Begin(h.ctx.Canvas, h.in)
				plain = Banner(h.ctx, r, BannerError, text)
				h.ctx.End()
				sel := &SelectableText{}
				h.ctx.Begin(h.ctx.Canvas, h.in)
				selectable = BannerSelectable(h.ctx, r, BannerError, text, sel)
				h.ctx.End()
				if plain != selectable {
					t.Fatalf("Banner=%d vs BannerSelectable=%d for %q at width %d style %d", plain, selectable, text, width, style)
				}
			}
		}
	}
}

func TestSelectableTextChangeResetsSelection(t *testing.T) {
	h := newHarness(400, 100)
	sel := &SelectableText{ID: "sel"}
	h.frame(nil, selBody(sel, selRect, "hello world"))
	h.click(30, 20, selBody(sel, selRect, "hello world"))
	h.click(30, 20, selBody(sel, selRect, "hello world"))
	h.click(30, 20, selBody(sel, selRect, "hello world"))
	if !sel.HasSelection() {
		t.Fatal("nothing selected after triple-click")
	}
	h.frame(nil, selBody(sel, selRect, "something else entirely"))
	if sel.HasSelection() {
		t.Fatalf("stale selection %q survived a text change", sel.SelectedText())
	}
}
