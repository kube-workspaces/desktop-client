// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

// This is the only file in the repository that imports an SDL binding. See the
// package comment in backend.go for why that boundary exists and what it costs
// to move it.

import (
	"fmt"

	"github.com/Zyko0/go-sdl3/bin/binsdl"
	"github.com/Zyko0/go-sdl3/sdl"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

// SDLBackend implements [Backend] on SDL3 through the purego binding
// github.com/Zyko0/go-sdl3, which needs no cgo.
//
// The library itself is the copy bundled with the binding, unpacked to a
// temporary directory at [SDLBackend.Open]. It is emphatically not the system
// SDL: the binding resolves every symbol it knows about at load time and
// panics — not returns an error — when one is missing, and it knows about
// symbols that only exist in SDL 3.4.0 and later. Debian currently ships
// 3.2.x, so loading the system library by way of sdl.Path() takes the process
// down. The bundled library is the only supportable option until the binding
// gains a version check.
type SDLBackend struct {
	loaded   bool
	unload   func()
	window   *sdl.Window
	renderer *sdl.Renderer
	texture  *sdl.Texture

	scale ScaleQuality

	// winW/winH are window coordinates; outW/outH are drawable pixels. They
	// differ on a high-density display, and every coordinate leaving this file
	// is in the drawable space.
	winW, winH int
	outW, outH int

	buttons Buttons

	// fullscreen is the state this backend asked for, not the state SDL
	// reports. On Wayland (and anywhere else the compositor owns window
	// state) SDL_SetWindowFullscreen only records an intent: the window flag
	// does not change until the compositor agrees, which is often after the
	// next event poll. Reading the flag back straight away therefore returns
	// the old value, and a fullscreen toggle built on it gets stuck on the
	// first press. The ENTER/LEAVE_FULLSCREEN events reconcile this with
	// reality, including when the user toggles fullscreen through the window
	// manager rather than through the client.
	fullscreen bool

	// wheelX/wheelY accumulate fractional wheel movement. Trackpads report
	// continuous deltas, and RFB only has whole clicks.
	wheelX, wheelY float32

	// pressed remembers what was reported for each physical key, so that the
	// release reports the same thing. Without it, releasing Shift before the
	// letter turns "A down" into "a up" and leaves the guest holding 'A'
	// forever.
	pressed map[sdl.Scancode]EventKey

	event sdl.Event
}

// NewSDLBackend returns an unopened SDL backend.
func NewSDLBackend() *SDLBackend {
	return &SDLBackend{pressed: make(map[sdl.Scancode]EventKey)}
}

// Open loads SDL, creates the window and renderer, and prepares the streaming
// texture path.
//
// It must run on the main OS thread. The binding locks the goroutine that
// imports it to its thread in an init function, which covers the normal case
// of calling this from main, but a caller that moves the loop elsewhere must
// call runtime.LockOSThread itself.
func (b *SDLBackend) Open(opts WindowOptions) error {
	if b.loaded {
		return fmt.Errorf("viewer: SDL backend already open")
	}
	// binsdl.Load calls log.Fatal rather than returning an error if the
	// bundled library cannot be unpacked; there is nothing this code can do
	// about that beyond documenting it.
	lib := binsdl.Load()
	b.unload = lib.Unload
	b.loaded = true

	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		b.Close()
		return fmt.Errorf("sdl init: %w", err)
	}

	flags := sdl.WINDOW_RESIZABLE
	if opts.Fullscreen {
		flags |= sdl.WINDOW_FULLSCREEN
	}
	// SDL_WINDOW_HIGH_PIXEL_DENSITY is deliberately not requested. The guest
	// is resized to match the window, so the framebuffer already arrives at
	// the window's resolution and there is nothing to gain from a denser
	// backbuffer except a scaling factor to get wrong.
	width, height := opts.Width, opts.Height
	if width <= 0 || height <= 0 {
		width, height = 1280, 800
	}
	window, renderer, err := sdl.CreateWindowAndRenderer(opts.Title, width, height, flags)
	if err != nil {
		b.Close()
		return fmt.Errorf("sdl create window: %w", err)
	}
	b.window, b.renderer = window, renderer

	if opts.VSync {
		// Not fatal: a software renderer may refuse, and a session that
		// tears is better than no session.
		if err := renderer.SetVSync(1); err != nil {
			_ = err
		}
	}

	b.scale = opts.ScaleQuality
	if b.scale == "" {
		b.scale = ScaleLinear
	}
	b.fullscreen = opts.Fullscreen
	b.refreshMetrics()
	return nil
}

