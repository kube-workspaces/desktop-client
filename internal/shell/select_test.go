// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

// TestBrowserWaitCopyButtonCopiesURL proves the waiting screen's Copy button
// carries the whole authorization URL to the host clipboard: a browser that
// opened on another desktop leaves the user with only this window.
func TestBrowserWaitCopyButtonCopiesURL(t *testing.T) {
	r := newRig(savedProfile(), "")
	r.start()
	const url = "https://idp.example.com/realms/kw/protocol/openid-connect/auth?client_id=kw&state=abc"
	r.app.m.State = StateLogin
	r.app.m.Busy = true
	r.app.m.BusyText = i18n.Get("busy.waitingBrowser") + "..."
	r.app.authorizeURL = url
	r.step()

	r.focus(idCopyURL)
	r.clickFocused()
	if got := r.be.getClipboard(); got != url {
		t.Fatalf("Copy button put %q on the clipboard, want the URL", got)
	}
}

// TestMessageBannerCopiesError proves the message strip is selectable: an
// error on the server screen can be selected and copied for a bug report.
func TestMessageBannerCopiesError(t *testing.T) {
	r := newRig(nil, "")
	r.start()
	const msg = "connection refused: dial tcp 10.0.0.4:443"
	r.app.m.Err = msg
	r.step()
	if h := r.app.messageHeight(1000); h <= 0 {
		t.Fatalf("messageHeight reserved %d px for a visible error", h)
	}

	r.focus(idMsgSel)
	r.be.send(ui.EventKey{Rune: 'a', Down: true, Mods: keysym.ModControl})
	r.step()
	r.be.send(ui.EventKey{Rune: 'c', Down: true, Mods: keysym.ModControl})
	r.step()
	if got := r.be.getClipboard(); got != msg {
		t.Fatalf("banner copy put %q on the clipboard, want %q", got, msg)
	}
}
