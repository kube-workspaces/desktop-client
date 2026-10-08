// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/chrome"
	"github.com/kube-workspaces/desktop-client/internal/connection"
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

// sessionRecord is one held session: the workspace, which seat of it (a
// display, or a VM's serial/SSH console alongside the display), whether it
// is an observer, and the transport that outlives its windows. A display
// and its consoles are independent single-seat slots, so one workspace may
// hold all three at once under different keys.
type sessionRecord struct {
	ws       kwclient.Workspace
	terminal string
	observer bool
	handle   SessionHandle
}

// sessionKey names the held seat: the workspace key for a display, suffixed
// for a console seat. The switcher, the live map and the held map all key
// on it, which is what lets a display and its consoles stay open — and
// parked — side by side.
func sessionKey(ws kwclient.Workspace, seat string) string {
	if seat != "" {
		return ws.Key() + "#" + seat
	}
	return ws.Key()
}

// sessionEntry is the model's display copy of a held session.
func sessionEntryFor(rec *sessionRecord, open bool) SessionEntry {
	title := rec.ws.Key()
	kind := rec.handle.Kind()
	if rec.observer {
		title += i18n.Get("workspaces.observer")
	}
	if rec.terminal != "" {
		title += " — " + rec.terminal
	}
	return SessionEntry{Key: sessionKey(rec.ws, rec.terminal), Title: title, Kind: kind, Open: open}
}

// syncSessions rebuilds the model's session list from the held transports,
// in stable key order. Entries for live windows are marked open, so the
// switcher can focus them instead of resuming them.
func (a *App) syncSessions() {
	keys := make([]string, 0, len(a.sessions))
	for key := range a.sessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]SessionEntry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, sessionEntryFor(a.sessions[key], a.isLive(key)))
	}
	a.m.Sessions = entries
}

// closeSession releases one held session and forgets it. It is idempotent.
// An open window on the session goes with it, silently: the caller reports
// whatever the disconnect means (the switcher names it, teardown reports
// nothing).
func (a *App) closeSession(key string) {
	a.closeLiveWindow(key)
	if rec, ok := a.sessions[key]; ok {
		delete(a.sessions, key)
		rec.handle.Close()
		a.syncSessions()
	}
}

// closeAllSessions releases every held session and closes every open
// window. The shell calls it whenever the identity or the instance goes
// away — sign-out, profile switch, server change, process exit — so a held
// transport never outlives the session that authenticated it, and the
// server's single-seat slots are handed back. Windows go first so the
// guests are told their keys are up while the connections are still open.
func (a *App) closeAllSessions() {
	if a.opts.nativeWeb {
		closeWebChildren()
	}
	a.closeAllLiveWindows()
	a.closePopup()
	for key, rec := range a.sessions {
		delete(a.sessions, key)
		rec.handle.Close()
	}
	a.syncSessions()
}

