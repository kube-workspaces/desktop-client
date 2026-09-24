// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

const (
	listRowHeight = 20
	listRows      = 5 // 100 / 20
)

var listRect = Rect{X: 0, Y: 0, W: 200, H: listRowHeight * listRows}

// listHarness lays a list out over count rows, frame by frame.
type listHarness struct {
	*harness
	list      *List
	count     int
	activated int
	drawn     []int
}

func newListHarness(count int) *listHarness {
	h := &listHarness{
		harness: newHarness(listRect.W, listRect.H),
		list:    &List{ID: "list", RowHeight: listRowHeight},
		count:   count,
	}
	h.list.Selected = 0
	// Give the list the keyboard from the outset: the tests are about what it
	// does with key presses, not about how it comes by the focus.
	h.ctx.Focus().Set("list")
	return h
}

func (h *listHarness) step(events ...Event) {
	h.frame(events, func(ctx *Context) {
		h.drawn = h.drawn[:0]
		if h.list.Layout(ctx, listRect, h.count, func(_ *Context, _ Rect, state RowState) {
			h.drawn = append(h.drawn, state.Index)
		}) {
			h.activated++
		}
	})
}

func TestListKeyboardSelection(t *testing.T) {
	h := newListHarness(20)
	h.step()
	if h.list.Visible() != listRows {
		t.Fatalf("%d rows visible, want %d", h.list.Visible(), listRows)
	}

	h.step(keyDown(keysym.KeyDown, keysym.ModNone), keyDown(keysym.KeyDown, keysym.ModNone))
	if h.list.Selected != 2 {
		t.Fatalf("two Downs selected row %d, want 2", h.list.Selected)
	}
	if h.list.Offset != 0 {
		t.Fatalf("the view scrolled (offset %d) while the selection was still visible", h.list.Offset)
	}

	// Moving past the bottom scrolls by the minimum needed, not by a page.
	h.step(keyDown(keysym.KeyDown, keysym.ModNone), keyDown(keysym.KeyDown, keysym.ModNone),
		keyDown(keysym.KeyDown, keysym.ModNone))
	if h.list.Selected != 5 || h.list.Offset != 1 {
		t.Fatalf("selected %d offset %d, want 5 and 1", h.list.Selected, h.list.Offset)
	}

	h.step(keyDown(keysym.KeyHome, keysym.ModNone))
	if h.list.Selected != 0 || h.list.Offset != 0 {
		t.Fatalf("Home left selected=%d offset=%d", h.list.Selected, h.list.Offset)
	}

	h.step(keyDown(keysym.KeyEnd, keysym.ModNone))
	if h.list.Selected != 19 {
		t.Fatalf("End selected %d, want 19", h.list.Selected)
	}
	if h.list.Offset != 20-listRows {
		t.Fatalf("End left offset %d, want %d", h.list.Offset, 20-listRows)
	}

	// The selection clamps at both ends rather than wrapping: a list that
	// wraps makes "hold Down to get to the bottom" overshoot silently.
	h.step(keyDown(keysym.KeyDown, keysym.ModNone))
	if h.list.Selected != 19 {
		t.Fatalf("Down at the end wrapped to %d", h.list.Selected)
	}
	h.step(keyDown(keysym.KeyHome, keysym.ModNone), keyDown(keysym.KeyUp, keysym.ModNone))
	if h.list.Selected != 0 {
		t.Fatalf("Up at the start wrapped to %d", h.list.Selected)
	}
}

func TestListPageKeys(t *testing.T) {
	h := newListHarness(50)
	h.step()
	h.step(keyDown(keysym.KeyPageDown, keysym.ModNone))
	// A page is one row short of the view, so the row the user was looking at
	// stays on screen as an anchor.
	if want := listRows - 1; h.list.Selected != want {
		t.Fatalf("PageDown selected %d, want %d", h.list.Selected, want)
	}
	h.step(keyDown(keysym.KeyPageUp, keysym.ModNone))
	if h.list.Selected != 0 {
		t.Fatalf("PageUp selected %d, want 0", h.list.Selected)
	}
}

