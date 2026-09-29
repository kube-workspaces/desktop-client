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
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
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

// fakeMSIUpdater is an MSI-capable updater: downloads resolve to a staged
// installer package instead of an archive stage.
type fakeMSIUpdater struct {
	fakeUpdater
	pkg *update.MSIPackage
	err error
	dir string
}

func (u *fakeMSIUpdater) DetectMSI() (*update.MsiInstall, error) {
	return &update.MsiInstall{Dir: u.dir}, nil
}

func (u *fakeMSIUpdater) PrepareMSI(context.Context, *update.Release, func(int64)) (*update.MSIPackage, error) {
	return u.pkg, u.err
}

// TestUpdateNotesPreview trims the notes the Updates screen shows: blank
// edges go, long bodies end in an ellipsis line, short ones pass through.
func TestUpdateNotesPreview(t *testing.T) {
	if got := updateNotesPreview("", 10); got != "" {
		t.Fatalf("empty notes = %q", got)
	}
	if got := updateNotesPreview("\n\n- fix\n\n", 10); got != "- fix" {
		t.Fatalf("padded notes = %q", got)
	}
	long := ""
	for i := 0; i < 15; i++ {
		long += "line\n"
	}
	got := updateNotesPreview(long, 10)
	lines := 1
	for _, c := range got {
		if c == '\n' {
			lines++
		}
	}
	if lines != 11 || got[len(got)-len("…"):] != "…" {
		t.Fatalf("capped notes = %q", got)
	}
}

// TestSanitizeNotes drops decorative symbols the face cannot draw (emoji
// category headers like "🐛 Fixes") while keeping covered runes and
// uncovered scripts: stripping follows the face, not a fixed list.
func TestSanitizeNotes(t *testing.T) {
	if got := sanitizeNotes("🐛 Fixes\n🔧 CI", nil); got != " Fixes\n CI" {
		t.Fatalf("sanitized = %q", got)
	}
	if got := sanitizeNotes("修复中文问题 ★ →", nil); got != "修复中文问题  " {
		t.Fatalf("sanitized = %q, want scripts kept", got)
	}
	all := func(rune) bool { return true }
	if got := sanitizeNotes("🐛 Fixes", all); got != "🐛 Fixes" {
		t.Fatalf("covered emoji stripped: %q", got)
	}
}

// TestUpdateDisplayNotes prefers the changelog section and falls back to
// the notes excerpt, staying silent when no update awaits.
func TestUpdateDisplayNotes(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	if got := r.app.updateDisplayNotes(); got != "" {
		t.Fatalf("display notes without a check = %q", got)
	}
	r.app.updates.result = update.Check("v1.2.2", &update.Release{
		Tag: "v1.2.3", Notes: "full body", Changes: "- fix\n",
	})
	if got := r.app.updateDisplayNotes(); got != "- fix\n" {
		t.Fatalf("display notes = %q, want the section", got)
	}
	r.app.updates.result = update.Check("v1.2.2", &update.Release{Tag: "v1.2.3", Notes: "full body"})
	if got := r.app.updateDisplayNotes(); got != "full body" {
		t.Fatalf("display notes = %q, want the fallback", got)
	}
}

// TestUpdateNotesGatesOnAvailability: the screen shows notes only while an
// update awaits, never for the up-to-date or unknown states.
func TestUpdateNotesGatesOnAvailability(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	if got := r.app.updateNotes(); got != "" {
		t.Fatalf("notes without a check = %q", got)
	}
	r.app.updates.result = update.Check("v1.2.3", &update.Release{Tag: "v1.2.3", Notes: "notes"})
	if got := r.app.updateNotes(); got != "" {
		t.Fatalf("notes when up to date = %q", got)
	}
	r.app.updates.result = update.Check("v1.2.2", &update.Release{Tag: "v1.2.3", Notes: "notes"})
	if got := r.app.updateNotes(); got != "notes" {
		t.Fatalf("notes when available = %q", got)
	}
}

