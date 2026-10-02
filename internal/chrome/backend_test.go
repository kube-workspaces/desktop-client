// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package chrome

import (
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

type surface struct {
	viewer.Backend
	w, h   int
	full   bool
	frame  viewer.Rect
	status viewer.Overlay
	chrome viewer.Rect
}

func (s *surface) Open(o viewer.WindowOptions) error {
	s.w, s.h, s.full = o.Width, o.Height, o.Fullscreen
	return nil
}
func (s *surface) Size() (int, int)                            { return s.w, s.h }
func (s *surface) ScaleFactor() float64                        { return 1 }
func (s *surface) Fullscreen() bool                            { return s.full }
func (s *surface) SetFullscreen(v bool) error                  { s.full = v; return nil }
func (s *surface) SetKeyboardGrab(bool) error                  { return nil }
func (s *surface) SetCursor(*viewer.CursorShape) error         { return nil }
func (s *surface) SetChromeSize(int, int) error                { return nil }
func (s *surface) UploadChrome(viewer.Rect, []byte, int) error { return nil }
func (s *surface) PresentChrome(r viewer.Rect, o viewer.Overlay, c viewer.Rect) error {
	s.frame, s.status, s.chrome = r, o, c
	return nil
}

func rig(t *testing.T, full bool) (*Backend, *surface, time.Time) {
	t.Helper()
	s := &surface{}
	b := New(s)
	if err := b.Open(viewer.WindowOptions{Width: 1280, Height: 720, Fullscreen: full}); err != nil {
		t.Fatal(err)
	}
	b.UpdateConnection(connection.Snapshot{Workspace: "team/vm", Surface: connection.Desktop, State: connection.Connected})
	now := time.Now()
	b.FilterConnectionEvents(now, nil)
	return b, s, now
}

func TestToolbarClickNeverReachesGuestAndDispatchesDisconnect(t *testing.T) {
	b, _, now := rig(t, false)
	r := b.layout.Bar
	// Disconnect is the rightmost button in every layout.
	x, y := r.X+r.W-b.ctx.Theme.Pad-10, r.Y+b.ctx.Theme.Pad+10
	out := b.FilterConnectionEvents(now, []viewer.Event{
		viewer.EventPointer{X: x, Y: y, Buttons: viewer.ButtonLeft}, viewer.EventPointer{X: x, Y: y},
	})
	actions := 0
	for _, ev := range out {
		switch e := ev.(type) {
		case viewer.EventPointer:
			t.Fatal("chrome click leaked to guest")
		case viewer.EventConnectionAction:
			if e.Action != connection.Disconnect {
				t.Fatalf("action=%s", e.Action)
			}
			actions++
		}
	}
	if actions != 1 {
		t.Fatalf("disconnect actions=%d", actions)
	}
}

func TestGuestDragCrossingToolbarKeepsItsRelease(t *testing.T) {
	b, _, now := rig(t, false)
	y := b.layout.Content.Y
	out := b.FilterConnectionEvents(now, []viewer.Event{
		viewer.EventPointer{X: 100, Y: y + 100, Buttons: viewer.ButtonLeft},
		viewer.EventPointer{X: 100, Y: 10, Buttons: viewer.ButtonLeft},
		viewer.EventPointer{X: 100, Y: 10},
	})
	if len(out) != 3 {
		t.Fatalf("drag got %v", out)
	}
	if out[0].(viewer.EventPointer).Y != 100 || out[2].(viewer.EventPointer).Buttons != 0 {
		t.Fatal("mapping/release broken")
	}
}

func TestFullscreenHideRevealAndMenuDoNotChangeContentGeometry(t *testing.T) {
	b, _, now := rig(t, true)
	w, h := b.Size()
	b.FilterConnectionEvents(now.Add(5*time.Second), nil)
	if !b.hidden {
		t.Fatal("fullscreen bar did not auto-hide")
	}
	b.FilterConnectionEvents(now.Add(6*time.Second), []viewer.Event{viewer.EventPointer{X: b.layout.Bar.X + 10, Y: 5}})
	if b.hidden {
		t.Fatal("handle hover did not reveal toolbar")
	}
	b.bar.Menu = "tools"
	b.render(now.Add(20*time.Second), nil)
	if b.hidden {
		t.Fatal("open menu auto-hid")
	}
	if nw, nh := b.Size(); nw != w || nh != h {
		t.Fatal("reveal changed content size")
	}
	b.bar.Pinned = true
	b.bar.Menu = ""
	b.render(now.Add(30*time.Second), nil)
	if b.hidden {
		t.Fatal("pinned toolbar auto-hid")
	}
}

func TestToolbarKeyboardGestureSwallowsKeyUpAfterEscape(t *testing.T) {
	b, _, now := rig(t, false)
	mods := keysym.ModControl | keysym.ModAlt | keysym.ModShift
	b.FilterConnectionEvents(now, []viewer.Event{viewer.EventKey{Rune: 't', Mods: mods, Down: true}})
	out := b.FilterConnectionEvents(now, []viewer.Event{
		viewer.EventKey{Key: keysym.KeyEscape, Down: true},
		viewer.EventKey{Rune: 't'}, viewer.EventKey{Key: keysym.KeyEscape},
	})
	for _, e := range out {
		if _, ok := e.(viewer.EventKey); ok {
			t.Fatal("local key release leaked after focus return")
		}
	}
	if b.local {
		t.Fatal("Esc did not return input focus")
	}
}

func TestStatusAndChromeAreSeparateLayers(t *testing.T) {
	b, s, _ := rig(t, false)
	y := b.layout.Content.Y
	if err := b.Present(viewer.Rect{W: 100, H: 100}, viewer.Overlay{Dim: 100, Rect: viewer.Rect{X: 10, Y: 20, W: 50, H: 30}}); err != nil {
		t.Fatal(err)
	}
	if s.frame.Y != y || s.status.Rect.Y != 20+y || s.chrome.Empty() {
		t.Fatal("content/status/chrome composition incorrect")
	}
}
