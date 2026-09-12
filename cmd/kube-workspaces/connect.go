package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

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

	view := viewer.New(viewer.NewSDLBackend(), cfg)

	// The viewer's callbacks have to be installed before the handshake, since
	// rfb.Config is read once when the connection is created.
	sess, err := session.Dial(ctx, client, ns, name, view.RFBConfig(rfb.Config{Encodings: encs}))
	if err != nil {
		return err
	}
	// Closing the session is what releases the server's single VNC slot; the
	// KubeVirt console has no takeover endpoint, so leaking it locks the
	// display out until the idle timeout.
	defer sess.Close()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		// Cancelling on the way out stops the viewer as soon as the stream
		// ends, rather than leaving a dead window on screen.
		defer cancel()
		errCh <- sess.Run(runCtx)
	}()
	sess.RequestUpdates(runCtx, *interval)

	fmt.Printf("Connected to %s/%s — %s\n", ns, name, sess.Conn().ServerName())
	for _, line := range cfg.Hotkeys() {
		fmt.Printf("  %s\n", line)
	}

	viewErr := view.Run(runCtx, sess.Conn())
	cancel()
	_ = sess.Close()

	var sessErr error
	select {
	case sessErr = <-errCh:
	case <-time.After(2 * time.Second):
	}

	stats := sess.Conn().Stats()
	fmt.Printf("Disconnected from %s/%s — %s over %d update(s)\n",
		ns, name, humanBytes(stats.BytesRead), stats.Updates)

	if viewErr != nil {
		return viewErr
	}
	if sessErr != nil && !errors.Is(sessErr, context.Canceled) && ctx.Err() == nil {
		return fmt.Errorf("session ended: %w", sessErr)
	}
	return nil
}
