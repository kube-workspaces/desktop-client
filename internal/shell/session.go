// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/reconnect"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
	"github.com/kube-workspaces/desktop-client/internal/selkies"
	"github.com/kube-workspaces/desktop-client/internal/session"
	"github.com/kube-workspaces/desktop-client/internal/terminal"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

// Control is a [viewer.Tier1Input]: the run of every input method it needs
// exists on the adapter with exactly the window-facing signatures.
var _ viewer.Tier1Input = (*selkies.Control)(nil)

// sessionRecord is one held session: the workspace, whether it is an
// observer, and the transport that outlives its windows.
type sessionRecord struct {
	ws       kwclient.Workspace
	observer bool
	handle   SessionHandle
}

// sessionEntry is the model's display copy of a held session.
func sessionEntryFor(rec *sessionRecord) SessionEntry {
	title := rec.ws.Key()
	kind := rec.handle.Kind()
	if rec.observer {
		title += i18n.Get("workspaces.observer")
	}
	return SessionEntry{Key: rec.ws.Key(), Title: title, Kind: kind}
}

// syncSessions rebuilds the model's session list from the held transports,
// in stable key order.
func (a *App) syncSessions() {
	keys := make([]string, 0, len(a.sessions))
	for key := range a.sessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]SessionEntry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, sessionEntryFor(a.sessions[key]))
	}
	a.m.Sessions = entries
}

// closeSession releases one held session and forgets it. It is idempotent.
func (a *App) closeSession(key string) {
	if rec, ok := a.sessions[key]; ok {
		delete(a.sessions, key)
		rec.handle.Close()
		a.syncSessions()
	}
}

// closeAllSessions releases every held session. The shell calls it whenever
// the identity or the instance goes away — sign-out, profile switch, server
// change, process exit — so a held transport never outlives the session that
// authenticated it, and the server's single-seat slots are handed back.
func (a *App) closeAllSessions() {
	for key, rec := range a.sessions {
		delete(a.sessions, key)
		rec.handle.Close()
	}
	a.syncSessions()
}

// runSession attaches the opening workspace's session and blocks until its
// window closes: a display for a VM, an integrated terminal otherwise.
//
// The first open dials the transport; later opens of the same workspace
// resume the held one. A clean window close parks the session — it stays
// connected in the background and the shell returns to the list — while a
// mid-flight failure closes it and reports. Explicit disconnects happen in
// the sessions modal.
//
// It runs on the loop's goroutine, which is the goroutine that owns the
// window, which is what the viewer requires.
func (a *App) runSession(ctx context.Context) error {
	ws := a.m.Opening
	observer := a.m.OpenObserver
	if a.dialer == nil {
		a.afterSession()
		a.m.SessionEnded(errors.New("this build cannot open sessions"))
		return nil
	}

	key := ws.Key()
	rec := a.sessions[key]
	if rec == nil || rec.observer != observer {
		if rec != nil {
			// A display and an observer of the same workspace are different
			// sessions, not two windows on one: release the old before
			// dialling the new.
			a.closeSession(key)
		}
		a.logf("dialling a session to %s", key)
		handle, err := a.dialer.Dial(ctx, ws, observer)
		if err != nil {
			a.afterSession()
			a.m.SessionEnded(err)
			return nil
		}
		rec = &sessionRecord{ws: ws, observer: observer, handle: handle}
		a.sessions[key] = rec
		a.syncSessions()
	}

	a.logf("attaching a session window to %s", key)
	err := rec.handle.Attach(ctx)
	a.afterSession()

	if ctx.Err() != nil {
		// The process is shutting down, not returning to the list.
		a.closeAllSessions()
		a.quit = true
		return nil
	}
	if err != nil {
		a.closeSession(key)
		a.m.SessionEnded(err)
		return nil
	}
	a.m.SessionParked(ws)
	return nil
}

// afterSession cleans up the shell's state after a session ends.
//
// The event queue holds whatever happened to the shell's window while the
// session was live, and none of it should be acted on. And the input state
// describes a pointer and a set of modifiers from a window the user was not
// interacting with.
func (a *App) afterSession() {
	a.events = a.be.PollEvents(a.events[:0])
	a.events = a.events[:0]
	a.in = ui.Input{Focused: true}
	a.ctx.Focus().Clear()
	a.lastState = State(-1)
	// Refresh as soon as the list is back: the workspace was just used, and
	// whatever else happened in the meantime should be visible immediately.
	a.nextRefresh = time.Time{}
	a.dirty = true
}

