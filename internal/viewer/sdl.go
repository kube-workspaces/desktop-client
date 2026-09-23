// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

// This is the only file in the repository that imports an SDL binding. See the
// package comment in backend.go for why that boundary exists and what it costs
// to move it.

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/png"
	"math"
	"sync"
	"time"

	"github.com/Zyko0/go-sdl3/bin/binsdl"
	"github.com/Zyko0/go-sdl3/sdl"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

// appIconPNG is the 256×256 Kube Workspaces cube generated from
// assets/icon.svg by `make icons` (cmd/mkicon). It is shown on the window via
// SDL_SetWindowIcon, which is how the icon reaches the Windows title bar and
// taskbar button, the Linux window manager and the macOS window while running;
// the macOS Dock icon comes from the .icns in the .app bundle instead.
//
//go:embed icon.png
var appIconPNG []byte

// wakeEventType is the event [SDLBackend.Wake] pushes.
//
// SDL_EVENT_USER is the first type reserved for the application, and SDL's own
// advice is to claim types with SDL_RegisterEvents rather than hard-coding it.
// The binding does not expose that function, and this process is the only
// thing pushing user events into its own queue, so the fixed type is
// unambiguous here. If a library that pushes its own user events is ever
// linked in, this becomes a registered type instead — the only cost is that
// the constant stops being a constant.
const wakeEventType = sdl.EVENT_USER

// sdlLifecycle guards the single load of the bundled SDL library and the
// reference count of video initialisation. Since binsdl.Load and sdl.Init
// must run on the main thread, and every Backend.Open call also runs there,
// the mutex protects against concurrent Wake calls while the last backend is
// closing.
var sdlLifecycle struct {
	mu     sync.Mutex
	unload func()
	refs   int
}

func acquireSDL() error {
	sdlLifecycle.mu.Lock()
	defer sdlLifecycle.mu.Unlock()

	if sdlLifecycle.refs == 0 {
		lib := binsdl.Load()
		if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
			lib.Unload()
			return fmt.Errorf("sdl init: %w", err)
		}
		sdlLifecycle.unload = lib.Unload
	}
	sdlLifecycle.refs++
	return nil
}

