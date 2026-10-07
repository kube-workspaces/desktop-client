// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

func TestClipboardTypingWireAndHotkeyReleases(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{ClipboardInterval: -1})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)
	h.be.clipboard = "aZ9_!"
	h.be.push(keyDown(keysym.KeyControlL, keysym.ModControl), keyDown(keysym.KeyAltL, keysym.ModControl|keysym.ModAlt))
	h.step(t)
	h.srv.drain(t)
	h.be.push(EventKey{Rune: 'v', Down: true, Mods: keysym.ModControl | keysym.ModAlt})
	h.step(t)
	got := h.srv.keys(t)
	// The modifiers that invoked the hotkey must be released before typing.
	if len(got) < 4 || got[0].down || got[1].down {
		t.Fatalf("missing modifier release before typing: %v", got)
	}
	got = got[2:]
	h.be.push(EventKey{Rune: 'v'}, keyUp(keysym.KeyAltL, 0), keyUp(keysym.KeyControlL, 0))
	h.step(t)
	for _, k := range h.srv.keys(t) {
		if k.down || k.sym == 'v' {
			t.Fatalf("hotkey leaked into guest: %v", k)
		}
	}
	for range 4 {
		h.advance(30 * time.Millisecond)
		h.step(t)
		got = append(got, h.srv.keys(t)...)
	}
	want := []keyMsg{
		{sym: 'a', down: true}, {sym: 'a'},
		{sym: uint32(keysym.ShiftL), down: true}, {sym: 'z', down: true}, {sym: 'z'}, {sym: uint32(keysym.ShiftL)},
		{sym: '9', down: true}, {sym: '9'},
		{sym: uint32(keysym.ShiftL), down: true}, {sym: '-', down: true}, {sym: '-'}, {sym: uint32(keysym.ShiftL)},
		{sym: uint32(keysym.ShiftL), down: true}, {sym: '1', down: true}, {sym: '1'}, {sym: uint32(keysym.ShiftL)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keyboard stream = %v, want %v", got, want)
	}
	if len(h.v.clipboardTyping) != 0 || len(h.v.held) != 0 || len(h.v.mods.ReleaseAll()) != 0 {
		t.Fatal("typing left pending text or held keys")
	}
}

func TestClipboardTypingRejectsEntireInvalidValue(t *testing.T) {
	h := newHarness(t, 800, 600, 800, 600, Config{ClipboardInterval: -1})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)
	for _, text := range []string{"", "password\n", "password\t", "password\x00", "passwordé", strings.Repeat("a", 257)} {
		h.be.clipboard = text
		h.be.push(EventConnectionAction{Action: connection.TypeClipboard})
		h.step(t)
		if got := h.srv.keys(t); len(got) != 0 {
			t.Fatalf("invalid clipboard sent keys: %v", got)
		}
		if h.v.inputNotice == "" || len(h.v.clipboardTyping) != 0 {
			t.Fatal("invalid clipboard was not rejected with a message")
		}
	}
}

func TestClipboardTypingCancellationAndObserverGate(t *testing.T) {
	for _, event := range []Event{EventFocus{}, EventKey{Rune: 'x', Down: true}, EventPointer{Buttons: ButtonLeft}} {
		h := newHarness(t, 800, 600, 800, 600, Config{ClipboardInterval: -1})
		h.step(t)
		h.srv.drain(t)
		h.be.clipboard = "abc"
		h.be.push(EventConnectionAction{Action: connection.TypeClipboard})
		h.step(t)
		h.srv.drain(t)
		h.be.push(event)
		h.step(t)
		h.srv.drain(t)
		h.advance(time.Second)
		h.step(t)
		if got := h.srv.keys(t); len(got) != 0 || len(h.v.clipboardTyping) != 0 {
			t.Fatalf("cancelled text continued: %v", got)
		}
		h.v.stop()
	}
	h := newHarness(t, 800, 600, 800, 600, Config{ReadOnly: true, ClipboardInterval: -1})
	defer h.v.stop()
	h.step(t)
	h.srv.drain(t)
	h.be.clipboard = "secret"
	h.be.push(EventConnectionAction{Action: connection.TypeClipboard}, EventKey{Rune: 'v', Down: true, Mods: keysym.ModControl | keysym.ModAlt})
	h.step(t)
	if got := h.srv.keys(t); len(got) != 0 || len(h.v.clipboardTyping) != 0 {
		t.Fatalf("observer injected text: %v", got)
	}
}

func TestClipboardTypingDoesNotResumeAfterConnectionOrControlLoss(t *testing.T) {
	for _, drop := range []bool{false, true} {
		h := newHarness(t, 800, 600, 800, 600, Config{ClipboardInterval: -1})
		h.step(t)
		h.srv.drain(t)
		h.be.clipboard = "abc"
		h.be.push(EventConnectionAction{Action: connection.TypeClipboard})
		h.step(t)
		h.srv.drain(t)
		if drop {
			h.v.dropConn(nil)
			h.v.attach(h.conn, t.Context(), h.now)
		} else {
			h.v.SetReadOnly(true)
			h.step(t)
			h.v.SetReadOnly(false)
		}
		h.advance(time.Second)
		h.step(t)
		if got := h.srv.keys(t); len(got) != 0 || len(h.v.clipboardTyping) != 0 {
			t.Fatalf("typing resumed after control/transport loss: %v", got)
		}
		h.v.stop()
	}
}