// openLiveSession opens the opening workspace's session in a new window
// beside the shell: a display for a VM, an integrated terminal otherwise.
//
// The first open dials the transport; later opens of the same workspace
// resume the held one. Opening never blocks — the window registers with
// the pump and the shell stays on the list — so several sessions stay live
// side by side. A clean window close parks the session (it stays connected
// in the background); a mid-flight failure closes it and reports.
// Explicit disconnects happen in the sessions modal or the window's quit chord.
//
// It runs on the loop's goroutine, which is the goroutine that owns every
// window, which is what the viewer requires.
func (a *App) openLiveSession(ctx context.Context) error {
	ws := a.m.Opening
	observer := a.m.OpenObserver
	seat := a.m.OpenTerminal
	if a.dialer == nil {
		a.m.SessionEnded(errors.New("this build cannot open sessions"))
		return nil
	}

	key := sessionKey(ws, seat)
	if e, ok := a.live[key]; ok {
		if e.observer == observer {
			// Already open: focus instead of opening a second window on
			// the same transport.
			_ = e.window.Raise()
			a.m.SessionOpened(ws)
			return nil
		}
		// A display and an observer of the same workspace are different
		// sessions, not two windows on one: tear the live window down
		// before releasing the old transport below.
		a.closeLiveWindow(key)
	}
	rec := a.sessions[key]
	if rec == nil || rec.observer != observer {
		if rec != nil {
			// A display and an observer of the same workspace are different
			// sessions, not two windows on one: release the old before
			// dialling the new.
			a.closeSession(key)
		}
		a.logf("dialling a session to %s", key)
		var handle SessionHandle
		var err error
		if seat != "" {
			handle, err = a.dialer.DialTerminal(ctx, ws, TerminalDialOpts{
				Kind:      seat,
				SSHUser:   a.m.OpenSSHUser,
				SSHKeyPEM: a.m.OpenSSHKeyPEM,
			})
		} else {
			handle, err = a.dialer.Dial(ctx, ws, observer)
		}
		if err != nil {
			a.m.SessionEnded(err)
			return nil
		}
		rec = &sessionRecord{ws: ws, terminal: seat, observer: observer, handle: handle}
		a.sessions[key] = rec
		a.syncSessions()
	}

	a.logf("opening a session window to %s", key)
	w, err := rec.handle.Open(ctx)
	if err != nil {
		a.closeSession(key)
		a.m.SessionEnded(err)
		return nil
	}
	if ctx.Err() != nil {
		// The process is shutting down, not opening windows.
		w.Close()
		a.closeAllSessions()
		a.quit = true
		return nil
	}
	a.live[key] = &liveEntry{key: key, ws: ws, observer: observer, window: w}
	a.syncSessions()
	a.m.SessionOpened(ws)
	a.dirty = true
	// The workspace was just opened; the list underneath should show it as
	// soon as the pump gets there.
	a.nextRefresh = time.Time{}
	return nil
}

// viewerLiveWindow drives a detached RFB viewer from the pump.
type viewerLiveWindow struct {
	view *viewer.Viewer
}

// Step implements [liveWindow].
func (w *viewerLiveWindow) Step(_ context.Context, now time.Time, events []viewer.Event) error {
	return w.view.StepExternal(now, events)
}

// IdleWait implements [liveWindow].
func (w *viewerLiveWindow) IdleWait(now time.Time) time.Duration {
	return w.view.IdleWait(now)
}

// Closed implements [liveWindow].
func (w *viewerLiveWindow) Closed() bool { return w.view.DetachedClosed() }

// Result implements [liveWindow].
func (w *viewerLiveWindow) Result() error { return w.view.DetachedResult() }

func (w *viewerLiveWindow) CloseDisposition() connection.CloseDisposition {
	return w.view.CloseDisposition()
}

// Close implements [liveWindow].
func (w *viewerLiveWindow) Close() { w.view.CloseDetached() }

// WindowBackend implements [liveWindow].
func (w *viewerLiveWindow) WindowBackend() viewer.Backend { return w.view.WindowBackend() }

// Raise implements [liveWindow].
func (w *viewerLiveWindow) Raise() error { return w.view.WindowBackend().Raise() }

// ReleaseInput implements [liveWindow].
func (w *viewerLiveWindow) ReleaseInput() { w.view.ReleaseInput() }

// terminalLiveWindow drives a detached integrated terminal from the pump.
type terminalLiveWindow struct {
	det *terminal.Detached
}

// Step implements [liveWindow].
func (w *terminalLiveWindow) Step(_ context.Context, now time.Time, events []viewer.Event) error {
	w.det.Step(now, events)
	return nil
}

// IdleWait implements [liveWindow].
func (w *terminalLiveWindow) IdleWait(now time.Time) time.Duration {
	return w.det.IdleWait(now)
}

// Closed implements [liveWindow].
func (w *terminalLiveWindow) Closed() bool { return w.det.Closed() }

// Result implements [liveWindow]: a terminal has no terminal failure — a
// dropped bridge redials, a clean shell exit ends the window.
func (w *terminalLiveWindow) Result() error { return nil }

func (w *terminalLiveWindow) CloseDisposition() connection.CloseDisposition {
	return w.det.CloseDisposition()
}

