// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
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
// Ctrl modifies the navigation keys into the "modified" CSI forms
// (ESC [ 1;5D and friends); Alt prefixes a second ESC.
func namedKeyBytes(e viewer.EventKey) []byte {
	ctrl := e.Mods.Has(keysym.ModControl) && !e.Mods.Has(keysym.ModAlt)
	alt := e.Mods.Has(keysym.ModAlt) && !e.Mods.Has(keysym.ModControl)

	var seq string
	switch e.Key {
	case keysym.KeyReturn, keysym.KeyKPEnter:
		seq = "\r"

	case keysym.KeyTab:
		if e.Mods.Has(keysym.ModShift) {
			seq = "\x1b[Z"
		} else {
			seq = "\t"
		}

	case keysym.KeyBackSpace:
		seq = "\x7f"

	case keysym.KeyEscape:
		seq = "\x1b"

	case keysym.KeyDelete:
		seq = "\x1b[3~"
	case keysym.KeyInsert:
		seq = "\x1b[2~"

	case keysym.KeyLeft:
		seq = csiCtrl("D", ctrl)
	case keysym.KeyRight:
		seq = csiCtrl("C", ctrl)
	case keysym.KeyUp:
		seq = csiCtrl("A", ctrl)
	case keysym.KeyDown:
		seq = csiCtrl("B", ctrl)

	case keysym.KeyHome:
		if ctrl {
			seq = "\x1b[1;5H"
		} else {
			seq = "\x1b[H"
		}
	case keysym.KeyEnd:
		if ctrl {
			seq = "\x1b[1;5F"
		} else {
			seq = "\x1b[F"
		}
	case keysym.KeyPageUp:
		if ctrl {
			seq = "\x1b[5;5~"
		} else {
			seq = "\x1b[5~"
		}
	case keysym.KeyPageDown:
		if ctrl {
			seq = "\x1b[6;5~"
		} else {
			seq = "\x1b[6~"
		}

	case keysym.KeyF1:
		seq = "\x1bOP"
	case keysym.KeyF2:
		seq = "\x1bOQ"
	case keysym.KeyF3:
		seq = "\x1bOR"
	case keysym.KeyF4:
		seq = "\x1bOS"
	case keysym.KeyF5:
		seq = "\x1b[15~"
	case keysym.KeyF6:
		seq = "\x1b[17~"
	case keysym.KeyF7:
		seq = "\x1b[18~"
	case keysym.KeyF8:
		seq = "\x1b[19~"
	case keysym.KeyF9:
		seq = "\x1b[20~"
	case keysym.KeyF10:
		seq = "\x1b[21~"
	case keysym.KeyF11:
		seq = "\x1b[23~"
	case keysym.KeyF12:
		seq = "\x1b[24~"

	case keysym.KeyKP1:
		seq = "1"
	case keysym.KeyKP2:
		seq = "2"
	case keysym.KeyKP3:
		seq = "3"
	case keysym.KeyKP4:
		seq = "4"
	case keysym.KeyKP5:
		seq = "5"
	case keysym.KeyKP6:
		seq = "6"
	case keysym.KeyKP7:
		seq = "7"
	case keysym.KeyKP8:
		seq = "8"
	case keysym.KeyKP9:
		seq = "9"
	case keysym.KeyKP0:
		seq = "0"
	case keysym.KeyKPSpace:
		seq = " "
	case keysym.KeyKPAdd:
		seq = "+"
	case keysym.KeyKPSubtract:
		seq = "-"
	case keysym.KeyKPMultiply:
		seq = "*"
	case keysym.KeyKPDivide:
		seq = "/"
	case keysym.KeyKPDecimal:
		seq = "."

	default:
		return nil
	}
	if alt {
		return append([]byte{0x1b}, seq...)
	}
	return []byte(seq)
}

// csiCtrl renders a plain or Ctrl-modified CSI final byte: ESC [ D when Ctrl
// is not held, ESC [ 1;5D when it is.
func csiCtrl(final string, ctrl bool) string {
	if ctrl {
		return "\x1b[1;5" + final
	}
	return "\x1b[" + final
}

func lowerRune(r rune) rune {
	return unicode.ToLower(r)
}