// SessionOptions tunes the display sessions the shell opens.
type SessionOptions struct {
	// FixedQuality disables adaptive RFB tuning and keeps Quality/Compress
	// unchanged throughout the session. The default is automatic quality.
	FixedQuality bool
	// Quality is the JPEG quality level 0-9 requested from the server, or -1
	// to omit the pseudo-encoding. QEMU sends no JPEG at all without one.
	Quality int
	// Compress is the zlib compression level 0-9, or -1 to omit it.
	Compress int
	// UpdateInterval paces framebuffer update requests. Zero means the
	// session package's default.
	UpdateInterval time.Duration
	// ScaleQuality selects the scaling filter for the guest image.
	ScaleQuality viewer.ScaleQuality
	// Logf, if set, receives session diagnostics.
	Logf func(format string, args ...any)
}

// sessionDialer is the production [SessionDialer]: it opens the workspace in
// the client's own window. A VM gets its supervised RFB display session (via
// Tier 1 when the image advertises Selkies); any other workspace gets the
// integrated terminal over the /exec bridge.
type sessionDialer struct {
	client *kwclient.Client
	opts   SessionOptions
}

// NewSessionDialer builds the production [SessionDialer] over a concrete
// client. The concrete type is needed because sessions dial the WebSocket
// bridges through it, while the screens only ever see [API].
func NewSessionDialer(client *kwclient.Client, opts SessionOptions) SessionDialer {
	return &sessionDialer{client: client, opts: opts}
}

// Dial establishes the session's transport without opening any window. The
// window appears on the first Attach; the transport survives window closes
// until Close, which is what makes several sessions concurrent.
func (d *sessionDialer) Dial(ctx context.Context, ws kwclient.Workspace, observer bool) (SessionHandle, error) {
	switch {
	case observer:
		if !ws.IsVM() {
			return nil, errors.New("only VM workspaces can be observed")
		}
		return dialObserverSession(ctx, d.client, ws, d.opts)
	case !ws.IsVM():
		return &terminalHandle{
			run: TerminalConnector(d.client, TerminalOptions{
				Scale: 1, // the 5x8 bitmap at 1x (6x11 px cells); scale 2 reads too large
				Logf:  d.opts.Logf,
			}),
			ws: ws,
		}, nil
	default:
		if ws.RemoteDesktop != nil && ws.RemoteDesktop.Protocol == "selkies" {
			if d.opts.Logf != nil {
				d.opts.Logf("workspace %s advertises Selkies Tier 1 transport", ws.Key())
			}
			return &tier1Handle{client: d.client, ws: ws, opts: d.opts}, nil
		}
		return dialExclusiveSession(ctx, d.client, ws, d.opts)
	}
}

// viewRef is the viewer the supervisor callbacks talk to. The handle swaps it
// on every Attach, so redials and status updates after a resume reach the
// live window instead of the parked one.
type viewRef struct {
	mu sync.Mutex
	v  *viewer.Viewer
}

func (r *viewRef) get() *viewer.Viewer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.v
}

func (r *viewRef) set(v *viewer.Viewer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.v = v
}

// exclusiveHandle is a held Tier 0 (RFB) display session: one supervised
// transport, any number of sequential windows over its lifetime.
type exclusiveHandle struct {
	client *kwclient.Client
	ws     kwclient.Workspace
	opts   SessionOptions
	base   rfb.Config
	sess   *session.ReconnectingSession
	ref    *viewRef

	closeOnce sync.Once
}

// Kind implements [SessionHandle].
func (h *exclusiveHandle) Kind() string { return "display" }