// Close implements [liveWindow].
func (w *terminalLiveWindow) Close() { w.det.Close() }

// WindowBackend implements [liveWindow].
func (w *terminalLiveWindow) WindowBackend() viewer.Backend { return w.det.Backend() }

// Raise implements [liveWindow].
func (w *terminalLiveWindow) Raise() error { return w.det.Backend().Raise() }

// ReleaseInput implements [liveWindow]: the terminal sends bytes, not held
// keys, so there is nothing to release.
func (w *terminalLiveWindow) ReleaseInput() { w.det.ReleaseInput() }

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
	// AgentTier opts a VM workspace into the premium agent transport
	// (video/audio/resize via kw-agent-v1) instead of Selkies/RFB.
	// Explicit until the API reports transport truth: the client never
	// guesses agent availability from other capability data. The window
	// is observer-grade in v1 (no guest input injection yet); refusal,
	// busy seats and auth failures never fall back silently.
	AgentTier bool
	// AgentClipboard adds guest Unicode clipboard to the normal RFB console.
	// Explicit opt-in; requires serve --clipboard in the logged-in guest.
	AgentClipboard bool
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
	// Optional test seam; production always preflights native decoders.
	tier1Available func() error
}

func (d *sessionDialer) preflightTier1() error {
	if d.tier1Available != nil {
		return d.tier1Available()
	}
	return session.Tier1Available()
}

// NewSessionDialer builds the production [SessionDialer] over a concrete
// client. The concrete type is needed because sessions dial the WebSocket
// bridges through it, while the screens only ever see [API].
func NewSessionDialer(client *kwclient.Client, opts SessionOptions) SessionDialer {
	return &sessionDialer{client: client, opts: opts}
}

// Dial establishes the session's transport without opening any window. The
// window appears on the first Open; the transport survives window closes
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
			client: d.client,
			opts: TerminalOptions{
				Scale: 1, // the 5x8 bitmap at 1x (6x11 px cells); scale 2 reads too large
				Logf:  d.opts.Logf,
			},
			ws: ws,
		}, nil
	default:
		if d.opts.AgentTier {
			if err := d.preflightTier1(); err != nil {
				if d.opts.Logf != nil {
					d.opts.Logf("workspace %s requested agent premium but this build cannot decode it (%v); using Tier 0", ws.Key(), err)
				}
				return dialExclusiveSession(ctx, d.client, ws, d.opts)
			}
			if d.opts.Logf != nil {
				d.opts.Logf("workspace %s opening agent premium transport", ws.Key())
			}
			return &agentHandle{client: d.client, ws: ws, opts: d.opts}, nil
		}
		if ws.RemoteDesktop != nil && ws.RemoteDesktop.Protocol == "selkies" {
			// A process that cannot decode the pinned wire format has no Tier 1
			// to offer, and asking anyway costs the display: the claim is held
			// past the failure, the Tier 0 fallback below can collide with it,
			// and the user is prompted to take over their own session. The
			// transport is chosen here, before any window and any dial, so a
			// missing codec costs nothing but a slower stream.
			if err := d.preflightTier1(); err != nil {
				if d.opts.Logf != nil {
					d.opts.Logf("workspace %s advertises Selkies but this build cannot decode it (%v); using Tier 0", ws.Key(), err)
				}
				return dialExclusiveSession(ctx, d.client, ws, d.opts)
			}
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
	mu     sync.Mutex
	v      *viewer.Viewer
	events rfb.Config
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
	r.events = rfb.Config{}
	if v != nil {
		r.events = v.RFBConfig(rfb.Config{})
	}
}

func (r *viewRef) callbacks() rfb.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events
}

