package shell

import (
	"context"
	"errors"
	"image"
	"image/color"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/update"
)

type fakeUpdater struct {
	checks atomic.Int64
	err    error
}

func (u *fakeUpdater) Check(_ context.Context, current string) (update.Result, error) {
	u.checks.Add(1)
	return update.Check(current, &update.Release{Tag: "v1.2.3"}), u.err
}
func (*fakeUpdater) Prepare(context.Context, *update.Release, func(int64)) (*update.Prepared, error) {
	return nil, errors.New("fixture download failed")
}

func TestUpdateCadenceAndPreferencesSurviveAppearanceSaves(t *testing.T) {
	r := newRig(savedProfile(), "token")
	u := &fakeUpdater{}
	r.app.opts.Version = "v1.0.0"
	r.app.opts.Updater = u
	r.start()
	if u.checks.Load() != 1 || !r.app.updates.result.Available {
		t.Fatal("startup did not discover newer release")
	}
	if r.store.settings.LastUpdateCheck == 0 {
		t.Fatal("check time was not saved")
	}
	r.app.act(context.Background(), intent{kind: intentToggleUpdate})
	r.settle()
	r.app.applySettings(DefaultSettings())
	r.app.saveSettings()
	r.settle()
	if r.store.settings.AutoUpdateEnabled() || r.store.settings.LastUpdateCheck == 0 {
		t.Fatal("appearance save reset update preferences")
	}
	r.app.checkUpdate(context.Background(), false)
	r.settle()
	if u.checks.Load() != 1 {
		t.Fatal("opt-out ignored")
	}
	r.app.checkUpdate(context.Background(), true)
	r.settle()
	if u.checks.Load() != 2 {
		t.Fatal("manual check blocked by preference")
	}
}

func TestUpdateStartupSuppression(t *testing.T) {
	for _, which := range []string{"dev", "managed", "environment", "recent"} {
		t.Run(which, func(t *testing.T) {
			r := newRig(nil, "")
			u := &fakeUpdater{}
			r.app.opts.Version = "v1.0.0"
			r.app.opts.Updater = u
			r.app.opts.UpdatePolicy = func() (bool, bool) { return which == "managed", which == "environment" }
			if which == "dev" {
				r.app.opts.Version = "v1.0.0-3-gabc"
			}
			if which == "recent" {
				r.store.settings = config.Settings{LastUpdateCheck: time.Now().Unix()}
			}
			r.start()
			if u.checks.Load() != 0 {
				t.Fatal("unexpected startup request")
			}
			r.app.checkUpdate(context.Background(), true)
			r.settle()
			want := int64(1)
			if which == "managed" {
				want = 0
			}
			if u.checks.Load() != want {
				t.Fatal("manual check policy mismatch")
			}
		})
	}
}

func TestRestartUpdatePreservesHeldSessions(t *testing.T) {
	r := newRig(nil, "")
	r.start()
	r.app.updates.prepared = &update.Prepared{Tag: "v1.2.3"}
	restarted := false
	r.app.opts.RestartUpdate = func(*update.Prepared) error { restarted = true; return nil }
	r.app.sessions["held"] = &sessionRecord{}
	r.app.restartUpdate()
	if restarted || r.app.quit {
		t.Fatal("update interrupted a parked session")
	}
	delete(r.app.sessions, "held")
	webProcesses.Add(1)
	r.app.restartUpdate()
	webProcesses.Add(-1)
	if restarted || r.app.quit {
		t.Fatal("update interrupted a web child")
	}
	// Assert failures leave the shell running and the verified stage retryable.
	r.app.opts.RestartUpdate = func(*update.Prepared) error { return errors.New("install read-only") }
	r.app.restartUpdate()
	if r.app.quit || r.app.updates.prepared == nil {
		t.Fatal("failed handoff lost the stage or quit")
	}
}

func TestUpdateUpToDateShowsGreenTick(t *testing.T) {
	r := newRig(savedProfile(), "token")
	u := &fakeUpdater{}
	r.app.opts.Version = "v1.2.3" // equal to the fixture tag: nothing newer
	r.app.opts.Updater = u
	r.start()
	r.settle()

	if r.app.updates.result.Available || !r.app.updates.upToDate {
		t.Fatalf("update state = available:%v upToDate:%v, want up to date",
			r.app.updates.result.Available, r.app.updates.upToDate)
	}
	if r.app.updates.status != i18n.Get("updates.upToDate") {
		t.Fatalf("status = %q, want %q", r.app.updates.status, i18n.Get("updates.upToDate"))
	}

	r.app.act(context.Background(), intent{kind: intentUpdates})
	r.settle()
	if r.app.m.State != StateUpdates {
		t.Fatalf("updates screen did not open (state=%v)", r.app.m.State)
	}
	if !containsColor(r.app.img, r.app.opts.Theme.Success) {
		t.Fatalf("no green tick drawn next to the up-to-date status")
	}
}

func TestUpdateAvailableDrawsNoTick(t *testing.T) {
	r := newRig(savedProfile(), "token")
	u := &fakeUpdater{}
	r.app.opts.Version = "v1.0.0"
	r.app.opts.Updater = u
	r.start()
	r.settle()

	if !r.app.updates.result.Available || r.app.updates.upToDate {
		t.Fatalf("update state = available:%v upToDate:%v, want an update available",
			r.app.updates.result.Available, r.app.updates.upToDate)
	}

	r.app.act(context.Background(), intent{kind: intentUpdates})
	r.settle()
	if containsColor(r.app.img, r.app.opts.Theme.Success) {
		t.Fatal("green tick drawn while an update is still available")
	}
}

// containsColor reports whether the drawable holds an exact match for col.
func containsColor(img *image.RGBA, col color.RGBA) bool {
	if img == nil {
		return false
	}
	for i := 0; i+3 < len(img.Pix); i += 4 {
		if img.Pix[i] == col.R && img.Pix[i+1] == col.G && img.Pix[i+2] == col.B && img.Pix[i+3] == col.A {
			return true
		}
	}
	return false
}
