// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

func openUserMenu(t *testing.T, r *rig) {
	t.Helper()
	r.focus(idUserMenu)
	r.clickFocused()
	if !r.app.userMenuOpen || r.app.m.State != StateWorkspaces {
		t.Fatal("avatar did not open the menu, or opening also activated an action")
	}
}

func menuPointer(r *rig, p ui.Point, down bool) {
	buttons := ui.Buttons(0)
	if down {
		buttons = ui.ButtonLeft
	}
	r.be.send(ui.EventPointer{X: p.X, Y: p.Y, Buttons: buttons})
	r.step()
}

func TestUserMenuKeyboardAndFocus(t *testing.T) {
	r := newRig(savedProfile(), "token")
	r.start()
	order := slices.Clone(r.app.ctx.Focus().Order())
	if !slices.Contains(order, idUserMenu) || slices.Contains(order, idSettings) || slices.Contains(order, idProfiles) || slices.Contains(order, idSignOut) {
		t.Fatalf("closed header traversal = %v", order)
	}
	openUserMenu(t, r)
	if got := r.app.ctx.Focus().Order(); !slices.Equal(got, []ui.FocusID{idSettings, idProfiles, idSignOut}) {
		t.Fatalf("popup traversal includes background controls: %v", got)
	}
	r.press(keysym.KeyDown, keysym.ModNone)
	if !r.app.ctx.Focused(idProfiles) {
		t.Fatal("Down did not reach Profiles")
	}
	r.press(keysym.KeyTab, keysym.ModNone)
	if !r.app.ctx.Focused(idSignOut) {
		t.Fatal("Tab did not reach Sign out")
	}
	r.press(keysym.KeyTab, keysym.ModShift)
	if !r.app.ctx.Focused(idProfiles) {
		t.Fatal("Shift+Tab did not reach Profiles")
	}
	r.clickFocused()
	if !r.app.m.Profiles || r.app.userMenuOpen {
		t.Fatal("Profiles did not replace the dropdown with its existing modal")
	}
	r.press(keysym.KeyEscape, keysym.ModNone)
	if !r.app.ctx.Focused(idUserMenu) {
		t.Fatal("closing Profiles did not restore avatar focus")
	}
	openUserMenu(t, r)
	r.clickFocused()
	if r.app.m.State != StateSettings || r.app.userMenuOpen {
		t.Fatal("Settings did not close the menu and navigate")
	}
	r.press(keysym.KeyEscape, keysym.ModNone)
	if r.app.m.State != StateWorkspaces || !r.app.ctx.Focused(idUserMenu) {
		t.Fatal("returning from Settings lost avatar focus")
	}
}

func TestUserMenuDismissalConsumesBackgroundInput(t *testing.T) {
	r := newRig(savedProfile(), "token")
	r.start()
	r.app.filterField.SetValue("keep me")
	r.app.m.Filter = "keep me"
	r.app.m.Notice = "keep this too"
	openUserMenu(t, r)
	r.press(keysym.KeyEscape, keysym.ModNone)
	if r.app.userMenuOpen || r.app.filterField.Value() != "keep me" || r.app.m.Notice != "keep this too" || !r.app.ctx.Focused(idUserMenu) {
		t.Fatal("Escape leaked to the workspace screen or lost focus")
	}
	openUserMenu(t, r)
	// The filter is outside the popup. Neither half of the dismissal click
	// should give it focus or begin editing it.
	th := r.app.opts.Theme
	p := ui.Point{X: th.Pad + th.Gap, Y: th.Pad + ui.TextHeight(th.Title, th.Font) + 2*th.Gap + th.ControlHeight/2}
	menuPointer(r, p, true)
	if r.app.userMenuOpen {
		t.Fatal("outside press did not dismiss")
	}
	menuPointer(r, p, false)
	if !r.app.ctx.Focused(idUserMenu) || r.app.filterField.Value() != "keep me" {
		t.Fatal("outside dismissal clicked through to the filter")
	}
	// The original toolbar traversal is restored after dismissal.
	r.app.dirty = true
	r.step()
	if !slices.Contains(r.app.ctx.Focus().Order(), idCreate) || !slices.Contains(r.app.ctx.Focus().Order(), idRefresh) {
		t.Fatal("toolbar controls lost their focus registration")
	}
}