func TestMSIDownloadAndRestart(t *testing.T) {
	r := newRig(nil, "")
	u := &fakeMSIUpdater{pkg: &update.MSIPackage{Tag: "v1.2.3", Dir: t.TempDir()}}
	u.dir = u.pkg.Dir
	r.app.opts.Version = "v1.0.0"
	r.app.opts.Updater = u
	r.start()
	r.app.checkUpdate(context.Background(), true)
	r.settle()
	if !r.app.updates.result.Available {
		t.Fatal("check did not report an available update")
	}
	r.app.downloadUpdate(context.Background())
	r.settle()
	if r.app.updates.msi == nil {
		t.Fatalf("no MSI package staged (status=%q)", r.app.updates.status)
	}
	if r.app.updates.status != i18n.Get("updates.msiReady") {
		t.Fatalf("status = %q", r.app.updates.status)
	}
	if r.app.updates.prepared != nil {
		t.Fatal("archive stage should stay empty on the MSI flow")
	}
	restarted := false
	r.app.opts.RestartMSI = func(*update.MSIPackage) error { restarted = true; return nil }
	r.app.restartUpdate()
	if !restarted || !r.app.quit || r.app.updates.msi != nil {
		t.Fatal("MSI restart did not hand off and quit")
	}
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
	webChildStarted("test/web-blocker")
	r.app.restartUpdate()
	webChildExited("test/web-blocker")
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

// TestUpdateBadgeOpensTheUpdatesScreen covers the button that relabels itself
// "Update available": whatever screen it is drawn on, it must go to the
// Updates screen, because that is what the user pressed it for. The standalone
// top-left button (login/server) and the workspace-list header button are the
// same button, so both are exercised.
func TestUpdateBadgeOpensTheUpdatesScreen(t *testing.T) {
	t.Run("header", func(t *testing.T) {
		r := newRig(savedProfile(), "token")
		r.api.set(func(f *fakeAPI) {
			f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
		})
		r.app.opts.Version = "v1.0.0"
		r.app.opts.Updater = &fakeUpdater{}
		r.start()
		if !r.app.updates.result.Available {
			t.Fatal("setup failed: no update available, so the badge never shows")
		}
		if r.app.m.State != StateWorkspaces {
			t.Fatalf("setup failed: state = %v", r.app.m.State)
		}
		r.focus(idUpdates)
		r.clickFocused()
		r.settle()
		if r.app.m.State != StateUpdates {
			t.Fatalf("the Update available button left the shell on %v, want the updates screen", r.app.m.State)
		}
		// Done from Updates returns directly to the workspace list the badge
		// was pressed on — routing through Settings strands the user on a
		// screen they never asked for.
		r.focus("update-back")
		r.clickFocused()
		r.settle()
		if r.app.m.State != StateWorkspaces {
			t.Fatalf("Done on Updates left the shell on %v, want the workspace list", r.app.m.State)
		}
	})

	t.Run("standalone", func(t *testing.T) {
		r := newRig(nil, "")
		r.app.opts.Version = "v1.0.0"
		r.app.opts.Updater = &fakeUpdater{}
		r.start()
		if !r.app.updates.result.Available {
			t.Fatal("setup failed: no update available, so the badge never shows")
		}
		// The standalone button is drawn on both pre-workspace screens; with
		// no profile this rig lands on the server one.
		if r.app.m.State != StateLogin && r.app.m.State != StateServer {
			t.Fatalf("setup failed: state = %v", r.app.m.State)
		}
		from := r.app.m.State
		// The round trip must come back to the screen the badge was pressed
		// on, not detour through Settings.
		r.focus(idUpdates)
		r.clickFocused()
		r.settle()
		if r.app.m.State != StateUpdates {
			t.Fatalf("the Update available button left the shell on %v, want the updates screen", r.app.m.State)
		}
		r.focus("update-back")
		r.clickFocused()
		r.settle()
		if r.app.m.State != from {
			t.Fatalf("Done left the shell on %v, want %v", r.app.m.State, from)
		}
		// And Settings stays reachable beside the badge.
		r.focus(idSettings)
		r.clickFocused()
		r.settle()
		if r.app.m.State != StateSettings {
			t.Fatalf("the standalone Settings button led to %v, want settings", r.app.m.State)
		}
	})
}

// TestSettingsButtonWithoutAnUpdateStillOpensSettings is the other half: the
// badge is what changes the target, so with nothing pending the button must
// keep meaning "Settings".
func TestSettingsButtonWithoutAnUpdateStillOpensSettings(t *testing.T) {
	r := newRig(savedProfile(), "token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()
	if r.app.updates.result.Available {
		t.Fatal("setup failed: an update is available, so the button is badged")
	}
	r.focus(idSettings)
	r.clickFocused()
	r.settle()
	if r.app.m.State != StateSettings {
		t.Fatalf("the settings button left the shell on %v", r.app.m.State)
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

// TestUpdatesFromSettingsDoneExitsSettings proves the other route: Settings
// → Updates → Done also lands home, because Done means done with settings,
// not one level up.
func TestUpdatesFromSettingsDoneExitsSettings(t *testing.T) {
	r := newRig(savedProfile(), "token")
	// The Updates button disables itself without an update service.
	r.app.opts.Updater = &fakeUpdater{}
	r.start()
	r.app.m.State = StateWorkspaces
	r.app.dirty = true
	r.step()

	r.focus(idSettings)
	r.clickFocused()
	if r.app.m.State != StateSettings {
		t.Fatalf("the header button led to %v, want Settings", r.app.m.State)
	}
	r.focus(idUpdates)
	r.clickFocused()
	if r.app.m.State != StateUpdates {
		t.Fatalf("the Settings Updates button led to %v, want Updates", r.app.m.State)
	}
	r.focus(ui.FocusID("update-back"))
	r.clickFocused()
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("Done on Updates led to %v, want the workspace list", r.app.m.State)
	}
}

// TestUpdatesEscapeReturnsToOrigin proves Escape matches Done.
func TestUpdatesEscapeReturnsToOrigin(t *testing.T) {
	r := newRig(savedProfile(), "token")
	r.start()
	r.app.m.State = StateWorkspaces
	r.app.updates.result.Available = true
	r.app.dirty = true
	r.step()

	r.focus(idUpdates)
	r.clickFocused()
	if r.app.m.State != StateUpdates {
		t.Fatalf("the update badge led to %v, want Updates", r.app.m.State)
	}
	r.press(keysym.KeyEscape, keysym.ModNone)
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("Escape on Updates led to %v, want the workspace list", r.app.m.State)
	}
}

// TestBadgeAndSettingsBothReachable proves an available update adds a badge
// button without removing Settings: both the header and the standalone
// entry point offer the two side by side.
func TestBadgeAndSettingsBothReachable(t *testing.T) {
	r := newRig(savedProfile(), "token")
	r.start()
	r.app.m.State = StateWorkspaces
	r.app.updates.result.Available = true
	r.app.dirty = true
	r.step()

	r.focus(idUpdates)
	r.clickFocused()
	if r.app.m.State != StateUpdates {
		t.Fatalf("the header badge led to %v, want Updates", r.app.m.State)
	}
	r.press(keysym.KeyEscape, keysym.ModNone)
	r.focus(idSettings)
	r.clickFocused()
	if r.app.m.State != StateSettings {
		t.Fatalf("the header Settings button led to %v, want Settings", r.app.m.State)
	}
}

// TestUpdateStatusCopies: the Updates status line is selectable and carries
// a Copy button, so failure sentences (installer exit codes with their log
// path) can be pasted into a bug report.
func TestUpdateStatusCopies(t *testing.T) {
	r := newRig(savedProfile(), "token")
	r.start()
	r.settle()
	r.app.m.State = StateUpdates
	r.app.updates.status = "update: installer failed with exit code 1603 (see C:\\log)"
	r.app.dirty = true
	r.step()
	order := append([]ui.FocusID(nil), r.app.ctx.Focus().Order()...)
	at := func(id ui.FocusID) int {
		for i, got := range order {
			if got == id {
				return i
			}
		}
		return -1
	}
	if at(idStatusSel) < 0 {
		t.Fatalf("status text missing from focus order %v", order)
	}
	if at(idCopyStatus) < 0 {
		t.Fatalf("copy button missing from focus order %v", order)
	}
	r.focus(idCopyStatus)
	r.clickFocused()
	r.step()
	if got := r.be.getClipboard(); got != r.app.updates.status {
		t.Fatalf("clipboard = %q, want the status text", got)
	}
}
