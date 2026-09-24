// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"strconv"
	"unicode"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// input translates backend events into the byte stream the shell expects.
//
// A terminal session is bytes in and bytes out: the shell derives everything —
// including Ctrl held or not — from the bytes it receives, so no modifier
// state needs forwarding, just the right sequence for each key. The encoding
// table deliberately matches what xterm sends, so readline, vim, tmux and
// every other program that reads Ctrl sequences keeps working.
type input struct {
	// quitRune is the letter that quits the session when pressed with
	// Ctrl+Alt; see [input.isQuit]. Zero means the default, 'q'.
	quitRune rune

	// composedText is true for backends that deliver text as EventText (SDL
	// does, always). A backend that emits only key events — the tested fake —
	// falls back to taking the character out of the key press instead.
	composedText bool

	// swallow remembers hotkey presses whose release must not reach the
	// shell. Without it, the key-up for a Ctrl+Alt+Q the session never saw
	// would be forwardable as a lone "q", and stray characters appear in the
	// workspace.
	swallow map[inputHotkey]bool
}

// inputHotkey identifies one physical press, the same shape the viewer uses.
type inputHotkey struct {
	key keysym.Key
	r   rune
}

func newInput() *input {
	return &input{quitRune: 'q', composedText: true, swallow: make(map[inputHotkey]bool)}
}

// handle translates one backend event. A result of out != nil carries bytes
// for the shell; quit reports that the user asked to close the session. Any
// event not produced by this input is returned as an empty result.
func (in *input) handle(e viewer.Event) (out []byte, quit bool) {
	switch ev := e.(type) {
	case viewer.EventQuit:
		return nil, true
	case viewer.EventKey:
		return in.handleKey(ev)
	case viewer.EventText:
		if in.composedText {
			return []byte(ev.Text), false
		}
	}
	return nil, false
}

// handleKey turns one key event into output. It never forwards the accidental
// by-products of a hotkey: the releases of keys the session never saw are
// swallowed.
func (in *input) handleKey(e viewer.EventKey) (out []byte, quit bool) {
	id := inputHotkey{key: e.Key, r: e.Rune}
	if !e.Down {
		delete(in.swallow, id)
		return nil, false
	}
	if in.isQuit(e) {
		in.swallow[id] = true
		// Auto-repeat must not quit twice over; one press acts once.
		if e.Repeat {
			return nil, false
		}
		return nil, true
	}
	return in.keyBytes(e), false
}

// isQuit reports whether this is the Ctrl+Alt+<quitRune> chord. It mirrors the
// viewer's quit binding so the two sessions end the same way: a session a
// user cannot leave is a session they will force-kill.
func (in *input) isQuit(e viewer.EventKey) bool {
	const chordMods = keysym.ModControl | keysym.ModAlt
	if !e.Mods.Has(chordMods) {
		return false
	}
	r := in.quitRune
	if r == 0 {
		r = 'q'
	}
	return e.Rune != 0 && lowerRune(e.Rune) == lowerRune(r)
}

// keyBytes translates a key press into the byte sequence to send. A nil slice
// means the key produces nothing — a modifier press, say, or a key the shell
// has no encoding for.
func (in *input) keyBytes(e viewer.EventKey) []byte {
	// A backend that composes text delivers it via EventText, so the key
	// event for a printable character is redundant: forwarding it would type
	// every character twice. A bare backend (or a test fake) has no text
	// events, and must take the character from here instead.
	if !in.composedText && ui.IsTextRune(e) {
		return []byte(string(e.Rune))
	}
	if e.Key != keysym.KeyUnknown {
		return namedKeyBytes(e)
	}
	if e.Rune != 0 {
		return chordBytes(e)
	}
	return nil
}

// chordBytes encodes Ctrl and Alt chords on a character. It is deliberately
// narrower than the code that could be written: Ctrl+Alt is the quit chord's
// territory (and AltGr's) and is left alone rather than half-typed.
func chordBytes(e viewer.EventKey) []byte {
	r := e.Rune
	if r < 0x20 || r > 0x7e {
		return nil
	}
	mods := e.Mods
	ctrl := mods.Has(keysym.ModControl) && !mods.Has(keysym.ModAlt) && !mods.Has(keysym.ModAltGr)
	meta := mods.Has(keysym.ModAlt) && !mods.Has(keysym.ModControl) && !mods.Has(keysym.ModSuper) && !mods.Has(keysym.ModAltGr)

	// Ctrl+letter is the rune with bit 6 cleared: Ctrl+A is 0x01, Ctrl+] is
	// 0x1d. Upper and lower case both work because the high bits are ignored.
	if ctrl {
		if r >= '@' && r <= '^' || r >= 'a' && r <= 'z' {
			return []byte{byte(r & 0x1f)}
		}
		return nil
	}
	// Alt alone is Meta: an ESC prefix turns the next character into a
	// keystroke the shell interprets as "with meta held".
	if meta {
		return append([]byte{0x1b}, byte(r))
	}
	return nil
}