func releaseSDL() {
	sdlLifecycle.mu.Lock()
	defer sdlLifecycle.mu.Unlock()

	sdlLifecycle.refs--
	if sdlLifecycle.refs == 0 {
		sdl.Quit()
		if sdlLifecycle.unload != nil {
			sdlLifecycle.unload()
			sdlLifecycle.unload = nil
		}
	}
}

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
	opened   bool
	window   *sdl.Window
	windowID sdl.WindowID
	renderer *sdl.Renderer
	texture  *sdl.Texture

	// overlay holds the status plate. It is a separate texture so that the
	// frozen frame in the main texture is never overwritten to draw a state
	// message over it: the whole point of the frozen frame is that the pixels
	// survive until a new connection replaces them.
	overlay *sdl.Texture

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

	// wakeMu guards wakeOK, which is the only state [SDLBackend.Wake] may
	// touch. Wake is the one method callable from another goroutine, and it
	// reaches into the SDL library; Close unloads that library out from under
	// it. A read lock held across the push, and the write lock taken by Close
	// before anything is destroyed, is what stops a wake landing in a library
	// that has already been dlclose'd — which is a segfault, not an error.
	// The lock is uncontended in the steady state and never held across
	// anything that blocks.
	wakeMu sync.RWMutex
	wakeOK bool

	// audio holds the output device playing guest PCM. It is only used when
	// this backend is also an [AudioSink]; audio opens lazily when a connection
	// that negotiated it becomes live, and is closed with the session. It
	// follows the same concurrency rule as everything else here: only the
	// window-owning goroutine touches it.
	audio struct {
		opened bool
		dev    sdl.AudioDeviceID
		stream *sdl.AudioStream
	}
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
	if b.opened {
		return fmt.Errorf("viewer: SDL backend already open")
	}

	if err := acquireSDL(); err != nil {
		return err
	}
	b.opened = true

	flags := sdl.WINDOW_RESIZABLE | sdl.WINDOW_HIGH_PIXEL_DENSITY
	if opts.Fullscreen {
		flags |= sdl.WINDOW_FULLSCREEN
	}
	// HIGH_PIXEL_DENSITY is requested so the drawable — and therefore the
	// shell surface and the session texture — actually has the display's
	// pixels on a scaled display. Without it SDL hands back a 1x drawable the
	// compositor upscales, and everything the client draws is blurry.
	// The cost is a scaling factor to get right rather than wrong: the shell
	// multiplies its theme by ScaleFactor, and the session guest follows the
	// drawable size (a Retina guest at full pixels, which is the point).
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
	if id, err := window.ID(); err == nil {
		b.windowID = id
	}

	// Move the window onto the display it was launched from before it is
	// presented for the first time; see centerOnLaunchDisplay.
	b.centerOnLaunchDisplay()

	// The title-bar/taskbar icon. Not fatal on failure: a window with no icon
	// is cosmetic damage, not a broken session.
	b.setWindowIcon()

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

	// SDL3 does not collect text by default, and without this the window
	// receives key events only — which is how the client used to end up
	// synthesising characters from keycodes and typing ";" for ":".
	//
	// It is not fatal if it fails: a window that reports key events but no
	// text is degraded, not useless, and taking the whole session down over
	// an IME that would not start would be the worse failure.
	if err := b.StartTextInput(); err != nil {
		_ = err
	}

	// Only now may another goroutine push into the event queue.
	b.wakeMu.Lock()
	b.wakeOK = true
	b.wakeMu.Unlock()
	return nil
}

// setWindowIcon shows the embedded cube on the window's title bar and taskbar
// button. SDL copies the surface's pixels, so it is safe to destroy it here.
//
// The conversion from the decoded PNG's NRGBA layout to ARGB8888 is an
// explicit byte swap: ARGB8888 is a big-endian word, and every architecture
// this repo builds is little-endian, so the memory bytes are B,G,R,A while the
// Go image stores R,G,B,A. Writing the four bytes by hand keeps the pixel
// layout correct without guessing at platform pixel format constants.
func (b *SDLBackend) setWindowIcon() {
	if len(appIconPNG) == 0 {
		return
	}
	img, err := png.Decode(bytes.NewReader(appIconPNG))
	if err != nil {
		return
	}
	nrgba, ok := img.(*image.NRGBA)
	if !ok {
		bounds := img.Bounds()
		nrgba = image.NewNRGBA(bounds)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				nrgba.Set(x, y, img.At(x, y))
			}
		}
	}
	surface, err := sdl.CreateSurface(nrgba.Bounds().Dx(), nrgba.Bounds().Dy(), sdl.PIXELFORMAT_ARGB8888)
	if err != nil {
		return
	}
	defer surface.Destroy()
	pix := surface.Pixels()
	src := nrgba.Pix
	n := min(len(pix), len(src)) / 4
	for i := 0; i < n; i++ {
		pix[i*4+0] = src[i*4+2]
		pix[i*4+1] = src[i*4+1]
		pix[i*4+2] = src[i*4+0]
		pix[i*4+3] = src[i*4+3]
	}
	_ = b.window.SetIcon(surface)
}

