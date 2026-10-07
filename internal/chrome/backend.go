// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package chrome decorates a session content backend with client-owned tools.
// It shares the shell's software widgets while keeping viewer free of UI imports.
package chrome

import (
	"fmt"
	"image"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

const hideDelay = 3 * time.Second

// Backend presents a virtual content surface below windowed chrome. Fullscreen
// chrome floats without changing that surface. All state except Wake is owned
// by the window thread, exactly like viewer.Backend.
type Backend struct {
	viewer.Backend
	bar                        ui.ConnectionToolbar
	ctx                        *ui.Context
	input                      ui.Input
	snapshot                   connection.Snapshot
	layout                     ui.ToolbarLayout
	image                      *image.RGBA
	clip                       viewer.Rect
	textureW, textureH         int
	dirty                      bool
	local                      bool
	guestButtons, localButtons viewer.Buttons
	swallow                    map[viewer.EventKey]bool
	until                      time.Time
	hidden                     bool
	drag                       bool
	dragX                      int
	navigate                   func(connection.Action)
	style                      ui.Style
	mode                       ui.Mode
	lastScale                  float64
	guestCursor                *viewer.CursorShape
}

func New(be viewer.Backend) *Backend {
	return &Backend{Backend: be, ctx: ui.NewContext(ui.ThemeFor(ui.StyleClean, ui.ModeDark)),
		swallow: make(map[viewer.EventKey]bool), dirty: true, style: ui.StyleClean,
		snapshot: connection.Snapshot{Surface: connection.Desktop, State: connection.Connecting}}
}

func (b *Backend) NativeBackend() viewer.Backend { return b.Backend }

func (b *Backend) ConnectionNeedsPresent() bool { return b.dirty }

func (b *Backend) SetNavigation(fn func(connection.Action)) { b.navigate = fn }

func (b *Backend) SetTheme(style ui.Style, mode ui.Mode) {
	if b.style != style || b.mode != mode {
		b.style, b.mode, b.dirty = style, mode, true
	}
}

func (b *Backend) Open(opts viewer.WindowOptions) error {
	// Preserve the requested content height instead of taking rows from it.
	if !opts.Fullscreen {
		opts.Height += b.ctx.Theme.ControlHeight + 2*b.ctx.Theme.Pad
	}
	if err := b.Backend.Open(opts); err != nil {
		return err
	}
	b.until = time.Now().Add(hideDelay)
	b.render(time.Now(), nil)
	return nil
}

func (b *Backend) UpdateConnection(s connection.Snapshot) {
	s.Capabilities.Shell = b.navigate != nil
	s.Fullscreen = b.Fullscreen()
	if s != b.snapshot {
		b.snapshot, b.dirty = s, true
	}
}

func (b *Backend) render(now time.Time, events []viewer.Event) connection.Action {
	w, h := b.Backend.Size()
	if w <= 0 || h <= 0 {
		return ""
	}
	scale := b.ScaleFactor()
	if scale != b.lastScale || b.dirty {
		b.ctx.Theme = ui.ThemeFor(b.style, b.mode).Scaled(scale)
		b.lastScale = scale
	}
	if b.image == nil || b.image.Bounds().Dx() != w || b.image.Bounds().Dy() != h {
		b.image = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	clear(b.image.Pix)
	b.input = b.input.Fold(now, events)
	b.ctx.Begin(ui.NewCanvas(b.image), b.input)
	// Layout is executed once against input; the action is returned to the
	// session loop, never run while rendering or presenting a GPU frame.
	var action connection.Action
	b.layout, action = b.bar.Layout(b.ctx, ui.Rect{W: w, H: h}, b.snapshot)
	b.ctx.End()
	b.hidden = b.snapshot.Fullscreen && !b.bar.Pinned && !b.local && b.bar.Menu == "" && !b.drag && !now.Before(b.until)
	if b.hidden {
		clear(b.image.Pix)
		th := b.ctx.Theme
		r := ui.Rect{X: b.layout.Bar.X + b.layout.Bar.W/2 - 70*th.Small, Y: 0, W: 140 * th.Small, H: max(12, th.ControlHeight/2)}
		b.ctx.Canvas.FillRounded(r, th.Radius, th.Surface)
		ui.Label(b.ctx, r, "Tools", ui.LabelStyle{Scale: th.Small, Align: ui.AlignCenter, Middle: true})
		b.layout.Bar, b.layout.Panel, b.layout.Grip = r, ui.Rect{}, ui.Rect{}
	}
	r := b.layout.Bar
	if p := b.layout.Panel; p.W > 0 {
		x, y := min(r.X, p.X), min(r.Y, p.Y)
		r = ui.Rect{X: x, Y: y, W: max(r.X+r.W, p.X+p.W) - x, H: max(r.Y+r.H, p.Y+p.H) - y}
	}
	b.clip = viewer.Rect{X: r.X, Y: r.Y, W: r.W, H: min(r.H, h-r.Y)}
	b.dirty = true // the cached raster needs one upload
	return action
}

func keyID(e viewer.EventKey) viewer.EventKey { return viewer.EventKey{Key: e.Key, Rune: e.Rune} }

func (b *Backend) setLocal(on bool, out *[]viewer.Event) {
	if b.local == on {
		return
	}
	b.local = on
	*out = append(*out, viewer.EventFocus{Gained: !on})
	if on {
		_ = b.Backend.SetKeyboardGrab(false)
		_ = b.Backend.SetCursor(nil)
	} else {
		_ = b.Backend.SetCursor(b.guestCursor)
	}
}

func (b *Backend) FilterConnectionEvents(now time.Time, events []viewer.Event) []viewer.Event {
	b.snapshot.Fullscreen = b.Fullscreen()
	if b.dirty || b.snapshot.Fullscreen && !b.hidden && !now.Before(b.until) {
		b.render(now, nil)
	}
	var out []viewer.Event
	for _, ev := range events {
		local := false
		switch e := ev.(type) {
		case viewer.EventResize:
			b.render(now, nil)
			w, h := b.Size()
			out = append(out, viewer.EventResize{W: w, H: h})
			continue
		case viewer.EventFocus:
			if !e.Gained {
				b.local, b.drag = false, false
				b.localButtons, b.guestButtons = 0, 0
				clear(b.swallow)
				b.bar.Menu = ""
			}
		case viewer.EventPointer:
			p := ui.Point{X: e.X, Y: e.Y}
			inside := b.layout.Bar.Contains(p.X, p.Y) || b.layout.Panel.Contains(p.X, p.Y)
			if b.guestButtons != 0 {
				b.guestButtons = e.Buttons
			} else if inside || b.localButtons != 0 || b.drag {
				local = true
				b.until = now.Add(hideDelay)
				if e.Buttons != 0 {
					b.setLocal(true, &out)
				}
				if b.snapshot.Fullscreen && !b.hidden && b.layout.Grip.Contains(p.X, p.Y) && b.localButtons == 0 && e.Buttons.Has(viewer.ButtonLeft) {
					b.drag, b.dragX = true, e.X
				}
				if b.drag {
					b.bar.Offset += e.X - b.dragX
					b.dragX = e.X
					if e.Buttons == 0 {
						b.drag = false
					}
				}
				b.localButtons = e.Buttons
			} else {
				if e.Buttons != 0 {
					b.bar.Menu = ""
					b.setLocal(false, &out)
				}
				b.guestButtons = e.Buttons
			}
			if !local {
				e.Y -= b.layout.Content.Y
				out = append(out, e)
				continue
			}
		case viewer.EventWheel:
			local = b.local || b.localButtons != 0
		case viewer.EventText:
			local = b.local
		case viewer.EventKey:
			id := keyID(e)
			if !e.Down && b.swallow[id] {
				delete(b.swallow, id)
				continue
			}
			if e.Down && (e.Rune == 't' || e.Rune == 'T') && e.Mods.Has(keysym.ModControl|keysym.ModAlt|keysym.ModShift) {
				b.swallow[id] = true
				b.until = now.Add(hideDelay)
				b.setLocal(true, &out)
				b.render(now, nil)
				continue
			}
			local = b.local
			if local && e.Down {
				b.swallow[id] = true
				if !e.Repeat && (e.Key == keysym.KeyF11 || (e.Rune == 'q' || e.Rune == 'Q') && e.Mods.Has(keysym.ModControl|keysym.ModAlt)) {
					a := connection.Fullscreen
					if e.Key != keysym.KeyF11 {
						a = connection.Disconnect
					}
					out = append(out, viewer.EventConnectionAction{Action: a})
					continue
				}
				if e.Key == keysym.KeyEscape {
					b.bar.Menu = ""
					b.until = now.Add(hideDelay)
					b.setLocal(false, &out)
					b.render(now, nil)
					continue
				}
			}
		}
		if local {
			// Capture one local event at a time to preserve multiple gestures
			// in one SDL batch. Local releases never leak back to the guest.
			a := b.render(now, []viewer.Event{ev})
			if a == connection.Sessions || a == connection.WorkspaceList {
				if b.navigate != nil {
					b.navigate(a)
				}
			} else if a == connection.HostInput {
				// Hand input back the way Esc does: the grab is released,
				// the guest cursor returns, and focus goes downstream.
				b.setLocal(false, &out)
				b.render(now, nil)
			} else if a != "" {
				if a == connection.TypeClipboard {
					// Keyboard injection targets the guest's last focused field.
					// Restore downstream focus before dispatching the command.
					b.bar.Menu = ""
					b.setLocal(false, &out)
					b.render(now, nil)
				}
				out = append(out, viewer.EventConnectionAction{Action: a})
			}
			continue
		}
		out = append(out, ev)
	}
	return out
}

func (b *Backend) Size() (int, int) {
	if b.image == nil {
		b.render(time.Now(), nil)
	}
	w, h := b.Backend.Size()
	if b.Fullscreen() {
		return w, h
	}
	return w, max(1, h-b.layout.Content.Y)
}

func (b *Backend) SetSize(w, h int) error {
	if !b.Fullscreen() {
		h += b.layout.Content.Y
	}
	err := b.Backend.SetSize(w, h)
	b.render(time.Now(), nil)
	return err
}

func (b *Backend) SetFullscreen(on bool) error {
	if err := b.Backend.SetFullscreen(on); err != nil {
		return err
	}
	b.snapshot.Fullscreen = on
	b.until = time.Now().Add(hideDelay)
	b.render(time.Now(), nil)
	return nil
}

func (b *Backend) SetKeyboardGrab(on bool) error { return b.Backend.SetKeyboardGrab(on && !b.local) }

func (b *Backend) SetCursor(s *viewer.CursorShape) error {
	b.guestCursor = s
	if b.local {
		return b.Backend.SetCursor(nil)
	}
	return b.Backend.SetCursor(s)
}

func (b *Backend) Present(frame viewer.Rect, ov viewer.Overlay) error {
	y := b.layout.Content.Y
	if !frame.Empty() {
		frame.Y += y
	}
	if !ov.Rect.Empty() {
		ov.Rect.Y += y
	}
	if p, ok := b.Backend.(viewer.ChromePresenter); ok {
		if b.dirty && !b.clip.Empty() {
			r := b.clip
			img := image.NewRGBA(image.Rect(0, 0, r.W, r.H))
			for row := 0; row < r.H; row++ {
				src := (r.Y+row)*b.image.Stride + r.X*4
				copy(img.Pix[row*img.Stride:], b.image.Pix[src:src+r.W*4])
			}
			if r.W != b.textureW || r.H != b.textureH {
				if err := p.SetChromeSize(r.W, r.H); err != nil {
					return err
				}
				b.textureW, b.textureH = r.W, r.H
			}
			if err := p.UploadChrome(viewer.Rect{W: r.W, H: r.H}, img.Pix, img.Stride); err != nil {
				return err
			}
			b.dirty = false
		}
		return p.PresentChrome(frame, ov, b.clip)
	}
	return b.Backend.Present(frame, ov)
}

// Preserve optional audio/system-theme capabilities through the decorator.
func (b *Backend) OpenAudio(f viewer.AudioFormat) error {
	if a, ok := b.Backend.(viewer.AudioSink); ok {
		return a.OpenAudio(f)
	}
	return fmt.Errorf("audio output unavailable")
}
func (b *Backend) PlayPCM(p []byte) {
	if a, ok := b.Backend.(viewer.AudioSink); ok {
		a.PlayPCM(p)
	}
}
func (b *Backend) CloseAudio() {
	if a, ok := b.Backend.(viewer.AudioSink); ok {
		a.CloseAudio()
	}
}
