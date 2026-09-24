// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

// Layout constants. They are named because a number that appears twice in a
// layout is a number that will be changed once.
const (
	// cardWidth is the width of the centred card the first two screens use.
	// It is fixed rather than proportional because a form whose fields grow
	// to 1200 pixels on a wide monitor looks like a mistake.
	cardWidth = 520

	// listMinWidth is the width below which the workspace list drops its
	// secondary columns rather than overlapping them.
	listMinWidth = 680

	// typeColumnWidth and statusColumnWidth size the right-hand columns of a
	// workspace row. The status column is the wider of the two because it
	// carries a container reason ("starting: ImagePullBackOff") as well as a
	// state.
	typeColumnWidth   = 140
	statusColumnWidth = 210

	// infoCardWidth is the width of the workspace info modal. Values that do
	// not fit are wrapped rather than stretching the card.
	infoCardWidth = 560

	// infoLabelWidth is the base width of the label column in the info modal.
	// The column grows to fit the widest label ("Condition Ready") so nothing
	// truncates; this floor keeps short, single-word headers ("CPU") from
	// leaving the value column oddly wide.
	infoLabelWidth = 120
)

// modalScrim is the translucent layer between the info modal and the frozen
// list behind it. The list stays visible so the modal reads as "behind glass",
// but the scrim is what says it can no longer be touched.
var modalScrim = color.RGBA{A: 0xaa}

// drawServerScreen asks which instance to talk to.
//
// This is the first thing a new user sees, so it says what the field is for
// and nothing else. The only decoration is the TLS escape hatch, which is
// there because the people who install this first are the people running it
// with a self-signed certificate.
func (a *App) drawServerScreen(bounds ui.Rect) intent {
	th := a.opts.Theme
	ctx := a.ctx
	var out intent

	card := ui.CenterRect(bounds, cardWidth, 0)
	card.Y = bounds.Y + bounds.H/6
	card.H = bounds.H - card.Y - th.Pad
	body := ui.NewStack(card, th.Gap)

	ui.Label(ctx, body.Next(ui.TextHeight(th.Title, th.Font)), i18n.Get("app.name"), ui.LabelStyle{
		Scale: th.Title,
	})
	body.Skip(th.Gap / 2)
	ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)*2), i18n.Get("server.subtitle"), ui.LabelStyle{
		Color: th.TextMuted,
		Wrap:  true,
	})
	body.Skip(th.Pad)

	// Returning users pick up where they left off: every configured profile
	// is one click from a switch, and typing an address below still works for
	// a brand-new instance.
	if len(a.profiles) > 0 {
		ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), i18n.Get("profiles.title"), ui.LabelStyle{
			Color: th.TextMuted,
			Scale: th.Small,
		})
		for i, p := range a.profiles {
			if p == nil {
				continue
			}
			b := ui.Button{
				ID:      ui.FocusID(fmt.Sprintf("profile-%d", i)),
				Text:    p.Name + "  ·  " + p.Server,
				Variant: ui.ButtonSecondary,
			}
			if b.Layout(ctx, body.Next(th.ControlHeight)) {
				out = intent{kind: intentSwitchProfile, profile: p.Name}
				return out
			}
		}
		body.Skip(th.Pad)
	}

	ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), i18n.Get("server.address"), ui.LabelStyle{
		Color: th.TextMuted,
		Scale: th.Small,
	})
	submitted := a.serverField.Layout(ctx, body.Next(th.ControlHeight))
	body.Skip(th.Gap / 2)
	if a.insecureBox.Layout(ctx, body.Next(th.ControlHeight)) {
		// Changing the trust setting invalidates whatever the previous probe
		// concluded, so the user has to press Connect again.
		a.m.Auth = nil
	}

	body.Skip(th.Pad)
	connect := ui.Button{ID: idConnect, Text: i18n.Get("server.connect"), Variant: ui.ButtonPrimary, Disabled: a.m.Busy}
	row := ui.Row(body.Next(th.ControlHeight), th.Gap, 160, 0)
	if connect.Layout(ctx, row[0]) || (submitted && !a.m.Busy) {
		out = intent{kind: intentConnectServer}
	}
	if a.m.Busy {
		a.drawBusy(row[1], a.m.BusyText)
	}

	body.Skip(th.Gap)
	a.drawMessagesIn(body.Rest())

	a.drawFooterHint(bounds, i18n.Get("server.hint"))
	if ctx.Input.KeyPressed(keysym.KeyEscape) && a.m.Busy {
		out = intent{kind: intentCancel}
	}
	return out
}

// drawLoginScreen authenticates against the instance.
//
// Which controls appear is entirely the server's decision: an instance with
// local accounts gets email and password fields, one backed by an identity
// provider gets a browser button, and an instance that offers both gets both
// with the password form first, because that is the one that can be completed
// without leaving the window.
func (a *App) drawLoginScreen(bounds ui.Rect) intent {
	th := a.opts.Theme
	ctx := a.ctx
	var out intent

	card := ui.CenterRect(bounds, cardWidth, 0)
	card.Y = bounds.Y + bounds.H/8
	card.H = bounds.H - card.Y - th.Pad
	body := ui.NewStack(card, th.Gap)

	ui.Label(ctx, body.Next(ui.TextHeight(th.Title, th.Font)), i18n.Get("login.title"), ui.LabelStyle{Scale: th.Title})
	body.Skip(th.Gap / 2)
	ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)), a.m.Server, ui.LabelStyle{Color: th.TextMuted})
	body.Skip(th.Pad)

	waiting := a.m.Busy && strings.HasPrefix(a.m.BusyText, i18n.Get("busy.waitingBrowser"))

	switch {
	case waiting:
		out = a.drawBrowserWait(body)

	case a.m.AuthDisabled():
		ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)*2),
			i18n.Get("login.noSignin"), ui.LabelStyle{Color: th.TextMuted, Wrap: true})
		body.Skip(th.Gap)
		cont := ui.Button{ID: idSignIn, Text: i18n.Get("login.continue"), Variant: ui.ButtonPrimary, Disabled: a.m.Busy}
		if cont.Layout(ctx, ui.Row(body.Next(th.ControlHeight), th.Gap, 160, 0)[0]) {
			out = intent{kind: intentSignInLocal}
		}

	default:
		out = a.drawCredentials(body)
	}

	body.Skip(th.Gap)
	a.drawMessagesIn(body.Rest())

	// The way back is always available, and always quiet: it is not what the
	// user came here to do, but a user pointed at the wrong instance has no
	// other way out.
	back := ui.Button{ID: idBack, Text: i18n.Get("login.back"), Variant: ui.ButtonQuiet}
	backRect, _ := ui.CutBottom(ui.Inset(bounds, th.Pad), th.ControlHeight)
	backRect, _ = ui.CutLeft(backRect, back.Width(ctx))
	if back.Layout(ctx, backRect) {
		out = intent{kind: intentChangeServer}
	}
	return out
}