// dialExclusiveSession dials the supervised RFB display session for a VM
// workspace. Closing the returned handle releases the server's single display
// slot; merely returning from Attach does not.
func dialExclusiveSession(ctx context.Context, client *kwclient.Client, ws kwclient.Workspace, opts SessionOptions) (*exclusiveHandle, error) {
	h := &exclusiveHandle{client: client, ws: ws, opts: opts, ref: &viewRef{}}

	encodings := append([]rfb.Encoding(nil), rfb.DefaultEncodings...)
	if opts.Quality >= 0 {
		encodings = append(encodings, rfb.QualityLevel(opts.Quality))
	}
	if opts.Compress >= 0 {
		encodings = append(encodings, rfb.CompressLevel(opts.Compress))
	}
	h.base = rfb.Config{Encodings: encodings}

	// The first window exists from the dial: its audio capability shapes the
	// handshake, and its status overlay shows the dial itself.
	first := h.newView(ctx)
	h.ref.set(first)
	if fm := first.AudioFormat(); fm != nil {
		h.base.AudioFormat = fm
	}
	sess, err := session.DialReconnecting(ctx, client, ws.Namespace, ws.Name, h.base, session.Options{
		Policy:         reconnect.Default(),
		UpdateInterval: opts.UpdateInterval,
		OnState: func(state session.State, err error) {
			v := h.ref.get()
			if v == nil {
				return
			}
			if status, detail, ok := viewerStatus(state, err); ok {
				v.SetStatus(status, detail)
			}
		},
		// The RFB handshake reads its config once, when the connection is
		// created, so the callbacks cannot be swapped later; the supervisor
		// calls this immediately before each dial, and it always answers
		// from the live window.
		Config: func() rfb.Config {
			if v := h.ref.get(); v != nil {
				return v.RFBConfig(h.base)
			}
			return h.base
		},
	})
	if err != nil {
		return nil, err
	}
	h.sess = sess
	return h, nil
}

// newView builds a fresh window for the session, wired to this handle. Every
// Attach gets one: windows are cheap, transports are not.
func (h *exclusiveHandle) newView(ctx context.Context) *viewer.Viewer {
	cfg := viewer.Config{
		AdaptiveQuality: !h.opts.FixedQuality,
		Title:           h.ws.Key(),
		ScaleQuality:    h.opts.ScaleQuality,
		Logf:            h.opts.Logf,
	}
	view := viewer.New(viewer.NewSDLBackend(), cfg)
	// Match the CLI consent flow, including after Tier 1 falls back.
	view.SetTakeoverHandler(func() error {
		res, err := h.client.VNCTakeover(ctx, h.ws.Namespace, h.ws.Name)
		if err != nil {
			return err
		}
		if !res.OK {
			return errors.New(i18n.Get("session.takeoverDeclined"))
		}
		h.sess.RetryNow()
		return nil
	})
	return view
}

// Attach implements [SessionHandle]: it runs a fresh window over the held
// transport and returns when the window closes.
func (h *exclusiveHandle) Attach(ctx context.Context) error {
	view := h.newView(ctx)
	h.ref.set(view)
	return view.Run(ctx, h.sess)
}

// Close implements [SessionHandle].
func (h *exclusiveHandle) Close() {
	h.closeOnce.Do(func() {
		if h.sess != nil {
			_ = h.sess.Close()
		}
	})
}

// terminalHandle is a held integrated terminal: every Attach redials the
// /exec bridge fresh (the bridge is multi-session, so nothing needs holding).
// Scrollback does not survive a switch; the shell underneath does.
type terminalHandle struct {
	run Connector
	ws  kwclient.Workspace
}

// Kind implements [SessionHandle].
func (h *terminalHandle) Kind() string { return "terminal" }

// Attach implements [SessionHandle].
func (h *terminalHandle) Attach(ctx context.Context) error { return h.run(ctx, h.ws) }

// Close implements [SessionHandle]: nothing is held past the window.
func (h *terminalHandle) Close() {}

// tier1Handle is a held Tier 1 (Selkies) session. Like the terminal it
// re-establishes per Attach; unlike the terminal it falls back to Tier 0 on
// a recoverable failure, for the rest of the handle's life — once a tier is
// dropped it is not probed again mid-session (plan §7.2).
type tier1Handle struct {
	client *kwclient.Client
	ws     kwclient.Workspace
	opts   SessionOptions

	mu       sync.Mutex
	fallback *exclusiveHandle
}

// Kind implements [SessionHandle].
func (h *tier1Handle) Kind() string { return "tier1" }

