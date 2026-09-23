// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"io"
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

// runSession hands the window to the session viewer and blocks until the
// session ends: a display for a VM, an integrated terminal otherwise.
//
// It runs on the loop's goroutine, which is the goroutine that owns the
// window, which is what the viewer requires. Blocking here is the design:
// while a session is on screen there is no shell to draw, and a shell that
// kept polling would be competing with the viewer for the same event queue.
func (a *App) runSession(ctx context.Context) error {
	ws := a.m.Opening
	if a.connect == nil {
		a.afterSession()
		a.m.SessionEnded(errors.New("this build cannot open sessions"))
		return nil
	}

	a.logf("opening a display session to %s", ws.Key())
	err := a.connect(ctx, ws)
	a.afterSession()

	if ctx.Err() != nil {
		// The process is shutting down, not returning to the list.
		a.quit = true
		return nil
	}
	a.m.SessionEnded(err)
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

// SessionConnector returns the production [Connector]: it opens the workspace
// in the client's own window. A VM gets its supervised RFB display session; any
// other workspace gets the integrated terminal over the /exec bridge.
func SessionConnector(client *kwclient.Client, opts SessionOptions) Connector {
	terminalConnector := TerminalConnector(client, TerminalOptions{
		Scale: 1, // the 5x8 bitmap at 1x (6x11 px cells); scale 2 reads too large
		Logf:  opts.Logf,
	})
	return func(ctx context.Context, ws kwclient.Workspace) error {
		if !ws.IsVM() {
			return terminalConnector(ctx, ws)
		}

		if ws.RemoteDesktop != nil && ws.RemoteDesktop.Protocol == "selkies" {
			if opts.Logf != nil {
				opts.Logf("workspace %s advertises Selkies Tier 1 transport", ws.Key())
			}
			err := connectTier1(ctx, client, ws, opts)
			if err == nil {
				return nil
			}
			if errors.Is(err, session.ErrNoFallback) {
				// A refused agent, a rejected credential or a display owned
				// elsewhere must surface, not be routed around.
				return err
			}
			// Recoverable: dial, negotiation, startup or decode failed. Fall
			// back (also after bounded live recovery) for the rest of this connection — once we drop a
			// tier we do not probe it again mid-session (plan §7.2).
			if opts.Logf != nil {
				opts.Logf("Tier 1 to %s failed (%v); falling back to Tier 0", ws.Key(), err)
			}
		}

		encodings := append([]rfb.Encoding(nil), rfb.DefaultEncodings...)
		if opts.Quality >= 0 {
			encodings = append(encodings, rfb.QualityLevel(opts.Quality))
		}
		if opts.Compress >= 0 {
			encodings = append(encodings, rfb.CompressLevel(opts.Compress))
		}

		cfg := viewer.Config{
			AdaptiveQuality: !opts.FixedQuality,
			Title:           ws.Key(),
			ScaleQuality:    opts.ScaleQuality,
			Logf:            opts.Logf,
		}
		view := viewer.New(viewer.NewSDLBackend(), cfg)

		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		base := rfb.Config{Encodings: encodings}
		if fm := view.AudioFormat(); fm != nil {
			base.AudioFormat = fm
		}
		sess, err := session.DialReconnecting(runCtx, client, ws.Namespace, ws.Name, base, session.Options{
			Policy:         reconnect.Default(),
			UpdateInterval: opts.UpdateInterval,
			OnState: func(state session.State, err error) {
				if status, detail, ok := viewerStatus(state, err); ok {
					view.SetStatus(status, detail)
				}
			},
			// The RFB handshake reads its config once, when the connection is
			// created, so the callbacks cannot be swapped later; the
			// supervisor calls this immediately before each dial.
			Config: func() rfb.Config { return view.RFBConfig(base) },
		})
		if err != nil {
			return err
		}
		// Closing the session releases the server's single display slot. The
		// KubeVirt console has no takeover endpoint, so leaking it locks the
		// workspace's display out until the idle timeout — and the user is
		// about to be looking at a list with that workspace on it.
		defer func() { _ = sess.Close() }()
		// Match the CLI consent flow, including after Tier 1 falls back.
		view.SetTakeoverHandler(func() error {
			res, err := client.VNCTakeover(runCtx, ws.Namespace, ws.Name)
			if err != nil {
				return err
			}
			if !res.OK {
				return errors.New(i18n.Get("session.takeoverDeclined"))
			}
			sess.RetryNow()
			return nil
		})

		runErr := view.Run(runCtx, sess)
		cancel()
		_ = sess.Close()

		if ctx.Err() != nil {
			return nil
		}
		return runErr
	}
}

// connectObserver joins a shared display session as an observer and presents
// it in a supervised window: the stream reconnects after drops, Ctrl+Alt+C
// requests/releases control (with an Enter-to-take-over confirmation when
// another participant holds it), and a poll applies remote role changes —
// a take-over demotes us, a transfer promotes us — by re-attaching the
// stream with the registry's role, the same contract as the browser screen.
func connectObserver(ctx context.Context, client API, ws kwclient.Workspace, opts SessionOptions) error {
	// The shared display ships opt-in: check the capability advert before
	// joining so a gated platform answers with its own message rather than a
	// bare join failure.
	cap, err := client.Display(ctx, ws.Namespace, ws.Name)
	if err != nil {
		return err
	}
	if !cap.Enabled {
		return errors.New(i18n.Get("session.sharedDisabled"))
	}

	join, err := client.JoinDisplay(ctx, ws.Namespace, ws.Name, kwclient.DisplayRoleObserver)
	if err != nil {
		return err
	}
	participantID := join.Participant.ID
	defer func() {
		leaveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.LeaveDisplay(leaveCtx, ws.Namespace, ws.Name, participantID)
	}()

	view := viewer.New(viewer.NewSDLBackend(), viewer.Config{
		AdaptiveQuality: !opts.FixedQuality,
		Title:           ws.Key() + i18n.Get("workspaces.observer"),
		ReadOnly:        true,
		ControlRune:     'c',
		ScaleQuality:    opts.ScaleQuality,
		Logf:            opts.Logf,
	})

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
			if status, detail, ok := viewerStatus(state, err); ok {
				view.SetStatus(status, detail)
			}
		},
		// The RFB handshake reads its config once, at connect time, so the
		// viewer's callbacks are installed per generation, like the
		// exclusive-session path.
		Config: func() rfb.Config { return view.RFBConfig(base) },
		Dial: func(dialCtx context.Context, cfg rfb.Config, role string, force bool) (session.Link, error) {
			conn, err := client.DialDisplayWS(dialCtx, ws.Namespace, ws.Name, participantID, role, force)
			if err != nil {
				return nil, err
			}
			return session.SharedLink(conn, cfg)
		},
	})
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()

	ctl := &sharedControl{client: client, ws: ws, participantID: participantID, sess: sess, view: view}
	view.SetControlHandler(ctl.toggle)

	pollCtx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()
	go ctl.watch(pollCtx)

	runErr := view.Run(ctx, sess)
	if ctx.Err() != nil {
		return nil
	}
	return runErr
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

// connectTier1 opens an interactive Tier 1 (Selkies) session in a fresh window
// for a workspace whose image advertises the selkies protocol. The window
// belongs to this call and this call only, like the RFB viewer's own window.
func connectTier1(ctx context.Context, client *kwclient.Client, ws kwclient.Workspace, opts SessionOptions) error {
	agentBase := ""
	if ws.RemoteDesktop != nil && ws.RemoteDesktop.Path != nil {
		agentBase = *ws.RemoteDesktop.Path
	}
	return session.RunTier1(ctx, client, ws.Namespace, ws.Name, agentBase,
		viewer.NewSDLBackend(), session.Tier1Config{
			Title:        ws.Key(),
			Audio:        true,
			ScaleQuality: opts.ScaleQuality,
			Logf:         opts.Logf,
		})
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