// drawCredentials draws the password form, the browser button, or both.
func (a *App) drawCredentials(body *ui.Stack) intent {
	th := a.opts.Theme
	ctx := a.ctx
	var out intent

	local := a.m.CanUseLocalAuth()
	browser := a.m.CanUseBrowserAuth()
	if !local && !browser {
		if a.m.Busy {
			a.drawBusy(body.Next(th.ControlHeight), a.m.BusyText)
			return out
		}
		ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)*3),
			i18n.Get("login.noMethod"),
			ui.LabelStyle{Color: th.TextMuted, Wrap: true})
		return out
	}

	if local {
		ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), i18n.Get("login.email"), ui.LabelStyle{
			Color: th.TextMuted, Scale: th.Small,
		})
		emailSubmit := a.emailField.Layout(ctx, body.Next(th.ControlHeight))
		body.Skip(th.Gap / 2)
		ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), i18n.Get("login.password"), ui.LabelStyle{
			Color: th.TextMuted, Scale: th.Small,
		})
		passwordSubmit := a.passwordField.Layout(ctx, body.Next(th.ControlHeight))
		body.Skip(th.Pad)

		signIn := ui.Button{ID: idSignIn, Text: i18n.Get("login.signin"), Variant: ui.ButtonPrimary, Disabled: a.m.Busy}
		row := ui.Row(body.Next(th.ControlHeight), th.Gap, 160, 0)
		submitted := emailSubmit || passwordSubmit
		if signIn.Layout(ctx, row[0]) || (submitted && !a.m.Busy) {
			out = intent{kind: intentSignInLocal}
		}
		if a.m.Busy {
			a.drawBusy(row[1], a.m.BusyText)
		}
	}

	if browser {
		if local {
			body.Skip(th.Pad)
			ui.DividerLabel(ctx, body.Next(ui.LineHeight(th.Body, th.Font)), i18n.Get("login.or"))
			body.Skip(th.Gap / 2)
		}
		variant := ui.ButtonPrimary
		if local {
			variant = ui.ButtonSecondary
		}
		btn := ui.Button{ID: idBrowser, Text: i18n.Get("login.browser"), Variant: variant, Disabled: a.m.Busy}
		if btn.Layout(ctx, ui.Row(body.Next(th.ControlHeight), th.Gap, btn.Width(ctx), 0)[0]) {
			out = intent{kind: intentSignInBrowser}
		}
		if issuer := a.issuerHost(); issuer != "" {
			body.Skip(th.Gap / 2)
			ui.Label(ctx, body.Next(ui.LineHeight(th.Small, th.Font)), i18n.Sprintf("login.sentTo", issuer),
				ui.LabelStyle{Color: th.TextMuted, Scale: th.Small})
		}
	}
	return out
}

// drawBrowserWait is the screen shown while the user is in their browser.
//
// It shows the authorization URL as well as the spinner. A browser that opens
// on another desktop, or not at all over SSH, is common enough that hiding the
// URL would strand the user with a spinner and no explanation.
func (a *App) drawBrowserWait(body *ui.Stack) intent {
	th := a.opts.Theme
	ctx := a.ctx
	var out intent

	row := body.Next(th.ControlHeight)
	spinner, rest := ui.CutLeft(row, th.ControlHeight+th.Gap/2)
	ui.Spinner(ctx, ui.Inset(spinner, th.Gap/2), th.Accent)
	ui.Label(ctx, rest, i18n.Get("login.waitingLabel"), ui.LabelStyle{Middle: true})

	body.Skip(th.Gap / 2)
	ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)*2),
		i18n.Get("login.complete"),
		ui.LabelStyle{Color: th.TextMuted, Wrap: true})

	if a.authorizeURL != "" {
		body.Skip(th.Gap)
		ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), i18n.Get("login.ifNothing"), ui.LabelStyle{
			Color: th.TextMuted, Scale: th.Small,
		})
		urlBox := body.Next(ui.LineHeight(th.Small, th.Font)*3 + th.Gap)
		ctx.Canvas.FillRounded(urlBox, th.Radius, th.SurfaceAlt)
		ui.Label(ctx, ui.Inset(urlBox, th.Gap/2), a.authorizeURL, ui.LabelStyle{
			Color: th.TextMuted, Scale: th.Small, Wrap: true,
		})
	}

	body.Skip(th.Pad)
	cancel := ui.Button{ID: idCancel, Text: i18n.Get("login.cancel"), Variant: ui.ButtonSecondary}
	if cancel.Layout(ctx, ui.Row(body.Next(th.ControlHeight), th.Gap, 160, 0)[0]) ||
		ctx.Input.KeyPressed(keysym.KeyEscape) {
		out = intent{kind: intentCancel}
	}
	return out
}

// drawWorkspacesScreen is the main screen.
func (a *App) drawWorkspacesScreen(bounds ui.Rect) intent {
	th := a.opts.Theme
	ctx := a.ctx
	var out intent

	content := ui.Inset(bounds, th.Pad)
	header, content := ui.CutTop(content, ui.TextHeight(th.Title, th.Font)+th.Gap)
	a.drawHeader(header, &out)

	content.Y += th.Gap
	content.H -= th.Gap

	// Toolbar: filter on the left, refresh on the right.
	toolbar, content := ui.CutTop(content, th.ControlHeight)
	refresh := ui.Button{ID: idRefresh, Text: i18n.Get("workspaces.refresh"), Variant: ui.ButtonSecondary}
	refreshRect, filterRect := ui.CutRight(toolbar, refresh.Width(ctx))
	filterRect, spinnerRect := ui.CutLeft(filterRect, min(360, filterRect.W-th.Gap))
	if a.filterField.Layout(ctx, filterRect) {
		out = intent{kind: intentActivate}
	}
	if refresh.Layout(ctx, refreshRect) {
		out = intent{kind: intentRefresh}
	}
	if a.refreshing {
		ui.Spinner(ctx, ui.Inset(ui.Rect{X: spinnerRect.X + th.Gap, Y: spinnerRect.Y, W: th.ControlHeight, H: th.ControlHeight}, th.Gap/2), th.TextMuted)
	}
	a.m.Filter = a.filterField.Value()

	content.Y += th.Gap
	content.H -= th.Gap

	// Footer first, so the list gets exactly what is left.
	footer, content := ui.CutBottom(content, th.ControlHeight+th.Gap)
	messages, content := ui.CutBottom(content, a.messageHeight(content.W))

	rows := a.m.Filtered()
	a.drawList(content, rows, &out)

	if messages.H > 0 {
		messages.Y += th.Gap / 2
		a.drawMessagesIn(messages)
	}
	a.drawWorkspaceFooter(footer, rows, &out)

	// Shortcuts. They are handled here rather than inside the widgets because
	// they belong to the screen: F5 refreshes whatever has the keyboard.
	switch {
	case ctx.Input.KeyPressed(keysym.KeyF5),
		ctx.Input.RuneChord(keysym.ModControl, 'r'):
		out = intent{kind: intentRefresh}
	case ctx.Input.RuneChord(keysym.ModControl, 'f'):
		ctx.Focus().Set(idFilter)
	case ctx.Input.KeyPressed(keysym.KeyEscape):
		if a.filterField.Len() > 0 {
			a.filterField.Clear()
			a.m.Filter = ""
		}
		a.m.Err, a.m.Notice = "", ""
	}
	return out
}