// Attach implements [SessionHandle].
func (h *tier1Handle) Attach(ctx context.Context) error {
	h.mu.Lock()
	fb := h.fallback
	h.mu.Unlock()
	if fb != nil {
		return fb.Attach(ctx)
	}

	agentBase := ""
	if h.ws.RemoteDesktop != nil && h.ws.RemoteDesktop.Path != nil {
		agentBase = *h.ws.RemoteDesktop.Path
	}
	err := session.RunTier1(ctx, h.client, h.ws.Namespace, h.ws.Name, agentBase,
		viewer.NewSDLBackend(), session.Tier1Config{
			Title:        h.ws.Key(),
			Audio:        true,
			ScaleQuality: h.opts.ScaleQuality,
			Logf:         h.opts.Logf,
		})
	if err == nil || ctx.Err() != nil {
		return err
	}
	if errors.Is(err, session.ErrNoFallback) {
		// A refused agent, a rejected credential or a display owned
		// elsewhere must surface, not be routed around.
		return err
	}
	// Recoverable: dial, negotiation, startup or decode failed. Fall back
	// for the rest of this handle's life.
	if h.opts.Logf != nil {
		h.opts.Logf("Tier 1 to %s failed (%v); falling back to Tier 0", h.ws.Key(), err)
	}
	fb, derr := dialExclusiveSession(ctx, h.client, h.ws, h.opts)
	if derr != nil {
		return derr
	}
	h.mu.Lock()
	h.fallback = fb
	h.mu.Unlock()
	return fb.Attach(ctx)
}

// Close implements [SessionHandle].
func (h *tier1Handle) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fallback != nil {
		h.fallback.Close()
	}
}

// observerHandle is a held shared-display observer session: one membership,
// one supervised stream, any number of sequential windows. The membership
// survives window closes by design (a disconnect keeps the participant id),
// so a resume re-attaches the same participant instead of re-joining.
type observerHandle struct {
	client        API
	ws            kwclient.Workspace
	opts          SessionOptions
	participantID string
	sess          *session.SharedDisplay
	ref           *viewRef

	closeOnce sync.Once
}

// Kind implements [SessionHandle].
func (h *observerHandle) Kind() string { return "observer" }

// dialObserverSession joins a shared display session as an observer and
// supervises the stream. The window appears on Attach; leaving the membership
// happens on Close, not when a window closes.
func dialObserverSession(ctx context.Context, client API, ws kwclient.Workspace, opts SessionOptions) (*observerHandle, error) {
	// The shared display ships opt-in: check the capability advert before
	// joining so a gated platform answers with its own message rather than a
	// bare join failure.
	cap, err := client.Display(ctx, ws.Namespace, ws.Name)
	if err != nil {
		return nil, err
	}
	if !cap.Enabled {
		return nil, errors.New(i18n.Get("session.sharedDisabled"))
	}

	join, err := client.JoinDisplay(ctx, ws.Namespace, ws.Name, kwclient.DisplayRoleObserver)
	if err != nil {
		return nil, err
	}
	h := &observerHandle{client: client, ws: ws, opts: opts, participantID: join.Participant.ID, ref: &viewRef{}}

	// The broker serves the canonical framebuffer as Raw and treats each
	// participant's encodings as its own negotiation, so the
	// quality/compress pseudo-encodings the exclusive path threads through
	// SessionOptions mean nothing here. The default list already advertises
	// every encoding the client can decode for the day the broker gains one.
	base := rfb.Config{Encodings: append([]rfb.Encoding(nil), rfb.DefaultEncodings...)}
	sess, err := session.DialSharedDisplay(ctx, session.SharedDisplayOptions{
		Policy:         reconnect.Default(),
		UpdateInterval: opts.UpdateInterval,
		OnState: func(state session.State, err error) {
			v := h.ref.get()
			if v == nil {
				return
			}
			if status, detail, ok := viewerStatus(state, err); ok {
				v.SetStatus(status, detail)
			}
		},
		// The RFB handshake reads its config once, at connect time, so the
		// live window's callbacks are installed per generation, like the
		// exclusive-session path.
		Config: func() rfb.Config {
			if v := h.ref.get(); v != nil {
				return v.RFBConfig(base)
			}
			return base
		},
		Dial: func(dialCtx context.Context, cfg rfb.Config, role string, force bool) (session.Link, error) {
			conn, err := client.DialDisplayWS(dialCtx, ws.Namespace, ws.Name, h.participantID, role, force)
			if err != nil {
				return nil, err
			}
			return session.SharedLink(conn, cfg)
		},
	})
	if err != nil {
		leaveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.LeaveDisplay(leaveCtx, ws.Namespace, ws.Name, h.participantID)
		return nil, err
	}
	h.sess = sess
	return h, nil
}