// rfbConfig routes every event to the current window, even if its RFB
// connection was dialled while an earlier/parked window owned the handle.
func (r *viewRef) rfbConfig(base rfb.Config) rfb.Config {
	cfg := base
	if v := r.get(); v != nil {
		cfg = v.RFBConfig(base)
	}
	cfg.OnFramebufferUpdate = func(fb *rfb.Framebuffer, damage []rfb.Rect) {
		if base.OnFramebufferUpdate != nil {
			base.OnFramebufferUpdate(fb, damage)
		}
		if cb := r.callbacks().OnFramebufferUpdate; cb != nil {
			cb(fb, damage)
		}
	}
	cfg.OnResize = func(w, h int) {
		if base.OnResize != nil {
			base.OnResize(w, h)
		}
		if cb := r.callbacks().OnResize; cb != nil {
			cb(w, h)
		}
	}
	cfg.OnCutText = func(text string) {
		if base.OnCutText != nil {
			base.OnCutText(text)
		}
		if cb := r.callbacks().OnCutText; cb != nil {
			cb(text)
		}
	}
	cfg.OnCursor = func(image []byte, w, h, hotX, hotY int) {
		if base.OnCursor != nil {
			base.OnCursor(image, w, h, hotX, hotY)
		}
		if cb := r.callbacks().OnCursor; cb != nil {
			cb(image, w, h, hotX, hotY)
		}
	}
	cfg.OnAudio = func(data []byte) {
		if base.OnAudio != nil {
			base.OnAudio(data)
		}
		if cb := r.callbacks().OnAudio; cb != nil {
			cb(data)
		}
	}
	return cfg
}

// exclusiveHandle is a held Tier 0 (RFB) display session: one supervised
// transport, any number of sequential windows over its lifetime.
type exclusiveHandle struct {
	client    *kwclient.Client
	ws        kwclient.Workspace
	opts      SessionOptions
	base      rfb.Config
	sess      *session.ReconnectingSession
	ref       *viewRef
	clipboard *session.AgentClipboard

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
			return h.ref.rfbConfig(h.base)
		},
	})
	if err != nil {
		return nil, err
	}
	h.sess = sess
	if opts.AgentClipboard {
		bridge, err := session.OpenAgentClipboard(ctx, client, ws.Namespace, ws.Name, func(text string) {
			if v := h.ref.get(); v != nil {
				v.GuestClipboard(text)
			}
		})
		if err != nil {
			_ = sess.Close()
			return nil, fmt.Errorf("open RFB clipboard helper: %w", err)
		}
		h.clipboard = bridge
	}
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
	view := viewer.New(chrome.New(viewer.NewSDLBackend()), cfg)
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

// openWindow builds a fresh window over the held transport for the pump to
// drive. Windows are cheap, transports are not: every Open gets one.
func (h *exclusiveHandle) openWindow(ctx context.Context) (liveWindow, error) {
	view := h.newView(ctx)
	h.ref.set(view)
	var source viewer.ConnSource = h.sess
	if h.clipboard != nil {
		source = session.WithAgentClipboard(source, h.clipboard)
	}
	if _, err := view.OpenDetached(ctx, source); err != nil {
		return nil, err
	}
	return &viewerLiveWindow{view: view}, nil
}

// Open implements [SessionHandle]: it returns a fresh live window over the
// held transport.
func (h *exclusiveHandle) Open(ctx context.Context) (liveWindow, error) {
	return h.openWindow(ctx)
}

// Close implements [SessionHandle].
func (h *exclusiveHandle) Close() {
	h.closeOnce.Do(func() {
		if h.clipboard != nil {
			h.clipboard.Close()
		}
		if h.sess != nil {
			_ = h.sess.Close()
		}
	})
}

// terminalHandle is a held integrated terminal: every Open redials fresh
// (the container /exec bridge is multi-session and a VM console seat is a
// single slot claimed per dial, so nothing needs holding past the window).
//
// A VM console seat — "serial" over /exec, "ssh" over /ssh — is a
// single-seat slot beside the display: the window checks the seat status
// before dialling and takes over only on explicit Enter consent, mirroring
// the web console. A dial that loses the race answers 409, which the window
// maps back onto the busy plate rather than stealing.
type terminalHandle struct {
	client *kwclient.Client
	opts   TerminalOptions
	ws     kwclient.Workspace
	kind   string
	// sshUser and sshKeyPEM authenticate the /ssh bridge. The key bytes
	// live in memory only, held for redials across window closes, and are
	// never written anywhere; dropping the handle forgets them.
	sshUser   string
	sshKeyPEM []byte
}

