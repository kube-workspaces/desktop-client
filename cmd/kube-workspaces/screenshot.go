package main

import (
	"context"
	"flag"
	"fmt"
	"image/png"
	"os"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
	"github.com/kube-workspaces/desktop-client/internal/session"
)

// runScreenshot captures one frame of a VM workspace's display to a PNG.
//
// Besides being useful on its own, this is the end-to-end proof that the whole
// chain works: API auth, WebSocket bridge, RFB handshake, encoding negotiation
// and pixel decoding all have to be correct to produce a recognisable image.
func runScreenshot(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("screenshot", flag.ExitOnError)
	profileName := fs.String("profile", "", "profile to use")
	namespace := fs.String("namespace", "", "workspace namespace")
	out := fs.String("o", "", "output PNG path (default <workspace>.png)")
	timeout := fs.Duration("timeout", 30*time.Second, "how long to wait for a complete frame")
	settle := fs.Duration("settle", 750*time.Millisecond, "extra time to collect follow-up updates before saving")
	quality := fs.Int("quality", -1, "JPEG quality level 0-9 to request (-1 to omit)")
	wake := fs.Bool("wake", false, "send a keypress first to wake a blanked display")
	wakeKey := fs.String("wake-key", "shift_l", "key to send when -wake is set (e.g. shift_l, return, space)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kube-workspaces screenshot <workspace> [-o out.png]\n\n")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return fmt.Errorf("a workspace name is required")
	}
	name := fs.Arg(0)
	path := *out
	if path == "" {
		path = name + ".png"
	}

	client, profile, err := clientFor(*profileName)
	if err != nil {
		return err
	}
	ns, err := resolveNamespace(ctx, client, profile, *namespace, name)
	if err != nil {
		return err
	}

	encs := append([]rfb.Encoding(nil), rfb.DefaultEncodings...)
	if *quality >= 0 {
		encs = append(encs, rfb.QualityLevel(*quality))
	}

	// A "complete" frame is not a concept RFB exposes: the server answers a
	// non-incremental request with the whole screen, but may split it across
	// updates. Wait for the first update, then allow a short settling window
	// for any follow-ups before saving.
	firstFrame := make(chan struct{}, 1)
	cfg := rfb.Config{
		Encodings: encs,
		OnFramebufferUpdate: func(_ *rfb.Framebuffer, _ []rfb.Rect) {
			select {
			case firstFrame <- struct{}{}:
			default:
			}
		},
	}

	sess, err := session.Dial(ctx, client, ns, name, cfg)
	if err != nil {
		return err
	}
	defer sess.Close()

	runCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- sess.Run(runCtx) }()

	// A guest that has been idle blanks its display (DPMS), and QEMU then has
	// nothing to send but a black frame. Tapping a modifier is the least
	// invasive way to wake it: it cannot type anything into the session.
	if *wake {
		key, ok := keysym.ParseKey(*wakeKey)
		if !ok {
			return fmt.Errorf("unknown wake key %q", *wakeKey)
		}
		sym := uint32(key.Keysym())
		if err := sess.Conn().KeyEvent(sym, true); err != nil {
			return fmt.Errorf("send wake key: %w", err)
		}
		if err := sess.Conn().KeyEvent(sym, false); err != nil {
			return fmt.Errorf("release wake key: %w", err)
		}
		// Give the guest time to redraw before asking for pixels.
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-runCtx.Done():
		}
	}

	if err := sess.Conn().RequestUpdate(false); err != nil {
		return err
	}

	select {
	case <-firstFrame:
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("session ended before a frame arrived: %w", err)
		}
		return fmt.Errorf("session closed before a frame arrived")
	case <-runCtx.Done():
		return fmt.Errorf("timed out waiting for a frame after %s", *timeout)
	}

	select {
	case <-time.After(*settle):
	case <-runCtx.Done():
	}

	var (
		width, height int
		snapshot      *rfb.Framebuffer
	)
	sess.Conn().WithFramebuffer(func(fb *rfb.Framebuffer) {
		width, height = fb.Width, fb.Height
		snapshot = fb.Clone()
	})
	cancel()
	_ = sess.Close()

	if snapshot == nil || width == 0 || height == 0 {
		return fmt.Errorf("no framebuffer was received")
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, snapshot.RGBA()); err != nil {
		return fmt.Errorf("encode png: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}

	stats := sess.Conn().Stats()
	fmt.Printf("Wrote %s (%dx%d) from %s/%s — %s over %d rectangle(s)\n",
		path, width, height, ns, name, humanBytes(stats.BytesRead), stats.Rects)
	return nil
}