func TestListWheelScrollsWithoutMovingTheSelection(t *testing.T) {
	h := newListHarness(20)
	h.step()
	h.step(pointer(50, 50, false))

	h.step(EventWheel{DY: -1})
	if h.list.Offset != 3 {
		t.Fatalf("one wheel click scrolled to offset %d, want 3 rows", h.list.Offset)
	}
	if h.list.Selected != 0 {
		t.Fatal("the wheel moved the selection; it should only move the view")
	}
	if h.drawn[0] != 3 {
		t.Fatalf("the first drawn row is %d, want 3", h.drawn[0])
	}

	// Scrolling cannot run off either end.
	for i := 0; i < 20; i++ {
		h.step(EventWheel{DY: -1})
	}
	if h.list.Offset != 20-listRows {
		t.Fatalf("scrolling past the end left offset %d, want %d", h.list.Offset, 20-listRows)
	}
	for i := 0; i < 30; i++ {
		h.step(EventWheel{DY: 1})
	}
	if h.list.Offset != 0 {
		t.Fatalf("scrolling past the start left offset %d", h.list.Offset)
	}

	// A wheel outside the list belongs to whatever is under the pointer.
	h.step(pointer(500, 500, false))
	h.step(EventWheel{DY: -1})
	if h.list.Offset != 0 {
		t.Fatal("the list scrolled for a wheel event outside it")
	}
}

func TestListMouseSelectionAndActivation(t *testing.T) {
	h := newListHarness(20)
	h.step()

	// A single click selects.
	h.step(pointer(50, 2*listRowHeight+5, true))
	h.step(pointer(50, 2*listRowHeight+5, false))
	if h.list.Selected != 2 {
		t.Fatalf("clicking the third row selected %d", h.list.Selected)
	}
	if h.activated != 0 {
		t.Fatal("a single click activated a row; opening an exclusive display should take more than that")
	}

	// A second click soon afterwards activates.
	h.step(pointer(50, 2*listRowHeight+5, true))
	h.step(pointer(50, 2*listRowHeight+5, false))
	if h.activated != 1 {
		t.Fatalf("a double click produced %d activations, want 1", h.activated)
	}

	// Two clicks far apart in time do not.
	h.activated = 0
	h.step(pointer(50, 5, true))
	h.step(pointer(50, 5, false))
	h.now = h.now.Add(2 * time.Second)
	h.step(pointer(50, 5, true))
	h.step(pointer(50, 5, false))
	if h.activated != 0 {
		t.Fatal("two slow clicks counted as a double click")
	}

	// A click in the empty space below the last row selects nothing new.
	short := newListHarness(2)
	short.step()
	short.step(pointer(50, 4*listRowHeight+5, true))
	short.step(pointer(50, 4*listRowHeight+5, false))
	if short.list.Selected != 0 {
		t.Fatalf("clicking past the last row selected %d", short.list.Selected)
	}
}

func TestListEnterActivatesTheSelection(t *testing.T) {
	h := newListHarness(20)
	h.step()
	h.step(keyDown(keysym.KeyDown, keysym.ModNone))
	h.step(keyDown(keysym.KeyReturn, keysym.ModNone))
	if h.activated != 1 {
		t.Fatalf("Enter produced %d activations, want 1", h.activated)
	}
	if h.list.Selected != 1 {
		t.Fatalf("Enter moved the selection to %d", h.list.Selected)
	}

	// An unfocused list ignores the keyboard.
	h.ctx.Focus().Set("elsewhere")
	h.activated = 0
	h.step(keyDown(keysym.KeyReturn, keysym.ModNone))
	if h.activated != 0 {
		t.Fatal("an unfocused list answered Enter")
	}
}

