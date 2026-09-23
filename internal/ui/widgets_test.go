// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"image"
	"strings"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

// harness drives a context over a real canvas, the way the shell does, so that
// the widgets are exercised through the code path that actually runs rather
// than through a headless shortcut that could drift away from it.
type harness struct {
	ctx *Context
	in  Input
	now time.Time
}

func newHarness(w, h int) *harness {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	return &harness{ctx: &Context{Theme: DefaultTheme(), Canvas: NewCanvas(img)}, now: epoch}
}

// frame folds a batch of events and runs one layout pass.
func (h *harness) frame(events []Event, body func(*Context)) {
	h.now = h.now.Add(16 * time.Millisecond)
	h.in = h.in.Fold(h.now, events)
	h.ctx.Begin(h.ctx.Canvas, h.in)
	body(h.ctx)
	h.ctx.End()
}

// click sends a complete press and release at (x, y) as two frames, which is
// how a real backend delivers one.
func (h *harness) click(x, y int, body func(*Context)) {
	h.frame([]Event{pointer(x, y, true)}, body)
	h.frame([]Event{pointer(x, y, false)}, body)
}

var buttonRect = Rect{X: 10, Y: 10, W: 120, H: 34}

func TestButtonHitTesting(t *testing.T) {
	h := newHarness(200, 100)
	b := &Button{Text: "Connect", Variant: ButtonPrimary}

	fired := 0
	body := func(ctx *Context) {
		if b.Layout(ctx, buttonRect) {
			fired++
		}
	}

	h.click(50, 20, body)
	if fired != 1 {
		t.Fatalf("a click inside the button fired %d times, want 1", fired)
	}

	// Outside on both axes, and on the exclusive edges.
	fired = 0
	for _, p := range [][2]int{{5, 20}, {50, 5}, {135, 20}, {50, 50}, {130, 44}} {
		h.click(p[0], p[1], body)
	}
	if fired != 0 {
		t.Fatalf("clicks outside the button fired %d times", fired)
	}

	// Press inside, release outside: not an activation.
	fired = 0
	h.frame([]Event{pointer(50, 20, true)}, body)
	h.frame([]Event{pointer(180, 90, false)}, body)
	if fired != 0 {
		t.Fatal("sliding off a button before releasing still activated it")
	}
}

func TestButtonKeyboardActivation(t *testing.T) {
	h := newHarness(200, 100)
	b := &Button{ID: "go", Text: "Connect"}

	fired := 0
	body := func(ctx *Context) {
		if b.Layout(ctx, buttonRect) {
			fired++
		}
	}

	// The first frame registers the button; End gives it the focus because
	// nothing else has it.
	h.frame(nil, body)
	if !h.ctx.Focused("go") {
		t.Fatal("the only focusable widget did not receive the focus")
	}

	h.frame([]Event{keyDown(keysym.KeyReturn, keysym.ModNone)}, body)
	h.frame([]Event{runeDown(' ', keysym.ModNone)}, body)
	if fired != 2 {
		t.Fatalf("Enter and Space fired %d activations, want 2", fired)
	}

	// A focused button does not answer to somebody else's shortcut.
	fired = 0
	h.frame([]Event{runeDown('r', keysym.ModControl)}, body)
	if fired != 0 {
		t.Fatal("Ctrl-R activated a button")
	}
}

func TestButtonDisabled(t *testing.T) {
	h := newHarness(200, 100)
	b := &Button{ID: "go", Text: "Open", Disabled: true}

	fired := 0
	body := func(ctx *Context) {
		if b.Layout(ctx, buttonRect) {
			fired++
		}
	}
	h.click(50, 20, body)
	h.frame([]Event{keyDown(keysym.KeyReturn, keysym.ModNone)}, body)
	if fired != 0 {
		t.Fatal("a disabled button activated")
	}
	if h.ctx.Focused("go") {
		t.Fatal("a disabled button took the keyboard focus")
	}
}

