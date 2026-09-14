package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/reconnect"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
	"github.com/kube-workspaces/desktop-client/internal/session"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// connectCommand exists so that the name, summary and entry point live next to
// the implementation and the entry in main.go's table stays a one-liner. The
// table is a slice of command values built inside main(), so there is no
// package-level registry an init function could append to.
func connectCommand() command {
	return command{
		name:    "connect",
		summary: "Open a graphical session to a VM workspace",
		run:     runConnect,
	}
}

// runConnect opens a window onto a VM workspace's display.
//
// This is the session process from the client's architecture: the RFB read
// loop runs on its own goroutine, the reconnect supervisor on another, and the
// window and all input run on the main OS thread. They meet in internal/viewer.
//
// The window is opened once and kept: connections are swapped underneath it by
// the supervisor, so a drop freezes the last frame under an overlay instead of
// destroying and recreating the window (and with it SDL) per attempt.
func runConnect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	profileName := fs.String("profile", "", "profile to use")
	namespace := fs.String("namespace", "", "workspace namespace")
	fullscreen := fs.Bool("fullscreen", false, "start the session fullscreen")
	quality := fs.Int("quality", 8, "JPEG quality level 0-9 to request (-1 to omit)")
	compress := fs.Int("compress", -1, "zlib compression level 0-9 to request (-1 to omit)")
	scaleQuality := fs.String("scale-quality", "linear", "scaling filter: nearest, linear or pixelart")
	interval := fs.Duration("interval", 16*time.Millisecond, "framebuffer update request interval")
	autoReconnect := fs.Bool("reconnect", true, "reconnect automatically after a transient failure")
	width := fs.Int("width", 0, "initial window width (0 to match the guest)")
	height := fs.Int("height", 0, "initial window height (0 to match the guest)")
	noResize := fs.Bool("no-resize", false, "do not resize the guest display to match the window")
	noVSync := fs.Bool("no-vsync", false, "do not synchronise presentation with the display")
	verbose := fs.Bool("v", false, "log session diagnostics")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kube-workspaces connect <workspace> [flags]\n\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nHotkeys:\n")
		for _, line := range (&viewer.Config{}).Hotkeys() {
			fmt.Fprintf(os.Stderr, "  %s\n", line)
		}
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return fmt.Errorf("a workspace name is required")
	}
	name := fs.Arg(0)

	scale, err := viewer.ParseScaleQuality(*scaleQuality)
	if err != nil {
		return err
	}

	client, profile, err := clientFor(*profileName)
	if err != nil {
		return err
	}
	ns, err := resolveNamespace(ctx, client, profile, *namespace, name)
	if err != nil {
		return err
	}

	encs, err := buildEncodings("", *quality, *compress, false)
	if err != nil {
		return err
	}

	// SDL must be driven from the thread that initialised the video subsystem,
	// and on macOS that thread must be the process's first one. main() calls
	// this function directly, so this goroutine is the main goroutine and
	// locking it here pins the whole render loop to the main OS thread. The
	// lock is never released: the process exits when the session ends.
	runtime.LockOSThread()

	cfg := viewer.Config{
		Title:        ns + "/" + name,
		Width:        *width,
		Height:       *height,
		Fullscreen:   *fullscreen,
		ScaleQuality: scale,
		NoVSync:      *noVSync,
	}
	if *noResize {
		// A guest with no resize support, or a user who wants a fixed
		// resolution, is served by scaling into the window instead.
		cfg.ResizeDebounce = 365 * 24 * time.Hour
	}
	if *verbose {
		cfg.Logf = func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "connect: "+format+"\n", a...)
		}
	}

	// One viewer, one window, for the whole session — however many connections
	// that turns out to span.
	view := viewer.New(viewer.NewSDLBackend(), cfg)
	ui := &sessionUI{ns: ns, name: name, view: view, previous: session.State(-1)}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	base := rfb.Config{Encodings: encs}
	if fm := view.AudioFormat(); fm != nil {
		// Opt the connection into guest audio; the viewer enables it on the
		// wire once the guest acknowledges the encoding. A guest with no
		// soundDevice simply never streams.
		base.AudioFormat = fm
	}
	opts := session.Options{
		Policy:         reconnect.Default(),
		UpdateInterval: *interval,
		OnState:        ui.onState,
		// The RFB handshake reads its rfb.Config exactly once, when the
		// connection is created, so callbacks cannot be swapped later. The
		// supervisor calls this immediately before each dial, and the viewer's
		// callbacks are stable for its lifetime, so every generation gets a
		// fresh config pointing at the same window.
		Config: func() rfb.Config { return view.RFBConfig(base) },
	}
	if !*autoReconnect {
		// One attempt, and a display held by somebody else is reported rather
		// than queued for.
		opts.Policy.MaxAttempts = 1
		opts.InUseTimeout = -1
	}

	sess, err := session.DialReconnecting(runCtx, client, ns, name, base, opts)
	if err != nil {
		return err
	}
	ui.sess.Store(sess)
	// Closing the session is what releases the server's single VNC slot;
	// leaking it locks the display out until the idle timeout.
	defer func() { _ = sess.Close() }()

	// Enter while the display-in-use overlay is up takes over the server's
	// single VNC slot and nudges the supervisor to re-dial immediately.
	view.SetTakeoverHandler(func() error {
		res, err := client.VNCTakeover(runCtx, ns, name)
		if err != nil {
			return err
		}
		if !res.OK {
			return fmt.Errorf("server declined the takeover")
		}
		sess.RetryNow()
		return nil
	})

	fmt.Printf("Opening %s/%s\n", ns, name)
	for _, line := range cfg.Hotkeys() {
		fmt.Printf("  %s\n", line)
	}

	// *session.ReconnectingSession is a viewer.ConnSource: Attach hands over
	// each connection along with the context that bounds it.
	runErr := view.Run(runCtx, sess)

	cancel()
	_ = sess.Close()

	if last := ui.last.Load(); last != nil {
		stats := last.Stats()
		fmt.Printf("Disconnected from %s/%s — %s over %d update(s)\n",
			ns, name, humanBytes(stats.BytesRead), stats.Updates)
	}
	if runErr != nil && ctx.Err() == nil {
		return runErr
	}
	return nil
}

