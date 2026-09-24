package shell

import (
	"context"
	"runtime"
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
	closed              bool
	auto, started, busy bool
	upToDate            bool
	last                int64
	status              string
	result              update.Result
	prepared            *update.Prepared
	bytes               atomic.Int64
}

func (a *App) drawStandaloneSettings(bounds ui.Rect) bool {
	th := a.opts.Theme
	label := i18n.Get("header.settings")
	if a.updates.result.Available {
		label = i18n.Get("updates.availableBadge")
	}
	b := ui.Button{ID: idSettings, Text: label, Disabled: a.m.Busy}
	return b.Layout(a.ctx, ui.Rect{X: bounds.X + th.Pad, Y: bounds.Y + th.Gap, W: cardWidth / 2, H: th.ControlHeight})
}

func (a *App) updatePolicy() update.Policy {
	p := update.Policy{AutoUpdate: &a.updates.auto}
	if a.opts.UpdatePolicy != nil {
		p.Managed, p.EnvDisabled = a.opts.UpdatePolicy()
	}
	return p
}

func (a *App) checkUpdate(ctx context.Context, manual bool) {
	if a.opts.Updater == nil || a.updates.busy || a.updates.prepared != nil {
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
	if a.opts.Updater == nil || a.updates.busy || !a.updates.result.Available || a.updates.prepared != nil || a.updatePolicy().Managed {
		return
	}
	rel := a.updates.result.Release
	a.updates.busy = true
	a.updates.bytes.Store(0)
	a.updates.status = i18n.Get("updates.downloading")
	a.updates.upToDate = false
	a.background(func() func() {
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
	if a.updates.prepared == nil || a.opts.RestartUpdate == nil || a.updatePolicy().Managed {
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

func (a *App) stopUpdates() {
	a.updates.resourceMu.Lock()
	defer a.updates.resourceMu.Unlock()
	a.updates.closed = true
	if a.updates.resource != nil {
		a.updates.resource.Close()
		a.updates.resource = nil
	}
}

func (a *App) drawUpdatesScreen(bounds ui.Rect) intent {
	th, ctx := a.opts.Theme, a.ctx
	card := ui.CenterRect(bounds, cardWidth, 0)
	card.Y = bounds.Y + th.Pad
	body := ui.NewStack(card, th.Gap)
	ui.Label(ctx, body.Next(ui.LineHeight(th.Title, th.Font)), i18n.Get("updates.title"), ui.LabelStyle{Scale: th.Title})
	ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)), i18n.Sprintf("updates.current", a.opts.Version), ui.LabelStyle{})
	last := i18n.Get("updates.never")
	if a.updates.last != 0 {
		last = time.Unix(a.updates.last, 0).Format(time.RFC3339)
	}
	ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)), i18n.Sprintf("updates.checked", last), ui.LabelStyle{})
	statusRect := body.Next(ui.LineHeight(th.Body, th.Font) * 3)
	if a.updates.upToDate && !a.updates.busy {
		icon, rest := ui.CutLeft(statusRect, ui.TextHeight(th.Body, th.Font)+2*th.Body+th.Gap/2)
		ui.CheckMark(ctx, icon, th.Success)
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
		{"update-check", i18n.Get("updates.check"), intentCheckUpdate, managed || a.updates.busy || a.updates.prepared != nil},
		{"update-download", i18n.Get("updates.download"), intentDownloadUpdate, managed || a.updates.busy || !a.updates.result.Available || a.updates.prepared != nil},
		{"update-restart", i18n.Get("updates.restart"), intentRestartUpdate, managed || a.updates.prepared == nil || len(a.sessions) != 0 || webProcesses.Load() != 0},
		{"update-back", i18n.Get("settings.done"), intentOpenSettings, false},
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
		out = intent{kind: intentOpenSettings}
	}
	return out
}