func TestButtonWidthFitsItsLabel(t *testing.T) {
	h := newHarness(400, 100)
	short := &Button{Text: "Go"}
	long := &Button{Text: "Open in browser"}
	if long.Width(h.ctx) <= short.Width(h.ctx) {
		t.Fatal("intrinsic width does not follow the label")
	}
	if short.Width(h.ctx) <= TextWidth("Go", h.ctx.Theme.Body, h.ctx.Theme.Font) {
		t.Fatal("intrinsic width leaves no padding")
	}
}

func TestTextInputEditing(t *testing.T) {
	var f TextInput

	f.Insert([]rune("hello")...)
	if f.Value() != "hello" || f.Cursor() != 5 {
		t.Fatalf("after insert: %q cursor %d", f.Value(), f.Cursor())
	}

	f.MoveCursor(-2)
	f.Insert('X')
	if f.Value() != "helXlo" || f.Cursor() != 4 {
		t.Fatalf("insert at the cursor: %q cursor %d", f.Value(), f.Cursor())
	}

	if !f.Backspace() || f.Value() != "hello" || f.Cursor() != 3 {
		t.Fatalf("after backspace: %q cursor %d", f.Value(), f.Cursor())
	}
	if !f.DeleteForward() || f.Value() != "helo" || f.Cursor() != 3 {
		t.Fatalf("after delete: %q cursor %d", f.Value(), f.Cursor())
	}

	f.MoveHome()
	if f.Cursor() != 0 {
		t.Fatal("Home did not reach the start")
	}
	if f.Backspace() {
		t.Fatal("backspace at the start deleted something")
	}
	f.MoveEnd()
	if f.Cursor() != 4 {
		t.Fatal("End did not reach the end")
	}
	if f.DeleteForward() {
		t.Fatal("delete at the end deleted something")
	}

	// The cursor clamps rather than wraps: holding an arrow key down should
	// settle at the edge, not jump to the other one.
	f.MoveCursor(-100)
	if f.Cursor() != 0 {
		t.Fatalf("cursor ran past the start to %d", f.Cursor())
	}
	f.MoveCursor(100)
	if f.Cursor() != f.Len() {
		t.Fatalf("cursor ran past the end to %d", f.Cursor())
	}
	f.SetCursor(-5)
	if f.Cursor() != 0 {
		t.Fatal("SetCursor did not clamp below zero")
	}

	f.KillToEnd()
	if f.Value() != "" {
		t.Fatalf("Ctrl-K from the start left %q", f.Value())
	}

	// Non-printable input is dropped rather than stored and drawn as a box.
	f.Insert('\n', '\t', 0x7f, 'o', 'k')
	if f.Value() != "ok" {
		t.Fatalf("control characters were inserted: %q", f.Value())
	}

	f.Clear()
	if f.Value() != "" || f.Cursor() != 0 {
		t.Fatal("Clear left something behind")
	}
}

func TestTextInputHandlesMultiByteRunes(t *testing.T) {
	// Rune positions, not byte positions: a field holding a non-ASCII
	// character must still delete one character per backspace.
	var f TextInput
	f.SetValue("naïve")
	if f.Len() != 5 {
		t.Fatalf("length in runes = %d, want 5", f.Len())
	}
	// Cursor after "naï"; one backspace removes the whole two-byte rune. A
	// byte-indexed field would leave half of it behind.
	f.MoveCursor(-2)
	f.Backspace()
	if f.Value() != "nave" {
		t.Fatalf("backspace over a multi-byte rune gave %q", f.Value())
	}
	if f.Cursor() != 2 {
		t.Fatalf("the cursor is at %d, want 2", f.Cursor())
	}
}

func TestTextInputMaxLen(t *testing.T) {
	f := TextInput{MaxLen: 4}
	f.Insert([]rune("abcdef")...)
	if f.Value() != "abcd" {
		t.Fatalf("MaxLen not enforced on insert: %q", f.Value())
	}
	f.SetValue("123456789")
	if f.Value() != "1234" {
		t.Fatalf("MaxLen not enforced on SetValue: %q", f.Value())
	}
}

