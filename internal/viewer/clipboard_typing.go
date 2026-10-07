// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"strings"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

// This is keyboard injection, independent of guest clipboard/agent support.
// The guest must use a US keyboard layout with Caps Lock off. Reject the whole
// value before sending anything; in particular, never turn a newline into Enter.
func (v *Viewer) startClipboardTyping() {
	if !v.connectionSnapshot().Availability(connection.TypeClipboard).Enabled || !v.focused || len(v.clipboardTyping) != 0 {
		return
	}
	text, err := v.be.Clipboard()
	if err != nil {
		v.clipboardTypingNotice("viewer.clipboardUnavailable")
		return
	}
	if len(text) == 0 || len(text) > 256 || strings.IndexFunc(text, func(r rune) bool { return r < 32 || r > 126 }) >= 0 {
		v.clipboardTypingNotice("viewer.clipboardInvalid")
		return
	}
	// Release physical modifiers before injecting text. Do not let a hotkey's
	// eventual V-up escape to the guest after releaseInput clears the map.
	swallow := v.swallow
	v.releaseInput()
	v.swallow = swallow
	v.inputNotice = ""
	v.clipboardTyping = []byte(text)
	v.clipboardTypingDue = time.Time{}
	v.markPresent()
}

func (v *Viewer) clipboardTypingNotice(key string) {
	v.inputNotice = i18n.Get(key)
	v.inputNoticeDue = time.Now().Add(4 * time.Second)
	v.markPresent()
}

func (v *Viewer) cancelClipboardTyping() {
	clear(v.clipboardTyping)
	v.clipboardTyping = nil
}

// Pace complete key taps on the main-thread pump, without sleeps or goroutines.
// Abort on focus/control/transport changes rather than replaying a password
// into a newly connected display or a different field.
func (v *Viewer) typeClipboardStep(now time.Time) error {
	if len(v.clipboardTyping) == 0 {
		return nil
	}
	if !v.focused || !v.connectionSnapshot().Availability(connection.TypeClipboard).Enabled {
		v.cancelClipboardTyping()
		return nil
	}
	if now.Before(v.clipboardTypingDue) {
		return nil
	}
	ch := v.clipboardTyping[0]
	v.clipboardTyping[0] = 0
	v.clipboardTyping = v.clipboardTyping[1:]
	v.clipboardTypingDue = now.Add(30 * time.Millisecond)
	base, shift := usTypingKey(ch)
	sequence := []keysym.KeyAction{{Sym: keysym.Keysym(base), Down: true}, {Sym: keysym.Keysym(base), Down: false}}
	if shift {
		sequence = append([]keysym.KeyAction{{Sym: keysym.ShiftL, Down: true}}, sequence...)
		sequence = append(sequence, keysym.KeyAction{Sym: keysym.ShiftL, Down: false})
	}
	for _, a := range sequence {
		// Track synthetic keys too so shutdown/focus loss releases a key if a
		// write fails partway through its tap.
		if a.Down {
			v.noteHeld(a.Sym)
		} else {
			v.forgetHeld(a.Sym)
		}
		v.mods.TrackAction(a)
		if err := v.conn.KeyEvent(uint32(a.Sym), a.Down); err != nil {
			v.cancelClipboardTyping()
			return v.connWrite(err)
		}
	}
	return nil
}

func usTypingKey(ch byte) (byte, bool) {
	if ch >= 'A' && ch <= 'Z' {
		return ch + ('a' - 'A'), true
	}
	const shifted = "~!@#$%^&*()_+{}|:\"<>?"
	const unshifted = "`1234567890-=[]\\;',./"
	if i := strings.IndexByte(shifted, ch); i >= 0 {
		return unshifted[i], true
	}
	return ch, false
}