// drawSettingsScreen is where the client's own appearance is chosen: the font
// style and the colour scheme — a concrete one, or "follow the system" —
// both remembered between runs.
//
// Every choice is applied and saved at the moment it is made, so the screen
// doubles as a live preview and closing the window a second later cannot lose
// a selection. The [ui] package keeps style and colour on separate axes, and
// this screen is the only place that exposes the separation to the user.
func (a *App) drawSettingsScreen(bounds ui.Rect) intent {
	th := a.opts.Theme
	ctx := a.ctx
	var out intent

	card := ui.CenterRect(bounds, cardWidth, 0)
	card.Y = bounds.Y + bounds.H/6
	card.H = bounds.H - card.Y - th.Pad
	body := ui.NewStack(card, th.Gap)

	ui.Label(ctx, body.Next(ui.TextHeight(th.Title, th.Font)), i18n.Get("settings.title"), ui.LabelStyle{Scale: th.Title})
	body.Skip(th.Gap / 2)
	ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)*2),
		i18n.Get("settings.subtitle"), ui.LabelStyle{Color: th.TextMuted, Wrap: true})
	body.Skip(th.Pad)

	ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), i18n.Get("settings.style"), ui.LabelStyle{
		Color: th.TextMuted, Scale: th.Small,
	})
	styleRow := body.Next(th.ControlHeight)
	if picked := a.drawChoice(ctx, styleRow, styleIndex(a.settings.Style), []styleOption{
		{id: idStyleBubbly, label: i18n.Get("settings.bubbly")},
		{id: idStyleRetro, label: i18n.Get("settings.retro")},
		{id: idStyleClean, label: i18n.Get("settings.clean")},
	}); picked != styleIndex(a.settings.Style) {
		a.applySettings(Settings{Style: styleFromIndex(picked), Mode: a.settings.Mode, UIScale: a.settings.UIScale})
		a.saveSettings()
	}

	body.Skip(th.Pad)
	ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), i18n.Get("settings.colours"), ui.LabelStyle{
		Color: th.TextMuted, Scale: th.Small,
	})
	modeRow := body.Next(th.ControlHeight)
	modeOpts := []styleOption{
		{id: idModeSystem, label: i18n.Get("settings.system")},
		{id: idModeDark, label: i18n.Get("settings.dark")},
		{id: idModeLight, label: i18n.Get("settings.light")},
	}
	modeIdx := modeIndex(a.settings.Mode)
	if picked := a.drawChoice(ctx, modeRow, modeIdx, modeOpts); picked != modeIdx {
		a.applySettings(Settings{Style: a.settings.Style, Mode: modeFromIndex(picked), UIScale: a.settings.UIScale})
		a.saveSettings()
	}

	body.Skip(th.Pad)
	ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), i18n.Get("settings.size"), ui.LabelStyle{
		Color: th.TextMuted, Scale: th.Small,
	})
	scaleRow := body.Next(th.ControlHeight)
	scaleOpts := []styleOption{
		{id: idScaleAuto, label: i18n.Get("settings.auto")},
		{id: idScale100, label: i18n.Get("settings.scale100")},
		{id: idScale150, label: i18n.Get("settings.scale150")},
		{id: idScale200, label: i18n.Get("settings.scale200")},
	}
	if picked := a.drawChoice(ctx, scaleRow, uiScaleIndex(a.settings.UIScale), scaleOpts); picked != uiScaleIndex(a.settings.UIScale) {
		a.applySettings(Settings{Style: a.settings.Style, Mode: a.settings.Mode, UIScale: uiScaleSteps()[picked]})
		a.saveSettings()
	}
	// A launch flag wins over the stored choice and the screen shows what is
	// stored, so say so when they disagree rather than letting the row lie.
	if a.opts.UIScale > 0 && a.opts.UIScale != a.settings.UIScale {
		ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)),
			i18n.Get("settings.flagNote"), ui.LabelStyle{Color: th.TextMuted})
	}

	body.Skip(th.Pad)
	done := ui.Button{ID: idSettingsDone, Text: i18n.Get("settings.done"), Variant: ui.ButtonPrimary}
	buttons := ui.Row(body.Next(th.ControlHeight), th.Gap, 160, 0)
	updates := ui.Button{ID: "updates", Text: i18n.Get("updates.title"), Disabled: a.opts.Updater == nil}
	if updates.Layout(ctx, buttons[1]) {
		out = intent{kind: intentUpdates}
	}
	if done.Layout(ctx, buttons[0]) ||
		ctx.Input.KeyPressed(keysym.KeyEscape) {
		out = intent{kind: intentSettingsDone}
	}

	a.drawFooterHint(bounds, i18n.Get("settings.hint"))
	return out
}

// drawChoice renders a one-of-N choice as a row of buttons and returns the
// index of the option selected after this frame.
//
// The selected option carries the accent, the others the ordinary surface, so
// a user can tell which is which without reading any label twice: a settings
// screen is two of these rows side by side. The option labels are mapped to
// ids so the focus ring (and therefore keyboard navigation) treats each pill
// as its own control.
func (a *App) drawChoice(ctx *ui.Context, r ui.Rect, selected int, opts []styleOption) int {
	th := a.opts.Theme
	widths := make([]int, len(opts)) // Row spreads every zero over the free space
	cols := ui.Row(r, th.Gap, widths...)
	chosen := selected
	for i, o := range opts {
		b := ui.Button{ID: o.id, Text: o.label, Variant: ui.ButtonSecondary}
		if i == selected {
			b.Variant = ui.ButtonPrimary
		}
		if b.Layout(ctx, cols[i]) {
			chosen = i
		}
	}
	return chosen
}

// styleIndex maps a style to its position in the settings screen's row, and
// its inverse styleFromIndex maps the row back. The row order is the enum
// order, so both are three-line functions that do not drift because of
// switch defaults.
func styleIndex(s ui.Style) int {
	switch s {
	case ui.StyleBubbly:
		return 0
	case ui.StyleRetro:
		return 1
	default:
		return 2
	}
}

func styleFromIndex(i int) ui.Style {
	switch i {
	case 0:
		return ui.StyleBubbly
	case 1:
		return ui.StyleRetro
	default:
		return ui.StyleClean
	}
}

// modeIndex maps a mode to its position in the settings screen's row, and its
// inverse modeFromIndex maps the row back. The row leads with "follow the
// system" because it is the default; the two concrete schemes follow in the
// historic order.
func modeIndex(m ui.Mode) int {
	switch m {
	case ui.ModeDark:
		return 1
	case ui.ModeLight:
		return 2
	default:
		return 0
	}
}

func modeFromIndex(i int) ui.Mode {
	switch i {
	case 1:
		return ui.ModeDark
	case 2:
		return ui.ModeLight
	default:
		return ui.ModeSystem
	}
}

type styleOption struct {
	id    ui.FocusID
	label string
}

// drawHeader draws the title bar: who is signed in, the way out, and the way
// to the appearance settings.
func (a *App) drawHeader(r ui.Rect, out *intent) {
	th := a.opts.Theme
	ctx := a.ctx

	profiles := ui.Button{ID: idProfiles, Text: i18n.Get("header.profiles"), Variant: ui.ButtonQuiet}
	signOut := ui.Button{ID: idSignOut, Text: i18n.Get("header.signout"), Variant: ui.ButtonQuiet}
	settings := ui.Button{ID: idSettings, Text: i18n.Get("header.settings"), Variant: ui.ButtonQuiet}
	if a.updates.result.Available {
		settings.Text = i18n.Get("updates.availableBadge")
	}
	buttonsW := profiles.Width(ctx) + signOut.Width(ctx) + settings.Width(ctx) + th.Gap
	signOutRect, rest := ui.CutRight(r, buttonsW)
	cols := ui.Row(signOutRect, th.Gap/2, profiles.Width(ctx), settings.Width(ctx), signOut.Width(ctx))
	if profiles.Layout(ctx, cols[0]) {
		*out = intent{kind: intentOpenProfiles}
	}
	if settings.Layout(ctx, cols[1]) {
		*out = intent{kind: intentOpenSettings}
	}
	if signOut.Layout(ctx, cols[2]) {
		*out = intent{kind: intentSignOut}
	}

	title, identity := ui.CutLeft(rest, ui.TextWidth(i18n.Get("workspaces.title"), th.Title, th.Font)+th.Pad)
	ui.Label(ctx, title, i18n.Get("workspaces.title"), ui.LabelStyle{Scale: th.Title, Middle: true})
	ui.Label(ctx, ui.InsetXY(identity, th.Gap, 0), a.identityLine(), ui.LabelStyle{
		Color:  th.TextMuted,
		Scale:  th.Small,
		Align:  ui.AlignRight,
		Middle: true,
	})
}