func TestTextInputKeyboard(t *testing.T) {
	h := newHarness(400, 100)
	f := &TextInput{ID: "field"}
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}

	submitted := 0
	body := func(ctx *Context) {
		if f.Layout(ctx, rect) {
			submitted++
		}
	}
	h.frame(nil, body)

	h.frame([]Event{
		runeDown('h', keysym.ModNone),
		runeDown('i', keysym.ModNone),
	}, body)
	if f.Value() != "hi" {
		t.Fatalf("typing produced %q", f.Value())
	}

	h.frame([]Event{keyDown(keysym.KeyLeft, keysym.ModNone), keyDown(keysym.KeyBackSpace, keysym.ModNone)}, body)
	if f.Value() != "i" {
		t.Fatalf("left then backspace produced %q", f.Value())
	}

	h.frame([]Event{keyDown(keysym.KeyReturn, keysym.ModNone)}, body)
	if submitted != 1 {
		t.Fatalf("Enter produced %d submissions, want 1", submitted)
	}
	if f.Value() != "i" {
		t.Fatal("Enter was inserted as text")
	}

	// Ctrl-U clears the whole line; Ctrl-A now selects it (see below).
	h.frame([]Event{runeDown('u', keysym.ModControl)}, body)
	if f.Value() != "" {
		t.Fatalf("Ctrl-U left %q", f.Value())
	}

	// An unfocused field ignores the keyboard entirely.
	h.ctx.Focus().Set("somebody-else")
	h.frame([]Event{runeDown('x', keysym.ModNone)}, func(ctx *Context) {
		ctx.Focus().Register("somebody-else")
		f.Layout(ctx, rect)
	})
	if f.Value() != "" {
		t.Fatalf("an unfocused field accepted input: %q", f.Value())
	}
}

// TestTextInputTypesComposedText is the fix for the reported bug, at the level
// the user experiences it: a field is typed into and the characters that come
// out are the ones on the keycaps.
func TestTextInputTypesComposedText(t *testing.T) {
	h := newHarness(400, 100)
	f := &TextInput{ID: "field"}
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}
	body := func(ctx *Context) { f.Layout(ctx, rect) }
	h.frame(nil, body)

	// One keystroke as a composing backend reports it: the physical key, and
	// the character the platform made of it.
	h.frame([]Event{runeDown('h', keysym.ModNone), text("h")}, body)
	if f.Value() != "h" {
		t.Fatalf("typing one character produced %q", f.Value())
	}

	// A commit carrying several characters at once — an IME finishing a word,
	// or a paste that some platforms deliver this way. All of it goes in, not
	// just the first rune.
	h.frame([]Event{text("ello")}, body)
	if f.Value() != "hello" {
		t.Fatalf("a multi-character commit produced %q, want %q", f.Value(), "hello")
	}

	// Non-ASCII, to prove the field is not taking the first byte.
	h.frame([]Event{text("日本語")}, body)
	if f.Value() != "hello日本語" {
		t.Fatalf("a multi-byte commit produced %q", f.Value())
	}
	if f.Cursor() != f.Len() {
		t.Fatalf("the cursor is at %d after inserting %d runes", f.Cursor(), f.Len())
	}
}

// TestTextInputTypesShiftedPunctuation is the regression test for the reported
// bug itself.
//
// Shift and the ";" key is ":" on a US layout. SDL3 reports the *unshifted*
// keycode in its key event — it discards the modifier state on purpose — so
// anything that derives the character from the keycode produces ";", and the
// user typing "https://..." gets "https;//...". The composed text is the only
// description of that keystroke that is right, and it must be the one that
// reaches the field.
func TestTextInputTypesShiftedPunctuation(t *testing.T) {
	h := newHarness(400, 100)
	f := &TextInput{ID: "field"}
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}
	body := func(ctx *Context) { f.Layout(ctx, rect) }
	h.frame(nil, body)

	// "https" then Shift+";" — the key event carries the unshifted ';' that
	// SDL reports, the text event carries the ':' the user actually typed.
	for _, r := range "https" {
		h.frame([]Event{runeDown(r, keysym.ModNone), text(string(r))}, body)
	}
	h.frame([]Event{runeDown(';', keysym.ModShift), text(":")}, body)
	for _, r := range "//kw.example.com" {
		h.frame([]Event{runeDown(r, keysym.ModNone), text(string(r))}, body)
	}

	const want = "https://kw.example.com"
	if f.Value() != want {
		t.Fatalf("typing a server URL produced %q, want %q", f.Value(), want)
	}
	if strings.Contains(f.Value(), ";") {
		t.Fatalf("the unshifted keycode reached the field: %q", f.Value())
	}
}

