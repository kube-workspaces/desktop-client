package shell

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/update"
)

// UpdateService keeps release I/O independent from the shell event loop.
type UpdateService interface {
	Check(context.Context, string) (update.Result, error)
	Prepare(context.Context, *update.Release, func(int64)) (*update.Prepared, error)
}

type updateState struct {
	resourceMu          sync.Mutex
	resource            *update.Prepared
	msiResource         *update.MSIPackage
	closed              bool
	auto, started, busy bool
	upToDate            bool
	last                int64
	status              string
	result              update.Result
	prepared            *update.Prepared
	msi                 *update.MSIPackage
	bytes               atomic.Int64
}

// drawStandaloneSettings draws the top-left settings entry point for the
// server and login screens and reports what it was asked to do. When an
// update is available the badge joins it as its own button instead of
// relabelling it, so both stay reachable.
func (a *App) drawStandaloneSettings(bounds ui.Rect) intentKind {
	th := a.opts.Theme
	y := bounds.Y + th.Gap
	if !a.updates.result.Available {
		b := ui.Button{ID: idSettings, Text: i18n.Get("header.settings"), Disabled: a.m.Busy}
		if b.Layout(a.ctx, ui.Rect{X: bounds.X + th.Pad, Y: y, W: cardWidth / 2, H: th.ControlHeight}) {
			return intentOpenSettings
		}
		return intentNone
	}
	badge := ui.Button{ID: idUpdates, Text: i18n.Get("updates.availableBadge"), Disabled: a.m.Busy, Variant: ui.ButtonSecondary}
	settings := ui.Button{ID: idSettings, Text: i18n.Get("header.settings"), Disabled: a.m.Busy, Variant: ui.ButtonSecondary}
	cols := ui.Row(ui.Rect{X: bounds.X + th.Pad, Y: y, W: badge.Width(a.ctx) + th.Gap + settings.Width(a.ctx), H: th.ControlHeight},
		th.Gap, badge.Width(a.ctx), settings.Width(a.ctx))
	if badge.Layout(a.ctx, cols[0]) {
		return intentUpdates
	}
	if settings.Layout(a.ctx, cols[1]) {
		return intentOpenSettings
	}
	return intentNone
}

func (a *App) updatePolicy() update.Policy {
	p := update.Policy{AutoUpdate: &a.updates.auto}
	if a.opts.UpdatePolicy != nil {
		p.Managed, p.EnvDisabled = a.opts.UpdatePolicy()
	}
	return p
}

func (a *App) checkUpdate(ctx context.Context, manual bool) {
	if a.opts.Updater == nil || a.updates.busy || a.updates.prepared != nil || a.updates.msi != nil {
		return
	}
	p := a.updatePolicy()
	if p.Managed {
		a.updates.status = i18n.Get("updates.managed")
		a.updates.upToDate = false
		return
	}
	// Only auto-check if current is a valid release; manual check always proceeds.
	if !manual && update.IsUnversioned(a.opts.Version) {
		return
	}
	if !manual && !p.ShouldCheck(time.Unix(a.updates.last, 0), time.Now()) {
		return
	}
	if !manual && a.updates.status != "" {
		return
	}
	a.updates.busy = true
	a.updates.status = i18n.Get("updates.checking")
	a.updates.upToDate = false
	a.updates.last = time.Now().Unix()
	a.saveSettings()
	a.background(func() func() {
		r, err := a.opts.Updater.Check(ctx, a.opts.Version)
		return func() {
			a.updates.busy = false
			if err != nil {
				a.updates.status = i18n.Sprintf("updates.failed", err)
				return
			}
			a.updates.result = r
			if !r.Available {
				a.updates.status = i18n.Get("updates.upToDate")
				a.updates.upToDate = true
			} else {
				a.updates.status = i18n.Sprintf("updates.latest", r.Latest)
			}
		}
	})
}