// Close destroys everything Open created, in reverse order, and unloads the
// library. It is safe to call more than once.
func (b *SDLBackend) Close() {
	if b.texture != nil {
		b.texture.Destroy()
		b.texture = nil
	}
	if b.renderer != nil {
		b.renderer.Destroy()
		b.renderer = nil
	}
	if b.window != nil {
		b.window.Destroy()
		b.window = nil
	}
	if b.loaded {
		sdl.Quit()
		if b.unload != nil {
			b.unload()
		}
		b.unload = nil
		b.loaded = false
	}
}

// SetTextureSize allocates the streaming texture that holds the guest
// framebuffer.
func (b *SDLBackend) SetTextureSize(w, h int) error {
	if b.renderer == nil {
		return fmt.Errorf("viewer: SDL backend is not open")
	}
	if w <= 0 || h <= 0 {
		return fmt.Errorf("viewer: invalid texture size %dx%d", w, h)
	}
	if b.texture != nil {
		b.texture.Destroy()
		b.texture = nil
	}
	// PIXELFORMAT_RGBA32 is byte-order R,G,B,A on every architecture, which is
	// exactly rfb.Framebuffer's layout, so uploads are a straight memcpy with
	// no conversion pass.
	texture, err := b.renderer.CreateTexture(sdl.PIXELFORMAT_RGBA32, sdl.TEXTUREACCESS_STREAMING, w, h)
	if err != nil {
		return fmt.Errorf("sdl create texture: %w", err)
	}
	if err := texture.SetScaleMode(sdlScaleMode(b.scale)); err != nil {
		texture.Destroy()
		return fmt.Errorf("sdl set scale mode: %w", err)
	}
	b.texture = texture
	return nil
}

func sdlScaleMode(q ScaleQuality) sdl.ScaleMode {
	switch q {
	case ScaleNearest:
		return sdl.SCALEMODE_NEAREST
	case ScalePixelArt:
		return sdl.SCALEMODE_PIXELART
	default:
		return sdl.SCALEMODE_LINEAR
	}
}

// Upload copies one sub-rectangle of the framebuffer into the texture.
func (b *SDLBackend) Upload(r Rect, pix []byte, stride int) error {
	if b.texture == nil {
		return fmt.Errorf("viewer: no texture")
	}
	if r.Empty() || stride <= 0 {
		return nil
	}
	// SDL reads r.H rows of r.W pixels starting at the offset, stepping by
	// stride. Check that the last row is inside the slice rather than trusting
	// the caller: an out-of-range read here is a segfault, not a panic.
	offset := r.Y*stride + r.X*4
	last := (r.Y+r.H-1)*stride + (r.X+r.W)*4
	if offset < 0 || last > len(pix) {
		return fmt.Errorf("viewer: upload %s exceeds framebuffer (%d bytes, stride %d)", r, len(pix), stride)
	}
	rect := sdl.Rect{X: int32(r.X), Y: int32(r.Y), W: int32(r.W), H: int32(r.H)}
	if err := b.texture.Update(&rect, pix[offset:], int32(stride)); err != nil {
		return fmt.Errorf("sdl update texture: %w", err)
	}
	return nil
}

// Present clears the window and draws the texture into dst.
func (b *SDLBackend) Present(dst Rect) error {
	if b.renderer == nil {
		return fmt.Errorf("viewer: SDL backend is not open")
	}
	if err := b.renderer.SetDrawColor(0, 0, 0, 255); err != nil {
		return fmt.Errorf("sdl set draw color: %w", err)
	}
	// The clear is what paints the letterbox bars.
	if err := b.renderer.Clear(); err != nil {
		return fmt.Errorf("sdl clear: %w", err)
	}
	if b.texture != nil && !dst.Empty() {
		rect := sdl.FRect{X: float32(dst.X), Y: float32(dst.Y), W: float32(dst.W), H: float32(dst.H)}
		if err := b.renderer.RenderTexture(b.texture, nil, &rect); err != nil {
			return fmt.Errorf("sdl render texture: %w", err)
		}
	}
	if err := b.renderer.Present(); err != nil {
		return fmt.Errorf("sdl present: %w", err)
	}
	return nil
}