// TestTextInputInsertsEachCharacterOnce is the other half of the same fix. A
// composing backend describes one keystroke twice, and a field that believed
// both descriptions would double every character the user typed.
func TestTextInputInsertsEachCharacterOnce(t *testing.T) {
	h := newHarness(400, 100)
	f := &TextInput{ID: "field"}
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}
	body := func(ctx *Context) { f.Layout(ctx, rect) }
	h.frame(nil, body)

	h.frame([]Event{runeDown('a', keysym.ModNone), text("a")}, body)
	if f.Value() != "a" {
		t.Fatalf("one keystroke described twice produced %q, want %q", f.Value(), "a")
	}

	// And it stays that way once the backend has proved it composes: a later
	// key press with no text beside it — a dead key waiting for its second
	// keystroke — must not be typed either.
	h.frame([]Event{runeDown('^', keysym.ModNone)}, body)
	if f.Value() != "a" {
		t.Fatalf("a dead key was typed as a character: %q", f.Value())
	}
	h.frame([]Event{runeDown('e', keysym.ModNone), text("ê")}, body)
	if f.Value() != "aê" {
		t.Fatalf("the composed character produced %q, want %q", f.Value(), "aê")
	}
}

// TestTextInputKeepsKeyRunesWithoutAComposingBackend is the compatibility
// half: a backend that reports no text at all — a test double, or a platform
// with no composition API — must still be able to type.
func TestTextInputKeepsKeyRunesWithoutAComposingBackend(t *testing.T) {
	h := newHarness(400, 100)
	f := &TextInput{ID: "field"}
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}
	body := func(ctx *Context) { f.Layout(ctx, rect) }
	h.frame(nil, body)

	h.frame([]Event{runeDown('o', keysym.ModNone), runeDown('k', keysym.ModNone)}, body)
	if f.Value() != "ok" {
		t.Fatalf("a key-only backend typed %q, want %q", f.Value(), "ok")
	}
}

// TestTextInputEditingKeysSurviveComposedText: only character production moves
// to text events. Navigation and editing are still key events, and they must
// keep working in a batch that also carries text.
func TestTextInputEditingKeysSurviveComposedText(t *testing.T) {
	h := newHarness(400, 100)
	f := &TextInput{ID: "field"}
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}
	submitted := 0
	body := func(ctx *Context) {
		if f.Layout(ctx, rect) {
			submitted++
		}
	}
	h.frame(nil, body)

	h.frame([]Event{runeDown('a', keysym.ModNone), text("abc")}, body)
	if f.Value() != "abc" {
		t.Fatalf("setup typed %q", f.Value())
	}

	h.frame([]Event{keyDown(keysym.KeyBackSpace, keysym.ModNone)}, body)
	if f.Value() != "ab" {
		t.Fatalf("backspace produced %q", f.Value())
	}
	h.frame([]Event{keyDown(keysym.KeyHome, keysym.ModNone)}, body)
	if f.Cursor() != 0 {
		t.Fatalf("Home left the cursor at %d", f.Cursor())
	}
	h.frame([]Event{keyDown(keysym.KeyDelete, keysym.ModNone)}, body)
	if f.Value() != "b" {
		t.Fatalf("delete produced %q", f.Value())
	}
	h.frame([]Event{keyDown(keysym.KeyRight, keysym.ModNone), keyDown(keysym.KeyReturn, keysym.ModNone)}, body)
	if f.Cursor() != 1 {
		t.Fatalf("the right arrow left the cursor at %d", f.Cursor())
	}
	if submitted != 1 {
		t.Fatalf("Enter produced %d submissions, want 1", submitted)
	}
	if f.Value() != "b" {
		t.Fatalf("Enter or an arrow was inserted as text: %q", f.Value())
	}

	// Text arriving in the same batch as an editing key is applied where it
	// arrived, not after everything else.
	h.frame([]Event{text("x"), keyDown(keysym.KeyBackSpace, keysym.ModNone)}, body)
	if f.Value() != "b" {
		t.Fatalf("text then backspace produced %q, want %q", f.Value(), "b")
	}
}