func (a *App) downloadUpdate(ctx context.Context) {
	if a.opts.Updater == nil || a.updates.busy || !a.updates.result.Available || a.updates.prepared != nil || a.updates.msi != nil || a.updatePolicy().Managed {
		return
	}
	rel := a.updates.result.Release
	a.updates.busy = true
	a.updates.bytes.Store(0)
	a.updates.status = i18n.Get("updates.downloading")
	a.updates.upToDate = false
	a.background(func() func() {
		// MSI-managed installs download the .msi, not the archive: the
		// installer keeps Add/Remove Programs honest. Manual installs
		// (or updaters without the MSI capability, including test fakes)
		// keep the archive flow below.
		if msi, ok := a.opts.Updater.(update.MSIInstaller); ok {
			if pkg, err := msi.PrepareMSI(ctx, rel, a.updates.bytes.Store); err == nil {
				a.updates.resourceMu.Lock()
				if a.updates.closed {
					pkg.Close()
					a.updates.resourceMu.Unlock()
					return nil
				}
				a.updates.msiResource = pkg
				a.updates.resourceMu.Unlock()
				return func() {
					a.updates.busy = false
					a.updates.msi = pkg
					a.updates.status = i18n.Get("updates.msiReady")
					a.updates.upToDate = false
				}
			} else if !errors.Is(err, update.ErrNotMSI) {
				if ctx.Err() == nil {
					return func() {
						a.updates.busy = false
						a.updates.status = i18n.Sprintf("updates.failed", err)
					}
				}
				return nil
			}
		}
		p, err := a.opts.Updater.Prepare(ctx, rel, a.updates.bytes.Store)
		if ctx.Err() != nil && p != nil {
			p.Close()
			p = nil
			err = ctx.Err()
		}
		if p != nil {
			a.updates.resourceMu.Lock()
			if a.updates.closed {
				p.Close()
				a.updates.resourceMu.Unlock()
				return nil
			}
			a.updates.resource = p
			a.updates.resourceMu.Unlock()
		}
		return func() {
			a.updates.busy = false
			if err != nil {
				a.updates.status = i18n.Sprintf("updates.failed", err)
				return
			}
			a.updates.prepared = p
			a.updates.status = i18n.Get("updates.ready")
			a.updates.upToDate = false
		}
	})
}

func (a *App) restartUpdate() {
	if a.updatePolicy().Managed {
		return
	}
	if a.updates.msi != nil {
		if a.opts.RestartMSI == nil {
			a.updates.status = i18n.Sprintf("updates.failed", "installer restart unavailable")
			return
		}
		if len(a.sessions) != 0 || webProcesses.Load() != 0 || a.m.State == StateSession {
			a.updates.status = i18n.Get("updates.sessions")
			a.updates.upToDate = false
			return
		}
		if err := a.opts.RestartMSI(a.updates.msi); err != nil {
			a.updates.status = i18n.Sprintf("updates.failed", err)
			return
		}
		a.updates.msi = nil // ownership transferred to the helper
		a.updates.resourceMu.Lock()
		a.updates.msiResource = nil
		a.updates.resourceMu.Unlock()
		a.quit = true
		return
	}
	if a.updates.prepared == nil || a.opts.RestartUpdate == nil {
		return
	}
	if len(a.sessions) != 0 || webProcesses.Load() != 0 || a.m.State == StateSession {
		a.updates.status = i18n.Get("updates.sessions")
		a.updates.upToDate = false
		return
	}
	if err := a.opts.RestartUpdate(a.updates.prepared); err != nil {
		a.updates.status = i18n.Sprintf("updates.failed", err)
		return
	}
	a.updates.prepared = nil // ownership transferred to the helper
	a.updates.resourceMu.Lock()
	a.updates.resource = nil
	a.updates.resourceMu.Unlock()
	a.quit = true
}

// updateNotesLines caps the release notes shown on the Updates screen, so
// a long changelog cannot push the buttons off the card.
const updateNotesLines = 10

// updateNotes returns the pending update's release notes, or "" when no
// update is available.
func (a *App) updateNotes() string {
	if !a.updates.result.Available || a.updates.result.Release == nil {
		return ""
	}
	return a.updates.result.Release.Notes
}

// updateNotesPreview trims release notes to maxLines lines for display,
// dropping blank edges. A capped note ends in an ellipsis line.
func updateNotesPreview(notes string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(notes, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], "…")
	}
	return strings.Join(lines, "\n")
}

func (a *App) stopUpdates() {
	a.updates.resourceMu.Lock()
	defer a.updates.resourceMu.Unlock()
	a.updates.closed = true
	if a.updates.resource != nil {
		a.updates.resource.Close()
		a.updates.resource = nil
	}
	if a.updates.msiResource != nil {
		a.updates.msiResource.Close()
		a.updates.msiResource = nil
	}
}