// Kind implements [SessionHandle].
func (h *terminalHandle) Kind() string {
	if h.kind != "" {
		return h.kind
	}
	return "terminal"
}

// DialTerminal implements [SessionDialer]: it opens a VM's serial or SSH
// console seat. Missing credentials fail fast here, in the workspace list,
// instead of in a blank window; the form validates the key file itself, so
// reaching this without one is a programming error, not user input.
func (d *sessionDialer) DialTerminal(_ context.Context, ws kwclient.Workspace, opts TerminalDialOpts) (SessionHandle, error) {
	if !ws.IsVM() {
		return nil, errors.New("console seats are only available for VM workspaces")
	}
	switch opts.Kind {
	case "serial":
		return &terminalHandle{client: d.client, ws: ws, kind: "serial",
			opts: TerminalOptions{Scale: 1, Logf: d.opts.Logf}}, nil
	case "ssh":
		if opts.SSHUser == "" {
			return nil, errors.New("ssh needs a username")
		}
		if len(opts.SSHKeyPEM) == 0 {
			return nil, errors.New("ssh needs a private key")
		}
		return &terminalHandle{client: d.client, ws: ws, kind: "ssh",
			sshUser: opts.SSHUser, sshKeyPEM: opts.SSHKeyPEM,
			opts: TerminalOptions{Scale: 1, Logf: d.opts.Logf}}, nil
	default:
		return nil, errors.New("unknown console seat")
	}
}

// Open implements [SessionHandle]: it returns a fresh live terminal window.
func (h *terminalHandle) Open(ctx context.Context) (liveWindow, error) {
	dial, check, take := h.bridge()
	title := h.ws.Key()
	if h.kind != "" {
		title += " — " + h.kind
	}
	det, err := terminal.OpenDetached(ctx, dial, terminal.Options{
		Surface:   connection.Surface(h.kind),
		Title:     title,
		Theme:     h.opts.Theme,
		Scale:     h.opts.Scale,
		Logf:      h.opts.Logf,
		CheckSeat: check,
		TakeSeat:  take,
	})
	if err != nil {
		return nil, err
	}
	return &terminalLiveWindow{det: det}, nil
}

// bridge wires a console seat to its status, takeover and dial. A container
// terminal (no kind) keeps the old behaviour: no seat, no consent.
func (h *terminalHandle) bridge() (terminal.Dial, func(ctx context.Context) (bool, error), func(ctx context.Context) error) {
	if h.kind == "" {
		return func(ctx context.Context, cols, rows uint16) (io.ReadWriteCloser, error) {
			conn, err := h.client.DialExec(ctx, h.ws.Namespace, h.ws.Name, cols, rows)
			if err != nil {
				return nil, err
			}
			return wsio.New(conn), nil
		}, nil, nil
	}
	status := h.client.ConsoleStatus
	take := h.client.ConsoleTakeover
	if h.kind == "ssh" {
		status = h.client.SSHStatus
		take = h.client.SSHTakeover
	}
	check := func(ctx context.Context) (bool, error) {
		st, err := status(ctx, h.ws.Namespace, h.ws.Name)
		if err != nil {
			return false, err
		}
		return st.InUse, nil
	}
	takeSeat := func(ctx context.Context) error {
		res, err := take(ctx, h.ws.Namespace, h.ws.Name)
		if err != nil {
			return err
		}
		if !res.OK {
			return errors.New(i18n.Get("session.takeoverDeclined"))
		}
		return nil
	}
	dial := func(ctx context.Context, cols, rows uint16) (io.ReadWriteCloser, error) {
		conn, err := h.connect(ctx, cols, rows)
		if err != nil {
			if errors.Is(err, kwclient.ErrSessionInUse) {
				return nil, terminal.ErrInUse
			}
			return nil, err
		}
		return wsio.New(conn), nil
	}
	return dial, check, takeSeat
}