// centerOnLaunchDisplay moves the freshly created window so that it is centred,
// horizontally and vertically, on the display the pointer is on when the client
// starts.
//
// SDL's default placement centres on the *primary* display, which is wrong on a
// multi-monitor desktop: a window launched from the second screen's taskbar or
// Dock lands back on the first. There is no OS API for "which display launched
// this process", so the display under the pointer is the closest the platform
// can say — it is the same heuristic Explorer, the Dock and X11 launchers use.
// The pointer is read after the window exists but before anything has been
// presented, and creating a window never moves the mouse.
//
// Positioning is a request, not a guarantee: Wayland compositors decide
// placement themselves and ignore it, and tiling window managers override it.
// Those platforms keep SDL's default centring, which is the best that can be
// done there. Everywhere else the move is applied before the first frame, so
// the window never visibly flashes up centred on the primary screen and then
// jumps.
func (b *SDLBackend) centerOnLaunchDisplay() {
	bounds, ok := LaunchDisplayBounds()
	if !ok {
		return
	}
	w, h, err := b.window.Size()
	if err != nil || w <= 0 || h <= 0 {
		return
	}
	x, y := centerInBounds(sdl.Rect{X: int32(bounds.X), Y: int32(bounds.Y), W: int32(bounds.W), H: int32(bounds.H)}, w, h)
	_ = b.window.SetPosition(x, y)
	// Sync, as in SetSize and SetFullscreen: block until the window manager has
	// applied the position so the first presented frame is already in it.
	_ = b.window.Sync()
}

// LaunchDisplayBounds returns the usable bounds, in physical screen pixels, of
// the display the pointer is on at the moment of the call, or ok=false when SDL
// is not initialised or there are no displays. It is the same query
// [SDLBackend.centerOnLaunchDisplay] runs, lifted so a caller with a loaded SDL
// can answer "which monitor are we on?" for another window without owning one.
//
// The shell uses it to tell the embedded-webview child which monitor to open
// on: the child links the browser engine's cgo and no SDL, so it cannot ask
// this itself, and without the answer it opens on the primary display. The
// shell's copy of the question is asked from its window-owning goroutine while
// the shell window is open, and the refcount check here enforces that
// precondition — a call after the last backend closed reports ok=false rather
// than calling into an unloaded library.
func LaunchDisplayBounds() (Rect, bool) {
	sdlLifecycle.mu.Lock()
	refs := sdlLifecycle.refs
	sdlLifecycle.mu.Unlock()
	if refs == 0 {
		return Rect{}, false
	}
	_, mx, my := sdl.GetGlobalMouseState()
	display := sdl.GetDisplayForPoint(&sdl.Point{X: int32(mx), Y: int32(my)})
	if display == 0 {
		return Rect{}, false
	}
	bounds, err := display.UsableBounds()
	if err != nil || bounds == nil || bounds.W <= 0 || bounds.H <= 0 {
		return Rect{}, false
	}
	return Rect{X: int(bounds.X), Y: int(bounds.Y), W: int(bounds.W), H: int(bounds.H)}, true
}

// centerInBounds returns the top-left corner of a w×h window centred inside the
// display area bounds, in screen coordinates.
//
// The halves round down: an odd pixel of slack goes to the top and left, which
// is unobservable. The arithmetic is integer on purpose, like FitLetterbox —
// window geometry is int32 and staying in integers keeps the two rounding
// conventions from ever disagreeing.
func centerInBounds(bounds sdl.Rect, w, h int32) (x, y int32) {
	return bounds.X + (bounds.W-w)/2, bounds.Y + (bounds.H-h)/2
}

