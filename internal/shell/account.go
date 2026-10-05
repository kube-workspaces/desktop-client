// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

// A new instance per visit prevents late network results from changing a later
// visit (or another account). Password fields live only as long as this view.
type accountView struct {
	loading, busy                                   bool
	keys                                            []kwclient.SSHKey
	devices                                         []kwclient.DeviceInfo
	keyErr, deviceErr, err, notice                  string
	confirm                                         string
	passwordOpen                                    bool
	name, publicKey, current, next, confirmPassword ui.TextInput
	scroll                                          int
	focus                                           ui.FocusID
}

func (a *App) openAccount(ctx context.Context) {
	if a.api == nil {
		return
	}
	p := &accountView{loading: true}
	p.name = ui.TextInput{ID: "account-key-name", Placeholder: i18n.Get("account.namePlaceholder")}
	p.publicKey = ui.TextInput{ID: "account-public-key", Placeholder: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA..."}
	p.current = ui.TextInput{ID: "account-current-password", Password: true}
	p.next = ui.TextInput{ID: "account-new-password", Password: true}
	p.confirmPassword = ui.TextInput{ID: "account-confirm-password", Password: true}
	a.account = p
	a.m.State = StateProfile
	a.m.Err, a.m.Notice = "", ""
	api, token := a.api, a.api.Token()
	a.background(func() func() {
		opCtx, cancel := context.WithTimeout(ctx, listTimeout)
		defer cancel()
		cfg, cfgErr := api.AuthConfig(opCtx)
		keys, keyErr := api.ListSSHKeys(opCtx)
		var devices []kwclient.DeviceInfo
		var deviceErr error
		if cfg != nil && cfg.Enabled {
			devices, deviceErr = api.ListDevices(opCtx)
		}
		return func() {
			if !a.accountCurrent(p, api, token) {
				return
			}
			p.loading = false
			if cfgErr == nil {
				a.m.Auth = cfg
			}
			p.keys, p.devices = keys, devices
			p.err = a.accountError(cfgErr)
			p.keyErr = a.accountError(keyErr)
			p.deviceErr = a.accountError(deviceErr)
		}
	})
}

func (a *App) accountCurrent(p *accountView, api API, token string) bool {
	return a.m.State == StateProfile && a.account == p && a.api == api && api.Token() == token
}

func (a *App) accountError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, kwclient.ErrUnauthorized) {
		a.m.Fail(err)
		a.closeAllSessions()
		a.stopWorkspaceWatch()
	}
	return err.Error()
}

func (a *App) accountNamespace(ctx context.Context, namespace string) {
	if a.profile == nil || a.m.Identity == nil {
		return
	}
	if namespace != "" && namespace != a.m.Identity.PersonalNamespace && !slices.Contains(a.m.Identity.Namespaces, namespace) {
		return
	}
	updated := *a.profile
	updated.Namespace = namespace
	if err := a.opts.Store.Save(&updated, a.api.Token()); err != nil {
		a.account.err = err.Error()
		return
	}
	a.profile = &updated
	a.stopWorkspaceWatch()
	a.m.Workspaces = nil
	a.m.Selected = ""
	a.nextRefresh = time.Time{}
	a.reconcileWorkspaceWatch(ctx)
}

// Match the web form's DNS-safe resource name while retaining the display name.
func accountKeySlug(label string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.ToLower(label) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if separator && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			separator = false
		} else {
			separator = true
		}
	}
	return b.String()[:min(63, b.Len())]
}