// sessionUI mirrors the supervisor's state onto the window and the terminal.
//
// Its methods run on the supervisor's goroutine — never the render loop's — so
// they touch nothing that belongs to the window: [viewer.Viewer.SetStatus] is
// safe from any goroutine and hands the change to the render loop, and the
// connection pointer is swapped atomically.
type sessionUI struct {
	ns, name string
	view     *viewer.Viewer

	sess atomic.Pointer[session.ReconnectingSession]
	// last is the most recent live connection, kept only so that the session
	// can be summarised after the window has gone.
	last atomic.Pointer[rfb.Conn]

	// previous and connections are only touched from the supervisor's
	// goroutine, which announces states one at a time.
	previous    session.State
	connections int
}

// onState is the session's OnState callback. It must not block.
func (u *sessionUI) onState(state session.State, err error) {
	repeat := state == u.previous
	u.previous = state

	if status, detail, ok := viewerStatus(state, err); ok {
		u.view.SetStatus(status, detail)
	}

	switch state {
	case session.StateConnected:
		u.connections++
		server := ""
		if sess := u.sess.Load(); sess != nil {
			if conn := sess.Conn(); conn != nil {
				u.last.Store(conn)
				server = " — " + conn.ServerName()
			}
		}
		if u.connections == 1 {
			fmt.Fprintf(os.Stderr, "connect: connected to %s/%s%s\n", u.ns, u.name, server)
		} else {
			fmt.Fprintf(os.Stderr, "connect: reconnected to %s/%s\n", u.ns, u.name)
		}

	case session.StateReconnecting:
		fmt.Fprintf(os.Stderr, "connect: %s/%s disconnected (%v); reconnecting\n", u.ns, u.name, orClosed(err))

	case session.StateDisplayInUse:
		// A session waiting for a busy display re-reports every poll, and one
		// line every few seconds saying the same thing is noise. The window
		// says so continuously anyway.
		if !repeat {
			// There is no VNC takeover endpoint, so waiting is the only honest
			// option; say so rather than looking stuck.
			fmt.Fprintf(os.Stderr, "connect: the display of %s/%s is in use by another session; waiting for it to be released\n", u.ns, u.name)
		}
	}
	// StateFailed is deliberately silent here: the reason is shown in the
	// window and returned from runConnect, and printing it in three places is
	// two too many.
}

// viewerStatus maps a supervisor state onto what the window should say.
//
// The mapping lives here rather than in internal/viewer so that the viewer
// stays independent of the session package: a different supervisor, or the
// shell process, can drive the same overlay.
func viewerStatus(state session.State, err error) (viewer.Status, string, bool) {
	switch state {
	case session.StateConnecting:
		return viewer.StatusConnecting, "", true
	case session.StateConnected:
		return viewer.StatusLive, "", true
	case session.StateReconnecting:
		return viewer.StatusReconnecting, orClosed(err).Error(), true
	case session.StateDisplayInUse:
		return viewer.StatusDisplayInUse, "", true
	case session.StateFailed:
		return viewer.StatusFailed, orClosed(err).Error(), true
	default:
		// StateClosed is the session shutting down, which happens when the
		// window is already closing; there is nobody left to tell.
		return viewer.StatusLive, "", false
	}
}

// orClosed names the reason a connection ended when the server gave none.
func orClosed(err error) error {
	if err == nil {
		return errors.New("closed by the server")
	}
	return err
}
