// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package terminal draws the integrated container terminal: a VT-compatible
// screen fed by the workspace /exec bridge and rendered into a viewer window.
// The screen itself is xterm-go's parser; this package owns the gluing — the
// cell grid the renderer reads, the palette the attributes resolve against,
// and the byte flows in and out.
package terminal

import (
	"image/color"
	"sync"
	"unicode/utf8"

	"github.com/gitpod-io/xterm-go"
)

// DefaultScrollback is how many lines of history the emulator keeps above the
// viewport. It is what makes "scroll up to read the log" work without a
// scrolling client: 1000 lines is a short interactive session's worth.
const DefaultScrollback = 1000

// Cell is one grid cell, resolved from the terminal's attributes into the
// concrete colours the renderer draws. Everything the renderer needs sits in
// one value, so a frame compares cells by equality and repaints the ones that
// changed.
type Cell struct {
	// Rune is the glyph to draw, or 0 for a continuation cell that only
	// carries background.
	Rune rune
	// Fg and Bg are the resolved foreground and background colours.
	Fg, Bg color.RGBA
	// Wide marks the leading half of a width-2 character; the next column is
	// its continuation and must not draw ink of its own.
	Wide bool
	// Invisible hides the ink: the character was rendered invisible by SGR 8,
	// and paints only its background.
	Invisible bool
}

// Palette is the set of named colours the emulator resolves attribute values
// against. A terminal's 16-colour table is not fixed by any standard beyond
// the first two entries, so it lives here rather than being hardcoded in the
// attribute decoder.
type Palette struct {
	// DefaultFg and DefaultBg are what an uncoloured cell draws with.
	DefaultFg, DefaultBg color.RGBA
	// Ansi is the 16-colour table, index 0..7 dark and 8..15 bright.
	Ansi [16]color.RGBA
}

