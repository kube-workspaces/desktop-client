// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"fmt"
	"image/color"
	"strings"
	"time"

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
)

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

	ui.Label(ctx, body.Next(ui.TextHeight(th.Title, th.Font)), "Kube Workspaces", ui.LabelStyle{
		Scale: th.Title,
	})
	body.Skip(th.Gap / 2)
	ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)*2), "Connect to your workspaces instance.", ui.LabelStyle{
		Color: th.TextMuted,
		Wrap:  true,
	})
	body.Skip(th.Pad)

	ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), "SERVER ADDRESS", ui.LabelStyle{
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
	connect := ui.Button{ID: idConnect, Text: "Connect", Variant: ui.ButtonPrimary, Disabled: a.m.Busy}
	row := ui.Row(body.Next(th.ControlHeight), th.Gap, 160, 0)
	if connect.Layout(ctx, row[0]) || (submitted && !a.m.Busy) {
		out = intent{kind: intentConnectServer}
	}
	if a.m.Busy {
		a.drawBusy(row[1], a.m.BusyText)
	}

	body.Skip(th.Gap)
	a.drawMessagesIn(body.Rest())

	a.drawFooterHint(bounds, "Tab moves between fields  ·  Enter connects")
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

	ui.Label(ctx, body.Next(ui.TextHeight(th.Title, th.Font)), "Sign in", ui.LabelStyle{Scale: th.Title})
	body.Skip(th.Gap / 2)
	ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)), a.m.Server, ui.LabelStyle{Color: th.TextMuted})
	body.Skip(th.Pad)

	waiting := a.m.Busy && strings.HasPrefix(a.m.BusyText, "Waiting")

	switch {
	case waiting:
		out = a.drawBrowserWait(body)

	case a.m.AuthDisabled():
		ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)*2),
			"This instance does not require sign-in.", ui.LabelStyle{Color: th.TextMuted, Wrap: true})
		body.Skip(th.Gap)
		cont := ui.Button{ID: idSignIn, Text: "Continue", Variant: ui.ButtonPrimary, Disabled: a.m.Busy}
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
	back := ui.Button{ID: idBack, Text: "Use a different server", Variant: ui.ButtonQuiet}
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
			"This instance offers no sign-in method this client can use. "+
				"Ask an administrator whether browser sign-in is enabled.",
			ui.LabelStyle{Color: th.TextMuted, Wrap: true})
		return out
	}

	if local {
		ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), "EMAIL", ui.LabelStyle{
			Color: th.TextMuted, Scale: th.Small,
		})
		emailSubmit := a.emailField.Layout(ctx, body.Next(th.ControlHeight))
		body.Skip(th.Gap / 2)
		ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), "PASSWORD", ui.LabelStyle{
			Color: th.TextMuted, Scale: th.Small,
		})
		passwordSubmit := a.passwordField.Layout(ctx, body.Next(th.ControlHeight))
		body.Skip(th.Pad)

		signIn := ui.Button{ID: idSignIn, Text: "Sign in", Variant: ui.ButtonPrimary, Disabled: a.m.Busy}
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
			ui.DividerLabel(ctx, body.Next(ui.LineHeight(th.Body, th.Font)), "or")
			body.Skip(th.Gap / 2)
		}
		variant := ui.ButtonPrimary
		if local {
			variant = ui.ButtonSecondary
		}
		btn := ui.Button{ID: idBrowser, Text: "Sign in with browser", Variant: variant, Disabled: a.m.Busy}
		if btn.Layout(ctx, ui.Row(body.Next(th.ControlHeight), th.Gap, btn.Width(ctx), 0)[0]) {
			out = intent{kind: intentSignInBrowser}
		}
		if issuer := a.issuerHost(); issuer != "" {
			body.Skip(th.Gap / 2)
			ui.Label(ctx, body.Next(ui.LineHeight(th.Small, th.Font)), "You will be sent to "+issuer,
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
	ui.Label(ctx, rest, "Waiting for your browser...", ui.LabelStyle{Middle: true})

	body.Skip(th.Gap / 2)
	ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)*2),
		"Complete the sign-in in the window that opened, then come back here.",
		ui.LabelStyle{Color: th.TextMuted, Wrap: true})

	if a.authorizeURL != "" {
		body.Skip(th.Gap)
		ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), "IF NOTHING OPENED, VISIT", ui.LabelStyle{
			Color: th.TextMuted, Scale: th.Small,
		})
		urlBox := body.Next(ui.LineHeight(th.Small, th.Font)*3 + th.Gap)
		ctx.Canvas.FillRounded(urlBox, th.Radius, th.SurfaceAlt)
		ui.Label(ctx, ui.Inset(urlBox, th.Gap/2), a.authorizeURL, ui.LabelStyle{
			Color: th.TextMuted, Scale: th.Small, Wrap: true,
		})
	}

	body.Skip(th.Pad)
	cancel := ui.Button{ID: idCancel, Text: "Cancel", Variant: ui.ButtonSecondary}
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
	refresh := ui.Button{ID: idRefresh, Text: "Refresh", Variant: ui.ButtonSecondary}
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
// style and the dark/light colours, both remembered between runs.
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

	ui.Label(ctx, body.Next(ui.TextHeight(th.Title, th.Font)), "Settings", ui.LabelStyle{Scale: th.Title})
	body.Skip(th.Gap / 2)
	ui.Label(ctx, body.Next(ui.LineHeight(th.Body, th.Font)*2),
		"How the client looks. Choices save as you make them.", ui.LabelStyle{Color: th.TextMuted, Wrap: true})
	body.Skip(th.Pad)

	ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), "STYLE", ui.LabelStyle{
		Color: th.TextMuted, Scale: th.Small,
	})
	styleRow := body.Next(th.ControlHeight)
	if picked := a.drawChoice(ctx, styleRow, styleIndex(a.settings.Style), []styleOption{
		{id: idStyleBubbly, label: "Bubbly"},
		{id: idStyleRetro, label: "Retro"},
		{id: idStyleClean, label: "Clean"},
	}); picked != styleIndex(a.settings.Style) {
		a.applySettings(Settings{Style: styleFromIndex(picked), Mode: a.settings.Mode})
		a.saveSettings()
	}

	body.Skip(th.Pad)
	ui.Label(ctx, body.Next(ui.TextHeight(th.Small, th.Font)+2), "COLOURS", ui.LabelStyle{
		Color: th.TextMuted, Scale: th.Small,
	})
	modeRow := body.Next(th.ControlHeight)
	modeOpts := []styleOption{{id: idModeDark, label: "Dark"}, {id: idModeLight, label: "Light"}}
	modeIdx := 0
	if a.settings.Mode == ui.ModeLight {
		modeIdx = 1
	}
	if picked := a.drawChoice(ctx, modeRow, modeIdx, modeOpts); picked != modeIdx {
		mode := ui.ModeDark
		if picked == 1 {
			mode = ui.ModeLight
		}
		a.applySettings(Settings{Style: a.settings.Style, Mode: mode})
		a.saveSettings()
	}

	body.Skip(th.Pad)
	done := ui.Button{ID: idSettingsDone, Text: "Done", Variant: ui.ButtonPrimary}
	if done.Layout(ctx, ui.Row(body.Next(th.ControlHeight), th.Gap, 160, 0)[0]) ||
		ctx.Input.KeyPressed(keysym.KeyEscape) {
		out = intent{kind: intentSettingsDone}
	}

	a.drawFooterHint(bounds, "Changes save as you pick  ·  Escape closes")
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