// Attach implements [SessionHandle]: it runs a fresh read-only window over
// the held membership and returns when the window closes.
func (h *observerHandle) Attach(ctx context.Context) error {
	view := viewer.New(viewer.NewSDLBackend(), viewer.Config{
		AdaptiveQuality: !h.opts.FixedQuality,
		Title:           h.ws.Key() + i18n.Get("workspaces.observer"),
		ReadOnly:        true,
		ControlRune:     'c',
		ScaleQuality:    h.opts.ScaleQuality,
		Logf:            h.opts.Logf,
	})
	h.ref.set(view)

	ctl := &sharedControl{client: h.client, ws: h.ws, participantID: h.participantID, sess: h.sess, view: view}
	view.SetControlHandler(ctl.toggle)

	pollCtx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()
	go ctl.watch(pollCtx)

	return view.Run(ctx, h.sess)
}

// Close implements [SessionHandle]: it leaves the membership and closes the
// stream.
func (h *observerHandle) Close() {
	h.closeOnce.Do(func() {
		leaveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = h.client.LeaveDisplay(leaveCtx, h.ws.Namespace, h.ws.Name, h.participantID)
		if h.sess != nil {
			_ = h.sess.Close()
		}
	})
}

// sharedControl owns the control-transfer UX of one shared display window:
// the Ctrl+Alt+C binding, the Enter-to-take-over confirmation and the
// membership poll that applies remote role changes. Its methods run on the
// viewer's handler goroutine and on the poll goroutine, serialised by mu.
type sharedControl struct {
	client        API
	ws            kwclient.Workspace
	participantID string
	sess          *session.SharedDisplay
	view          *viewer.Viewer

	mu        sync.Mutex
	prompting bool
}

// sharedControlREST bounds one membership/control call. The window stays
// responsive while a request is in flight because the viewer fires the
// binding on its own goroutine.
const sharedControlREST = 10 * time.Second

// sharedControlPoll is how often the registry is asked for the
// authoritative role. It matches the browser screen's cadence.
const sharedControlPoll = 5 * time.Second

// toggle is the Ctrl+Alt+C binding: with a prompt up it backs out of it,
// as an observer it asks for control, as the controller it lets go.
func (c *sharedControl) toggle() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.prompting {
		c.clearPromptLocked()
		return
	}
	if c.sess.Role() == kwclient.DisplayRoleObserver {
		c.requestLocked(false)
		return
	}
	c.releaseLocked()
}

// requestLocked asks the registry for control. A display that is already
// controlled earns the take-over confirmation instead of a silent steal.
func (c *sharedControl) requestLocked(force bool) {
	ctx, cancel := context.WithTimeout(context.Background(), sharedControlREST)
	defer cancel()
	if _, err := c.client.AcquireDisplayControl(ctx, c.ws.Namespace, c.ws.Name, c.participantID, force); err != nil {
		if errors.Is(err, kwclient.ErrControllerPresent) && !force {
			c.prompting = true
			c.view.SetTakeoverHandler(c.takeover)
			c.view.SetStatus(viewer.StatusDisplayInUse, i18n.Get("session.observerHint"))
			return
		}
		c.failLocked(i18n.Get("session.controlFail"), err)
		return
	}
	c.clearPromptLocked()
	c.applyRoleLocked(kwclient.DisplayRoleController, force)
}

// takeover is the Enter key on the confirmation prompt: force-acquire, then
// re-attach as controller with the takeover flag.
func (c *sharedControl) takeover() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), sharedControlREST)
	defer cancel()
	if _, err := c.client.AcquireDisplayControl(ctx, c.ws.Namespace, c.ws.Name, c.participantID, true); err != nil {
		// The prompt stays armed: Enter retries, Ctrl+Alt+C backs out.
		return err
	}
	c.clearPromptLocked()
	c.applyRoleLocked(kwclient.DisplayRoleController, true)
	return nil
}

// releaseLocked hands control back; the window keeps the stream as an
// observer.
func (c *sharedControl) releaseLocked() {
	ctx, cancel := context.WithTimeout(context.Background(), sharedControlREST)
	defer cancel()
	if _, err := c.client.ReleaseDisplayControl(ctx, c.ws.Namespace, c.ws.Name, c.participantID); err != nil {
		c.failLocked(i18n.Get("session.releaseFail"), err)
		return
	}
	c.applyRoleLocked(kwclient.DisplayRoleObserver, false)
}

// applyRoleLocked moves the window and the supervised stream to a role.
func (c *sharedControl) applyRoleLocked(role string, force bool) {
	c.view.SetReadOnly(role == kwclient.DisplayRoleObserver)
	c.view.SetTitle(c.ws.Key() + " (" + role + ")")
	c.sess.SetRole(role, force)
}