// Size returns the drawable size in pixels.
func (b *SDLBackend) Size() (int, int) { return b.outW, b.outH }

// SetTitle updates the window title.
func (b *SDLBackend) SetTitle(title string) error {
	if b.window == nil {
		return nil
	}
	if err := b.window.SetTitle(title); err != nil {
		return fmt.Errorf("sdl set title: %w", err)
	}
	return nil
}

// SetFullscreen enters or leaves fullscreen.
func (b *SDLBackend) SetFullscreen(on bool) error {
	if b.window == nil {
		return nil
	}
	if err := b.window.SetFullscreen(on); err != nil {
		return fmt.Errorf("sdl set fullscreen: %w", err)
	}
	b.fullscreen = on
	// Sync blocks until the window manager has applied the change, so that
	// the size read below is the new one. It is best-effort: some backends
	// cannot synchronise, and the resize event that follows corrects us.
	_ = b.window.Sync()
	b.refreshMetrics()
	return nil
}

// Fullscreen reports the fullscreen state; see the field comment for why this
// is the requested state rather than SDL's flag.
func (b *SDLBackend) Fullscreen() bool { return b.fullscreen }

// Clipboard returns the host clipboard text.
func (b *SDLBackend) Clipboard() (string, error) {
	text, err := sdl.GetClipboardText()
	if err != nil {
		return "", fmt.Errorf("sdl get clipboard: %w", err)
	}
	return text, nil
}

// SetClipboard replaces the host clipboard text.
func (b *SDLBackend) SetClipboard(text string) error {
	if err := sdl.SetClipboardText(text); err != nil {
		return fmt.Errorf("sdl set clipboard: %w", err)
	}
	return nil
}

// PollEvents drains the SDL event queue, translating each event into a
// backend-neutral one. Events with no equivalent are dropped here rather than
// leaking an SDL concept into the viewer.
func (b *SDLBackend) PollEvents(dst []Event) []Event {
	for sdl.PollEvent(&b.event) {
		switch b.event.Type {
		case sdl.EVENT_QUIT, sdl.EVENT_WINDOW_CLOSE_REQUESTED:
			dst = append(dst, EventQuit{})

		case sdl.EVENT_KEY_DOWN, sdl.EVENT_KEY_UP:
			if ev, ok := b.translateKey(b.event.KeyboardEvent()); ok {
				dst = append(dst, ev)
			}

		case sdl.EVENT_MOUSE_MOTION:
			m := b.event.MouseMotionEvent()
			b.buttons = buttonsFromState(m.State)
			x, y := b.toSurface(m.X, m.Y)
			dst = append(dst, EventPointer{X: x, Y: y, Buttons: b.buttons})

		case sdl.EVENT_MOUSE_BUTTON_DOWN, sdl.EVENT_MOUSE_BUTTON_UP:
			m := b.event.MouseButtonEvent()
			if bit, ok := buttonBit(m.Button); ok {
				if m.Down {
					b.buttons |= bit
				} else {
					b.buttons &^= bit
				}
			}
			x, y := b.toSurface(m.X, m.Y)
			dst = append(dst, EventPointer{X: x, Y: y, Buttons: b.buttons})

		case sdl.EVENT_MOUSE_WHEEL:
			w := b.event.MouseWheelEvent()
			dx, dy := w.X, w.Y
			if w.Direction == sdl.MOUSEWHEEL_FLIPPED {
				dx, dy = -dx, -dy
			}
			var ticksX, ticksY int
			ticksX, b.wheelX = wheelTicks(b.wheelX, dx)
			ticksY, b.wheelY = wheelTicks(b.wheelY, dy)
			if ticksX != 0 || ticksY != 0 {
				dst = append(dst, EventWheel{DX: ticksX, DY: ticksY})
			}

		case sdl.EVENT_WINDOW_RESIZED, sdl.EVENT_WINDOW_PIXEL_SIZE_CHANGED:
			b.refreshMetrics()
			dst = append(dst, EventResize{W: b.outW, H: b.outH})

		case sdl.EVENT_WINDOW_ENTER_FULLSCREEN:
			b.fullscreen = true

		case sdl.EVENT_WINDOW_LEAVE_FULLSCREEN:
			b.fullscreen = false

		case sdl.EVENT_WINDOW_FOCUS_GAINED:
			dst = append(dst, EventFocus{Gained: true})

		case sdl.EVENT_WINDOW_FOCUS_LOST:
			// SDL stops delivering key events once focus is gone, so anything
			// still held is never released; the viewer reacts to this.
			b.pressed = make(map[sdl.Scancode]EventKey)
			b.buttons = 0
			dst = append(dst, EventFocus{Gained: false})

		case sdl.EVENT_CLIPBOARD_UPDATE:
			dst = append(dst, EventClipboard{})
		}
	}
	return dst
}