// Close destroys everything Open created, in reverse order, and decrements the
// SDL reference count. It is safe to call more than once.
func (b *SDLBackend) Close() {
	if !b.opened {
		return
	}
	// Shut the door on Wake first: everything below this line invalidates the
	// library a concurrent wake would be calling into.
	b.wakeMu.Lock()
	b.wakeOK = false
	b.wakeMu.Unlock()

	b.CloseAudio()

	if b.overlay != nil {
		b.overlay.Destroy()
		b.overlay = nil
	}
	if b.texture != nil {
		b.texture.Destroy()
		b.texture = nil
	}
	if b.renderer != nil {
		b.renderer.Destroy()
		b.renderer = nil
	}
	if b.window != nil {
		// Paired with the StartTextInput in Open. Destroying the window would
		// end composition anyway; stopping first keeps a platform IME from
		// being left attached to a window that is about to disappear.
		_ = b.StopTextInput()
		b.window.Destroy()
		b.window = nil
	}
	releaseSDL()
	b.opened = false
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

// SetOverlaySize allocates the streaming texture that holds the status plate.
func (b *SDLBackend) SetOverlaySize(w, h int) error {
	if b.renderer == nil {
		return fmt.Errorf("viewer: SDL backend is not open")
	}
	if w <= 0 || h <= 0 {
		return fmt.Errorf("viewer: invalid overlay size %dx%d", w, h)
	}
	if b.overlay != nil {
		b.overlay.Destroy()
		b.overlay = nil
	}
	texture, err := b.renderer.CreateTexture(sdl.PIXELFORMAT_RGBA32, sdl.TEXTUREACCESS_STREAMING, w, h)
	if err != nil {
		return fmt.Errorf("sdl create overlay texture: %w", err)
	}
	// The plate is translucent by design, and its glyphs are rasterised at an
	// integer scale, so it is blended but never filtered.
	if err := texture.SetBlendMode(sdl.BLENDMODE_BLEND); err != nil {
		texture.Destroy()
		return fmt.Errorf("sdl set overlay blend mode: %w", err)
	}
	if err := texture.SetScaleMode(sdl.SCALEMODE_NEAREST); err != nil {
		texture.Destroy()
		return fmt.Errorf("sdl set overlay scale mode: %w", err)
	}
	b.overlay = texture
	return nil
}

// UploadOverlay copies RGBA pixels into the overlay texture.
func (b *SDLBackend) UploadOverlay(r Rect, pix []byte, stride int) error {
	if b.overlay == nil {
		return fmt.Errorf("viewer: no overlay texture")
	}
	return updateTexture(b.overlay, r, pix, stride)
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
	return updateTexture(b.texture, r, pix, stride)
}

// updateTexture is the shared, bounds-checked path into SDL_UpdateTexture.
func updateTexture(texture *sdl.Texture, r Rect, pix []byte, stride int) error {
	if r.Empty() || stride <= 0 {
		return nil
	}
	// SDL reads r.H rows of r.W pixels starting at the offset, stepping by
	// stride. Check that the last row is inside the slice rather than trusting
	// the caller: an out-of-range read here is a segfault, not a panic.
	offset := r.Y*stride + r.X*4
	last := (r.Y+r.H-1)*stride + (r.X+r.W)*4
	if offset < 0 || last > len(pix) {
		return fmt.Errorf("viewer: upload %s exceeds source image (%d bytes, stride %d)", r, len(pix), stride)
	}
	rect := sdl.Rect{X: int32(r.X), Y: int32(r.Y), W: int32(r.W), H: int32(r.H)}
	if err := texture.Update(&rect, pix[offset:], int32(stride)); err != nil {
		return fmt.Errorf("sdl update texture: %w", err)
	}
	return nil
}

// Present clears the window, draws the framebuffer texture into frame, applies
// the overlay, and shows the result.
func (b *SDLBackend) Present(frame Rect, ov Overlay) error {
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
	if b.texture != nil && !frame.Empty() {
		rect := sdl.FRect{X: float32(frame.X), Y: float32(frame.Y), W: float32(frame.W), H: float32(frame.H)}
		if err := b.renderer.RenderTexture(b.texture, nil, &rect); err != nil {
			return fmt.Errorf("sdl render texture: %w", err)
		}
	}
	if err := b.drawOverlay(ov); err != nil {
		return err
	}
	if err := b.renderer.Present(); err != nil {
		return fmt.Errorf("sdl present: %w", err)
	}
	return nil
}

// drawOverlay dims the frame and draws the status plate over it.
func (b *SDLBackend) drawOverlay(ov Overlay) error {
	if ov.Empty() {
		return nil
	}
	// Blending has to be turned on for the dim and back off afterwards: the
	// clear above relies on the opaque default, and a renderer left in blend
	// mode would let the letterbox bars accumulate.
	if err := b.renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND); err != nil {
		return fmt.Errorf("sdl set blend mode: %w", err)
	}
	defer func() { _ = b.renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE) }()

	if ov.Dim > 0 {
		if err := b.renderer.SetDrawColor(0, 0, 0, ov.Dim); err != nil {
			return fmt.Errorf("sdl set draw color: %w", err)
		}
		// A nil rectangle fills the whole render target.
		if err := b.renderer.RenderFillRect(nil); err != nil {
			return fmt.Errorf("sdl fill dim: %w", err)
		}
	}
	if b.overlay != nil && !ov.Rect.Empty() {
		rect := sdl.FRect{X: float32(ov.Rect.X), Y: float32(ov.Rect.Y), W: float32(ov.Rect.W), H: float32(ov.Rect.H)}
		if err := b.renderer.RenderTexture(b.overlay, nil, &rect); err != nil {
			return fmt.Errorf("sdl render overlay: %w", err)
		}
	}
	return nil
}