// connect dials the seat's bridge: the serial stream over /exec, or the
// authenticated SSH session over /ssh.
func (h *terminalHandle) connect(ctx context.Context, cols, rows uint16) (*websocket.Conn, error) {
	if h.kind == "ssh" {
		return h.client.DialSSHWithAuth(ctx, h.ws.Namespace, h.ws.Name, cols, rows, h.sshUser, string(h.sshKeyPEM))
	}
	return h.client.DialExec(ctx, h.ws.Namespace, h.ws.Name, cols, rows)
}

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

// Kind implements [SessionHandle]. Once Tier 1 has fallen back the window
// on screen is a Tier 0 display, and the switcher names what the user sees.
func (h *tier1Handle) Kind() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fallback != nil {
		return "display"
	}
	return "tier1"
}

// Open implements [SessionHandle]: it returns a live Tier 1 window that
// falls back to Tier 0 in place on a recoverable failure, for the rest of
// the handle's life — once a tier is dropped it is not probed again
// mid-session (plan §7.2).
func (h *tier1Handle) Open(ctx context.Context) (liveWindow, error) {
	h.mu.Lock()
	fb := h.fallback
	h.mu.Unlock()
	if fb != nil {
		return fb.openWindow(ctx)
	}

	agentBase := ""
	if h.ws.RemoteDesktop != nil && h.ws.RemoteDesktop.Path != nil {
		agentBase = *h.ws.RemoteDesktop.Path
	}
	live, err := session.OpenTier1(ctx, h.client, h.ws.Namespace, h.ws.Name, agentBase,
		chrome.New(viewer.NewSDLBackend()), session.Tier1Config{
			Title:        h.ws.Key(),
			Audio:        true,
			ScaleQuality: h.opts.ScaleQuality,
			Logf:         h.opts.Logf,
		})
	if err != nil {
		return nil, err
	}
	return &tier1LiveWindow{handle: h, det: live.Detached}, nil
}

// tier1LiveWindow is the live window of a [tier1Handle]: a Tier 1 window
// that swaps itself for a Tier 0 window on a recoverable failure, without
// the pump ever seeing the seam. A refused agent, a rejected credential or
// a display owned elsewhere surfaces instead of being routed around.
type tier1LiveWindow struct {
	handle *tier1Handle
	det    *viewer.Tier1Detached
	fb     liveWindow

	closed      bool
	result      error
	disposition connection.CloseDisposition
}

// Step implements [liveWindow].
func (w *tier1LiveWindow) Step(ctx context.Context, now time.Time, events []viewer.Event) error {
	if w.fb != nil {
		return w.fb.Step(ctx, now, events)
	}
	if w.det == nil {
		return nil
	}
	if err := w.det.Step(now, events); err != nil {
		// A presenter failure (an upload the window cannot do) ends the
		// window, like a viewer step failure does.
		return err
	}
	if !w.det.Closed() {
		return nil
	}
	res := session.MapTier1Result(ctx, w.det.Result())
	w.disposition = w.det.CloseDisposition()
	w.det.Close()
	w.det = nil
	if res == nil || ctx.Err() != nil || errors.Is(res, session.ErrNoFallback) {
		w.closed, w.result = true, res
		return nil
	}
	if w.handle.opts.Logf != nil {
		w.handle.opts.Logf("Tier 1 to %s failed (%v); falling back to Tier 0", w.handle.ws.Key(), res)
	}
	fb, derr := dialExclusiveSession(ctx, w.handle.client, w.handle.ws, w.handle.opts)
	if derr != nil {
		w.closed, w.result = true, derr
		return nil
	}
	w.handle.mu.Lock()
	w.handle.fallback = fb
	w.handle.mu.Unlock()
	win, err := fb.openWindow(ctx)
	if err != nil {
		w.closed, w.result = true, err
		return nil
	}
	w.fb = win
	return nil
}

// IdleWait implements [liveWindow].
func (w *tier1LiveWindow) IdleWait(now time.Time) time.Duration {
	if w.fb != nil {
		return w.fb.IdleWait(now)
	}
	if w.det == nil {
		return 0
	}
	return w.det.IdleWait(now)
}