// refreshMetrics re-reads the window and drawable sizes. Both are needed: SDL
// reports pointer positions in window coordinates but renders in drawable
// pixels, and on a scaled display those are different units.
func (b *SDLBackend) refreshMetrics() {
	if b.window == nil || b.renderer == nil {
		return
	}
	if w, h, err := b.window.Size(); err == nil && w > 0 && h > 0 {
		b.winW, b.winH = int(w), int(h)
	}
	if w, h, err := b.renderer.RenderOutputSize(); err == nil && w > 0 && h > 0 {
		b.outW, b.outH = int(w), int(h)
	}
	if b.outW == 0 || b.outH == 0 {
		b.outW, b.outH = b.winW, b.winH
	}
}

// toSurface converts a window-coordinate pointer position into drawable
// pixels, which is the single coordinate space the Backend contract defines.
func (b *SDLBackend) toSurface(x, y float32) (int, int) {
	sx, sy := float64(x), float64(y)
	if b.winW > 0 && b.outW > 0 {
		sx = sx * float64(b.outW) / float64(b.winW)
	}
	if b.winH > 0 && b.outH > 0 {
		sy = sy * float64(b.outH) / float64(b.winH)
	}
	return int(sx), int(sy)
}

// wheelTicks accumulates a fractional wheel delta and returns the whole clicks
// that have accrued along with the remainder. A mouse reports ±1 and takes the
// fast path; a trackpad reports a stream of fractions that would otherwise
// either be lost or be rounded up into a scroll far faster than the user's
// finger.
func wheelTicks(acc, delta float32) (ticks int, rest float32) {
	acc += delta
	for acc >= 1 {
		ticks++
		acc--
	}
	for acc <= -1 {
		ticks--
		acc++
	}
	return ticks, acc
}

// buttonBit maps an SDL button index to a neutral button bit.
func buttonBit(index uint8) (Buttons, bool) {
	switch sdl.MouseButtonFlags(index) {
	case sdl.BUTTON_LEFT:
		return ButtonLeft, true
	case sdl.BUTTON_MIDDLE:
		return ButtonMiddle, true
	case sdl.BUTTON_RIGHT:
		return ButtonRight, true
	default:
		// X1/X2 have no RFB button, and forwarding them as one of the wheel
		// bits (as some clients do) makes back/forward scroll the guest.
		return 0, false
	}
}

// buttonsFromState converts SDL's motion-event button mask. SDL exposes button
// indices, not the mask bits, so the shift is done here.
func buttonsFromState(state sdl.MouseButtonFlags) Buttons {
	var out Buttons
	if state&(1<<(sdl.BUTTON_LEFT-1)) != 0 {
		out |= ButtonLeft
	}
	if state&(1<<(sdl.BUTTON_MIDDLE-1)) != 0 {
		out |= ButtonMiddle
	}
	if state&(1<<(sdl.BUTTON_RIGHT-1)) != 0 {
		out |= ButtonRight
	}
	return out
}