// drawList draws the workspace rows.
func (a *App) drawList(r ui.Rect, rows []kwclient.Workspace, out *intent) {
	th := a.opts.Theme
	ctx := a.ctx

	ui.Panel(ctx, r)
	inner := ui.Inset(r, th.BorderWidth)

	if len(rows) == 0 {
		ui.Label(ctx, ui.Inset(inner, th.Pad), a.emptyText(), ui.LabelStyle{
			Color: th.TextMuted, Align: ui.AlignCenter, Middle: true, Wrap: true,
		})
		return
	}

	// Hand the widget the selection the model holds, by key, and take back
	// whatever the user moved it to.
	if i := SelectedIndex(rows, a.m.Selected); i >= 0 {
		a.list.Selected = i
	}
	activated := a.list.Layout(ctx, inner, len(rows), func(ctx *ui.Context, row ui.Rect, state ui.RowState) {
		a.drawWorkspaceRow(row, rows[state.Index], state)
	})
	if a.list.Selected >= 0 && a.list.Selected < len(rows) {
		a.m.Selected = rows[a.list.Selected].Key()
	}
	if activated {
		if ws, ok := a.m.SelectedWorkspace(); ok {
			// Enter on a stopped row asks to start it. Enter on a running row
			// opens the workspace's primary in-app surface — VM display,
			// embedded webview for a non-VM workspace.
			kind := intentActivate
			switch {
			case ws.Stopped:
				kind = intentStartWorkspace
			case !ws.IsVM() && ws.Running() && (ws.Type == kwclient.WorkspaceTypeContainer || ws.Type == kwclient.WorkspaceTypeScratch):
				kind = intentOpenWeb
			}
			*out = intent{kind: kind, workspace: ws}
		}
	}
}

// drawWorkspaceRow draws one workspace.
//
// The layout is deliberately flat: a status dot, the name, then the things
// that distinguish two workspaces with similar names — namespace and type —
// in muted text on the right. Connectable and not-connectable are told apart
// by the dot's colour and by the name's, so the distinction survives a
// screenshot, a colour-blind user and a bad monitor.
func (a *App) drawWorkspaceRow(r ui.Rect, ws kwclient.Workspace, state ui.RowState) {
	th := a.opts.Theme
	ctx := a.ctx

	switch {
	case state.Selected:
		fill := th.SurfaceSelected
		if !state.ListFocused {
			fill = th.SurfaceAlt
		}
		ctx.Canvas.FillRounded(ui.InsetXY(r, th.Gap/2, 2), th.Radius, fill)
	case state.Hovered:
		ctx.Canvas.FillRounded(ui.InsetXY(r, th.Gap/2, 2), th.Radius, th.SurfaceAlt)
	}

	body := ui.InsetXY(r, th.Pad/2+th.Gap/2, 0)
	dot, body := ui.CutLeft(body, th.Body*4+th.Gap)
	ui.Dot(ctx, dot, a.statusColor(ws))

	// The right-hand columns are dropped rather than overlapped on a narrow
	// window: a truncated name is still identifiable, a truncated namespace
	// beside a truncated name is not.
	status, meta := ui.Rect{}, ui.Rect{}
	if r.W >= listMinWidth {
		status, body = ui.CutRight(body, statusColumnWidth)
		meta, body = ui.CutRight(body, typeColumnWidth)
		// Both are right-aligned, so without a gap the type would end exactly
		// where the status begins and the two would read as one word.
		meta = ui.CutRightGap(meta, th.Gap)
		body = ui.CutRightGap(body, th.Gap)
	}

	name := ws.Name
	ink := th.Text
	if !ws.Running() {
		ink = th.TextMuted
	}
	nameRect, subRect := ui.CutTop(body, body.H/2)
	nameRect.Y += th.Gap / 2
	ui.Label(ctx, nameRect, name, ui.LabelStyle{Color: ink, Middle: true})
	ui.Label(ctx, subRect, ws.Namespace, ui.LabelStyle{
		Color: th.TextMuted, Scale: th.Small, Middle: true,
	})

	if meta.W > 0 {
		text := string(ws.Type)
		if ws.HasTier1() {
			text += i18n.Get("workspaces.tier1")
		}
		ui.Label(ctx, meta, text, ui.LabelStyle{
			Color: th.TextMuted, Scale: th.Small, Align: ui.AlignRight, Middle: true,
		})
	}
	if status.W > 0 {
		ui.Label(ctx, status, StatusText(ws), ui.LabelStyle{
			Color:  a.statusColor(ws),
			Scale:  th.Small,
			Align:  ui.AlignRight,
			Middle: true,
		})
	}
}

