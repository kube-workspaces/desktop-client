// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package viewer presents a live RFB session in a window: it uploads the
// framebuffer to a GPU texture, scales it to the window, and turns local input
// into RFB events.
//
// The package is split so that exactly one file talks to the windowing
// library:
//
//   - backend.go (this file) declares [Backend] and the backend-neutral event
//     types. It imports nothing but the standard library and internal/keysym.
//   - sdl.go implements [Backend] on SDL3 and is the only file that imports an
//     SDL binding.
//   - viewer.go drives the session loop against the [Backend] interface and
//     never sees an SDL type.
//
// That split is not architectural decoration. The SDL3 binding in use
// (github.com/Zyko0/go-sdl3) is a young, single-maintainer project, and the
// project needs to be able to replace it — with another binding, or with a Gio
// or platform-native surface — in days. Everything that would otherwise be
// spread across the viewer is concentrated in one file behind one interface.
package viewer

import (
	"fmt"
	"strings"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

// Rect is an axis-aligned rectangle in integer pixels.
//
// It is deliberately distinct from rfb.Rect (whose fields are uint16, because
// that is what the protocol puts on the wire) and from any SDL rectangle type.
// Window geometry regularly goes negative during arithmetic, so the viewer's
// own geometry is signed.
type Rect struct {
	X, Y, W, H int
}

// Empty reports whether r covers no pixels.
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Area returns the pixel count covered by r.
func (r Rect) Area() int {
	if r.Empty() {
		return 0
	}
	return r.W * r.H
}

// Contains reports whether (x, y) lies inside r.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// String renders the rectangle as WxH+X+Y, matching rfb.Rect's format.
func (r Rect) String() string { return fmt.Sprintf("%dx%d+%d+%d", r.W, r.H, r.X, r.Y) }

// FitLetterbox returns the largest rectangle of aspect ratio srcW:srcH that
// fits inside a dstW by dstH surface, centred, with the remainder left as
// letterbox (or pillarbox) bars.
//
// The arithmetic is integer and deliberately so. The presented rectangle is
// used for two things — drawing the texture, and mapping the pointer back into
// the framebuffer — and those two must agree exactly. Rounding the scale
// factor through a float and applying it separately in each place is how a
// viewer ends up with a cursor that drifts a pixel or two from the guest's
// idea of where it is, which is the classic, maddening VDI bug. One integer
// rectangle computed once removes the possibility.
func FitLetterbox(srcW, srcH, dstW, dstH int) Rect {
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return Rect{}
	}
	// Try full width first; if the implied height overflows, the other axis is
	// the constraint. Both branches are exact: no rounding can push the result
	// outside the destination.
	w, h := dstW, srcH*dstW/srcW
	if h > dstH {
		w, h = srcW*dstH/srcH, dstH
	}
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	return Rect{X: (dstW - w) / 2, Y: (dstH - h) / 2, W: w, H: h}
}