// Size returns the drawable size in pixels.
func (b *SDLBackend) Size() (int, int) { return b.outW, b.outH }

// ScaleFactor returns the ratio of drawable pixels to window coordinates; see
// [Backend.ScaleFactor]. It is 1 until the first successful metrics read, so
// a backend that was never opened — or a platform that reports no window
// size — reads as unscaled rather than dividing by zero.
func (b *SDLBackend) ScaleFactor() float64 {
	if b.winW <= 0 || b.outW <= 0 {
		return 1
	}
	return float64(b.outW) / float64(b.winW)
}

// SetSize resizes the window, which the viewer does once, when the first
// connection reveals the guest's resolution.
func (b *SDLBackend) SetSize(w, h int) error {
	if b.window == nil || w <= 0 || h <= 0 {
		return nil
	}
	if b.fullscreen {
		// The compositor owns the size of a fullscreen window; asking is at
		// best ignored and at worst drops it out of fullscreen.
		return nil
	}
	if err := b.window.SetSize(int32(w), int32(h)); err != nil {
		return fmt.Errorf("sdl set window size: %w", err)
	}
	// Best-effort, as in SetFullscreen: the resize event that follows
	// corrects whatever the window manager actually did.
	_ = b.window.Sync()
	b.refreshMetrics()
	return nil
}

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

// StartTextInput asks the platform to compose keystrokes into text and deliver
// the result as [EventText]. [SDLBackend.Open] calls it, because SDL3 starts
// with text input switched off and a window that never calls it never sees a
// character the user typed.
//
// It stays on for the life of the window, and that is a judgement rather than
// an oversight. The tempting alternative is to stop it for the duration of a
// display session, on the grounds that a session's keystrokes belong to the
// guest and an IME candidate window floating over a remote desktop is
// unwanted. Two things argue against it:
//
//   - The guest does not read text events at all. It is fed X11 keysyms
//     derived from [EventKey], so nothing about a session changes when text
//     input is on; the viewer simply ignores [EventText].
//   - The window is not the session's to reconfigure. The shell owns it for
//     the life of the process and lends it to a viewer through the [Backend]
//     interface, which has no text-input control and should not grow one for
//     a single backend's benefit.
//
// So the cost is a possible IME popup over a session, and the benefit is that
// text entry can never be off when a field is focused — which is the failure
// this whole path exists to prevent. These methods are exported so that a
// caller holding a concrete backend (rather than the interface) can still make
// the other choice.
func (b *SDLBackend) StartTextInput() error {
	if b.window == nil {
		return nil
	}
	if err := b.window.StartTextInput(); err != nil {
		return fmt.Errorf("sdl start text input: %w", err)
	}
	return nil
}

