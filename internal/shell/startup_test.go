// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"errors"
	"strings"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/autostart"
)

type fakeAutostart struct {
	status autostart.Status
	err    error
	writes int
}

func (f *fakeAutostart) Status() (autostart.Status, error) { return f.status, nil }
func (f *fakeAutostart) SetEnabled(enabled bool) (autostart.Status, error) {
	f.writes++
	if f.err == nil {
		f.status.Enabled = enabled
	}
	return f.status, f.err
}

func TestStartupSettingsFollowOSAndReportFailures(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	f := &fakeAutostart{}
	r.app.opts.Autostart = f
	r.start()
	if f.writes != 0 {
		t.Fatal("launch modified startup registration")
	}
	r.focus(idSettings)
	r.clickFocused()
	r.settle()
	r.focus(idStartup)
	r.clickFocused()
	r.settle()
	if !r.app.startup.status.Enabled || f.writes != 1 {
		t.Fatalf("setting did not enable startup: %+v, writes=%d", r.app.startup, f.writes)
	}
	f.err = errors.New("permission denied")
	r.focus(idStartup)
	r.clickFocused()
	r.settle()
	if !r.app.startup.status.Enabled || !strings.Contains(r.app.startup.err, "permission denied") {
		t.Fatalf("failed disable lost real state / error: %+v", r.app.startup)
	}
	// An external OS change is discovered on the next visit, not repaired.
	f.status = autostart.Status{Enabled: false}
	r.focus(idSettingsDone)
	r.clickFocused()
	r.settle()
	r.focus(idSettings)
	r.clickFocused()
	r.settle()
	if r.app.startup.status.Enabled || f.writes != 2 || r.app.startup.err != "" {
		t.Fatalf("OS change was not reflected: %+v, writes=%d", r.app.startup, f.writes)
	}
}

func TestStartupSettingsApprovalAndSmallWindowScrolling(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	f := &fakeAutostart{status: autostart.Status{Enabled: true, NeedsApproval: true}}
	r.app.opts.Autostart = f
	r.start()
	r.focus(idSettings)
	r.clickFocused()
	r.settle()
	if !r.app.startup.status.NeedsApproval {
		t.Fatal("OS approval requirement was lost")
	}
	r.be.resize(DefaultWidth, 450)
	r.focus(idStartup)
	r.app.dirty = true
	r.settle()
	if r.app.settingsScroll <= 0 {
		t.Fatal("keyboard focus did not scroll the startup preference into view")
	}
	view := r.app.settingsFocusedRect
	if view.Y < 0 || view.Y+view.H > 450 {
		t.Fatalf("startup preference still outside window after scrolling: %+v", view)
	}
}