func (a *App) drawUpdatesScreen(bounds ui.Rect) intent {
	th, ctx := a.opts.Theme, a.ctx
	card := ui.CenterRect(bounds, cardWidth, 0)
	card.Y = bounds.Y + th.Pad
	body := ui.NewStack(card, th.Gap)
	ui.Label(ctx, body.Next(ui.LineHeight(th.Title, th.Font)), i18n.Get("updates.title"), ui.LabelStyle{Scale: th.Title})
	a.verSel.Layout(ctx, body.Next(ui.LineHeight(th.Body, th.Font)), i18n.Sprintf("updates.current", a.opts.Version), ui.SelectableStyle{})
	last := i18n.Get("updates.never")
	if a.updates.last != 0 {
		last = time.Unix(a.updates.last, 0).Format(time.RFC3339)
	}
	ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)), i18n.Sprintf("updates.checked", last), ui.LabelStyle{})
	statusRect := body.Next(ui.LineHeight(th.Body, th.Font) * 3)
	if a.updates.upToDate && !a.updates.busy {
		size := ui.TextHeight(th.Body, th.Font) + 2*th.Body
		icon, rest := ui.CutLeft(statusRect, size+th.Gap/2)
		lineH := ui.LineHeight(th.Body, th.Font)
		dy := (lineH - size) / 2
		if dy < 0 {
			dy = 0
		}
		ui.CheckMark(ctx, ui.Rect{X: icon.X, Y: icon.Y + dy, W: size, H: size}, th.Success)
		ui.Label(ctx, rest, a.updates.status, ui.LabelStyle{Wrap: true})
	} else {
		ui.Label(ctx, statusRect, a.updates.status, ui.LabelStyle{Wrap: true})
	}
	if a.updates.busy {
		ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)), i18n.Sprintf("updates.bytes", a.updates.bytes.Load()), ui.LabelStyle{})
		asset, err := a.updates.result.Release.AssetFor(runtime.GOOS, runtime.GOARCH)
		if err == nil && asset.Size > 0 {
			bar := body.Next(th.Gap)
			a.canvas.Fill(bar, th.TextMuted)
			bar.W = int(float64(bar.W) * min(1, float64(a.updates.bytes.Load())/float64(asset.Size)))
			a.canvas.Fill(bar, th.Text)
		} else {
			ui.Spinner(ctx, body.Next(th.ControlHeight), th.TextMuted)
		}
	}
	// Release notes for the pending update, so the user sees what it
	// contains before downloading it.
	if notes := updateNotesPreview(a.updateNotes(), updateNotesLines); notes != "" {
		ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)), i18n.Sprintf("updates.whatsNew", a.updates.result.Latest), ui.LabelStyle{Scale: th.Body})
		lines := strings.Count(notes, "\n") + 1
		ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)*lines), notes, ui.LabelStyle{Color: th.TextMuted, Wrap: true})
	}
	var out intent
	managed := a.updatePolicy().Managed
	label := "updates.autoOff"
	if a.updates.auto {
		label = "updates.autoOn"
	}
	for _, b := range []struct {
		id, text string
		kind     intentKind
		disabled bool
	}{
		{"update-auto", i18n.Get(label), intentToggleUpdate, managed},
		{"update-check", i18n.Get("updates.check"), intentCheckUpdate, managed || a.updates.busy || a.updates.prepared != nil || a.updates.msi != nil},
		{"update-download", i18n.Get("updates.download"), intentDownloadUpdate, managed || a.updates.busy || !a.updates.result.Available || a.updates.prepared != nil || a.updates.msi != nil},
		{"update-restart", i18n.Get("updates.restart"), intentRestartUpdate, managed || (a.updates.prepared == nil && a.updates.msi == nil) || len(a.sessions) != 0 || webProcesses.Load() != 0},
		{"update-back", i18n.Get("settings.done"), intentSettingsDone, false},
	} {
		button := ui.Button{ID: ui.FocusID(b.id), Text: b.text, Disabled: b.disabled}
		if button.Layout(ctx, body.Next(th.ControlHeight)) {
			out = intent{kind: b.kind}
		}
	}
	if len(a.sessions) != 0 || webProcesses.Load() != 0 {
		ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)*2), i18n.Get("updates.sessions"), ui.LabelStyle{Wrap: true})
	}
	if ctx.Input.KeyPressed(keysym.KeyEscape) {
		out = intent{kind: intentSettingsDone}
	}
	return out
}