// namedKeyBytes maps the non-text keys to the sequences xterm sends for them.
//
// An unmodified key sends the sequence the shipped table has always sent. The
// modifier-coded forms — what xterm emits for Shift/Ctrl and their
// combinations on the keys that have a CSI encoding — follow the modifier
// protocol (ESC [ 1;5D is Ctrl+Left, ESC [ 15;2~ is Shift+F5), so readline,
// tmux and vim keep working when a program binds a shifted or Ctrl'd key.
//
// Alt alone is deliberately not a modifier code. It is Meta, and meta is the
// two-key convention an ESC prefix expresses — the sequence default xterm has
// always sent, and the one readline, vim and emacs all expect. Super never
// participates: it is the OS's chord, not the shell's. AltGr is a third-level
// shift that produces characters, so it is treated as neither.
func namedKeyBytes(e viewer.EventKey) []byte {
	mods := e.Mods
	// AltGr is a third-level shift: it produces characters (on Windows it
	// often arrives as Ctrl+Alt plus the AltGr bit), so a key pressed with it
	// is not a command at all and keeps its unmodified sequence.
	if mods.Has(keysym.ModAltGr) {
		seq, ok := plainNamedKey(e.Key, mods)
		if !ok {
			return nil
		}
		return []byte(seq)
	}
	if meta := mods.Has(keysym.ModAlt) && !mods.HasAny(keysym.ModControl|keysym.ModSuper); meta {
		seq, ok := plainNamedKey(e.Key, mods)
		if !ok {
			return nil
		}
		return []byte("\x1b" + seq)
	}
	if code := xtermModCode(mods); code > 1 && modEncodable(e.Key) {
		return []byte(modifiedNamedKey(e.Key, code))
	}
	seq, ok := plainNamedKey(e.Key, mods)
	if !ok {
		return nil
	}
	return []byte(seq)
}

// xtermModCode encodes Shift, Alt and Ctrl into the modifier number the
// protocol carries after the semicolon: 1 with none, 2 Shift, 3 Alt, 4
// Shift+Alt, 5 Ctrl, 6 Ctrl+Shift, 7 Ctrl+Alt, 8 Ctrl+Shift+Alt. The two
// bits AltGr masks on anything (super, altgr, the lock keys) never add, and
// Alt alone never reaches the encoded form — [namedKeyBytes] sends it as the
// two-key ESC prefix instead.
func xtermModCode(mods keysym.Modifiers) int {
	code := 1
	if mods.Has(keysym.ModShift) {
		code += 1
	}
	if mods.Has(keysym.ModAlt) {
		code += 2
	}
	if mods.Has(keysym.ModControl) {
		code += 4
	}
	return code
}

// modEncodable reports whether k has a standard encoded form in the modifier
// protocol. Enter, Tab, Backspace and the keypad produce characters or have
// no distinct modified sequence, so they stay on the plain table.
func modEncodable(k keysym.Key) bool {
	switch k {
	case keysym.KeyLeft, keysym.KeyRight, keysym.KeyUp, keysym.KeyDown,
		keysym.KeyHome, keysym.KeyEnd, keysym.KeyPageUp, keysym.KeyPageDown,
		keysym.KeyInsert, keysym.KeyDelete,
		keysym.KeyF1, keysym.KeyF2, keysym.KeyF3, keysym.KeyF4,
		keysym.KeyF5, keysym.KeyF6, keysym.KeyF7, keysym.KeyF8,
		keysym.KeyF9, keysym.KeyF10, keysym.KeyF11, keysym.KeyF12:
		return true
	}
	return false
}