// MapToSource maps a point in surface coordinates back into a srcW by srcH
// source image that is being presented into the rectangle present.
//
// The returned coordinates are always inside the source: a point in the
// letterbox bars, or dragged outside the window entirely, is clamped to the
// nearest edge. That is what every usable remote-desktop client does — the
// guest pointer should slide along the edge rather than freeze or jump — so
// the clamped value is still worth sending. The bool reports whether the point
// was genuinely inside the presented image, for callers that care.
func MapToSource(x, y int, present Rect, srcW, srcH int) (sx, sy int, inside bool) {
	if srcW <= 0 || srcH <= 0 || present.Empty() {
		return 0, 0, false
	}
	inside = present.Contains(x, y)

	cx := clamp(x, present.X, present.X+present.W-1)
	cy := clamp(y, present.Y, present.Y+present.H-1)

	// (present.W-1)*srcW/present.W is always < srcW, so this cannot produce an
	// out-of-range coordinate and needs no second clamp.
	sx = (cx - present.X) * srcW / present.W
	sy = (cy - present.Y) * srcH / present.H
	return sx, sy, inside
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Buttons is a backend-neutral pointer button mask. The bit values match RFB's
// low three button bits, but the type is separate because a backend must not
// need to know the wire protocol.
type Buttons uint8

// The pointer buttons a viewer forwards. Wheel "buttons" are not here: a wheel
// is reported as [EventWheel] and only becomes a button mask at the RFB layer.
const (
	ButtonLeft Buttons = 1 << iota
	ButtonMiddle
	ButtonRight
)

// Has reports whether every bit in b is set in mask.
func (b Buttons) Has(mask Buttons) bool { return b&mask == mask }

// ScaleQuality selects the filter used when the guest framebuffer is scaled to
// the window.
type ScaleQuality string

// The supported scaling filters.
const (
	// ScaleNearest is point sampling: crisp, cheap, and aliased when the scale
	// factor is not an integer.
	ScaleNearest ScaleQuality = "nearest"
	// ScaleLinear is bilinear filtering: smooth, and the right default for a
	// desktop that is being scaled by an arbitrary factor.
	ScaleLinear ScaleQuality = "linear"
	// ScalePixelArt is nearest sampling with smoothing at pixel boundaries.
	ScalePixelArt ScaleQuality = "pixelart"
)

// ScaleQualities lists the accepted values, for help text and validation.
func ScaleQualities() []ScaleQuality {
	return []ScaleQuality{ScaleNearest, ScaleLinear, ScalePixelArt}
}

// ParseScaleQuality resolves a user-supplied filter name case-insensitively.
func ParseScaleQuality(s string) (ScaleQuality, error) {
	q := ScaleQuality(strings.ToLower(strings.TrimSpace(s)))
	for _, known := range ScaleQualities() {
		if q == known {
			return known, nil
		}
	}
	return "", fmt.Errorf("unknown scale quality %q (want one of nearest, linear, pixelart)", s)
}

// Overlay describes the modal status layer drawn on top of the guest frame.
//
// It is a value rather than a callback so that the whole frame — guest pixels,
// dim, status text — is one [Backend.Present] call: a backend that presented
// the frame and then the overlay separately would flash the undimmed frame for
// one refresh on any renderer that does not batch.
type Overlay struct {
	// Dim is the alpha of a black rectangle covering the whole surface, drawn
	// under the overlay texture. Zero draws nothing.
	Dim uint8

	// Rect is where the overlay texture is drawn, in surface pixels. An empty
	// rectangle draws no text.
	Rect Rect
}

// Empty reports whether o would draw nothing at all.
func (o Overlay) Empty() bool { return o.Dim == 0 && o.Rect.Empty() }

// WindowOptions describes the window a [Backend] should create.
type WindowOptions struct {
	// Title is the initial window title.
	Title string
	// Width and Height are the initial window size in pixels.
	Width, Height int
	// Fullscreen starts the session fullscreen.
	Fullscreen bool
	// ScaleQuality selects the texture filter. The empty value means
	// [ScaleLinear].
	ScaleQuality ScaleQuality
	// VSync asks the backend to synchronise presentation with the display.
	VSync bool
}

// Backend is the windowing surface the viewer draws into.
//
// All coordinates crossing this interface — [Backend.Size], the destination of
// [Backend.Present], and the positions in [EventPointer] and [EventResize] —
// are in one space: pixels of the drawable surface. A backend that renders to
// a high-density buffer converts window coordinates into that space before
// reporting an event, so the viewer never has to know about display scaling.
//
// Implementations are not required to be safe for concurrent use, and the
// viewer does not use them concurrently: every call is made from the goroutine
// that ran [Backend.Open]. That is a hard requirement of most windowing
// libraries (SDL included), not a simplification.
type Backend interface {
	// Open creates the window, renderer and any other resources. It must be
	// called from the OS thread that will own the event loop.
	Open(opts WindowOptions) error

	// Close destroys everything Open created. It is safe to call on a backend
	// that was never opened, and safe to call twice.
	Close()

	// SetTextureSize (re)allocates the streaming texture that holds the guest
	// framebuffer. It is called at startup and whenever the guest resolution
	// changes. The contents after the call are undefined; the caller repaints.
	SetTextureSize(w, h int) error

	// Upload copies the sub-rectangle r of an RGBA image into the texture at
	// the same position. pix is the whole image and stride is its row length
	// in bytes, so the implementation can address r without a staging copy.
	//
	// Uploading only the damaged sub-rectangles rather than the whole frame is
	// the single biggest cost saving in the render path.
	Upload(r Rect, pix []byte, stride int) error

	// SetOverlaySize (re)allocates the overlay texture: a second, small,
	// alpha-blended RGBA texture that holds the status plate. It is separate
	// from the framebuffer texture because the two change on completely
	// different schedules — the frame every few milliseconds, the overlay only
	// when the status or the window size does — and because the overlay must
	// survive being drawn over a frame that is no longer being updated.
	SetOverlaySize(w, h int) error

	// UploadOverlay copies RGBA pixels into the overlay texture, with the same
	// contract as [Backend.Upload].
	UploadOverlay(r Rect, pix []byte, stride int) error

	// Present clears the surface, draws the whole framebuffer texture into
	// frame, applies ov, and shows the result. frame is normally the
	// letterboxed rectangle from [FitLetterbox]; an empty frame draws no
	// guest pixels, which is what the viewer asks for before it has any.
	Present(frame Rect, ov Overlay) error

	// PollEvents drains all pending input events, appending them to dst and
	// returning the extended slice. It must not block. The append-style
	// signature lets the caller reuse one buffer for the life of the session.
	PollEvents(dst []Event) []Event

	// Size returns the current drawable size in pixels.
	Size() (w, h int)

	// SetSize resizes the window.
	//
	// The viewer calls it at most once per session: the window now opens
	// before the first connection exists — so that a session waiting for a
	// busy display is visible and closable — and the guest's resolution is
	// only learned when that connection arrives. A backend whose size is not
	// its own to choose (fullscreen, a tiling compositor) may ignore the
	// request; the viewer reads [Backend.Size] back rather than assuming.
	SetSize(w, h int) error

	// SetTitle updates the window title.
	SetTitle(title string) error

	// SetFullscreen enters or leaves fullscreen.
	SetFullscreen(on bool) error

	// Fullscreen reports the current fullscreen state.
	Fullscreen() bool

	// Clipboard returns the host clipboard's text, or "" if it holds
	// something that is not text.
	Clipboard() (string, error)

	// SetClipboard replaces the host clipboard's text.
	SetClipboard(text string) error
}

// Event is one input or window event, reported by [Backend.PollEvents].
//
// The set of event types is closed: the unexported marker method means only
// this package can define one. Backends in other packages can still construct
// and return the existing types, which is all a backend needs to do.
type Event interface {
	isViewerEvent()
}

// EventQuit reports that the user asked to close the window, or that the
// platform is shutting the application down.
type EventQuit struct{}

// EventKey is a key press or release.
//
// Exactly one of Key and Rune is meaningful. Key identifies a non-text key
// (function keys, arrows, modifiers, keypad); Rune carries the character a
// text key produced under the current layout and modifier state. Splitting
// them this way is what keeps the keysym mapping out of the backend: the
// backend answers "which physical key, or which character", and internal/keysym
// answers "which X11 keysym".
type EventKey struct {
	// Key is the non-text key, or keysym.KeyUnknown for a text key.
	Key keysym.Key
	// Rune is the character produced, or 0 for a non-text key.
	Rune rune
	// Down is true for a press, false for a release.
	Down bool
	// Repeat is true when the platform generated this press by auto-repeat.
	Repeat bool
	// Mods is the modifier state at the time of the event.
	Mods keysym.Modifiers
}

// EventPointer is an absolute pointer position plus the buttons held. X and Y
// are in drawable-surface pixels; see [Backend].
type EventPointer struct {
	X, Y    int
	Buttons Buttons
}

// EventWheel is a scroll wheel movement in whole clicks. DY is positive when
// scrolling away from the user (up) and DX positive when scrolling right,
// which matches both SDL's and X11's conventions.
type EventWheel struct {
	DX, DY int
}

// EventResize reports a new drawable size in pixels.
type EventResize struct {
	W, H int
}

// EventFocus reports a change in keyboard focus. Losing focus matters: see
// the modifier release in viewer.go.
type EventFocus struct {
	Gained bool
}

// EventClipboard reports that the host clipboard may have changed. It is a
// hint, not a guarantee — backends that cannot detect clipboard changes simply
// never send it, and the viewer polls regardless.
type EventClipboard struct{}

func (EventQuit) isViewerEvent()      {}
func (EventKey) isViewerEvent()       {}
func (EventPointer) isViewerEvent()   {}
func (EventWheel) isViewerEvent()     {}
func (EventResize) isViewerEvent()    {}
func (EventFocus) isViewerEvent()     {}
func (EventClipboard) isViewerEvent() {}