func TestListHandlesEmptyAndShrinkingContent(t *testing.T) {
	h := newListHarness(0)
	h.step()
	if h.list.Selected != -1 {
		t.Fatalf("an empty list has selection %d, want -1", h.list.Selected)
	}
	h.step(keyDown(keysym.KeyDown, keysym.ModNone), keyDown(keysym.KeyReturn, keysym.ModNone))
	if h.activated != 0 {
		t.Fatal("an empty list activated a row")
	}

	// A list that shrinks under a selection near its end — which is exactly
	// what a background refresh does when a workspace is deleted — must clamp
	// rather than index past the data.
	h.count = 20
	h.step()
	h.step(keyDown(keysym.KeyEnd, keysym.ModNone))
	h.count = 3
	h.step()
	if h.list.Selected != 2 {
		t.Fatalf("after shrinking, selection is %d, want 2", h.list.Selected)
	}
	if h.list.Offset != 0 {
		t.Fatalf("after shrinking, offset is %d, want 0", h.list.Offset)
	}
	if len(h.drawn) != 3 {
		t.Fatalf("drew %d rows for a 3-row list", len(h.drawn))
	}
}

func TestListDrawsOnlyVisibleRows(t *testing.T) {
	h := newListHarness(1000)
	h.step()
	if len(h.drawn) != listRows {
		t.Fatalf("drew %d rows of 1000; a list must not rasterise what is off screen", len(h.drawn))
	}
}

func TestListScrollbarDragScrollsTheView(t *testing.T) {
	h := newListHarness(20)
	h.step()
	if h.list.Selected != 0 {
		t.Fatalf("initial selection %d, want 0", h.list.Selected)
	}

	// Grab the thumb at the top of the track (offset 0, thumb spans the first
	// 25px) and drag it halfway down: with a 75px span over 15 offsets that
	// landing puts the view at row 9, and it must not touch the selection.
	h.step(pointer(listRect.W-4, 5, true))
	h.step(pointer(listRect.W-4, 50, true))
	if h.list.Offset != 9 {
		t.Fatalf("dragging to mid-track left offset %d, want 9", h.list.Offset)
	}
	if h.list.Selected != 0 {
		t.Fatalf("dragging the scrollbar moved the selection to %d", h.list.Selected)
	}

	// Still held, the thumb keeps following the pointer and clamps at both
	// ends of the track.
	h.step(pointer(listRect.W-4, 200, true))
	if h.list.Offset != 15 {
		t.Fatalf("dragging past the bottom left offset %d, want %d", h.list.Offset, 15)
	}
	h.step(pointer(listRect.W-4, 0, true))
	if h.list.Offset != 0 {
		t.Fatalf("dragging past the top left offset %d, want 0", h.list.Offset)
	}

	// Releasing where the gesture began ends it without selecting a row.
	h.step(pointer(listRect.W-4, 0, false))
	if h.list.Selected != 0 || h.activated != 0 {
		t.Fatalf("releasing a drag selected row %d with %d activations", h.list.Selected, h.activated)
	}
}

func TestListScrollbarTrackClickPages(t *testing.T) {
	h := newListHarness(20)
	h.step()

	// Pressing the track below the thumb (which covers 0..25 at offset 0)
	// pages down one view, anchoring the row the user was looking at.
	h.step(pointer(listRect.W-4, 90, true))
	h.step(pointer(listRect.W-4, 90, false))
	if h.list.Offset != 4 {
		t.Fatalf("track click left offset %d, want 4", h.list.Offset)
	}
	if h.list.Selected != 0 {
		t.Fatalf("track click moved the selection to %d", h.list.Selected)
	}

	// Above the thumb it pages back, as far as the start.
	h.step(pointer(listRect.W-4, 5, true))
	h.step(pointer(listRect.W-4, 5, false))
	if h.list.Offset != 0 {
		t.Fatalf("reverse track click left offset %d, want 0", h.list.Offset)
	}
}

func TestListWithoutOverflowHasNoScrollbar(t *testing.T) {
	h := newListHarness(2)
	h.step()

	// With every row visible there is no scrollbar and no hit band, so the
	// right edge is just a row: clicking there selects it.
	h.step(pointer(listRect.W-2, listRowHeight+5, true))
	h.step(pointer(listRect.W-2, listRowHeight+5, false))
	if h.list.Selected != 1 {
		t.Fatalf("clicking the right edge selected %d, want 1", h.list.Selected)
	}
	if h.activated != 0 {
		t.Fatal("a single click at the right edge activated a row")
	}
}