// TestTextInputIgnoresTextFromACommandChord: Ctrl-V pastes. If the platform
// also reported a "v", the field would paste and then type the v over it.
func TestTextInputIgnoresTextFromACommandChord(t *testing.T) {
	h := newHarness(400, 100)
	h.ctx.Clipboard = func() (string, error) { return "pasted", nil }
	f := &TextInput{ID: "field"}
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}
	body := func(ctx *Context) { f.Layout(ctx, rect) }
	h.frame(nil, body)

	// Prove the backend composes first, so the field is in the mode where a
	// text event would be believed.
	h.frame([]Event{runeDown('a', keysym.ModNone), text("a")}, body)

	h.frame([]Event{runeDown('v', keysym.ModControl), text("v")}, body)
	if f.Value() != "apasted" {
		t.Fatalf("Ctrl-V produced %q, want %q", f.Value(), "apasted")
	}

	// The readline bindings are chords too, and they move the cursor rather
	// than typing their letter.
	h.frame([]Event{runeDown('u', keysym.ModControl), text("u")}, body)
	if f.Value() != "" {
		t.Fatalf("Ctrl-U left %q", f.Value())
	}
}

func TestTextInputPastesFromTheClipboard(t *testing.T) {
	h := newHarness(400, 100)
	h.ctx.Clipboard = func() (string, error) { return "https://kw.example.com\nsecond line", nil }
	f := &TextInput{ID: "field"}
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}

	body := func(ctx *Context) { f.Layout(ctx, rect) }
	h.frame(nil, body)
	h.frame([]Event{runeDown('v', keysym.ModControl)}, body)

	if f.Value() != "https://kw.example.com" {
		t.Fatalf("paste produced %q; a single-line field must take the first line only", f.Value())
	}
}

func TestTextInputPasswordMasking(t *testing.T) {
	h := newHarness(400, 100)
	f := &TextInput{ID: "pw", Password: true}
	f.SetValue("hunter2")
	h.frame(nil, func(ctx *Context) { f.Layout(ctx, Rect{X: 10, Y: 10, W: 300, H: 34}) })

	// The value is intact...
	if f.Value() != "hunter2" {
		t.Fatalf("masking changed the value to %q", f.Value())
	}
	// ...and what was drawn is not it. Comparing the canvas against the same
	// field unmasked is the only way to assert this from outside.
	masked := h.ctx.Canvas.Image().Pix
	plain := newHarness(400, 100)
	f2 := &TextInput{ID: "pw"}
	f2.SetValue("hunter2")
	plain.frame(nil, func(ctx *Context) { f2.Layout(ctx, Rect{X: 10, Y: 10, W: 300, H: 34}) })
	if string(masked) == string(plain.ctx.Canvas.Image().Pix) {
		t.Fatal("a password field drew its contents in the clear")
	}
}

