// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/reconnect"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
	"github.com/kube-workspaces/desktop-client/internal/session"
	"github.com/kube-workspaces/desktop-client/internal/terminal"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

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
			opts.Logf("workspace %s supports Selkies Tier 1 transport", ws.Key())
			// For Spike D we only prove reachability. A full implementation
			// would swap the viewer/renderer here.
		}

		encodings := append([]rfb.Encoding(nil), rfb.DefaultEncodings...)
		if opts.Quality >= 0 {
			encodings = append(encodings, rfb.QualityLevel(opts.Quality))
		}
		if opts.Compress >= 0 {
			encodings = append(encodings, rfb.CompressLevel(opts.Compress))
		}

		cfg := viewer.Config{
			Title:        ws.Key(),
			ScaleQuality: opts.ScaleQuality,
			Logf:         opts.Logf,
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

		runErr := view.Run(runCtx, sess)
		cancel()
		_ = sess.Close()

		if ctx.Err() != nil {
			return nil
		}
		return runErr
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