// drawWorkspaceFooter draws the primary action and the keyboard hints.
func (a *App) drawWorkspaceFooter(r ui.Rect, rows []kwclient.Workspace, out *intent) {
	th := a.opts.Theme
	ctx := a.ctx
	r.Y += th.Gap
	r.H -= th.Gap

	ws, has := a.m.SelectedWorkspace()
	if has {
		if i := SelectedIndex(rows, ws.Key()); i < 0 {
			// The selection is filtered out of view; acting on it would be a
			// surprise.
			has = false
		}
	}

	kind := intentActivate
	var label string
	// A running non-VM workspace gets the embedded webview as the primary
	// hand-off (Track B), with the integrated terminal and the system
	// browser as explicit secondary choices on the same row.
	console, browser, observe := false, false, false
	switch {
	case !has:
		label = i18n.Get("workspaces.open")
	case ws.Stopped:
		kind = intentStartWorkspace
		label = i18n.Get("workspaces.start")
	case !ws.Running():
		label = i18n.Get("workspaces.notRunning")
	case ws.IsVM():
		label = i18n.Get("workspaces.openDisplay")
		observe = true
	case ws.Type == kwclient.WorkspaceTypeContainer, ws.Type == kwclient.WorkspaceTypeScratch:
		kind = intentOpenWeb
		label = i18n.Get("workspaces.openWeb")
		console, browser = true, true
	default:
		browser = true
	}

	open := ui.Button{
		ID:       idOpen,
		Text:     label,
		Variant:  ui.ButtonPrimary,
		Disabled: !has || ((kind == intentActivate || kind == intentOpenWeb) && !ws.Running()),
	}
	// Secondary alternatives, both offered for any running non-VM workspace:
	// the integrated terminal (Track A) and the system-browser grant path.
	consoleBtn := ui.Button{}
	if console {
		consoleBtn = ui.Button{ID: idConsole, Text: i18n.Get("workspaces.console"), Variant: ui.ButtonSecondary}
	}
	browserBtn := ui.Button{}
	if browser {
		browserBtn = ui.Button{ID: idOpenInBrowser, Text: i18n.Get("workspaces.openBrowser"), Variant: ui.ButtonSecondary}
	}
	observeBtn := ui.Button{}
	if observe {
		observeBtn = ui.Button{ID: idObserve, Text: i18n.Get("workspaces.observe"), Variant: ui.ButtonSecondary}
	}
	// Stop is the quiet inverse of the primary action, offered for exactly the
	// workspaces that have one to stop: the running ones.
	stop := ui.Button{ID: idStop, Text: i18n.Get("workspaces.stop"), Variant: ui.ButtonSecondary}
	showStop := has && ws.Running()
	// Info is for looking, not acting, so it is secondary to the open button
	// and disabled when there is no selection to look at.
	info := ui.Button{ID: idInfo, Text: i18n.Get("workspaces.info"), Variant: ui.ButtonSecondary, Disabled: !has}
	// New opens the create form. It is always available: an empty list is
	// exactly when creating is the thing to do.
	newBtn := ui.Button{ID: idCreate, Text: i18n.Get("workspaces.new"), Variant: ui.ButtonSecondary}
	// Sessions opens the switcher. The count is the point: it is how a user
	// learns windows they closed are still connected.
	sessionsBtn := ui.Button{ID: idSessions, Text: i18n.Sprintf("sessions.title", len(a.m.Sessions)), Variant: ui.ButtonSecondary}

	widths := []int{max(180, open.Width(ctx))}
	if console {
		widths = append(widths, consoleBtn.Width(ctx))
	}
	if browser {
		widths = append(widths, browserBtn.Width(ctx))
	}
	if observe {
		widths = append(widths, observeBtn.Width(ctx))
	}
	if showStop {
		widths = append(widths, stop.Width(ctx))
	}
	widths = append(widths, info.Width(ctx), newBtn.Width(ctx), sessionsBtn.Width(ctx), 0)
	cols := ui.Row(r, th.Gap, widths...)

	ci := 0
	if open.Layout(ctx, cols[ci]) {
		*out = intent{kind: kind, workspace: ws}
	}
	ci++
	if console {
		if consoleBtn.Layout(ctx, cols[ci]) {
			*out = intent{kind: intentActivate, workspace: ws}
		}
		ci++
	}
	if browser {
		if browserBtn.Layout(ctx, cols[ci]) {
			*out = intent{kind: intentOpenInBrowser, workspace: ws}
		}
		ci++
	}
	if observe {
		if observeBtn.Layout(ctx, cols[ci]) {
			*out = intent{kind: intentOpenObserver, workspace: ws}
		}
		ci++
	}
	if showStop {
		if stop.Layout(ctx, cols[ci]) {
			*out = intent{kind: intentStopWorkspace, workspace: ws}
		}
		ci++
	}
	if info.Layout(ctx, cols[ci]) {
		*out = intent{kind: intentInfoWorkspace, workspace: ws}
	}
	ci++
	if newBtn.Layout(ctx, cols[ci]) {
		*out = intent{kind: intentCreateWorkspace}
	}
	ci++
	if sessionsBtn.Layout(ctx, cols[ci]) {
		*out = intent{kind: intentOpenSessions}
	}
	ci++

	hint := i18n.Get("workspaces.hint")
	if !a.m.LastRefresh.IsZero() {
		hint = i18n.Sprintf("workspaces.updated", since(a.m.LastRefresh, a.ctx.Input.Now)) + "  ·  " + hint
	}
	ui.Label(ctx, ui.InsetXY(cols[ci], th.Gap, 0), hint, ui.LabelStyle{
		Color:  th.TextMuted,
		Scale:  th.Small,
		Align:  ui.AlignRight,
		Middle: true,
	})
}

// drawWorkspaceInfoModal draws the details [Model.Info] names, centred over a
// dimmed, frozen copy of the list, and reports what the user asked it to do.
//
// Only the modal is laid out: the list's widgets are neither drawn nor
// registered, so nothing behind it can take a click or the keyboard. Escape
// and the Close button are the only ways back to the list, which is what a
// modal is for — the list is deliberately out of reach while it is up.
func (a *App) drawWorkspaceInfoModal(bounds ui.Rect) intent {
	th := a.opts.Theme
	ctx := a.ctx
	ws := a.m.Info
	var out intent
	if ws == nil {
		return out
	}

	// The frame underneath was not cleared this frame; it is still in the
	// buffer as the user last saw it. Dimming it is what reads as "behind
	// glass" rather than a second window.
	ctx.Canvas.Fill(bounds, modalScrim)

	width := min(bounds.W-2*th.Pad, infoCardWidth)
	innerW := width - 2*th.Pad
	rows := workspaceInfoRows(ws)

	// The label column grows to fit the widest label — "Condition Ready"
	// outruns any fixed header — capped at a third of the card so the value
	// column keeps the two-thirds the wrapping is tuned for.
	labelW := infoLabelWidth
	for _, row := range rows {
		if w := ui.TextWidth(row[0], th.Small, th.Font); w > labelW {
			labelW = w
		}
	}
	labelW = min(labelW, innerW/3)
	valueW := innerW - labelW - th.Gap

	// Measure the card before placing it: a wrapped value decides how tall
	// the card has to be, and a modal that guessed its height would clip or
	// gape. The parts are the stack rows in draw order, gaps between them.
	parts := []int{
		ui.TextHeight(th.Title, th.Font) + th.Gap/2, // name
		ui.LineHeight(th.Small, th.Font),            // namespace
		1,                                           // divider
	}
	for _, row := range rows {
		lines := len(ui.Wrap(row[1], th.Body, th.Font, valueW))
		if lines < 1 {
			lines = 1
		}
		parts = append(parts, lines*ui.LineHeight(th.Body, th.Font))
	}
	parts = append(parts, th.ControlHeight) // close row

	content := 0
	for i, p := range parts {
		if i > 0 {
			content += th.Gap
		}
		content += p
	}
	content += 2 * th.Pad

	card := ui.CenterRect(bounds, width, min(content, bounds.H-2*th.Gap))
	if card.W <= 0 || card.H <= 0 {
		return out
	}
	card.Y = max(card.Y, th.Gap)

	ctx.Canvas.FillRounded(card, th.Radius, th.Surface)
	ctx.Canvas.StrokeRounded(card, th.Radius, th.BorderWidth, th.Border)
	body := ui.NewStack(ui.Inset(card, th.Pad), th.Gap)

	ui.Label(ctx, body.Next(parts[0]), ws.Name, ui.LabelStyle{Scale: th.Title})
	ui.Label(ctx, body.Next(parts[1]), ws.Key(), ui.LabelStyle{
		Color: th.TextMuted, Scale: th.Small,
	})
	ui.Divider(ctx, body.Next(parts[2]))

	for i, row := range rows {
		r := body.Next(parts[3+i])
		labelRect, valueRect := ui.CutLeft(r, labelW)
		ui.Label(ctx, labelRect, row[0], ui.LabelStyle{
			Color: th.TextMuted, Scale: th.Small,
		})
		ui.Label(ctx, valueRect, row[1], ui.LabelStyle{Wrap: true})
	}

	footer := body.Next(parts[len(parts)-1])
	closeRow := ui.Button{ID: idInfoClose, Text: i18n.Get("workspaces.close"), Variant: ui.ButtonPrimary}
	// Start/Stop is the one thing a detail sheet is worth acting on: the rest
	// of the modal is for looking. It is omitted in the in-between state
	// (neither stopped nor running) because neither action applies then.
	toggleKind, toggleText, toggleID := intentNone, "", ui.FocusID("")
	switch {
	case ws.Stopped:
		toggleKind, toggleText, toggleID = intentStartWorkspace, i18n.Get("workspaces.start"), idInfoStart
	case ws.Running():
		toggleKind, toggleText, toggleID = intentStopWorkspace, i18n.Get("workspaces.stop"), idInfoStop
	}
	rest := footer
	if toggleKind != intentNone {
		toggle := ui.Button{ID: toggleID, Text: toggleText, Variant: ui.ButtonSecondary}
		toggleRect, remaining := ui.CutRight(footer, toggle.Width(ctx)+th.Gap)
		if toggle.Layout(ctx, toggleRect) {
			out = intent{kind: toggleKind, workspace: *ws}
		}
		rest = remaining
	}
	closeRect, _ := ui.CutRight(rest, closeRow.Width(ctx))
	if closeRow.Layout(ctx, closeRect) || ctx.Input.KeyPressed(keysym.KeyEscape) {
		out = intent{kind: intentInfoClose}
	}
	return out
}