// defaultPalette is the classic XTerm table in the client's dark palette's
// ink: the uncoloured cell matches the shell's own background, so a workspace
// session does not look like a different product.
func defaultPalette() *Palette {
	return &Palette{
		DefaultFg: color.RGBA{R: 0xe3, G: 0xe6, B: 0xea, A: 0xff},
		DefaultBg: color.RGBA{R: 0x14, G: 0x16, B: 0x1a, A: 0xff},
		Ansi: [16]color.RGBA{
			{R: 0x00, G: 0x00, B: 0x00, A: 0xff},
			{R: 0xcd, G: 0x00, B: 0x00, A: 0xff},
			{R: 0x00, G: 0xcd, B: 0x00, A: 0xff},
			{R: 0xcd, G: 0xcd, B: 0x00, A: 0xff},
			{R: 0x00, G: 0x00, B: 0xee, A: 0xff},
			{R: 0xcd, G: 0x00, B: 0xcd, A: 0xff},
			{R: 0x00, G: 0xcd, B: 0xcd, A: 0xff},
			{R: 0xe5, G: 0xe5, B: 0xe5, A: 0xff},
			{R: 0x7f, G: 0x7f, B: 0x7f, A: 0xff},
			{R: 0xff, G: 0x00, B: 0x00, A: 0xff},
			{R: 0x00, G: 0xff, B: 0x00, A: 0xff},
			{R: 0xff, G: 0xff, B: 0x00, A: 0xff},
			{R: 0x5c, G: 0x5c, B: 0xff, A: 0xff},
			{R: 0xff, G: 0x00, B: 0xff, A: 0xff},
			{R: 0x00, G: 0xff, B: 0xff, A: 0xff},
			{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
		},
	}
}

// ansi256Levels are the six steps the 256-colour cube is built from.
var ansi256Levels = [6]uint8{0, 95, 135, 175, 215, 255}

// resolve maps an attribute value (colour mode plus value, packed the way
// xterm-go stores it) to a concrete colour. Bold brightens the first sixteen:
// the "bold simply picks a brighter shade" behaviour every terminal emulator
// ships, and the only part of bold this renderer can express.
func (p *Palette) resolve(attr uint32, def color.RGBA, bold bool) color.RGBA {
	idx := attr & xterm.AttrPColorMask
	switch attr & xterm.AttrCMMask {
	case xterm.AttrCMP16:
		if bold && idx < 8 {
			idx |= 8
		}
		return p.Ansi[idx]
	case xterm.AttrCMP256:
		return p.color256(idx)
	case xterm.AttrCMRGB:
		rgb := attr & xterm.AttrRGBMask
		return color.RGBA{
			R: uint8(rgb >> 16),
			G: uint8(rgb >> 8),
			B: uint8(rgb),
			A: 0xff,
		}
	default:
		return def
	}
}

// color256 expands a 256-colour index: the first sixteen are the ANSI table
// itself, 16..231 are the 6x6x6 cube and 232..255 the greyscale ramp.
func (p *Palette) color256(n uint32) color.RGBA {
	switch {
	case n < 16:
		return p.Ansi[n]
	case n < 232:
		n -= 16
		return color.RGBA{
			R: ansi256Levels[n/36],
			G: ansi256Levels[(n/6)%6],
			B: ansi256Levels[n%6],
			A: 0xff,
		}
	default:
		v := uint8(8 + (n-232)*10)
		return color.RGBA{R: v, G: v, B: v, A: 0xff}
	}
}

// Emulator is a thread-safe wrapper around an xterm-go terminal: a grid the
// renderer can read while the read pump writes to it.
//
// Exactly two goroutines touch an Emulator. The read pump calls [Emulator.Write]
// (and nothing else); the window loop owns every other method. The mutex
// lets the renderer take a consistent snapshot of cells and cursor while a
// network read is mid-flight.
type Emulator struct {
	mu sync.Mutex

	term *xterm.Terminal
	// cols and rows are the viewport size, cached because the buffer's own
	// numbers live behind the buffer service.
	cols, rows int

	pal *Palette
	// onData is the caller's handler for terminal-derived output (answerback
	// responses and the like). It is invoked after the lock is released, so a
	// slow consumer cannot stall the read pump holding the renderer's lock.
	onData func(string)

	// pending collects the strings the xterm-go OnData hook emitted during
	// the current Write. They are forwarded to onData once the write is done
	// and the lock is free.
	pending []string

	// version increments on every change that could affect a rendered frame:
	// a write, a resize, a reset or a palette swap. The window loop compares
	// it against the last frame it drew.
	version uint64
}

// NewEmulator returns an emulator with a cols by rows viewport and the default
// palette. The viewport is at least one cell on each axis.
func NewEmulator(cols, rows int) *Emulator {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	e := &Emulator{pal: defaultPalette()}
	e.term = e.newTerminal(cols, rows)
	e.cols, e.rows = cols, rows
	return e
}

// newTerminal builds the xterm-go terminal this emulator owns, wiring the
// OnData hook that collects terminal output for [Emulator.Write].
func (e *Emulator) newTerminal(cols, rows int) *xterm.Terminal {
	t := xterm.New(
		xterm.WithCols(cols),
		xterm.WithRows(rows),
		xterm.WithScrollback(DefaultScrollback),
	)
	t.OnData(func(s string) { e.pending = append(e.pending, s) })
	return t
}

// Write feeds p to the terminal parser. Output the terminal generates in
// response (answerback strings, bidi-reset acknowledgements) is delivered to
// the handler registered with [Emulator.SetOnData] after the write returns,
// in the order it was produced.
func (e *Emulator) Write(p []byte) error {
	e.mu.Lock()
	e.pending = e.pending[:0]
	_, err := e.term.Write(p)
	e.version++
	out := make([]string, len(e.pending))
	copy(out, e.pending)
	cb := e.onData
	e.mu.Unlock()

	// The handler writes to the session transport; give it the free lock so
	// it may block without holding back the renderer.
	if cb != nil {
		for _, s := range out {
			cb(s)
		}
	}
	return err
}

// Resize changes the viewport size, discarding nothing the terminal itself
// does not drop during a reflow.
func (e *Emulator) Resize(cols, rows int) {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.term.Resize(cols, rows)
	e.cols, e.rows = cols, rows
	e.version++
}

// Reset clears the screen and the scrollback, as a freshly connected session
// should begin. The palette and the output handler survive.
func (e *Emulator) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.term = e.newTerminal(e.cols, e.rows)
	e.version++
}

