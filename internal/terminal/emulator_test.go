// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"github.com/gitpod-io/xterm-go"
)

func TestNewEmulatorIsBlank(t *testing.T) {
	e := NewEmulator(4, 2)
	cols, rows := e.Dim()
	if cols != 4 || rows != 2 {
		t.Fatalf("Dim = %dx%d, want 4x2", cols, rows)
	}
	if v := e.Version(); v != 0 {
		t.Fatalf("fresh emulator Version = %d, want 0", v)
	}
	p := defaultPalette()
	if c := e.Cell(0, 0); c.Rune != 0 || c.Fg != p.DefaultFg || c.Bg != p.DefaultBg {
		t.Fatalf("blank cell = %+v", c)
	}
	if x, y, ok := e.Cursor(); !ok || x != 0 || y != 0 {
		t.Fatalf("fresh Cursor = (%d,%d,%v), want (0,0,true)", x, y, ok)
	}
}

func TestWritePlacesRunes(t *testing.T) {
	e := NewEmulator(4, 1)
	if err := e.Write([]byte("hi")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if v := e.Version(); v != 1 {
		t.Fatalf("Version = %d, want 1 after one write", v)
	}
	if c := e.Cell(0, 0); c.Rune != 'h' {
		t.Fatalf("Cell(0,0) = %q", c.Rune)
	}
	if c := e.Cell(1, 0); c.Rune != 'i' {
		t.Fatalf("Cell(1,0) = %q", c.Rune)
	}
	// The line was never filled past "hi": the cells are still empty.
	if c := e.Cell(2, 0); c.Rune != 0 {
		t.Fatalf("Cell(2,0) = %q, want 0", c.Rune)
	}
	if x, y, ok := e.Cursor(); !ok || x != 2 || y != 0 {
		t.Fatalf("Cursor = (%d,%d,%v), want (2,0,true)", x, y, ok)
	}
}

func TestWriteNewlineAdvances(t *testing.T) {
	// A bare LF is line-feed without carriage return, so shell output is
	// exercised with the CRLF pair terminals actually emit.
	e := NewEmulator(4, 2)
	if err := e.Write([]byte("ab\r\ncd")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if c := e.Cell(0, 1); c.Rune != 'c' {
		t.Fatalf("Cell(0,1) = %q, want 'c'", c.Rune)
	}
	if c := e.Cell(1, 1); c.Rune != 'd' {
		t.Fatalf("Cell(1,1) = %q, want 'd'", c.Rune)
	}
	// The first row still holds its line.
	if c := e.Cell(0, 0); c.Rune != 'a' {
		t.Fatalf("Cell(0,0) = %q, want 'a'", c.Rune)
	}
}

// LFWithoutCR is what a bare ^J does: the column is preserved, not reset.
func TestLFDoesNotCarriageReturn(t *testing.T) {
	e := NewEmulator(4, 1)
	if err := e.Write([]byte("ab\ncd")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if c := e.Cell(2, 0); c.Rune != 'c' {
		t.Fatalf("Cell(2,0) = %q, want 'c' after bare LF", c.Rune)
	}
}

func TestCursorFollowsCUP(t *testing.T) {
	e := NewEmulator(5, 2)
	if err := e.Write([]byte("\x1b[2;3H")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if x, y, ok := e.Cursor(); !ok || x != 2 || y != 1 {
		t.Fatalf("Cursor = (%d,%d,%v), want (2,1,true)", x, y, ok)
	}
}

func TestCursorOutOfViewport(t *testing.T) {
	// When the cursor is kept in view by the viewport, auto-scroll keeps the
	// latest line in view. A cursor outside the viewport only happens after
	// the user scrolls up into history, which the renderer handles by drawing
	// no block; make sure the two are not confused by a cursor sitting exactly
	// at the boundary.

	// Writing inside a one-row viewport keeps the cursor on that row, in
	// view. The final line carries no newline so it is the visible one.
	e := NewEmulator(2, 1)
	if err := e.Write([]byte("a\r\nb")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if x, y, ok := e.Cursor(); !ok || x != 1 || y != 0 {
		t.Fatalf("Cursor = (%d,%d,%v), want visible on row 0", x, y, ok)
	}
	if c := e.Cell(0, 0); c.Rune != 'b' {
		t.Fatalf("Cell(0,0) = %q, want the latest line", c.Rune)
	}
}

func TestScrollbackKeepsHistoryAndCursorVisible(t *testing.T) {
	e := NewEmulator(2, 2)
	for i := 0; i < 1200; i++ {
		if err := e.Write([]byte(fmt.Sprintf("%d\r\n", i%10))); err != nil {
			t.Fatalf("Write line %d: %v", i, err)
		}
	}
	// With far more lines than rows, the viewport auto-follows the cursor, so
	// the shell stays visible no matter how fast it produces output. The row
	// under the cursor is the freshly opened line; the one above shows the
	// completed line that scrolled in.
	if x, y, ok := e.Cursor(); !ok || y != 1 {
		t.Fatalf("Cursor = (%d,%d,%v), want on bottom row", x, y, ok)
	}
	if c := e.Cell(0, 0); c.Rune == 0 {
		t.Fatal("top viewport row is blank under scrollback load")
	}
}

func TestSGRColours(t *testing.T) {
	e := NewEmulator(8, 1)
	if err := e.Write([]byte("\x1b[31mR\x1b[32mG\x1b[0mD")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p := e.Palette()
	if c := e.Cell(0, 0); c.Fg != p.Ansi[1] {
		t.Fatalf("Cell(0,0).Fg = %v, want %v", c.Fg, p.Ansi[1])
	}
	if c := e.Cell(1, 0); c.Fg != p.Ansi[2] {
		t.Fatalf("Cell(1,0).Fg = %v, want %v", c.Fg, p.Ansi[2])
	}
	if c := e.Cell(2, 0); c.Fg != p.DefaultFg {
		t.Fatalf("Cell(2,0).Fg = %v, want default %v", c.Fg, p.DefaultFg)
	}
}

func TestBoldBrightensFirstSixteen(t *testing.T) {
	e := NewEmulator(4, 1)
	if err := e.Write([]byte("\x1b[1;31mA")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p := e.Palette()
	if c := e.Cell(0, 0); c.Fg != p.Ansi[9] {
		t.Fatalf("bold red Fg = %v, want %v", c.Fg, p.Ansi[9])
	}
}

func TestSGRInverseAndInvisible(t *testing.T) {
	e := NewEmulator(4, 1)
	if err := e.Write([]byte("\x1b[7mX\x1b[0m\x1b[8mY")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p := e.Palette()
	if c := e.Cell(0, 0); c.Fg != p.DefaultBg || c.Bg != p.DefaultFg {
		t.Fatalf("inverse cell = %+v, want fg default-bg, bg default-fg", c)
	}
	if c := e.Cell(1, 0); !c.Invisible || c.Fg != c.Bg {
		t.Fatalf("invisible cell = %+v, want ink hidden", c)
	}
}

func Test256ColourTable(t *testing.T) {
	cases := []struct {
		esc  string
		want color.RGBA
	}{
		{"\x1b[38;5;196m", color.RGBA{R: 255, G: 0, B: 0, A: 0xff}},
		{"\x1b[38;5;16m", color.RGBA{R: 0, G: 0, B: 0, A: 0xff}},
		{"\x1b[38;5;255m", color.RGBA{R: 238, G: 238, B: 238, A: 0xff}},
		{"\x1b[48;5;0m", color.RGBA{R: 0, G: 0, B: 0, A: 0xff}},
	}
	for _, tc := range cases {
		e := NewEmulator(1, 1)
		if err := e.Write([]byte(tc.esc + "X")); err != nil {
			t.Fatalf("Write %q: %v", tc.esc, err)
		}
		c := e.Cell(0, 0)
		if c.Fg != tc.want && c.Bg != tc.want {
			t.Fatalf("%q: colour = fg %v bg %v, want %v", tc.esc, c.Fg, c.Bg, tc.want)
		}
	}
}

func TestTrueColour(t *testing.T) {
	e := NewEmulator(1, 1)
	if err := e.Write([]byte("\x1b[38;2;10;20;30mA")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := color.RGBA{R: 10, G: 20, B: 30, A: 0xff}
	if c := e.Cell(0, 0); c.Fg != want {
		t.Fatalf("truecolour Fg = %v, want %v", c.Fg, want)
	}
}

func TestWideAndContinuationCells(t *testing.T) {
	e := NewEmulator(3, 1)
	if err := e.Write([]byte("中a")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if c := e.Cell(0, 0); !c.Wide || c.Rune != '中' {
		t.Fatalf("leading cell = %+v, want wide rune 中", c)
	}
	if c := e.Cell(1, 0); c.Rune != 0 {
		t.Fatalf("continuation cell = %+v, want no rune", c)
	}
	if c := e.Cell(2, 0); c.Rune != 'a' {
		t.Fatalf("Cell(2,0) = %q", c.Rune)
	}
}

func TestOnDataHandlesTerminalOutput(t *testing.T) {
	e := NewEmulator(2, 1)
	var got []string
	e.SetOnData(func(s string) { got = append(got, s) })
	if err := e.Write([]byte("\x1b[c")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("DA1 query produced no terminal output")
	}
	if !strings.HasPrefix(got[0], "\x1b[?") {
		t.Fatalf("DA1 answer = %q, want CSI query answer", got[0])
	}
}

func TestOnDataChangeReplacesHandler(t *testing.T) {
	e := NewEmulator(2, 1)
	e.SetOnData(func(string) { t.Fatal("stale handler called") })
	e.SetOnData(func(string) {})
	if err := e.Write([]byte("\x1b[c")); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

func TestResize(t *testing.T) {
	e := NewEmulator(2, 1)
	if err := e.Write([]byte("ab")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	e.Resize(4, 2)
	cols, rows := e.Dim()
	if cols != 4 || rows != 2 {
		t.Fatalf("Dim = %dx%d, want 4x2", cols, rows)
	}
	if c := e.Cell(0, 0); c.Rune != 'a' {
		t.Fatalf("Cell(0,0) after resize = %q, content should survive", c.Rune)
	}
}

func TestResetClearsScreen(t *testing.T) {
	e := NewEmulator(2, 1)
	if err := e.Write([]byte("ab")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	before := e.Version()
	e.Reset()
	if e.Version() <= before {
		t.Fatal("Reset did not bump Version")
	}
	if c := e.Cell(0, 0); c.Rune != 0 {
		t.Fatalf("Cell(0,0) after Reset = %q, want blank", c.Rune)
	}
	if cols, rows := e.Dim(); cols != 2 || rows != 1 {
		t.Fatalf("Reset changed Dim to %dx%d", cols, rows)
	}
}

func TestSetPaletteNilRestoresDefault(t *testing.T) {
	e := NewEmulator(2, 1)
	p := e.Palette()
	p.DefaultFg = color.RGBA{R: 1, G: 2, B: 3, A: 0xff}
	e.SetPalette(&p)
	if e.Cell(0, 0).Fg != p.DefaultFg {
		t.Fatal("custom palette not applied")
	}
	e.SetPalette(nil)
	if e.Cell(0, 0).Fg != defaultPalette().DefaultFg {
		t.Fatal("nil palette did not restore the default")
	}
}

func TestPaletteResolveModes(t *testing.T) {
	p := defaultPalette()
	def := color.RGBA{}

	if got := p.resolve(xterm.AttrCMP256|196, def, false); got != (color.RGBA{R: 255, G: 0, B: 0, A: 0xff}) {
		t.Fatalf("color256(196) = %v", got)
	}
	if got := p.resolve(xterm.AttrCMP16|1, def, false); got != p.Ansi[1] {
		t.Fatalf("P16 red = %v", got)
	}
	// Bold only shifts the dark half of the sixteen.
	if got := p.resolve(xterm.AttrCMP16|7, def, true); got != p.Ansi[15] {
		t.Fatalf("bold white = %v", got)
	}
	if got := p.resolve(xterm.AttrCMP16|9, def, true); got != p.Ansi[9] {
		t.Fatalf("bold bright red stayed bright = %v", got)
	}
	// The default mode returns the caller's default.
	if got := p.resolve(0, p.DefaultBg, false); got != p.DefaultBg {
		t.Fatalf("default mode = %v", got)
	}
}