// createTypes is the workspace-type row of the create form, in the order the
// API documents them.
var createTypes = []kwclient.WorkspaceType{
	kwclient.WorkspaceTypeContainer,
	kwclient.WorkspaceTypeVM,
	kwclient.WorkspaceTypeScratch,
}

// createTypeLabel names a workspace type the way the form shows it.
func createTypeLabel(t kwclient.WorkspaceType) string {
	switch t {
	case kwclient.WorkspaceTypeVM:
		return i18n.Get("create.vm")
	case kwclient.WorkspaceTypeScratch:
		return i18n.Get("create.scratch")
	default:
		return i18n.Get("create.container")
	}
}

// drawCreateModal draws the "new workspace" form over a dimmed, frozen copy
// of the list, like the info modal: only the form is laid out, so nothing
// behind it can take a click or the keyboard. Enter in a field submits, Esc
// and Cancel close.
func (a *App) drawCreateModal(bounds ui.Rect) intent {
	th := a.opts.Theme
	ctx := a.ctx
	var out intent

	ctx.Canvas.Fill(bounds, modalScrim)

	images := a.createImages()
	if a.createImageIdx >= len(images) {
		a.createImageIdx = max(len(images)-1, 0)
	}
	listRows := min(len(images), 5)
	listH := listRows*th.RowHeight + 2*th.BorderWidth
	if len(images) == 0 {
		listH = ui.LineHeight(th.Body, th.Font)
	}

	fieldH := th.ControlHeight
	labelH := ui.LineHeight(th.Small, th.Font)
	parts := []int{
		ui.TextHeight(th.Title, th.Font) + th.Gap/2, // title
		labelH + th.Gap/2 + fieldH,                  // name
		labelH + th.Gap/2 + fieldH,                  // namespace
		labelH + th.Gap/2 + th.ControlHeight,        // type row
		labelH + th.Gap/2 + listH,                   // image list
		th.ControlHeight,                            // buttons
	}
	if a.m.Err != "" {
		parts = append(parts, ui.LineHeight(th.Body, th.Font)) // error line
	}
	content := 2 * th.Pad
	for i, p := range parts {
		if i > 0 {
			content += th.Gap
		}
		content += p
	}

	width := min(bounds.W-2*th.Pad, infoCardWidth)
	card := ui.CenterRect(bounds, width, min(content, bounds.H-2*th.Gap))
	if card.W <= 0 || card.H <= 0 {
		return out
	}
	card.Y = max(card.Y, th.Gap)

	ctx.Canvas.FillRounded(card, th.Radius, th.Surface)
	ctx.Canvas.StrokeRounded(card, th.Radius, th.BorderWidth, th.Border)
	body := ui.NewStack(ui.Inset(card, th.Pad), th.Gap)

	ui.Label(ctx, body.Next(parts[0]), i18n.Get("create.title"), ui.LabelStyle{Scale: th.Title})

	ui.Label(ctx, body.Next(labelH), i18n.Get("create.name"), ui.LabelStyle{Color: th.TextMuted, Scale: th.Small})
	if a.createNameField.Layout(ctx, body.Next(fieldH)) && !a.m.Busy {
		out = intent{kind: intentCreateSubmit}
	}

	ui.Label(ctx, body.Next(labelH), i18n.Get("create.namespace"), ui.LabelStyle{Color: th.TextMuted, Scale: th.Small})
	if a.createNamespaceField.Layout(ctx, body.Next(fieldH)) && !a.m.Busy {
		out = intent{kind: intentCreateSubmit}
	}

	ui.Label(ctx, body.Next(labelH), i18n.Get("create.type"), ui.LabelStyle{Color: th.TextMuted, Scale: th.Small})
	typeRect := body.Next(th.ControlHeight)
	typeWidths := make([]int, len(createTypes))
	typeCols := make([]ui.Rect, len(createTypes))
	typeLabels := make([]string, len(createTypes))
	totalW := 0
	for i, t := range createTypes {
		typeLabels[i] = createTypeLabel(t)
		b := ui.Button{Text: typeLabels[i], Variant: ui.ButtonSecondary}
		if t == a.createType {
			b.Variant = ui.ButtonPrimary
		}
		typeWidths[i] = b.Width(ctx)
		totalW += typeWidths[i]
	}
	// Buttons share the row in proportion to their labels; the row is built
	// by hand because ui.Row divides evenly rather than by content.
	x := typeRect.X
	gap := th.Gap
	if len(createTypes) > 1 {
		spare := max(typeRect.W-totalW-(len(createTypes)-1)*gap, 0)
		x += spare / 2
	}
	for i := range createTypes {
		w := typeWidths[i]
		if i == len(createTypes)-1 {
			w = max(w, typeRect.X+typeRect.W-x)
		}
		typeCols[i] = ui.Rect{X: x, Y: typeRect.Y, W: w, H: typeRect.H}
		x += w + gap
	}
	for i, t := range createTypes {
		b := ui.Button{ID: ui.FocusID(fmt.Sprintf("create-type-%d", i)), Text: typeLabels[i], Variant: ui.ButtonSecondary}
		if t == a.createType {
			b.Variant = ui.ButtonPrimary
		}
		if b.Layout(ctx, typeCols[i]) && t != a.createType {
			a.createType = t
			a.createImageIdx = 0
			a.createImageList.Selected = 0
			a.createImageList.Offset = 0
		}
	}

	ui.Label(ctx, body.Next(labelH), i18n.Get("create.image"), ui.LabelStyle{Color: th.TextMuted, Scale: th.Small})
	imgRect := body.Next(listH)
	if len(images) == 0 {
		ui.Label(ctx, imgRect, i18n.Get("create.noImages"), ui.LabelStyle{Color: th.TextMuted})
	} else {
		a.createImageList.Selected = a.createImageIdx
		a.createImageList.Layout(ctx, imgRect, len(images), func(ctx *ui.Context, row ui.Rect, state ui.RowState) {
			img := images[state.Index]
			name := img.DisplayName
			if name == "" {
				name = img.Name
			}
			if state.Index == a.createImageIdx {
				ctx.Canvas.FillRounded(ui.InsetXY(row, th.Gap/2, 2), th.Radius, th.SurfaceSelected)
			}
			ui.Label(ctx, row, name+"  ·  "+img.Image, ui.LabelStyle{Middle: true})
		})
		if a.createImageList.Selected >= 0 && a.createImageList.Selected < len(images) {
			a.createImageIdx = a.createImageList.Selected
		}
	}

	if a.m.Err != "" {
		ui.Label(ctx, body.Next(parts[len(parts)-2]), a.m.Err, ui.LabelStyle{Color: th.Danger, Wrap: true})
	}

	foot := body.Next(parts[len(parts)-1])
	submit := ui.Button{ID: idCreateSubmit, Text: i18n.Get("create.create"), Variant: ui.ButtonPrimary, Disabled: a.m.Busy || len(images) == 0}
	cancel := ui.Button{ID: idCreateClose, Text: i18n.Get("create.cancel"), Variant: ui.ButtonSecondary}
	cancelRect, rest := ui.CutRight(foot, cancel.Width(ctx))
	submitRect, _ := ui.CutRight(rest, submit.Width(ctx)+th.Gap)
	if submit.Layout(ctx, submitRect) && !a.m.Busy && len(images) > 0 {
		out = intent{kind: intentCreateSubmit}
	}
	if cancel.Layout(ctx, cancelRect) || ctx.Input.KeyPressed(keysym.KeyEscape) {
		out = intent{kind: intentCreateClose}
	}
	return out
}