// plainNamedKey is the key's unmodified sequence: what xterm sends for it with
// no modifier, or with a modifier the key has no encoded form for. Shifted Tab
// is the one key whose modifier changes the plain form rather than the
// encoded one: ESC [ Z is the "backwards tab" sequence, older than the
// modifier protocol.
func plainNamedKey(k keysym.Key, mods keysym.Modifiers) (string, bool) {
	switch k {
	case keysym.KeyReturn, keysym.KeyKPEnter:
		return "\r", true

	case keysym.KeyTab:
		if mods.Has(keysym.ModShift) {
			return "\x1b[Z", true
		}
		return "\t", true

	case keysym.KeyBackSpace:
		return "\x7f", true

	case keysym.KeyEscape:
		return "\x1b", true

	case keysym.KeyDelete:
		return "\x1b[3~", true
	case keysym.KeyInsert:
		return "\x1b[2~", true

	case keysym.KeyLeft:
		return "\x1b[D", true
	case keysym.KeyRight:
		return "\x1b[C", true
	case keysym.KeyUp:
		return "\x1b[A", true
	case keysym.KeyDown:
		return "\x1b[B", true

	case keysym.KeyHome:
		return "\x1b[H", true
	case keysym.KeyEnd:
		return "\x1b[F", true
	case keysym.KeyPageUp:
		return "\x1b[5~", true
	case keysym.KeyPageDown:
		return "\x1b[6~", true

	case keysym.KeyF1:
		return "\x1bOP", true
	case keysym.KeyF2:
		return "\x1bOQ", true
	case keysym.KeyF3:
		return "\x1bOR", true
	case keysym.KeyF4:
		return "\x1bOS", true
	case keysym.KeyF5:
		return "\x1b[15~", true
	case keysym.KeyF6:
		return "\x1b[17~", true
	case keysym.KeyF7:
		return "\x1b[18~", true
	case keysym.KeyF8:
		return "\x1b[19~", true
	case keysym.KeyF9:
		return "\x1b[20~", true
	case keysym.KeyF10:
		return "\x1b[21~", true
	case keysym.KeyF11:
		return "\x1b[23~", true
	case keysym.KeyF12:
		return "\x1b[24~", true

	case keysym.KeyKP1:
		return "1", true
	case keysym.KeyKP2:
		return "2", true
	case keysym.KeyKP3:
		return "3", true
	case keysym.KeyKP4:
		return "4", true
	case keysym.KeyKP5:
		return "5", true
	case keysym.KeyKP6:
		return "6", true
	case keysym.KeyKP7:
		return "7", true
	case keysym.KeyKP8:
		return "8", true
	case keysym.KeyKP9:
		return "9", true
	case keysym.KeyKP0:
		return "0", true
	case keysym.KeyKPSpace:
		return " ", true
	case keysym.KeyKPAdd:
		return "+", true
	case keysym.KeyKPSubtract:
		return "-", true
	case keysym.KeyKPMultiply:
		return "*", true
	case keysym.KeyKPDivide:
		return "/", true
	case keysym.KeyKPDecimal:
		return ".", true

	default:
		return "", false
	}
}

// modifiedNamedKey is the modifier-protocol form of k: ESC [ 1;<code>X for
// the cursor keys and F1-F4, ESC [ <num>;<code>~ for the keys whose plain
// form is a numbered tilde sequence (F5-F12, PageUp/PageDown, Insert,
// Delete).
func modifiedNamedKey(k keysym.Key, code int) string {
	num := 0
	final := ""
	switch k {
	case keysym.KeyF1:
		final = "P"
	case keysym.KeyF2:
		final = "Q"
	case keysym.KeyF3:
		final = "R"
	case keysym.KeyF4:
		final = "S"
	case keysym.KeyF5:
		num = 15
	case keysym.KeyF6:
		num = 17
	case keysym.KeyF7:
		num = 18
	case keysym.KeyF8:
		num = 19
	case keysym.KeyF9:
		num = 20
	case keysym.KeyF10:
		num = 21
	case keysym.KeyF11:
		num = 23
	case keysym.KeyF12:
		num = 24

	case keysym.KeyLeft:
		final = "D"
	case keysym.KeyRight:
		final = "C"
	case keysym.KeyUp:
		final = "A"
	case keysym.KeyDown:
		final = "B"

	case keysym.KeyHome:
		final = "H"
	case keysym.KeyEnd:
		final = "F"
	case keysym.KeyPageUp:
		num = 5
	case keysym.KeyPageDown:
		num = 6

	case keysym.KeyInsert:
		num = 2
	case keysym.KeyDelete:
		num = 3

	default:
		return ""
	}
	if num != 0 {
		return "\x1b[" + strconv.Itoa(num) + ";" + strconv.Itoa(code) + "~"
	}
	return "\x1b[1;" + strconv.Itoa(code) + final
}

func lowerRune(r rune) rune {
	return unicode.ToLower(r)
}