// StopTextInput stops text composition; see [SDLBackend.StartTextInput].
func (b *SDLBackend) StopTextInput() error {
	if b.window == nil {
		return nil
	}
	if err := b.window.StopTextInput(); err != nil {
		return fmt.Errorf("sdl stop text input: %w", err)
	}
	return nil
}

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

// OpenAudio opens the default output device and binds a stream to it for the
// given PCM format. SDL converts the stream's format to whatever the device
// actually runs, so a common case (two-channel 16-bit little-endian at 44100
// Hz from a QEMU host of the same architecture) has no conversion at all.
//
// The device is selected by SDL_AUDIO_DEVICE_DEFAULT_PLAYBACK — note that this
// is SDL3's sentinel (0xFFFFFFFF). SDL2 used 0 for "default"; passing 0 here
// is not a valid instance ID and makes the open fail.
//
// It is safe to call with a device already open: the previous one is closed
// first, which is what the viewer asks for when the next connection negotiates
// audio.
func (b *SDLBackend) OpenAudio(format AudioFormat) error {
	if err := sdl.Init(sdl.INIT_AUDIO); err != nil {
		return fmt.Errorf("sdl init audio: %w", err)
	}
	fmtType, err := sdlAudioFormat(format)
	if err != nil {
		return err
	}
	if b.audio.opened {
		b.CloseAudio()
	}
	spec := &sdl.AudioSpec{Format: fmtType, Channels: format.Channels, Freq: format.SampleRate}
	dev, err := sdl.AUDIO_DEVICE_DEFAULT_PLAYBACK.OpenAudioDevice(spec)
	if err != nil {
		return fmt.Errorf("sdl open audio device: %w", err)
	}
	stream, err := sdl.CreateAudioStream(spec, spec)
	if err != nil {
		dev.Close()
		return fmt.Errorf("sdl create audio stream: %w", err)
	}
	if err := dev.BindAudioStream(stream); err != nil {
		stream.Destroy()
		dev.Close()
		return fmt.Errorf("sdl bind audio stream: %w", err)
	}
	if err := dev.Resume(); err != nil {
		stream.Destroy()
		dev.Close()
		return fmt.Errorf("sdl resume audio device: %w", err)
	}
	b.audio.dev = dev
	b.audio.stream = stream
	b.audio.opened = true
	return nil
}

// PlayPCM queues one batch of samples for playback. It is a no-op when no
// device is open.
func (b *SDLBackend) PlayPCM(data []byte) {
	if !b.audio.opened || b.audio.stream == nil {
		return
	}
	if err := b.audio.stream.PutData(data); err != nil {
		// A failing stream is going to fail on every write; drop it rather
		// than erroring the frame loop over audio. The next OpenAudio after a
		// reconnect rebuilds the chain.
		b.CloseAudio()
	}
}

// CloseAudio stops and frees the audio device and stream. It is safe when no
// device is open.
func (b *SDLBackend) CloseAudio() {
	if b.audio.stream != nil {
		b.audio.stream.Destroy()
		b.audio.stream = nil
	}
	if b.audio.opened {
		b.audio.dev.Close()
	}
	b.audio.opened = false
}

// sdlAudioFormat maps the backend-neutral format onto the SDL sample type the
// guest uses. One byte per sample is unsigned; anything wider is signed, in
// the configured byte order.
func sdlAudioFormat(f AudioFormat) (sdl.AudioFormat, error) {
	switch f.BytesPerSample {
	case 1:
		return sdl.AUDIO_U8, nil
	case 2:
		if f.LittleEndian {
			return sdl.AUDIO_S16LE, nil
		}
		return sdl.AUDIO_S16BE, nil
	case 4:
		if f.LittleEndian {
			return sdl.AUDIO_S32LE, nil
		}
		return sdl.AUDIO_S32BE, nil
	}
	return 0, fmt.Errorf("viewer: unsupported audio depth of %d bytes per sample", f.BytesPerSample)
}