// drawProfilesModal lists every configured instance profile over a dimmed,
// frozen copy of the list. Picking one switches the shell to it without a
// relaunch; Esc and Close back out.
func (a *App) drawProfilesModal(bounds ui.Rect) intent {
	th := a.opts.Theme
	ctx := a.ctx
	var out intent

	ctx.Canvas.Fill(bounds, modalScrim)

	current := ""
	if a.profile != nil {
		current = a.profile.Name
	}
	rows := len(a.profiles)
	listH := rows*th.ControlHeight + max(rows-1, 0)*th.Gap/2
	if rows == 0 {
		listH = ui.LineHeight(th.Body, th.Font)
	}
	parts := []int{
		ui.TextHeight(th.Title, th.Font) + th.Gap/2, // title
		listH,            // profiles
		th.ControlHeight, // close row
	}
	content := 2 * th.Pad
	for i, p := range parts {
		if i > 0 {
			content += th.Gap
		}
		content += p
	}

	width := min(bounds.W-2*th.Pad, infoCardWidth)
	card := ui.CenterRect(bounds, width, min(content, bounds.H-2*th.Gap))
	if card.W <= 0 || card.H <= 0 {
		return out
	}
	card.Y = max(card.Y, th.Gap)

	ctx.Canvas.FillRounded(card, th.Radius, th.Surface)
	ctx.Canvas.StrokeRounded(card, th.Radius, th.BorderWidth, th.Border)
	body := ui.NewStack(ui.Inset(card, th.Pad), th.Gap)

	ui.Label(ctx, body.Next(parts[0]), i18n.Get("profiles.title"), ui.LabelStyle{Scale: th.Title})

	listRect := body.Next(parts[1])
	if rows == 0 {
		ui.Label(ctx, listRect, i18n.Get("profiles.none"), ui.LabelStyle{Color: th.TextMuted, Wrap: true})
	} else {
		y := listRect.Y
		for i, p := range a.profiles {
			if p == nil {
				continue
			}
			r := ui.Rect{X: listRect.X, Y: y, W: listRect.W, H: th.ControlHeight}
			y += th.ControlHeight + th.Gap/2
			label := p.Name + "  ·  " + p.Server
			if p.Name == current {
				label += "  " + i18n.Get("profiles.current")
			}
			b := ui.Button{
				ID:       ui.FocusID(fmt.Sprintf("profile-%d", i)),
				Text:     label,
				Variant:  ui.ButtonSecondary,
				Disabled: p.Name == current,
			}
			if b.Layout(ctx, r) {
				out = intent{kind: intentSwitchProfile, profile: p.Name}
			}
		}
	}

	foot := body.Next(parts[2])
	closeRow := ui.Button{ID: idProfilesClose, Text: i18n.Get("workspaces.close"), Variant: ui.ButtonPrimary}
	closeRect, _ := ui.CutRight(foot, closeRow.Width(ctx))
	if closeRow.Layout(ctx, closeRect) || ctx.Input.KeyPressed(keysym.KeyEscape) {
		out = intent{kind: intentProfilesClose}
	}
	return out
}

// drawSessionsModal lists the held sessions over a dimmed, frozen copy of
// the list. Switch resumes one in a fresh window; Close disconnects it for
// good. Esc and Close back out.
func (a *App) drawSessionsModal(bounds ui.Rect) intent {
	th := a.opts.Theme
	ctx := a.ctx
	var out intent

	ctx.Canvas.Fill(bounds, modalScrim)

	rows := len(a.m.Sessions)
	listH := rows*(th.ControlHeight+th.Gap/2) + th.ControlHeight
	if rows == 0 {
		listH = ui.LineHeight(th.Body, th.Font) + th.Gap/2 + th.ControlHeight
	}
	parts := []int{
		ui.TextHeight(th.Title, th.Font) + th.Gap/2, // title
		listH,            // sessions
		th.ControlHeight, // close row
	}
	content := 2 * th.Pad
	for i, p := range parts {
		if i > 0 {
			content += th.Gap
		}
		content += p
	}

	width := min(bounds.W-2*th.Pad, infoCardWidth)
	card := ui.CenterRect(bounds, width, min(content, bounds.H-2*th.Gap))
	if card.W <= 0 || card.H <= 0 {
		return out
	}
	card.Y = max(card.Y, th.Gap)

	ctx.Canvas.FillRounded(card, th.Radius, th.Surface)
	ctx.Canvas.StrokeRounded(card, th.Radius, th.BorderWidth, th.Border)
	body := ui.NewStack(ui.Inset(card, th.Pad), th.Gap)

	ui.Label(ctx, body.Next(parts[0]), i18n.Sprintf("sessions.title", rows), ui.LabelStyle{Scale: th.Title})

	listRect := body.Next(parts[1])
	if rows == 0 {
		ui.Label(ctx, ui.Rect{X: listRect.X, Y: listRect.Y, W: listRect.W, H: ui.LineHeight(th.Body, th.Font)}, i18n.Get("sessions.none"), ui.LabelStyle{Color: th.TextMuted, Wrap: true})
	} else {
		y := listRect.Y
		for i, entry := range a.m.Sessions {
			r := ui.Rect{X: listRect.X, Y: y, W: listRect.W, H: th.ControlHeight}
			y += th.ControlHeight + th.Gap/2
			sw := ui.Button{ID: ui.FocusID(fmt.Sprintf("session-switch-%d", i)), Text: i18n.Get("sessions.switch"), Variant: ui.ButtonSecondary}
			cl := ui.Button{ID: ui.FocusID(fmt.Sprintf("session-close-%d", i)), Text: i18n.Get("sessions.close"), Variant: ui.ButtonSecondary}
			swW, clW := sw.Width(ctx), cl.Width(ctx)
			titleW := max(r.W-swW-clW-th.Gap, 0)
			titleRect, ctrls := ui.CutLeft(r, titleW)
			ui.Label(ctx, titleRect, entry.Title+"  ·  "+sessionKindLabel(entry.Kind), ui.LabelStyle{Middle: true})
			swRect, rest := ui.CutLeft(ctrls, swW)
			_, rest = ui.CutLeft(rest, th.Gap)
			clRect, _ := ui.CutLeft(rest, clW)
			if sw.Layout(ctx, swRect) {
				out = intent{kind: intentSwitchSession, sessionKey: entry.Key}
			}
			if cl.Layout(ctx, clRect) {
				out = intent{kind: intentCloseSession, sessionKey: entry.Key}
			}
		}
	}

	foot := body.Next(parts[2])
	closeRow := ui.Button{ID: idSessionsClose, Text: i18n.Get("workspaces.close"), Variant: ui.ButtonPrimary}
	closeRect, _ := ui.CutRight(foot, closeRow.Width(ctx))
	if closeRow.Layout(ctx, closeRect) || ctx.Input.KeyPressed(keysym.KeyEscape) {
		out = intent{kind: intentSessionsClose}
	}
	return out
}