func (a *App) mutateAccount(ctx context.Context, in intent) {
	p := a.account
	if p == nil || p.busy || p.loading || a.api == nil {
		return
	}
	api, token := a.api, a.api.Token()
	var operation func(context.Context) error
	var success func()
	switch in.kind {
	case intentAccountAddKey:
		payload := kwclient.CreateSSHKeyPayload{Name: accountKeySlug(p.name.Value()), KeyName: p.name.Value(), PublicKey: strings.TrimSpace(p.publicKey.Value())}
		if payload.Name == "" || payload.PublicKey == "" {
			return
		}
		var keys []kwclient.SSHKey
		operation = func(ctx context.Context) error {
			if _, err := api.CreateSSHKey(ctx, payload); err != nil {
				return err
			}
			var err error
			keys, err = api.ListSSHKeys(ctx)
			return err
		}
		success = func() { p.keys = keys; p.keyErr = ""; p.name.SetValue(""); p.publicKey.SetValue("") }
	case intentAccountDeleteKey:
		index := slices.IndexFunc(p.keys, func(k kwclient.SSHKey) bool { return k.Namespace+"/"+k.Name == in.profile })
		if index < 0 {
			return
		}
		key := p.keys[index]
		operation = func(ctx context.Context) error { return api.DeleteSSHKey(ctx, key.Namespace, key.Name) }
		success = func() { p.keys = slices.Delete(p.keys, index, index+1) }
	case intentAccountRevoke:
		id := in.profile
		if p.confirm != id || !slices.ContainsFunc(p.devices, func(d kwclient.DeviceInfo) bool { return d.DeviceID == id }) {
			return
		}
		operation = func(ctx context.Context) error { return api.RevokeDevice(ctx, id) }
		success = func() {
			p.devices = slices.DeleteFunc(p.devices, func(d kwclient.DeviceInfo) bool { return d.DeviceID == id })
			p.confirm = ""
			// Revoking this computer's credential must end the local sign-in too.
			a.checkAccountIdentity(ctx, p, api, token)
		}
	case intentAccountPassword:
		if a.m.Auth == nil || !a.m.Auth.LocalAuth.Enabled {
			return
		}
		current, next := p.current.Value(), p.next.Value()
		if utf8.RuneCountInString(next) < 12 {
			p.err = i18n.Get("account.passwordShort")
			return
		}
		if next != p.confirmPassword.Value() {
			p.err = i18n.Get("account.passwordMismatch")
			return
		}
		operation = func(ctx context.Context) error { return api.ChangePassword(ctx, current, next) }
		success = func() {
			p.current.SetValue("")
			p.next.SetValue("")
			p.confirmPassword.SetValue("")
			p.passwordOpen = false
			p.notice = i18n.Get("account.passwordChanged")
			a.checkAccountIdentity(ctx, p, api, token)
		}
	default:
		return
	}
	p.busy, p.err, p.notice = true, "", ""
	a.background(func() func() {
		opCtx, cancel := context.WithTimeout(ctx, listTimeout)
		defer cancel()
		err := operation(opCtx)
		return func() {
			if !a.accountCurrent(p, api, token) {
				return
			}
			p.busy = false
			if err != nil {
				p.err = a.accountError(err)
				return
			}
			success()
		}
	})
}

func (a *App) checkAccountIdentity(ctx context.Context, p *accountView, api API, token string) {
	a.background(func() func() {
		opCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		who, err := api.Me(opCtx)
		return func() {
			if !a.accountCurrent(p, api, token) {
				return
			}
			if err == nil && who != nil && who.AuthEnabled && !who.Authenticated {
				err = kwclient.ErrUnauthorized
			}
			if err != nil {
				p.err = a.accountError(err)
			} else {
				a.m.Identity = who
			}
		}
	})
}