func TestTextInputSelectionModel(t *testing.T) {
	var f TextInput
	f.SetValue("hello")
	if f.HasSelection() {
		t.Fatal("a fresh value must not be selected")
	}

	f.SelectAll()
	if lo, hi := f.SelectionRange(); lo != 0 || hi != 5 {
		t.Fatalf("select-all gave (%d, %d), want (0, 5)", lo, hi)
	}
	if f.SelectedText() != "hello" {
		t.Fatalf("selected text = %q, want %q", f.SelectedText(), "hello")
	}

	// Typing replaces the selection: the reason it exists.
	f.Insert('X')
	if f.Value() != "X" || f.HasSelection() {
		t.Fatalf("typing over a selection gave %q (selected: %v)", f.Value(), f.HasSelection())
	}

	// Backspace and DeleteForward prefer the selection over one rune.
	f.SetValue("hello")
	f.SelectAll()
	if !f.Backspace() || f.Value() != "" {
		t.Fatalf("backspace over a selection left %q", f.Value())
	}
	f.SetValue("hello")
	f.SelectAll()
	if !f.DeleteForward() || f.Value() != "" {
		t.Fatalf("delete over a selection left %q", f.Value())
	}

	// Movement collapses; it does not extend.
	f.SetValue("hello")
	f.SelectAll()
	f.MoveCursor(-1)
	if f.HasSelection() || f.Cursor() != 4 {
		t.Fatalf("MoveCursor left selection (%v) or cursor %d", f.HasSelection(), f.Cursor())
	}
	f.SelectAll()
	f.SetCursor(2)
	if f.HasSelection() || f.Cursor() != 2 {
		t.Fatal("SetCursor did not collapse the selection")
	}

	// Shift-arrow extends from the fixed anchor.
	f.SetCursor(5)
	f.extend(-2)
	if f.SelectedText() != "lo" {
		t.Fatalf("shift-left selected %q, want %q", f.SelectedText(), "lo")
	}
	// Extending back over the anchor flips the moving end past it.
	f.extend(1)
	if f.SelectedText() != "o" {
		t.Fatalf("shrinking the selection gave %q, want %q", f.SelectedText(), "o")
	}

	// KillToEnd takes the selection first, the tail second.
	f.SelectAll()
	f.KillToEnd()
	if f.Value() != "" {
		t.Fatalf("Ctrl-K over a selection left %q", f.Value())
	}
	f.SetValue("hello")
	f.SetCursor(2)
	f.KillToEnd()
	if f.Value() != "he" {
		t.Fatalf("Ctrl-K left %q, want %q", f.Value(), "he")
	}

	// A password field masks the drawing but copies what was typed.
	var pw TextInput
	pw.Password = true
	pw.SetValue("hunter2")
	pw.SelectAll()
	if pw.SelectedText() != "hunter2" {
		t.Fatalf("a masked field selected %q", pw.SelectedText())
	}
}

func TestTextInputShiftArrowSelection(t *testing.T) {
	h := newHarness(400, 100)
	f := &TextInput{ID: "field"}
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}
	body := func(ctx *Context) { f.Layout(ctx, rect) }
	h.frame(nil, body)

	h.frame([]Event{runeDown('h', keysym.ModNone), runeDown('e', keysym.ModNone),
		runeDown('l', keysym.ModNone), runeDown('l', keysym.ModNone),
		runeDown('o', keysym.ModNone)}, body)

	h.frame([]Event{keyDown(keysym.KeyLeft, keysym.ModShift), keyDown(keysym.KeyLeft, keysym.ModShift)}, body)
	if f.SelectedText() != "lo" {
		t.Fatalf("shift-left selected %q, want %q", f.SelectedText(), "lo")
	}

	// Typing replaces the selection in place.
	h.frame([]Event{runeDown('X', keysym.ModNone)}, body)
	if f.Value() != "helX" {
		t.Fatalf("typing over a selection gave %q, want %q", f.Value(), "helX")
	}

	// Plain movement collapses again.
	h.frame([]Event{keyDown(keysym.KeyLeft, keysym.ModNone)}, body)
	if f.HasSelection() {
		t.Fatal("a plain arrow key did not collapse the selection")
	}
}

