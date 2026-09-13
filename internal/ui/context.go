// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

// Context is what a widget is handed for one frame: somewhere to draw, the
// theme, the input that has arrived, and the keyboard focus.
//
// It lives for the whole run of the shell, not for one frame, because the
// focus has to. [Context.Begin] and [Context.End] bracket each frame.
type Context struct {
	// Theme is the palette and the metrics. It is never nil after [NewContext].
	Theme *Theme

	// Canvas is the surface for this frame.
	Canvas *Canvas

	// Input is the activity this frame is being drawn against.
	Input Input

	// Clipboard, when set, gives text fields a paste action. It is a function
	// rather than an interface because there is exactly one implementation
	// (the window backend) and the shell should be able to leave it nil in a
	// test without building a stub.
	Clipboard func() (string, error)

	ring FocusRing

	// repaint records that something this frame wants to be drawn again at
	// once: the focus moved, or a widget changed shape. The shell is
	// event-driven and would otherwise sit still.
	repaint bool

	// repaintAfter is the shortest delay any widget asked to be redrawn
	// after, for the two things that animate — the text cursor and the busy
	// indicator.
	//
	// A delay rather than a flag, because "repaint next frame" from a blinking
	// cursor means a full software repaint sixty times a second for as long as
	// a field has focus. Asking for a frame in half a second costs two.
	repaintAfter time.Duration

	// tabHandled stops two focusable widgets both acting on the same Tab.
	tabHandled bool
}

// NewContext returns a context using theme, or [DefaultTheme] when theme is
// nil.
func NewContext(theme *Theme) *Context {
	if theme == nil {
		theme = DefaultTheme()
	}
	return &Context{Theme: theme}
}

// Begin starts a frame drawn into canvas against in.
func (c *Context) Begin(canvas *Canvas, in Input) {
	c.Canvas = canvas
	c.Input = in
	canvas.font = c.Theme.Font
	c.repaint = false
	c.repaintAfter = 0
	c.tabHandled = false
	c.ring.Begin()
}

// End finishes the frame: it applies any pending Tab traversal and makes sure
// something is focused.
//
// Traversal is resolved here rather than where the key was seen because the
// order is only complete once every widget has been laid out. The consequence
// is that a Tab takes effect on the next frame, which is why it asks for a
// repaint.
func (c *Context) End() {
	if !c.tabHandled {
		switch {
		case c.Input.Chord(keysym.ModNone, keysym.KeyTab):
			c.ring.Move(1)
			c.repaint = true
		case c.Input.Chord(keysym.ModShift, keysym.KeyTab):
			c.ring.Move(-1)
			c.repaint = true
		}
	}
	if c.ring.Normalise() {
		c.repaint = true
	}
}

// Focus returns the focus ring, for a screen that wants to place the focus
// itself when it opens.
func (c *Context) Focus() *FocusRing { return &c.ring }

// Focused reports whether id has the keyboard.
func (c *Context) Focused(id FocusID) bool { return c.ring.Has(id) }

// Repaint asks for another frame immediately after this one.
func (c *Context) Repaint() { c.repaint = true }

// RepaintAfter asks for another frame no sooner than d from now. The shortest
// request in a frame wins.
func (c *Context) RepaintAfter(d time.Duration) {
	if d <= 0 {
		c.repaint = true
		return
	}
	if c.repaintAfter == 0 || d < c.repaintAfter {
		c.repaintAfter = d
	}
}

// NeedsRepaint reports whether anything in the frame just drawn asked to be
// drawn again immediately.
func (c *Context) NeedsRepaint() bool { return c.repaint }

// RepaintDelay returns the shortest deferred repaint request from the frame
// just drawn, and whether there was one.
func (c *Context) RepaintDelay() (time.Duration, bool) {
	return c.repaintAfter, c.repaintAfter > 0
}

// register adds a focusable widget to this frame's traversal order and gives
// it the focus if it was clicked.
func (c *Context) register(id FocusID, r Rect) bool {
	c.ring.Register(id)
	if c.Input.PressedIn(r) && !c.ring.Has(id) {
		c.ring.Set(id)
		c.repaint = true
	}
	return c.ring.Has(id)
}

// paste returns the host clipboard's text, or "" when there is no clipboard or
// it cannot be read. A clipboard that fails is not worth an error path in a
// text field: the paste simply does nothing.
func (c *Context) paste() string {
	if c.Clipboard == nil {
		return ""
	}
	text, err := c.Clipboard()
	if err != nil {
		return ""
	}
	return text
}