// failLocked shows a control failure over the live stream until the user
// dismisses it with the same binding that raised it.
func (c *sharedControl) failLocked(what string, err error) {
	c.prompting = true
	c.view.SetTakeoverHandler(nil)
	c.view.SetStatus(viewer.StatusDisplayInUse, what+": "+err.Error())
}

// clearPromptLocked drops any prompt and puts the stream back in front. A
// status that is not the prompt — a reconnect underneath it, say — belongs
// to the supervisor and stays.
func (c *sharedControl) clearPromptLocked() {
	c.prompting = false
	c.view.SetTakeoverHandler(nil)
	if status, _ := c.view.Status(); status == viewer.StatusDisplayInUse {
		c.view.SetStatus(viewer.StatusLive, "")
	}
}

// watch polls the registry for the authoritative role. A take-over by
// another participant demotes us, a transfer promotes us; either way the
// stream re-attaches with the registry's role. A poll that fails keeps the
// last known state, and a membership that vanished is left alone — the
// stream is still attached and the next transition will sort it out.
func (c *sharedControl) watch(ctx context.Context) {
	ticker := time.NewTicker(sharedControlPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			st, err := c.client.DisplayStatus(ctx, c.ws.Namespace, c.ws.Name)
			if err != nil {
				continue
			}
			role := ""
			if st.Controller != nil && st.Controller.ID == c.participantID {
				role = kwclient.DisplayRoleController
			} else {
				for _, obs := range st.Observers {
					if obs.ID == c.participantID {
						role = kwclient.DisplayRoleObserver
						break
					}
				}
			}
			if role == "" {
				continue
			}
			c.mu.Lock()
			if role != c.sess.Role() {
				c.clearPromptLocked()
				c.applyRoleLocked(role, false)
			}
			c.mu.Unlock()
		}
	}
}

// TerminalOptions tunes the integrated terminal sessions the shell opens.
type TerminalOptions struct {
	// Theme is the palette for the terminal window; nil means the terminal
	// package's dark default.
	Theme *ui.Theme
	// Scale is the integer grid scale; zero means the theme's Body scale.
	// 1 gives 6x11 px cells, 2 gives 12x22 px cells.
	Scale int
	// Logf, if set, receives terminal diagnostics.
	Logf func(format string, args ...any)
}

// TerminalConnector returns the production [Connector] for non-VM workspaces:
// it dials the /exec bridge and presents it in an integrated terminal window.
//
// The terminal owns reconnecting: [terminal.Run] redials DialExec with backoff
// after a transport failure, and returns nil when the shell exits cleanly,
// the user quits, or the session is torn down.
func TerminalConnector(client *kwclient.Client, opts TerminalOptions) Connector {
	return func(ctx context.Context, ws kwclient.Workspace) error {
		dial := func(ctx context.Context, cols, rows uint16) (io.ReadWriteCloser, error) {
			conn, err := client.DialExec(ctx, ws.Namespace, ws.Name, cols, rows)
			if err != nil {
				return nil, err
			}
			return wsio.New(conn), nil
		}
		return terminal.Run(ctx, dial, terminal.Options{
			Title: ws.Key(),
			Theme: opts.Theme,
			Scale: opts.Scale,
			Logf:  opts.Logf,
		})
	}
}

// viewerStatus maps a supervisor state onto what the window should say.
//
// It is a copy of the one in cmd/kube-workspaces/connect.go, and deliberately
// so: internal/viewer must not depend on internal/session (a different
// supervisor has to be able to drive the same overlay), which means somebody
// has to own the mapping, and each of the two front ends owning its own is
// cheaper than a third package existing to hold six lines.
func viewerStatus(state session.State, err error) (viewer.Status, string, bool) {
	switch state {
	case session.StateConnecting:
		return viewer.StatusConnecting, "", true
	case session.StateConnected:
		return viewer.StatusLive, "", true
	case session.StateReconnecting:
		return viewer.StatusReconnecting, reasonOrClosed(err), true
	case session.StateDisplayInUse:
		return viewer.StatusDisplayInUse, "", true
	case session.StateFailed:
		return viewer.StatusFailed, reasonOrClosed(err), true
	default:
		// StateClosed is the session shutting down, which happens when the
		// window is already going back to the shell.
		return viewer.StatusLive, "", false
	}
}

func reasonOrClosed(err error) string {
	if err == nil {
		return "closed by the server"
	}
	return err.Error()
}