func TestUserMenuCoversToolbarWithoutActivatingIt(t *testing.T) {
	r := newRig(savedProfile(), "token")
	r.start()
	openUserMenu(t, r)
	var before int
	r.api.set(func(f *fakeAPI) { before = f.listCalls })
	th := r.app.opts.Theme
	anchor := r.app.userMenuAnchor(r.app.canvas.Bounds())
	p := ui.Point{X: anchor.X + anchor.W/2, Y: th.Pad + ui.TextHeight(th.Title, th.Font) + 2*th.Gap + th.ControlHeight/2}
	menuPointer(r, p, true)
	menuPointer(r, p, false)
	r.press(keysym.KeyF5, keysym.ModNone)
	r.settle()
	r.api.set(func(f *fakeAPI) {
		if f.listCalls != before {
			t.Errorf("covered Refresh or F5 leaked through: %d -> %d", before, f.listCalls)
		}
	})
	if r.app.m.Creating || !r.app.userMenuOpen {
		t.Fatal("toolbar interaction changed the underlying screen")
	}
	// Data refresh still renders and keeps the popup open.
	r.app.m.WorkspacesLoaded([]kwclient.Workspace{workspace("team", "new-data", kwclient.WorkspaceTypeVM, true)}, r.now)
	r.app.dirty = true
	r.step()
	if !r.app.userMenuOpen || len(r.app.m.Workspaces) != 1 {
		t.Fatal("refresh discarded menu or workspace data")
	}
}

func TestUserMenuInfoAndLifecycle(t *testing.T) {
	r := profilesRig(t)
	r.app.m.Identity.DisplayName = "Example User"
	r.app.m.TokenExpiry = r.now.Add(30 * time.Minute)
	openUserMenu(t, r)
	text := strings.Join(r.app.userMenuInfo(), "\n")
	for _, want := range []string{"Example User", "user@example.com", "kw.example.com", "https://kw.example.com", "editor", "session expires"} {
		if !strings.Contains(text, want) {
			t.Errorf("menu missing %q: %s", want, text)
		}
	}
	r.app.act(context.Background(), intent{kind: intentSwitchProfile, profile: "other.example.com"})
	r.settle()
	if r.app.userMenuOpen {
		t.Fatal("menu persisted across profile switch")
	}
	openUserMenu(t, r)
	r.app.act(context.Background(), intent{kind: intentSignOut})
	r.settle()
	if r.app.userMenuOpen || r.app.m.State == StateWorkspaces {
		t.Fatal("menu persisted after sign out")
	}
}

func TestUserMenuLongInfoScrollsAndActionsRemainReachable(t *testing.T) {
	old := i18n.Locale()
	i18n.SetLocale("xx")
	t.Cleanup(func() { i18n.SetLocale(old) })
	r := newRig(savedProfile(), "token")
	r.start()
	r.app.m.Identity.DisplayName = strings.Repeat("long name ", 500)
	r.app.m.Server = "https://" + strings.Repeat("endpoint-", 40) + ".example.com"
	openUserMenu(t, r)
	if r.app.userMenuScroll <= 0 {
		t.Fatalf("focused Settings not brought into view past long account info (focus=%s, body=%d)", r.app.ctx.Focus().Focus(), r.app.opts.Theme.Body)
	}
	r.press(keysym.KeyDown, keysym.ModNone)
	r.clickFocused()
	if !r.app.m.Profiles {
		t.Fatal("Profiles unreachable after long translated account info")
	}
}