func TestTextInputWordNavigation(t *testing.T) {
	var f TextInput
	f.SetValue("foo bar/baz")

	f.SetCursor(0)
	f.cursor = wordEnd(f.text, f.cursor)
	if f.Cursor() != 3 {
		t.Fatalf("Ctrl-Right stopped at %d, want 3", f.Cursor())
	}
	f.cursor = wordEnd(f.text, f.cursor)
	if f.Cursor() != 7 {
		t.Fatalf("Ctrl-Right stopped at %d, want 7", f.Cursor())
	}

	f.SetCursor(f.Len())
	f.cursor = wordStart(f.text, f.cursor)
	if f.Cursor() != 8 {
		t.Fatalf("Ctrl-Left stopped at %d, want 8", f.Cursor())
	}

	// Word extension through the key path, the way a user reaches it.
	h := newHarness(400, 100)
	g := &TextInput{ID: "field"}
	g.SetValue("foo bar")
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}
	body := func(ctx *Context) { g.Layout(ctx, rect) }
	h.frame(nil, body)
	h.frame([]Event{keyDown(keysym.KeyLeft, keysym.ModControl|keysym.ModShift)}, body)
	if g.SelectedText() != "bar" {
		t.Fatalf("Ctrl-Shift-Left selected %q, want %q", g.SelectedText(), "bar")
	}
}

func TestTextInputClipboardKeys(t *testing.T) {
	h := newHarness(400, 100)
	var copied string
	h.ctx.SetClipboard = func(s string) error { copied = s; return nil }
	h.ctx.Clipboard = func() (string, error) { return "pasted", nil }
	f := &TextInput{ID: "field"}
	f.SetValue("hello")
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}
	body := func(ctx *Context) { f.Layout(ctx, rect) }
	h.frame(nil, body)

	// Copy with no selection must not wipe the clipboard.
	h.frame([]Event{runeDown('c', keysym.ModControl)}, body)
	if copied != "" {
		t.Fatalf("copy with no selection wrote %q to the clipboard", copied)
	}

	// Select all, copy, cut, paste back.
	h.frame([]Event{runeDown('a', keysym.ModControl)}, body)
	if !f.HasSelection() {
		t.Fatal("Ctrl-A did not select all")
	}
	h.frame([]Event{runeDown('c', keysym.ModControl)}, body)
	if copied != "hello" {
		t.Fatalf("copy put %q on the clipboard, want %q", copied, "hello")
	}
	h.frame([]Event{runeDown('x', keysym.ModControl)}, body)
	if f.Value() != "" || copied != "hello" {
		t.Fatalf("cut left %q (clipboard %q)", f.Value(), copied)
	}
	h.frame([]Event{runeDown('v', keysym.ModControl)}, body)
	if f.Value() != "pasted" {
		t.Fatalf("paste produced %q, want %q", f.Value(), "pasted")
	}

	// Cmd (Super) variants, for macOS muscle memory.
	h.frame([]Event{runeDown('a', keysym.ModSuper)}, body)
	h.frame([]Event{runeDown('c', keysym.ModSuper)}, body)
	if copied != "pasted" {
		t.Fatalf("Cmd-C put %q on the clipboard, want %q", copied, "pasted")
	}
}

func TestTextInputDragSelects(t *testing.T) {
	h := newHarness(400, 100)
	f := &TextInput{ID: "field"}
	f.SetValue("abcdefgh")
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}
	body := func(ctx *Context) { f.Layout(ctx, rect) }
	h.frame(nil, body)

	cell := h.ctx.Theme.Font.GlyphAdvance * h.ctx.Theme.Body
	x0 := rect.X + h.ctx.Theme.Gap

	// Press after the first glyph, drag to after the fourth, release.
	h.frame([]Event{pointer(x0+cell+1, 20, true)}, body)
	h.frame([]Event{pointer(x0+cell*4+1, 20, true)}, body)
	if lo, hi := f.SelectionRange(); lo != 1 || hi != 4 {
		t.Fatalf("drag selected (%d, %d), want (1, 4)", lo, hi)
	}
	h.frame([]Event{pointer(x0+cell*4+1, 20, false)}, body)
	if lo, hi := f.SelectionRange(); lo != 1 || hi != 4 {
		t.Fatalf("release collapsed the drag to (%d, %d)", lo, hi)
	}
}