// translateKey turns an SDL keyboard event into an [EventKey].
//
// The split between the two halves of [EventKey] follows the split in SDL
// itself. A scancode names a physical key and is layout-independent, which is
// what the guest needs for F1, Home or the keypad; a keycode names the
// character that key produces under the current layout, which is what the
// guest needs for text. Mapping everything from the keycode would break
// non-US layouts; mapping everything from the scancode would send a US-layout
// guest the wrong letters.
func (b *SDLBackend) translateKey(e *sdl.KeyboardEvent) (EventKey, bool) {
	if e == nil {
		return EventKey{}, false
	}
	mods := translateMods(e.Mod)

	if !e.Down {
		// Report exactly what the press reported; see the pressed map.
		if prev, ok := b.pressed[e.Scancode]; ok {
			delete(b.pressed, e.Scancode)
			prev.Down = false
			prev.Repeat = false
			prev.Mods = mods
			return prev, true
		}
	}

	ev := EventKey{Down: e.Down, Repeat: e.Repeat, Mods: mods}
	if key := keyForScancode(e.Scancode); key != keysym.KeyUnknown {
		ev.Key = key
	} else {
		keycode := e.Key
		// Ask SDL what this key produces with the current modifiers applied.
		// Its answer accounts for the layout, including dead keys and
		// third-level (AltGr) shifts, which no table in this repository could.
		if mods.HasAny(keysym.ModShift | keysym.ModAltGr | keysym.ModCapsLock) {
			if resolved := e.Scancode.KeyFrom(e.Mod, true); resolved != sdl.K_UNKNOWN {
				keycode = resolved
			}
		}
		ev.Rune = runeFromKeycode(keycode)
		if ev.Rune == 0 {
			return EventKey{}, false
		}
	}

	if e.Down {
		b.pressed[e.Scancode] = ev
	}
	return ev, true
}

// runeFromKeycode extracts the character an SDL keycode represents, or 0 if it
// does not represent one. SDL3 keycodes below the scancode mask are Unicode
// code points; above it they are named keys, which keyForScancode handles.
func runeFromKeycode(k sdl.Keycode) rune {
	if k == sdl.K_UNKNOWN || k&sdl.K_SCANCODE_MASK != 0 || k&sdl.K_EXTENDED_MASK != 0 {
		return 0
	}
	r := rune(k)
	// Control characters have dedicated keys and are reached through the
	// scancode table; letting them through here would send Return twice.
	if r < 0x20 || r == 0x7f {
		return 0
	}
	return r
}

// translateMods converts SDL's modifier mask.
func translateMods(m sdl.Keymod) keysym.Modifiers {
	var out keysym.Modifiers
	if m&sdl.KMOD_SHIFT != 0 {
		out |= keysym.ModShift
	}
	if m&sdl.KMOD_CTRL != 0 {
		out |= keysym.ModControl
	}
	if m&sdl.KMOD_ALT != 0 {
		out |= keysym.ModAlt
	}
	if m&sdl.KMOD_GUI != 0 {
		out |= keysym.ModSuper
	}
	if m&sdl.KMOD_MODE != 0 {
		out |= keysym.ModAltGr
	}
	if m&sdl.KMOD_CAPS != 0 {
		out |= keysym.ModCapsLock
	}
	if m&sdl.KMOD_NUM != 0 {
		out |= keysym.ModNumLock
	}
	return out
}

// keyForScancode maps a physical key to a [keysym.Key], or KeyUnknown for keys
// that produce text and must be resolved through the layout instead.
func keyForScancode(sc sdl.Scancode) keysym.Key {
	return scancodeKeys[sc]
}

