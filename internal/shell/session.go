// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/reconnect"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
	"github.com/kube-workspaces/desktop-client/internal/session"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// runSession hands the window to the display viewer and blocks until the
// session ends.
//
// It runs on the loop's goroutine, which is the goroutine that owns the
// window, which is what the viewer requires. Blocking here is the design:
// while a session is on screen there is no shell to draw, and a shell that
// kept polling would be competing with the viewer for the same event queue.
func (a *App) runSession(ctx context.Context) error {
	ws := a.m.Opening
	if a.connect == nil {
		a.afterSession()
		a.m.SessionEnded(errors.New("this build cannot open display sessions"))
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

// afterSession takes the window back.
//
// Three things have to be undone. The viewer resized the framebuffer texture
// to the guest's resolution, so the shell's cached size is wrong and the next
// frame must reallocate. The event queue holds whatever the session left in it
// — at minimum the release of the key that ended it — and none of it is the
// shell's. And the input state describes a pointer and a set of modifiers from
// a window the user was doing something else in.
func (a *App) afterSession() {
	a.sessionStarted = false
	a.texW, a.texH = 0, 0
	a.events = a.be.PollEvents(a.events[:0])
	a.events = a.events[:0]
	a.in = ui.Input{Focused: true}
	a.ctx.Focus().Clear()
	a.lastState = State(-1)
	// Refresh as soon as the list is back: the workspace was just used, and
	// whatever else happened in the meantime should be visible immediately.
	a.nextRefresh = time.Time{}
	a.dirty = true
	if err := a.be.SetTitle(a.opts.Title); err != nil {
		a.logf("restore window title: %v", err)
	}
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

// SessionConnector returns the production [Connector]: it opens a supervised
// RFB session to a VM workspace and presents it in the shell's own window.
//
// It is the same machinery as the connect subcommand — session.DialReconnecting
// under a viewer.Viewer — with one difference: the backend is borrowed rather
// than created, so the session appears in the window the user is already
// looking at instead of a second one.
func SessionConnector(be viewer.Backend, client *kwclient.Client, title string, opts SessionOptions) Connector {
	return func(ctx context.Context, ws kwclient.Workspace) error {
		if !ws.IsVM() {
			return fmt.Errorf("%s is a %s workspace and has no display", ws.Name, ws.Type)
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
			// The shell is behind this window and will redraw the moment the
			// session ends, so there is nothing to linger for: a failure is
			// reported on the workspace list instead, where the user can act
			// on it.
			FailureLinger: -1,
		}
		view := viewer.New(&borrowedBackend{Backend: be, title: title}, cfg)

		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		base := rfb.Config{Encodings: encodings}
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

// borrowedBackend lends the shell's window to a [viewer.Viewer].
//
// The viewer's contract is that it opens a window at the start of a session
// and destroys it at the end. That is exactly right when the session is the
// whole process, and exactly wrong here: the window belongs to the shell, it
// was open before the session and has to survive it, and destroying it would
// unload SDL and take the shell's surface with it.
//
// So Open, Close and SetSize are intercepted. Everything else — textures,
// uploads, presentation, input, clipboard, fullscreen — is forwarded
// untouched, because all of it is per-session state the viewer is entitled to
// manage. The shell repairs what it cares about afterwards; see
// [App.afterSession].
type borrowedBackend struct {
	viewer.Backend
	// title is the shell's own window title, restored on the way out.
	title string

	// restoreW/restoreH is the window size the shell was using when the
	// session borrowed the window, and restore says whether it was captured.
	restoreW, restoreH int
	restore            bool

	// userSized records that the window changed size during the session for a
	// reason this wrapper did not cause. Since SetSize is refused outright,
	// the only things left are the user dragging the frame, the window
	// manager, and fullscreen transitions — and in every one of those cases
	// the size on screen at the end is the one to keep.
	userSized bool
}

// Open configures the existing window instead of creating one, and remembers
// the size to give back at the end.
//
// The requested size is ignored: the window is already on screen at a size the
// user chose, and resizing it to the viewer's default would make every session
// jump. The viewer reads the real size back from Size and letterboxes into it.
func (b *borrowedBackend) Open(opts viewer.WindowOptions) error {
	// Through the embedded value, not through b: the point of this type is
	// that some of these calls are overridden, and a reader should be able to
	// see at a glance which window each one reaches.
	be := b.Backend
	b.restoreW, b.restoreH = be.Size()
	b.restore = b.restoreW > 0 && b.restoreH > 0
	b.userSized = false

	if opts.Fullscreen && !be.Fullscreen() {
		if err := be.SetFullscreen(true); err != nil {
			return err
		}
	}
	return be.SetTitle(opts.Title)
}

// SetSize refuses, because the shell's window is not the session's to resize.
//
// The viewer calls this once per session, to fit the window to the guest's
// resolution. That is right when the session is the whole application — the
// window was created for it a moment earlier — and wrong here: this window was
// already on screen, at a size the user chose, showing a list they were
// reading. Following the guest would resize it out from under them on connect,
// and again for every workspace they open, none of which they asked for.
//
// Refusing is not a hack around the interface, it is the documented behaviour
// of one: [viewer.Backend.SetSize] says a backend whose size is not its own to
// choose may ignore the request, and that the viewer reads the size back
// rather than assuming. It does, and letterboxes the guest into the window
// instead — which is the same thing it does for a fullscreen session or a
// tiling window manager, and is already the well-trodden path.
//
// The cost is that a guest whose resolution differs from the window is scaled
// rather than shown 1:1 until the user resizes the window, at which point the
// viewer's normal debounce asks the guest to match. That is a fair trade for a
// window that never moves on its own.
func (b *borrowedBackend) SetSize(int, int) error { return nil }

// PollEvents forwards the session's input, watching it for resizes.
func (b *borrowedBackend) PollEvents(dst []viewer.Event) []viewer.Event {
	return b.noteResizes(b.Backend.PollEvents(dst))
}

// WaitEvents forwards the session's blocking wait, watching it for resizes.
func (b *borrowedBackend) WaitEvents(dst []viewer.Event, timeout time.Duration) []viewer.Event {
	return b.noteResizes(b.Backend.WaitEvents(dst, timeout))
}

// noteResizes records that the window changed size while the session had it.
//
// Watching the event stream is the only way to tell: a resize the user
// performs is reported, not requested, and by the time [borrowedBackend.Close]
// runs the two possible sizes are indistinguishable. Reading the events on
// their way past costs a type switch over a batch that is almost always empty.
func (b *borrowedBackend) noteResizes(events []viewer.Event) []viewer.Event {
	if !b.restore || b.userSized {
		return events
	}
	for _, ev := range events {
		if r, ok := ev.(viewer.EventResize); ok && (r.W != b.restoreW || r.H != b.restoreH) {
			b.userSized = true
			return events
		}
	}
	return events
}

// Close returns the window to the shell rather than destroying it, at the size
// the shell was using.
//
// Restoring the size is the other half of refusing to change it: whatever did
// change it — a fullscreen toggle, a window manager with opinions — belongs to
// the session that has just ended, and the user's mental model is that they
// left a list open and are coming back to it. The exception is a resize the
// user performed themselves during the session: that was a deliberate act
// about this window, not about this session, so it survives it.
//
// A fullscreen session that the user never resized comes back through the
// leave-fullscreen path, which restores the pre-fullscreen size anyway; the
// explicit restore below then finds nothing to do.
func (b *borrowedBackend) Close() {
	be := b.Backend
	if be.Fullscreen() {
		// The shell is not a fullscreen application; leaving it fullscreen
		// because the last session was would be a surprising inheritance.
		_ = be.SetFullscreen(false)
	}
	if b.restore && !b.userSized {
		if w, h := be.Size(); w != b.restoreW || h != b.restoreH {
			_ = be.SetSize(b.restoreW, b.restoreH)
		}
	}
	_ = be.SetTitle(b.title)
}

// Compile-time proof that the wrapper is still a backend.
var _ viewer.Backend = (*borrowedBackend)(nil)