func (a *App) drawAccountScreen(bounds ui.Rect) intent {
	p, ctx, th := a.account, a.ctx, a.opts.Theme
	if p == nil {
		return intent{kind: intentAccountDone}
	}
	var out intent
	outer := ui.Inset(bounds, th.Pad)
	width := min(outer.W, 336*th.Body)
	outer.X += (outer.W - width) / 2
	outer.W = width
	outer.Y += th.Pad
	outer.H = max(0, outer.H-th.Pad)
	headerHeight := max(ui.TextHeight(th.Title, th.Font), th.ControlHeight)
	header, view := ui.CutTop(outer, headerHeight+th.Pad)
	header.H = headerHeight
	closePage := a.drawPageHeader(header, i18n.Get("account.title"), "account-close")
	if ctx.Input.Hovering(view) {
		p.scroll = max(0, p.scroll-ctx.Input.Wheel.Y*th.ControlHeight)
	}
	input := ctx.Input
	if !input.Mouse.In(view) {
		ctx.Input.Mouse = ui.Point{X: -1, Y: -1}
		ctx.Input.Pressed, ctx.Input.Released, ctx.Input.Down = false, false, false
	}
	ctx.Canvas.PushClip(view)
	y := view.Y - p.scroll
	var focused ui.Rect
	line := ui.LineHeight(th.Body, th.Font)
	small := ui.LineHeight(th.Small, th.Font)
	// Each card is sized to its content, so wrapped labels and long account
	// metadata remain readable at every theme scale.
	textHeight := func(text string) int {
		return max(1, len(ui.Wrap(text, th.Small, th.Font, max(1, width-2*th.Pad)))) * small
	}
	card := func(title string, height int) *ui.Stack {
		r := ui.Rect{X: view.X, Y: y, W: width, H: height + 2*th.Pad}
		ctx.Canvas.FillRounded(r, th.Radius, th.Surface)
		ctx.Canvas.StrokeRounded(r, th.Radius, th.BorderWidth, th.Border)
		y += r.H + th.Pad
		s := ui.NewStack(ui.Inset(r, th.Pad), th.Gap)
		if title != "" {
			ui.Label(ctx, s.Next(line), title, ui.LabelStyle{})
		}
		return s
	}
	label := func(s *ui.Stack, text string) {
		ui.Label(ctx, s.Next(textHeight(text)), text, ui.LabelStyle{Scale: th.Small, Color: th.TextMuted, Wrap: true})
	}
	button := func(r ui.Rect, id ui.FocusID, text string, disabled bool, kind intentKind, subject string) bool {
		b := ui.Button{ID: id, Text: text, Disabled: disabled, Variant: ui.ButtonSecondary, Smooth: true}
		switch kind {
		case intentAccountAddKey, intentAccountPassword:
			b.Variant = ui.ButtonPrimary
		case intentAccountDeleteKey, intentAccountRevoke:
			b.Variant = ui.ButtonDanger
		case intentAccountNamespace:
			if a.profile != nil && a.profile.Namespace == subject {
				b.Variant = ui.ButtonPrimary
			}
		}
		// Only the visible portion of scrolled controls accepts pointer input.
		saved := ctx.Input
		if !ctx.Input.Mouse.In(ui.Intersect(r, view)) {
			ctx.Input.Pressed, ctx.Input.Released, ctx.Input.Down = false, false, false
		}
		clicked := b.Layout(ctx, r)
		ctx.Input = saved
		if ctx.Focused(id) {
			focused = r
		}
		if clicked && kind != intentNone {
			out = intent{kind: kind, profile: subject}
		}
		return clicked
	}
	field := func(s *ui.Stack, f *ui.TextInput, title string) {
		label(s, title)
		r := s.Next(th.ControlHeight)
		saved := ctx.Input
		if p.busy {
			ctx.Input = ui.Input{Now: saved.Now}
		}
		f.Layout(ctx, r)
		ctx.Input = saved
		if ctx.Focused(f.ID) {
			focused = r
		}
	}
	if p.err != "" {
		ui.Label(ctx, card("", textHeight(p.err)).Next(textHeight(p.err)), p.err, ui.LabelStyle{Scale: th.Small, Color: th.Danger, Wrap: true})
	}
	if p.notice != "" {
		label(card("", textHeight(p.notice)), p.notice)
	}
	who := a.m.Identity
	if who == nil {
		label(card("", small), i18n.Get("account.notSignedIn"))
	} else {
		identityH := 3*line + 2*th.Gap
		local := a.m.Auth != nil && a.m.Auth.LocalAuth.Enabled
		if local {
			identityH += th.ControlHeight + th.Gap
		}
		if p.passwordOpen {
			for _, key := range []string{"account.currentPassword", "account.newPassword", "account.confirmPassword"} {
				identityH += textHeight(i18n.Get(key)) + th.ControlHeight + 2*th.Gap
			}
			identityH += th.ControlHeight + th.Gap
		}
		s := card("", identityH)
		r := s.Next(3*line + 2*th.Gap)
		avatar, info := ui.CutLeft(r, min(r.H, r.W/4))
		ctx.Canvas.FillRounded(avatar, avatar.H/2, th.SurfaceAlt)
		if !a.drawAvatarImage(avatar) {
			ui.Label(ctx, avatar, avatarInitials(who), ui.LabelStyle{Align: ui.AlignCenter, Middle: true})
		}
		info = ui.InsetXY(info, th.Gap, 0)
		is := ui.NewStack(info, th.Gap)
		name := who.DisplayName
		if name == "" {
			name = who.Email
		}
		ui.Label(ctx, is.Next(line), name, ui.LabelStyle{})
		ui.Label(ctx, is.Next(line), who.Email, ui.LabelStyle{Color: th.TextMuted})
		role := who.Role
		if a.m.Auth != nil && a.m.Auth.Enabled && a.m.Auth.IssuerURL != "" {
			role += " · " + i18n.Get("account.sso")
		}
		badge := is.Next(line)
		badge.W = min(badge.W, ui.TextWidth(role, th.Small, th.Font)+2*th.Gap)
		ctx.Canvas.FillRounded(badge, th.Radius, th.SurfaceSelected)
		roleInk := th.Text
		if who.Role == "admin" {
			roleInk = th.Danger
		}
		ui.Label(ctx, badge, role, ui.LabelStyle{Scale: th.Small, Color: roleInk, Align: ui.AlignCenter, Middle: true})
		if local && button(s.Next(th.ControlHeight), "account-password-toggle", i18n.Get("account.changePassword"), p.loading || p.busy, intentNone, "") {
			p.passwordOpen = !p.passwordOpen
			p.current.SetValue("")
			p.next.SetValue("")
			p.confirmPassword.SetValue("")
			ctx.Repaint()
		}
		if p.passwordOpen {
			field(s, &p.current, i18n.Get("account.currentPassword"))
			field(s, &p.next, i18n.Get("account.newPassword"))
			field(s, &p.confirmPassword, i18n.Get("account.confirmPassword"))
			button(s.Next(th.ControlHeight), "account-password-save", i18n.Get("account.savePassword"), p.busy || p.current.Value() == "", intentAccountPassword, "")
		}
		namespaces := slices.Clone(who.Namespaces)
		if who.PersonalNamespace != "" && !slices.Contains(namespaces, who.PersonalNamespace) {
			namespaces = append([]string{who.PersonalNamespace}, namespaces...)
		}
		nsH := line + th.Gap + th.ControlHeight + th.Gap + textHeight(i18n.Get("account.accessibleNamespaces")) + th.Gap
		if who.PersonalNamespace != "" {
			nsH += textHeight(i18n.Sprintf("account.personalNamespace", who.PersonalNamespace)) + th.Gap
		}
		for _, ns := range namespaces {
			nsH += max(th.ControlHeight, textHeight(ns)) + th.Gap
		}
		s = card(i18n.Get("account.namespaces"), nsH)
		if who.PersonalNamespace != "" {
			label(s, i18n.Sprintf("account.personalNamespace", who.PersonalNamespace))
		}
		label(s, i18n.Get("account.accessibleNamespaces"))
		active := ""
		if a.profile != nil {
			active = a.profile.Namespace
		}
		nsButton := func(ns, title string) {
			if active == ns {
				title += " · " + i18n.Get("account.active")
			}
			button(s.Next(max(th.ControlHeight, textHeight(title))), ui.FocusID("account-namespace-"+ns), title, p.busy, intentAccountNamespace, ns)
		}
		nsButton("", i18n.Get("account.allNamespaces"))
		for _, ns := range namespaces {
			nsButton(ns, ns)
		}
	}
	keyDescription := i18n.Get("account.keysDescription")
	keyH := line + th.Gap + textHeight(keyDescription) + th.Gap + textHeight(i18n.Get("account.name")) + textHeight(i18n.Get("account.publicKey")) + 3*th.ControlHeight + 5*th.Gap
	if p.loading || len(p.keys) == 0 {
		keyH += textHeight(i18n.Get("account.noKeys")) + th.Gap
	}
	if p.keyErr != "" {
		keyH += textHeight(p.keyErr) + th.Gap
	}
	for _, k := range p.keys {
		keyH += 2*(small+th.Gap) + textHeight(i18n.Sprintf("account.added", k.CreatedAt)) + th.ControlHeight + th.Gap
	}
	s := card(i18n.Get("account.keys"), keyH)
	label(s, keyDescription)
	if p.keyErr != "" {
		label(s, p.keyErr)
	}
	if p.loading {
		label(s, i18n.Get("account.loadingKeys"))
	} else if len(p.keys) == 0 {
		label(s, i18n.Get("account.noKeys"))
	}
	for _, k := range p.keys {
		name := k.KeyName
		if name == "" {
			name = k.Name
		}
		ui.Label(ctx, s.Next(small), name+" · "+k.Fingerprint, ui.LabelStyle{Scale: th.Small})
		ui.Label(ctx, s.Next(small), k.PublicKey, ui.LabelStyle{Scale: th.Small, Color: th.TextMuted})
		label(s, i18n.Sprintf("account.added", k.CreatedAt))
		key := k.Namespace + "/" + k.Name
		button(s.Next(th.ControlHeight), ui.FocusID("account-delete-"+key), i18n.Get("account.delete"), p.busy || p.loading, intentAccountDeleteKey, key)
	}
	field(s, &p.name, i18n.Get("account.name"))
	field(s, &p.publicKey, i18n.Get("account.publicKey"))
	button(s.Next(th.ControlHeight), "account-add-key", i18n.Get("account.addKey"), p.busy || p.loading || accountKeySlug(p.name.Value()) == "" || strings.TrimSpace(p.publicKey.Value()) == "", intentAccountAddKey, "")
	if a.m.Auth != nil && a.m.Auth.Enabled {
		description := i18n.Get("account.devicesDescription")
		dh := line + th.Gap + textHeight(description) + th.Gap
		if p.deviceErr != "" {
			dh += textHeight(p.deviceErr) + th.Gap
		}
		if p.loading || len(p.devices) == 0 {
			dh += textHeight(i18n.Get("account.noDevices")) + th.Gap
		}
		for _, d := range p.devices {
			dh += textHeight(d.Name) + textHeight(i18n.Sprintf("account.created", time.Unix(d.CreatedAt, 0).Local().Format(time.DateTime))) + textHeight(i18n.Sprintf("account.expires", time.Unix(d.ExpiresAt, 0).Local().Format(time.DateTime))) + th.ControlHeight + 4*th.Gap
		}
		s = card(i18n.Get("account.devices"), dh)
		label(s, description)
		if p.deviceErr != "" {
			label(s, p.deviceErr)
		}
		if p.loading {
			label(s, i18n.Get("account.loadingDevices"))
		} else if len(p.devices) == 0 {
			label(s, i18n.Get("account.noDevices"))
		}
		for _, d := range p.devices {
			label(s, d.Name)
			label(s, i18n.Sprintf("account.created", time.Unix(d.CreatedAt, 0).Local().Format(time.DateTime)))
			label(s, i18n.Sprintf("account.expires", time.Unix(d.ExpiresAt, 0).Local().Format(time.DateTime)))
			r := s.Next(th.ControlHeight)
			if p.confirm == d.DeviceID {
				left, right := ui.CutLeft(r, r.W/2)
				button(left, ui.FocusID("account-confirm-"+d.DeviceID), i18n.Get("account.confirmRevoke"), p.busy, intentAccountRevoke, d.DeviceID)
				if button(right, ui.FocusID("account-cancel-"+d.DeviceID), i18n.Get("account.cancel"), p.busy, intentNone, "") {
					p.confirm = ""
					ctx.Focus().Register(ui.FocusID("account-revoke-" + d.DeviceID))
					ctx.Focus().Set(ui.FocusID("account-revoke-" + d.DeviceID))
					ctx.Repaint()
				}
			} else if button(r, ui.FocusID("account-revoke-"+d.DeviceID), i18n.Get("account.revoke"), p.busy, intentNone, "") {
				p.confirm = d.DeviceID
				ctx.Focus().Register(ui.FocusID("account-confirm-" + d.DeviceID))
				ctx.Focus().Set(ui.FocusID("account-confirm-" + d.DeviceID))
				ctx.Repaint()
			}
		}
	}
	if who != nil && len(who.Groups) > 0 {
		groups := strings.Join(who.Groups, " · ")
		s = card(i18n.Get("account.groups"), line+th.Gap+textHeight(groups))
		label(s, groups)
	}
	ctx.Canvas.PopClip()
	ctx.Input = input
	a.finishPreferenceScroll(view, y, &p.scroll, &p.focus, focused)
	if closePage || ctx.Input.KeyPressed(keysym.KeyEscape) {
		out = intent{kind: intentAccountDone}
	}
	return out
}