// scancodeKeys is the physical-key table.
//
// It covers every non-text key SDL can report that has an X11 keysym, and
// deliberately omits the letter, digit and punctuation scancodes: those depend
// on the layout and are resolved by SDL at event time.
//
// Two entries are worth explaining. SDL's RALT is mapped to Alt_R rather than
// ISO_Level3_Shift even though many layouts make it AltGr, because SDL already
// reports the AltGr-shifted character through the keycode; sending
// ISO_Level3_Shift as well would apply the third level twice in the guest.
// SDL's GUI keys map to Super rather than Meta because that is what every
// modern Linux desktop binds.
var scancodeKeys = map[sdl.Scancode]keysym.Key{
	sdl.SCANCODE_RETURN:    keysym.KeyReturn,
	sdl.SCANCODE_ESCAPE:    keysym.KeyEscape,
	sdl.SCANCODE_BACKSPACE: keysym.KeyBackSpace,
	sdl.SCANCODE_TAB:       keysym.KeyTab,

	sdl.SCANCODE_CAPSLOCK: keysym.KeyCapsLock,

	sdl.SCANCODE_F1:  keysym.KeyF1,
	sdl.SCANCODE_F2:  keysym.KeyF2,
	sdl.SCANCODE_F3:  keysym.KeyF3,
	sdl.SCANCODE_F4:  keysym.KeyF4,
	sdl.SCANCODE_F5:  keysym.KeyF5,
	sdl.SCANCODE_F6:  keysym.KeyF6,
	sdl.SCANCODE_F7:  keysym.KeyF7,
	sdl.SCANCODE_F8:  keysym.KeyF8,
	sdl.SCANCODE_F9:  keysym.KeyF9,
	sdl.SCANCODE_F10: keysym.KeyF10,
	sdl.SCANCODE_F11: keysym.KeyF11,
	sdl.SCANCODE_F12: keysym.KeyF12,
	sdl.SCANCODE_F13: keysym.KeyF13,
	sdl.SCANCODE_F14: keysym.KeyF14,
	sdl.SCANCODE_F15: keysym.KeyF15,
	sdl.SCANCODE_F16: keysym.KeyF16,
	sdl.SCANCODE_F17: keysym.KeyF17,
	sdl.SCANCODE_F18: keysym.KeyF18,
	sdl.SCANCODE_F19: keysym.KeyF19,
	sdl.SCANCODE_F20: keysym.KeyF20,
	sdl.SCANCODE_F21: keysym.KeyF21,
	sdl.SCANCODE_F22: keysym.KeyF22,
	sdl.SCANCODE_F23: keysym.KeyF23,
	sdl.SCANCODE_F24: keysym.KeyF24,

	sdl.SCANCODE_PRINTSCREEN: keysym.KeyPrint,
	sdl.SCANCODE_SCROLLLOCK:  keysym.KeyScrollLock,
	sdl.SCANCODE_PAUSE:       keysym.KeyPause,
	sdl.SCANCODE_INSERT:      keysym.KeyInsert,
	sdl.SCANCODE_HOME:        keysym.KeyHome,
	sdl.SCANCODE_PAGEUP:      keysym.KeyPageUp,
	sdl.SCANCODE_DELETE:      keysym.KeyDelete,
	sdl.SCANCODE_END:         keysym.KeyEnd,
	sdl.SCANCODE_PAGEDOWN:    keysym.KeyPageDown,
	sdl.SCANCODE_RIGHT:       keysym.KeyRight,
	sdl.SCANCODE_LEFT:        keysym.KeyLeft,
	sdl.SCANCODE_DOWN:        keysym.KeyDown,
	sdl.SCANCODE_UP:          keysym.KeyUp,

	sdl.SCANCODE_NUMLOCKCLEAR: keysym.KeyNumLock,
	sdl.SCANCODE_KP_DIVIDE:    keysym.KeyKPDivide,
	sdl.SCANCODE_KP_MULTIPLY:  keysym.KeyKPMultiply,
	sdl.SCANCODE_KP_MINUS:     keysym.KeyKPSubtract,
	sdl.SCANCODE_KP_PLUS:      keysym.KeyKPAdd,
	sdl.SCANCODE_KP_ENTER:     keysym.KeyKPEnter,
	sdl.SCANCODE_KP_1:         keysym.KeyKP1,
	sdl.SCANCODE_KP_2:         keysym.KeyKP2,
	sdl.SCANCODE_KP_3:         keysym.KeyKP3,
	sdl.SCANCODE_KP_4:         keysym.KeyKP4,
	sdl.SCANCODE_KP_5:         keysym.KeyKP5,
	sdl.SCANCODE_KP_6:         keysym.KeyKP6,
	sdl.SCANCODE_KP_7:         keysym.KeyKP7,
	sdl.SCANCODE_KP_8:         keysym.KeyKP8,
	sdl.SCANCODE_KP_9:         keysym.KeyKP9,
	sdl.SCANCODE_KP_0:         keysym.KeyKP0,
	sdl.SCANCODE_KP_PERIOD:    keysym.KeyKPDecimal,
	sdl.SCANCODE_KP_EQUALS:    keysym.KeyKPEqual,
	sdl.SCANCODE_KP_COMMA:     keysym.KeyKPSeparator,
	sdl.SCANCODE_KP_SPACE:     keysym.KeyKPSpace,
	sdl.SCANCODE_KP_TAB:       keysym.KeyKPTab,

	sdl.SCANCODE_APPLICATION: keysym.KeyMenu,
	sdl.SCANCODE_MENU:        keysym.KeyMenu,
	sdl.SCANCODE_POWER:       keysym.KeyPowerOff,
	sdl.SCANCODE_EXECUTE:     keysym.KeyExecute,
	sdl.SCANCODE_HELP:        keysym.KeyHelp,
	sdl.SCANCODE_SELECT:      keysym.KeySelect,
	sdl.SCANCODE_AGAIN:       keysym.KeyRedo,
	sdl.SCANCODE_UNDO:        keysym.KeyUndo,
	sdl.SCANCODE_FIND:        keysym.KeyFind,
	sdl.SCANCODE_CANCEL:      keysym.KeyCancel,
	sdl.SCANCODE_CLEAR:       keysym.KeyClear,
	sdl.SCANCODE_PRIOR:       keysym.KeyPageUp,
	sdl.SCANCODE_RETURN2:     keysym.KeyReturn,
	sdl.SCANCODE_SEPARATOR:   keysym.KeyKPSeparator,
	sdl.SCANCODE_SYSREQ:      keysym.KeySysReq,

	sdl.SCANCODE_MUTE:       keysym.KeyAudioMute,
	sdl.SCANCODE_VOLUMEUP:   keysym.KeyAudioRaiseVolume,
	sdl.SCANCODE_VOLUMEDOWN: keysym.KeyAudioLowerVolume,

	sdl.SCANCODE_LCTRL:  keysym.KeyControlL,
	sdl.SCANCODE_LSHIFT: keysym.KeyShiftL,
	sdl.SCANCODE_LALT:   keysym.KeyAltL,
	sdl.SCANCODE_LGUI:   keysym.KeySuperL,
	sdl.SCANCODE_RCTRL:  keysym.KeyControlR,
	sdl.SCANCODE_RSHIFT: keysym.KeyShiftR,
	sdl.SCANCODE_RALT:   keysym.KeyAltR,
	sdl.SCANCODE_RGUI:   keysym.KeySuperR,
	sdl.SCANCODE_MODE:   keysym.KeyModeSwitch,

	sdl.SCANCODE_SLEEP: keysym.KeySleep,
	sdl.SCANCODE_WAKE:  keysym.KeyWakeUp,

	// XF86AudioPlay is what a keyboard's combined play/pause key produces, so
	// both SDL spellings of it map there.
	sdl.SCANCODE_MEDIA_PLAY_PAUSE:     keysym.KeyAudioPlay,
	sdl.SCANCODE_MEDIA_PLAY:           keysym.KeyAudioPlay,
	sdl.SCANCODE_MEDIA_STOP:           keysym.KeyAudioStop,
	sdl.SCANCODE_MEDIA_NEXT_TRACK:     keysym.KeyAudioNext,
	sdl.SCANCODE_MEDIA_PREVIOUS_TRACK: keysym.KeyAudioPrev,

	sdl.SCANCODE_AC_SEARCH:    keysym.KeyBrowserSearch,
	sdl.SCANCODE_AC_HOME:      keysym.KeyBrowserHome,
	sdl.SCANCODE_AC_BACK:      keysym.KeyBrowserBack,
	sdl.SCANCODE_AC_FORWARD:   keysym.KeyBrowserForward,
	sdl.SCANCODE_AC_REFRESH:   keysym.KeyBrowserRefresh,
	sdl.SCANCODE_AC_BOOKMARKS: keysym.KeyBrowserFavorites,

	// Deliberately absent: SCANCODE_AC_STOP, SCANCODE_MEDIA_SELECT and the
	// rest of SDL's application-control keys. internal/keysym has no XF86
	// keysym for them, and inventing an approximate mapping (AC_STOP as
	// "browser back", say) sends the guest a key the user did not press.
}

// Compile-time proof that the SDL backend satisfies the interface the viewer
// is written against.
var _ Backend = (*SDLBackend)(nil)