func TestTextInputDoubleClickSelectsWord(t *testing.T) {
	h := newHarness(400, 100)
	f := &TextInput{ID: "field"}
	f.SetValue("foo bar")
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}
	body := func(ctx *Context) { f.Layout(ctx, rect) }
	h.frame(nil, body)

	cell := h.ctx.Theme.Font.GlyphAdvance * h.ctx.Theme.Body
	// Mid-word in "bar" (runes 4-6).
	x := rect.X + h.ctx.Theme.Gap + cell*4 + 1

	h.click(x, 20, body)
	if f.HasSelection() {
		t.Fatal("a single click must not select")
	}
	h.click(x, 20, body)
	if f.SelectedText() != "bar" {
		t.Fatalf("double-click selected %q, want %q", f.SelectedText(), "bar")
	}
	h.click(x, 20, body)
	if lo, hi := f.SelectionRange(); lo != 0 || hi != f.Len() {
		t.Fatalf("triple-click selected (%d, %d), want the whole field", lo, hi)
	}
}

func TestTextInputPointerPlacesTheCursor(t *testing.T) {
	h := newHarness(400, 100)
	f := &TextInput{ID: "field"}
	f.SetValue("abcdefgh")
	rect := Rect{X: 10, Y: 10, W: 300, H: 34}
	body := func(ctx *Context) { f.Layout(ctx, rect) }

	h.frame(nil, body)
	if f.Cursor() != 8 {
		t.Fatalf("SetValue left the cursor at %d, want the end", f.Cursor())
	}

	// Click near the left edge of the text.
	cell := h.ctx.Theme.Font.GlyphAdvance * h.ctx.Theme.Body
	h.frame([]Event{pointer(rect.X+h.ctx.Theme.Gap+cell+1, 20, true)}, body)
	if f.Cursor() != 1 {
		t.Fatalf("clicking after the first glyph put the cursor at %d, want 1", f.Cursor())
	}
}

func TestCheckboxToggles(t *testing.T) {
	h := newHarness(400, 100)
	c := &Checkbox{ID: "tls", Label: "Ignore TLS certificate errors"}
	rect := Rect{X: 10, Y: 10, W: 360, H: 34}
	body := func(ctx *Context) { c.Layout(ctx, rect) }

	h.click(20, 20, body)
	if !c.Checked {
		t.Fatal("clicking did not check the box")
	}
	h.click(20, 20, body)
	if c.Checked {
		t.Fatal("clicking again did not uncheck the box")
	}

	h.frame(nil, body)
	h.frame([]Event{runeDown(' ', keysym.ModNone)}, body)
	if !c.Checked {
		t.Fatal("Space did not toggle the focused checkbox")
	}
}

func TestSpinnerAsksForAnotherFrame(t *testing.T) {
	h := newHarness(100, 100)
	h.frame(nil, func(ctx *Context) { Spinner(ctx, Rect{X: 10, Y: 10, W: 40, H: 40}, Transparent) })
	d, ok := h.ctx.RepaintDelay()
	if !ok || d <= 0 {
		t.Fatal("a busy indicator that never asks to be redrawn is a still image")
	}
	if d > time.Second {
		t.Fatalf("the spinner asked for a frame in %v; it would look stopped", d)
	}
}

func TestBannerReportsItsHeight(t *testing.T) {
	h := newHarness(400, 200)
	var short, long int
	h.frame(nil, func(ctx *Context) {
		short = Banner(ctx, Rect{X: 0, Y: 0, W: 380, H: 100}, BannerError, "Nope.")
		long = Banner(ctx, Rect{X: 0, Y: 100, W: 380, H: 100}, BannerError,
			"The server's TLS certificate was not accepted: it was not issued by a trusted authority. "+
				"If this is a development instance, enable the option below.")
	})
	if short <= 0 {
		t.Fatal("a banner with text reported no height")
	}
	if long <= short {
		t.Fatalf("a wrapped banner (%d) is not taller than a one-line one (%d)", long, short)
	}
	h.frame(nil, func(ctx *Context) {
		if got := Banner(ctx, Rect{X: 0, Y: 0, W: 380, H: 100}, BannerInfo, ""); got != 0 {
			t.Fatalf("an empty banner took %d pixels", got)
		}
	})
}