// Closed implements [liveWindow].
func (w *tier1LiveWindow) Closed() bool {
	if w.fb != nil {
		return w.fb.Closed()
	}
	return w.closed
}

// Result implements [liveWindow].
func (w *tier1LiveWindow) Result() error {
	if w.fb != nil {
		return w.fb.Result()
	}
	return w.result
}

func (w *tier1LiveWindow) CloseDisposition() connection.CloseDisposition {
	if w.fb != nil {
		return w.fb.CloseDisposition()
	}
	return w.disposition
}

// Close implements [liveWindow].
func (w *tier1LiveWindow) Close() {
	if w.det != nil {
		w.det.Close()
		w.det = nil
	}
	if w.fb != nil {
		w.fb.Close()
		w.fb = nil
	}
}

// WindowBackend implements [liveWindow].
func (w *tier1LiveWindow) WindowBackend() viewer.Backend {
	if w.fb != nil {
		return w.fb.WindowBackend()
	}
	if w.det == nil {
		return nil
	}
	return w.det.Backend()
}

// Raise implements [liveWindow].
func (w *tier1LiveWindow) Raise() error {
	be := w.WindowBackend()
	if be == nil {
		return nil
	}
	return be.Raise()
}

// ReleaseInput implements [liveWindow].
func (w *tier1LiveWindow) ReleaseInput() {
	if w.fb != nil {
		w.fb.ReleaseInput()
		return
	}
	if w.det != nil {
		w.det.ReleaseInput()
	}
}

// Close implements [SessionHandle].
func (h *tier1Handle) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fallback != nil {
		h.fallback.Close()
	}
}

// agentHandle is the explicit opt-in premium window (see
// SessionOptions.AgentTier): kw-agent-v1 video/audio/resize instead of
// Selkies/RFB. Observer-grade in v1 (no guest input injection yet).
// Recoverable transport failures fall back to Tier 0 in place;
// refusal, busy seats and auth failures surface instead.
type agentHandle struct {
	client *kwclient.Client
	ws     kwclient.Workspace
	opts   SessionOptions

	mu       sync.Mutex
	fallback *exclusiveHandle
}

// Kind implements [SessionHandle].
func (h *agentHandle) Kind() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fallback != nil {
		return "display"
	}
	return "agent"
}

// Open implements [SessionHandle].
func (h *agentHandle) Open(ctx context.Context) (liveWindow, error) {
	h.mu.Lock()
	fb := h.fallback
	h.mu.Unlock()
	if fb != nil {
		return fb.openWindow(ctx)
	}

	live, err := session.OpenAgentPremium(ctx, h.client, h.ws.Namespace, h.ws.Name, "",
		chrome.New(viewer.NewSDLBackend()), session.Tier1Config{
			Title:        h.ws.Key(),
			Audio:        true,
			ScaleQuality: h.opts.ScaleQuality,
			Logf:         h.opts.Logf,
		})
	if err != nil {
		return nil, err
	}
	return &agentLiveWindow{handle: h, det: live.Detached}, nil
}

// agentLiveWindow is the live window of an [agentHandle]: a premium window
// that swaps itself for a Tier 0 window on a recoverable failure. A refused
// ticket, a rejected credential or a busy seat surfaces instead of being
// routed around.
type agentLiveWindow struct {
	handle *agentHandle
	det    *viewer.Tier1Detached
	fb     liveWindow

	closed      bool
	result      error
	disposition connection.CloseDisposition
}