// sessionKindLabel names a handle kind the way the switcher shows it.
func sessionKindLabel(kind string) string {
	switch kind {
	case "terminal":
		return i18n.Get("sessions.kindTerminal")
	case "observer":
		return i18n.Get("sessions.kindObserver")
	case "tier1":
		return i18n.Get("sessions.kindTier1")
	default:
		return i18n.Get("sessions.kindDisplay")
	}
}

// workspaceInfoRows is the labelled detail the info modal shows. Empty values
// are dropped so a workspace the API did not decorate does not collect a wall
// of blank labels.
func workspaceInfoRows(ws *kwclient.Workspace) [][2]string {
	if ws == nil {
		return nil
	}
	var rows [][2]string
	add := func(label, value string) {
		if value != "" {
			rows = append(rows, [2]string{label, value})
		}
	}

	add(i18n.Get("info.namespace"), ws.Namespace)
	add(i18n.Get("info.type"), string(ws.Type))
	add(i18n.Get("info.status"), StatusText(*ws))
	add(i18n.Get("info.image"), ws.Image)
	if ws.Port != nil {
		add(i18n.Get("info.port"), fmt.Sprintf("%d", *ws.Port))
	}
	if rd := ws.RemoteDesktop; rd != nil {
		path := "/"
		if rd.Path != nil {
			path = *rd.Path
		}
		add(i18n.Get("info.transport"), fmt.Sprintf("%s (port %d, path %s)", rd.Protocol, rd.Port, path))
	}
	add(i18n.Get("info.cpu"), resourceRange(ws.CPURequest, ws.CPULimit))
	add(i18n.Get("info.memory"), resourceRange(ws.MemoryRequest, ws.MemoryLimit))
	if t, ok := ws.CreatedAtTime(); ok {
		add(i18n.Get("info.created"), t.Local().Format("2 Jan 2006 15:04"))
	}
	for _, vm := range ws.VolumeMounts {
		add(i18n.Get("info.volume"), vm.Name+" -> "+vm.MountPath)
	}
	for _, c := range ws.Conditions {
		detail := c.Status
		if c.Reason != "" {
			detail += " (" + c.Reason + ")"
		}
		if c.Message != "" && c.Message != c.Reason {
			detail += ": " + c.Message
		}
		add(i18n.Sprintf("info.condition", c.Type), detail)
	}
	return rows
}

// resourceRange joins a request and a limit into one readable row, e.g.
// "request 500m, limit 2". A missing side is skipped rather than shown as an
// empty slot, and an empty pair disappears from the modal entirely.
func resourceRange(request, limit *string) string {
	var parts []string
	if request != nil && *request != "" {
		parts = append(parts, i18n.Sprintf("info.request", *request))
	}
	if limit != nil && *limit != "" {
		parts = append(parts, i18n.Sprintf("info.limit", *limit))
	}
	return strings.Join(parts, ", ")
}

// drawMessagesIn draws whichever message the model is holding.
//
// An error and a notice are mutually exclusive by construction — every
// transition sets one and clears the other — so there is only ever one strip,
// and the layout does not have to reserve space for two.
func (a *App) drawMessagesIn(r ui.Rect) {
	switch {
	case a.m.Err != "":
		ui.Banner(a.ctx, r, ui.BannerError, a.m.Err)
	case a.m.Notice != "":
		ui.Banner(a.ctx, r, ui.BannerInfo, a.m.Notice)
	}
}

// messageHeight is how much room the message strip needs, so that the list
// above it can be given the rest.
func (a *App) messageHeight(width int) int {
	text := a.m.Err
	if text == "" {
		text = a.m.Notice
	}
	if text == "" || width <= 0 {
		return 0
	}
	th := a.opts.Theme
	lines := ui.Wrap(text, th.Body, th.Font, width-2*th.Gap)
	return len(lines)*ui.LineHeight(th.Body, th.Font) + th.Gap + th.Gap
}

// drawBusy draws a spinner and a caption inline.
func (a *App) drawBusy(r ui.Rect, text string) {
	th := a.opts.Theme
	spinner, rest := ui.CutLeft(r, r.H+th.Gap/2)
	ui.Spinner(a.ctx, ui.Inset(spinner, th.Gap/2), th.TextMuted)
	ui.Label(a.ctx, rest, text+"...", ui.LabelStyle{
		Color: th.TextMuted, Scale: th.Small, Middle: true,
	})
}

// drawFooterHint writes a keyboard hint along the bottom of the window.
func (a *App) drawFooterHint(bounds ui.Rect, text string) {
	th := a.opts.Theme
	strip, _ := ui.CutBottom(ui.Inset(bounds, th.Pad), ui.LineHeight(th.Small, th.Font))
	ui.Label(a.ctx, strip, text, ui.LabelStyle{
		Color: th.TextMuted, Scale: th.Small, Align: ui.AlignRight,
	})
}

// statusColor is the colour a workspace's state is drawn in.
func (a *App) statusColor(ws kwclient.Workspace) color.RGBA {
	th := a.opts.Theme
	switch {
	case ws.Running():
		return th.Success
	case ws.Stopped:
		return th.TextDisabled
	default:
		return th.Warning
	}
}

// identityLine is the "who am I" text in the header.
func (a *App) identityLine() string {
	if a.m.Identity == nil {
		return a.m.Server
	}
	who := a.m.Identity.Email
	if who == "" {
		who = a.m.Identity.DisplayName
	}
	if who == "" {
		who = i18n.Get("header.noAuth")
	}
	if a.m.Identity.Role != "" {
		who += " (" + a.m.Identity.Role + ")"
	}
	return who + "  ·  " + a.m.Server
}

// emptyText explains an empty list, which has three quite different causes.
func (a *App) emptyText() string {
	switch {
	case a.m.Filter != "" && len(a.m.Workspaces) > 0:
		return i18n.Sprintf("workspaces.noMatch", a.m.Filter)
	case a.m.LastRefresh.IsZero():
		return i18n.Get("workspaces.loading")
	default:
		return i18n.Get("workspaces.empty")
	}
}

// issuerHost is the identity provider's host, shown before sending the user to
// it. A user about to be redirected should be able to see where.
func (a *App) issuerHost() string {
	if a.m.Auth == nil || a.m.Auth.IssuerURL == "" {
		return ""
	}
	s := a.m.Auth.IssuerURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}

// since renders a coarse age. Coarse on purpose: a list that refreshes every
// five seconds does not need a second hand, and a label that changes every
// second is a label that draws the eye away from the list.
func since(then, now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	d := now.Sub(then)
	switch {
	case d < 10*time.Second:
		return i18n.Get("since.now")
	case d < time.Minute:
		return i18n.Sprintf("since.secs", int(d.Seconds()))
	case d < time.Hour:
		return i18n.Sprintf("since.mins", int(d.Minutes()))
	default:
		return i18n.Sprintf("since.hours", int(d.Hours()))
	}
}