// PollEvents drains the SDL event queue, translating each event into a
// backend-neutral one. Events with no equivalent are dropped here rather than
// leaking an SDL concept into the viewer.
func (b *SDLBackend) PollEvents(dst []Event) []Event {
	for sdl.PollEvent(&b.event) {
		dst = b.translate(dst)
	}
	return dst
}

// WaitEvents blocks for up to timeout waiting for the first event, then drains
// whatever else is queued behind it.
//
// On a real video driver SDL_WaitEventTimeout sleeps on the platform's event
// source — the X11 or Wayland connection — so a loop parked here costs nothing
// at all until the compositor, the keyboard or [SDLBackend.Wake] has something
// for it. Drivers with no waitable source, which in practice means the dummy
// driver used in headless tests, fall back inside SDL to polling every
// millisecond; a headless measurement of this therefore shows SDL's floor, not
// the application's.
func (b *SDLBackend) WaitEvents(dst []Event, timeout time.Duration) []Event {
	if timeout <= 0 {
		return b.PollEvents(dst)
	}
	// Round up rather than down: SDL's resolution is a millisecond, and a
	// sub-millisecond timeout truncated to zero would turn this into a spin.
	ms := (timeout + time.Millisecond - 1) / time.Millisecond
	if ms > math.MaxInt32 {
		ms = math.MaxInt32
	}
	if sdl.WaitEventTimeout(&b.event, int32(ms)) {
		dst = b.translate(dst)
	}
	// One event woke the wait; anything that arrived with it (a resize is
	// usually three) is still queued, and taking it now keeps the batching
	// behaviour identical to PollEvents.
	return b.PollEvents(dst)
}

// Wake pushes a user event, which ends any [SDLBackend.WaitEvents] in progress
// and, because it goes on the queue like any other event, is not lost if the
// wait has not started yet.
//
// SDL_PushEvent is documented as safe from any thread, which is the whole
// reason this is the one method another goroutine may call. See wakeMu for
// what happens when that goroutine races Close.
func (b *SDLBackend) Wake() {
	b.wakeMu.RLock()
	defer b.wakeMu.RUnlock()
	if !b.wakeOK {
		return
	}
	event := sdl.Event{Type: wakeEventType}
	// A queue so full that the push fails is a queue that is about to wake
	// the loop anyway, so there is nothing useful to do with the error.
	_ = sdl.PushEvent(&event)
}

// foreign reports whether the current event belongs to a window other than
// this backend's.
func (b *SDLBackend) foreign() bool {
	id := b.eventWindowID()
	// An event with no window (0) is global and belongs to every backend.
	return id != 0 && id != b.windowID
}

// eventWindowID extracts the window ID from the current event, or 0 if it is
// a global event or has no window.
func (b *SDLBackend) eventWindowID() sdl.WindowID {
	if b.event.Type >= sdl.EVENT_WINDOW_FIRST && b.event.Type <= sdl.EVENT_WINDOW_LAST {
		return b.event.WindowEvent().WindowID
	}
	switch b.event.Type {
	case sdl.EVENT_KEY_DOWN, sdl.EVENT_KEY_UP:
		return b.event.KeyboardEvent().WindowID
	case sdl.EVENT_TEXT_INPUT:
		return b.event.TextInputEvent().WindowID
	case sdl.EVENT_MOUSE_MOTION:
		return b.event.MouseMotionEvent().WindowID
	case sdl.EVENT_MOUSE_BUTTON_DOWN, sdl.EVENT_MOUSE_BUTTON_UP:
		return b.event.MouseButtonEvent().WindowID
	case sdl.EVENT_MOUSE_WHEEL:
		return b.event.MouseWheelEvent().WindowID
	default:
		return 0
	}
}