// SetOnData replaces the handler receiving terminal-derived output. Replacing
// it is how the window hands the connector a live transport without the
// emulator knowing what a transport is.
func (e *Emulator) SetOnData(fn func(string)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onData = fn
}

// SetPalette swaps the palette the next frame resolves against. A nil palette
// restores the dark default.
func (e *Emulator) SetPalette(p *Palette) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p == nil {
		p = defaultPalette()
	}
	e.pal = p
	e.version++
}

// Palette returns a copy of the active palette, for the renderer's default
// defaults and for tests.
func (e *Emulator) Palette() Palette {
	e.mu.Lock()
	defer e.mu.Unlock()
	return *e.pal
}

// Dim returns the viewport size.
func (e *Emulator) Dim() (cols, rows int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cols, e.rows
}

// Version returns the frame counter; see [Emulator.version].
func (e *Emulator) Version() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.version
}

// Cursor returns the cursor's viewport position. It reports false when the
// cursor lies outside the viewport — which is the scrolled-away case, a line
// the user has moved above the screen — and the renderer then draws no block
// anywhere.
func (e *Emulator) Cursor() (x, y int, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	buf := e.term.Buffer()
	y = buf.YBase + buf.Y - buf.YDisp
	if y < 0 || y >= e.rows {
		return 0, 0, false
	}
	return buf.X, y, true
}

// Cell returns the resolved cell at viewport position (x, y).
//
// The index math follows xterm's own Buffer.GetLine: viewport row y is buffer
// line YDisp+y, which is the one formula that stays correct when the user has
// scrolled up into history. Position (x, y) that falls outside the grid
// yields an empty cell with the default background.
func (e *Emulator) Cell(x, y int) Cell {
	e.mu.Lock()
	defer e.mu.Unlock()
	if x < 0 || x >= e.cols || y < 0 || y >= e.rows {
		return e.emptyCell()
	}
	buf := e.term.Buffer()
	lines := buf.Lines
	line := lines.Get(buf.YDisp + y)
	if line == nil {
		return e.emptyCell()
	}
	var cd xterm.CellData
	line.LoadCell(x, &cd)
	return e.resolve(&cd)
}

// emptyCell is the zero state a cell renders with before any text arrives.
func (e *Emulator) emptyCell() Cell {
	return Cell{Fg: e.pal.DefaultFg, Bg: e.pal.DefaultBg}
}

// resolve turns one xterm-go cell into the Cell the renderer understands:
// attributes become colours, and xterm's width semantics become the
// draw-glyph/skip-glyph distinction.
func (e *Emulator) resolve(cd *xterm.CellData) Cell {
	p := e.pal
	fgAttr, bgAttr := cd.Fg, cd.Bg
	bold := fgAttr&xterm.FgFlagBold != 0
	fg := p.resolve(fgAttr, p.DefaultFg, bold)
	bg := p.resolve(bgAttr, p.DefaultBg, false)
	if fgAttr&xterm.FgFlagInverse != 0 {
		fg, bg = bg, fg
	}
	c := Cell{Fg: fg, Bg: bg, Wide: cd.GetWidth() == 2}
	if fgAttr&xterm.FgFlagInvisible != 0 {
		c.Invisible = true
		c.Fg = c.Bg
	}
	// Width 0 marks a null or wide-continuation cell: nothing to draw on its
	// own, only the background other cells painted around it.
	if cd.GetWidth() < 1 {
		return c
	}
	if s := cd.GetChars(); s != "" {
		// GetChars is the raw UTF-8 bytes of the codepoint, so a multi-byte
		// glyph must be decoded as a whole — rune(s[0]) would split 中 into
		// one byte and draw the wrong glyph.
		if r, _ := utf8.DecodeRuneInString(s); r > 0 {
			c.Rune = r
		}
	}
	return c
}