// Step implements [liveWindow].
func (w *agentLiveWindow) Step(ctx context.Context, now time.Time, events []viewer.Event) error {
	if w.fb != nil {
		return w.fb.Step(ctx, now, events)
	}
	if w.det == nil {
		return nil
	}
	if err := w.det.Step(now, events); err != nil {
		return err
	}
	if !w.det.Closed() {
		return nil
	}
	res := session.MapAgentResult(ctx, w.det.Result())
	w.disposition = w.det.CloseDisposition()
	w.det.Close()
	w.det = nil
	if res == nil || ctx.Err() != nil || errors.Is(res, session.ErrNoFallback) {
		w.closed, w.result = true, res
		return nil
	}
	if w.handle.opts.Logf != nil {
		w.handle.opts.Logf("agent premium to %s failed (%v); falling back to Tier 0", w.handle.ws.Key(), res)
	}
	fb, derr := dialExclusiveSession(ctx, w.handle.client, w.handle.ws, w.handle.opts)
	if derr != nil {
		w.closed, w.result = true, derr
		return nil
	}
	w.handle.mu.Lock()
	w.handle.fallback = fb
	w.handle.mu.Unlock()
	win, err := fb.openWindow(ctx)
	if err != nil {
		w.closed, w.result = true, err
		return nil
	}
	w.fb = win
	return nil
}

// IdleWait implements [liveWindow].
func (w *agentLiveWindow) IdleWait(now time.Time) time.Duration {
	if w.fb != nil {
		return w.fb.IdleWait(now)
	}
	if w.det == nil {
		return 0
	}
	return w.det.IdleWait(now)
}

// Closed implements [liveWindow].
func (w *agentLiveWindow) Closed() bool {
	if w.fb != nil {
		return w.fb.Closed()
	}
	return w.closed
}

// Result implements [liveWindow].
func (w *agentLiveWindow) Result() error {
	if w.fb != nil {
		return w.fb.Result()
	}
	return w.result
}

func (w *agentLiveWindow) CloseDisposition() connection.CloseDisposition {
	if w.fb != nil {
		return w.fb.CloseDisposition()
	}
	return w.disposition
}

// Close implements [liveWindow].
func (w *agentLiveWindow) Close() {
	if w.det != nil {
		w.det.Close()
		w.det = nil
	}
	if w.fb != nil {
		w.fb.Close()
		w.fb = nil
	}
}

// WindowBackend implements [liveWindow].
func (w *agentLiveWindow) WindowBackend() viewer.Backend {
	if w.fb != nil {
		return w.fb.WindowBackend()
	}
	if w.det == nil {
		return nil
	}
	return w.det.Backend()
}

// Raise implements [liveWindow].
func (w *agentLiveWindow) Raise() error {
	be := w.WindowBackend()
	if be == nil {
		return nil
	}
	return be.Raise()
}

// ReleaseInput implements [liveWindow].
func (w *agentLiveWindow) ReleaseInput() {
	if w.fb != nil {
		w.fb.ReleaseInput()
		return
	}
	if w.det != nil {
		w.det.ReleaseInput()
	}
}

// Close implements [SessionHandle].
func (h *agentHandle) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fallback != nil {
		h.fallback.Close()
	}
}

// observerHandle is a held shared-display observer session: one membership,
// one supervised stream, any number of sequential windows. The membership
// survives parked window closes by design (Disconnect leaves the membership),
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

// Open implements [SessionHandle]: it returns a fresh live read-only
// window over the held membership.
func (h *observerHandle) Open(ctx context.Context) (liveWindow, error) {
	view := viewer.New(chrome.New(viewer.NewSDLBackend()), viewer.Config{
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

	if _, err := view.OpenDetached(ctx, h.sess); err != nil {
		return nil, err
	}
	// The membership poll applies remote role changes to the live window.
	// It stops with the window, not with the membership: the participant
	// survives window closes by design.
	pollCtx, stopPoll := context.WithCancel(context.Background())
	go ctl.watch(pollCtx)
	return &observerLiveWindow{
		viewerLiveWindow: viewerLiveWindow{view: view},
		stopWatch:        stopPoll,
	}, nil
}

// observerLiveWindow is a viewer window with a membership poll to stop: the
// poll belongs to the window's lifetime, the membership to the handle's.
type observerLiveWindow struct {
	viewerLiveWindow
	stopWatch context.CancelFunc
}

// Close implements [liveWindow].
func (w *observerLiveWindow) Close() {
	w.stopWatch()
	w.viewerLiveWindow.Close()
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