// translate converts the event most recently read into b.event, appending the
// backend-neutral form to dst. Events with no equivalent are dropped here
// rather than leaking an SDL concept into the viewer.
func (b *SDLBackend) translate(dst []Event) []Event {
	if b.foreign() {
		return dst
	}
	switch b.event.Type {
	case sdl.EVENT_QUIT, sdl.EVENT_WINDOW_CLOSE_REQUESTED:
		dst = append(dst, EventQuit{})

	case sdl.EVENT_KEY_DOWN, sdl.EVENT_KEY_UP:
		if ev, ok := b.translateKey(b.event.KeyboardEvent()); ok {
			dst = append(dst, ev)
		}

	case sdl.EVENT_TEXT_INPUT:
		if ev, ok := translateText(b.event.TextInputEvent()); ok {
			dst = append(dst, ev)
		}

	case sdl.EVENT_TEXT_EDITING, sdl.EVENT_TEXT_EDITING_CANDIDATES:
		// Pre-edit state: the half-composed text an IME is showing, and the
		// candidate list it is offering. Drawing them is the job of a toolkit
		// with a composition popup, which this is not; SDL falls back to the
		// platform's own IME window, and the finished text arrives as
		// EVENT_TEXT_INPUT. Forwarding the pre-edit as if it were committed
		// would type the candidate the user has not chosen yet.

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

	case wakeEventType:
		// A wake from another goroutine. Its only job was to end the wait it
		// was pushed for; there is nothing here for the viewer, and turning
		// it into an event would make every wake look like user input.
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

// translateText turns an SDL text input event into an [EventText].
//
// The binding has already decoded SDL's UTF-8 into a Go string, so there is
// nothing to do here but reject the empty commit — which SDL does send on some
// platforms when composition ends without producing anything, and which would
// otherwise look to the viewer like the user typing nothing at all.
func translateText(e *sdl.TextInputEvent) (EventText, bool) {
	if e == nil || e.Text == "" {
		return EventText{}, false
	}
	return EventText{Text: e.Text}, true
}

// keycodeFromScancode resolves the character a physical key produces under the
// current layout and the given modifier state.
//
// The key_event argument must be false. SDL_GetKeyFromScancode passing true
// means "give me the keycode as it appears in a key event", and SDL builds
// that keycode with the modifiers deliberately discarded:
//
//	if (key_event) {
//	    ...
//	    // We won't be applying any modifiers by default
//	    modstate = SDL_KMOD_NONE;
//
// which is why the keycode in an SDL3 key event — and the result of asking for
// one with true — is always the unshifted value of the key. Ask for ':' that
// way and you get ';'. Passing false takes the plain keymap lookup, which
// applies modstate as asked.
//
// The keyEvent argument is kept in the signature, rather than hard-coded
// inside, so that a test can stand in a layout that reproduces SDL's own
// behaviour and fail if this ever asks for a key-event keycode again.
//
// It is a variable so that the translation layer can be tested against a known
// layout without a keyboard, a window, or a loaded SDL.
var keycodeFromScancode = func(sc sdl.Scancode, mods sdl.Keymod, keyEvent bool) sdl.Keycode {
	return sc.KeyFrom(mods, keyEvent)
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
//
// The character here is for the guest and for modifier chords, not for local
// text entry: see the [EventKey] and [EventText] comments.
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
		// Ask the layout what this key produces with the modifiers actually
		// held, rather than reading the event's own keycode, which SDL has
		// already stripped the modifiers from. The lookup is unconditional:
		// even with no modifiers it is the better answer, because the event
		// keycode is subject to SDL_HINT_KEYCODE_OPTIONS and reports a
		// Russian or Thai letter key as its Latin equivalent, which is not
		// the key the guest should be told about.
		keycode := keycodeFromScancode(e.Scancode, e.Mod, false)
		if keycode == sdl.K_UNKNOWN {
			keycode = e.Key
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
