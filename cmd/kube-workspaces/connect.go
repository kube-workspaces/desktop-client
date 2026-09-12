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

// It cannot be wired up from here. The table is a slice of command values
// built inside main(), so there is no package-level registry for an init
// function to append to, and Go provides no way to add an entry to a local
// variable from another file. Adding the line below to the table in main.go is
// the whole change:
//
//	{"connect", "Open a graphical session to a VM workspace", runConnect},
//
// connectCommand exists so that the name, summary and entry point live next to
// the implementation and the edit in main.go stays a one-liner.
//
// Until that line exists, nothing reaches this file, so both it and
// runConnect are dead code as far as the linter is concerned. Delete the two
// //nolint:unused directives when the command is registered.
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
// loop runs on its own goroutine, the window and all input run on the main OS
// thread, and the two meet in internal/viewer.
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
	// locking it here pins the whole session loop to the main OS thread. The
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

	// The RFB handshake reads its rfb.Config exactly once, when the connection
	// is created, so the callbacks installed on it cannot be swapped later.
	// A reconnecting session creates a new connection — and this command a new
	// viewer with it — for every attempt, so the session is given one stable
	// set of callbacks that forwards to whichever viewer currently owns the
	// window. Updates that arrive before a viewer is installed are simply
	// dropped: they have already been decoded into the connection's
	// framebuffer, and a viewer's first frame is a full repaint regardless.
	base := rfb.Config{Encodings: encs}
	var live atomic.Pointer[rfb.Config]
	shared := base
	shared.OnFramebufferUpdate = func(fb *rfb.Framebuffer, damage []rfb.Rect) {
		if c := live.Load(); c != nil && c.OnFramebufferUpdate != nil {
			c.OnFramebufferUpdate(fb, damage)
		}
	}
	shared.OnResize = func(w, h int) {
		if c := live.Load(); c != nil && c.OnResize != nil {
			c.OnResize(w, h)
		}
	}
	shared.OnCutText = func(text string) {
		if c := live.Load(); c != nil && c.OnCutText != nil {
			c.OnCutText(text)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	opts := session.Options{
		Policy:         reconnect.Default(),
		UpdateInterval: *interval,
		OnState:        stateReporter(ns, name),
	}
	if !*autoReconnect {
		// One attempt, and a display held by somebody else is reported rather
		// than queued for.
		opts.Policy.MaxAttempts = 1
		opts.InUseTimeout = -1
	}

	sess, err := session.DialReconnecting(runCtx, client, ns, name, shared, opts)
	if err != nil {
		return err
	}
	// Closing the session is what releases the server's single VNC slot; the
	// KubeVirt console has no takeover endpoint, so leaking it locks the
	// display out until the idle timeout.
	defer func() { _ = sess.Close() }()

	var (
		last       *rfb.Conn
		generation int
		viewErr    error
	)
	for {
		// Attach waits for a connection and hands back the context that bounds
		// it, so the viewer below is torn down exactly when its connection
		// ends rather than when the whole session does.
		conn, connCtx, err := sess.Attach(runCtx)
		if err != nil {
			if runCtx.Err() != nil {
				break
			}
			return err
		}
		generation++
		last = conn

		// A new connection means a new viewer, and therefore a new window: the
		// viewer binds to one *rfb.Conn for its lifetime. The title carries
		// the reconnect count so a user on a flaky link can see what is
		// happening to their session.
		wcfg := cfg
		if generation > 1 {
			wcfg.Title = fmt.Sprintf("%s (reconnect %d)", cfg.Title, generation-1)
		}
		view := viewer.New(viewer.NewSDLBackend(), wcfg)
		vcfg := view.RFBConfig(base)
		live.Store(&vcfg)

		if generation == 1 {
			fmt.Printf("Connected to %s/%s — %s\n", ns, name, conn.ServerName())
			for _, line := range cfg.Hotkeys() {
				fmt.Printf("  %s\n", line)
			}
		}

		viewErr = view.Run(connCtx, conn)
		live.Store(nil)

		if connCtx.Err() == nil {
			// The viewer stopped while its connection was still good: the user
			// quit, or the window itself failed. Neither is worth reconnecting
			// for.
			break
		}
		if state, _ := sess.State(); state.Terminal() || runCtx.Err() != nil {
			break
		}
		// The connection dropped underneath the viewer. Any error it reports
		// is a consequence of writing to the dead connection, not a reason to
		// end the session.
		if viewErr != nil {
			if cfg.Logf != nil {
				cfg.Logf("viewer stopped with the connection: %v", viewErr)
			}
			viewErr = nil
		}
	}
	cancel()
	_ = sess.Close()

	if last != nil {
		stats := last.Stats()
		fmt.Printf("Disconnected from %s/%s — %s over %d update(s)\n",
			ns, name, humanBytes(stats.BytesRead), stats.Updates)
	}

	if viewErr != nil {
		return viewErr
	}
	if state, sessErr := sess.State(); state == session.StateFailed && ctx.Err() == nil {
		return fmt.Errorf("session ended: %w", sessErr)
	}
	return nil
}

// stateReporter returns a session state callback that keeps the user informed
// on stderr.
//
// It runs on the session's supervisor goroutine, so it does no more than
// print: the window belongs to the main thread and cannot be touched from
// here.
func stateReporter(ns, name string) func(session.State, error) {
	previous := session.State(-1)
	connections := 0
	return func(state session.State, err error) {
		// A session waiting for a busy display re-reports every poll, and one
		// line every few seconds saying the same thing is noise.
		repeat := state == previous
		previous = state
		if repeat && state == session.StateDisplayInUse {
			return
		}

		switch state {
		case session.StateConnected:
			connections++
			// The first connection is announced on stdout by the caller,
			// along with the hotkeys; only a reconnect is news here.
			if connections > 1 {
				fmt.Fprintf(os.Stderr, "connect: reconnected to %s/%s\n", ns, name)
			}
		case session.StateReconnecting:
			fmt.Fprintf(os.Stderr, "connect: %s/%s disconnected (%v); reconnecting\n", ns, name, orClosed(err))
		case session.StateDisplayInUse:
			// There is no VNC takeover endpoint, so waiting is the only
			// honest option; say so rather than looking stuck.
			fmt.Fprintf(os.Stderr, "connect: the display of %s/%s is in use by another session; waiting for it to be released\n", ns, name)
		case session.StateFailed:
			fmt.Fprintf(os.Stderr, "connect: %s/%s failed: %v\n", ns, name, orClosed(err))
		}
	}
}

// orClosed names the reason a connection ended when the server gave none.
func orClosed(err error) error {
	if err == nil {
		return errors.New("closed by the server")
	}
	return err
}