type styleOption struct {
	id    ui.FocusID
	label string
}

// drawHeader draws the title bar: who is signed in, the way out, and the way
// to the appearance settings.
func (a *App) drawHeader(r ui.Rect, out *intent) {
	th := a.opts.Theme
	ctx := a.ctx

	signOut := ui.Button{ID: idSignOut, Text: "Sign out", Variant: ui.ButtonQuiet}
	settings := ui.Button{ID: idSettings, Text: "Settings", Variant: ui.ButtonQuiet}
	buttonsW := signOut.Width(ctx) + settings.Width(ctx) + th.Gap/2
	signOutRect, rest := ui.CutRight(r, buttonsW)
	cols := ui.Row(signOutRect, th.Gap/2, settings.Width(ctx), signOut.Width(ctx))
	if settings.Layout(ctx, cols[0]) {
		*out = intent{kind: intentOpenSettings}
	}
	if signOut.Layout(ctx, cols[1]) {
		*out = intent{kind: intentSignOut}
	}

	title, identity := ui.CutLeft(rest, ui.TextWidth("Workspaces", th.Title, th.Font)+th.Pad)
	ui.Label(ctx, title, "Workspaces", ui.LabelStyle{Scale: th.Title, Middle: true})
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
			*out = intent{kind: intentActivate, workspace: ws}
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
		ui.Label(ctx, meta, string(ws.Type), ui.LabelStyle{
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
	switch {
	case !has:
		label = "Open"
	case !ws.Running():
		label = "Not running"
	case ws.IsVM():
		label = "Open display"
	default:
		label = "Open in browser"
		kind = intentOpenInBrowser
	}

	open := ui.Button{
		ID:       idOpen,
		Text:     label,
		Variant:  ui.ButtonPrimary,
		Disabled: !has || !ws.Running(),
	}
	openRect, hintRect := ui.CutLeft(r, max(180, open.Width(ctx)))
	if open.Layout(ctx, openRect) {
		*out = intent{kind: kind, workspace: ws}
	}

	hint := "Enter opens  ·  F5 refreshes  ·  Ctrl-F filters  ·  Tab moves"
	if !a.m.LastRefresh.IsZero() {
		hint = "Updated " + since(a.m.LastRefresh, a.ctx.Input.Now) + "  ·  " + hint
	}
	ui.Label(ctx, ui.InsetXY(hintRect, th.Gap, 0), hint, ui.LabelStyle{
		Color:  th.TextMuted,
		Scale:  th.Small,
		Align:  ui.AlignRight,
		Middle: true,
	})
}

// drawConnectingScreen is shown for the single frame between the user
// activating a workspace and the session viewer taking the window.
func (a *App) drawConnectingScreen(bounds ui.Rect) intent {
	th := a.opts.Theme
	ctx := a.ctx

	// Wide enough for a long "namespace/name": truncating the one thing the
	// screen exists to name would be absurd.
	card := ui.CenterRect(bounds, min(bounds.W-2*th.Pad, 900), th.ControlHeight*2)
	spinner, rest := ui.CutLeft(card, th.ControlHeight*2)
	ui.Spinner(ctx, ui.Inset(spinner, th.Gap), th.Accent)
	ui.Label(ctx, rest, "Opening "+a.m.Opening.Key()+"...", ui.LabelStyle{Middle: true})
	return intent{}
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
		who = "authentication disabled"
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
		return fmt.Sprintf("No workspace matches %q.", a.m.Filter)
	case a.m.LastRefresh.IsZero():
		return "Loading workspaces..."
	default:
		return "You have no workspaces yet. Create one in the web UI."
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
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}
