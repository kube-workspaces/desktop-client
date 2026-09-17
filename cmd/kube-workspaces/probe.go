package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/cmdutil"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
	"github.com/kube-workspaces/desktop-client/internal/session"
)

// runProbe connects to a VM workspace's display and reports what the server
// actually supports, plus a bandwidth sample.
//
// This exists because a client cannot ask an RFB server what it can do. The
// only signal is behavioural: which encodings come back in framebuffer updates,
// and which pseudo-encodings the server echoes as zero-sized rectangles. So we
// advertise everything we understand, drive real updates, and report what the
// server chose.
func runProbe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	profileName := fs.String("profile", "", "profile to use")
	namespace := fs.String("namespace", "", "workspace namespace")
	duration := fs.Duration("duration", 10*time.Second, "how long to sample updates for")
	interval := fs.Duration("interval", 33*time.Millisecond, "framebuffer update request interval")
	quality := fs.Int("quality", -1, "JPEG quality level 0-9 to request (-1 to omit)")
	compress := fs.Int("compress", -1, "zlib compression level 0-9 to request (-1 to omit)")
	audio := fs.Bool("audio", false, "also advertise the QEMU audio pseudo-encoding")
	encodings := fs.String("encodings", "", "comma-separated encoding override, e.g. tight,copyrect,raw")
	verbose := fs.Bool("v", false, "log every rectangle as it is decoded")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kube-workspaces probe <workspace> [flags]\n\n")
		fs.PrintDefaults()
	}
	if err := cmdutil.ParseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return fmt.Errorf("a workspace name is required")
	}
	name := fs.Arg(0)

	client, profile, err := cmdutil.For(version, *profileName)
	if err != nil {
		return err
	}
	ns, err := cmdutil.ResolveNamespace(ctx, client, profile, *namespace, name)
	if err != nil {
		return err
	}

	encs, err := buildEncodings(*encodings, *quality, *compress, *audio)
	if err != nil {
		return err
	}

	var (
		firstUpdate  time.Time
		resizes      []string
		cursorShapes int
		bellCount    int
		cutTexts     int
	)
	var nonBlack int
	cfg := rfb.Config{
		Encodings: encs,
		OnFramebufferUpdate: func(fb *rfb.Framebuffer, damage []rfb.Rect) {
			if firstUpdate.IsZero() {
				firstUpdate = time.Now()
			}
			// Count non-black pixels: a decoder that silently produces an empty
			// image is the failure mode that is easiest to miss, because the
			// byte counts and rectangle counts all look healthy.
			n := 0
			for i := 0; i+3 < len(fb.Pix); i += 4 {
				if fb.Pix[i] != 0 || fb.Pix[i+1] != 0 || fb.Pix[i+2] != 0 {
					n++
				}
			}
			nonBlack = n
			if *verbose {
				fmt.Printf("  update: %d damage rect(s), %d non-black pixels\n", len(damage), n)
			}
		},
		OnRect: func(enc rfb.Encoding, r rfb.Rect, bytes uint64) {
			if *verbose {
				fmt.Printf("  rect %-24s %-18s %6d bytes\n", enc, r, bytes)
			}
		},
		OnResize: func(w, h int) {
			resizes = append(resizes, fmt.Sprintf("%dx%d", w, h))
		},
		OnCursor:  func(_ []byte, _, _, _, _ int) { cursorShapes++ },
		OnBell:    func() { bellCount++ },
		OnCutText: func(string) { cutTexts++ },
	}

	fmt.Printf("Connecting to %s/%s ...\n", ns, name)
	start := time.Now()
	sess, err := session.Dial(ctx, client, ns, name, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()
	handshake := time.Since(start)
	conn := sess.Conn()

	runCtx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- sess.Run(runCtx) }()

	// A non-incremental request forces a full repaint so the first sample
	// includes a complete frame; after that, incremental requests are what a
	// real client sends.
	if err := conn.RequestUpdate(false); err != nil {
		return err
	}
	sess.RequestUpdates(runCtx, *interval)

	sampleStart := time.Now()
	<-runCtx.Done()
	elapsed := time.Since(sampleStart)
	_ = sess.Close()
	if err := <-errCh; err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "warning: session ended with: %v\n", err)
	}

	stats := conn.Stats()
	width, height := conn.Size()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "\n== Server ==\n")
	fmt.Fprintf(w, "Desktop name:\t%s\n", conn.ServerName())
	fmt.Fprintf(w, "Framebuffer:\t%dx%d\n", width, height)
	fmt.Fprintf(w, "Security type:\t%s\n", securityName(conn.SecurityType()))
	fmt.Fprintf(w, "Server pixel format:\t%s\n", conn.ServerPixelFormat())
	fmt.Fprintf(w, "Negotiated format:\t%s\n", conn.PixelReader().Format())
	fmt.Fprintf(w, "Fast RGBA path:\t%t\n", conn.PixelReader().Format().IsRGBA())
	fmt.Fprintf(w, "Handshake:\t%s\n", handshake.Round(time.Millisecond))
	if !firstUpdate.IsZero() {
		fmt.Fprintf(w, "First frame:\t%s after request\n", firstUpdate.Sub(sampleStart).Round(time.Millisecond))
	}
	_ = w.Flush()

	fmt.Printf("\n== Encodings actually used (%s sample) ==\n", elapsed.Round(time.Millisecond))
	if len(stats.RectsByEncoding) == 0 {
		fmt.Println("  (no rectangles received — is the guest's screen static?)")
	} else {
		w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  ENCODING\tRECTS\tBYTES\tSHARE")
		type row struct {
			enc   rfb.Encoding
			rects uint64
			bytes uint64
		}
		var rows []row
		var totalBytes uint64
		for enc, n := range stats.RectsByEncoding {
			rows = append(rows, row{enc, n, stats.BytesByEncoding[enc]})
			totalBytes += stats.BytesByEncoding[enc]
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].bytes > rows[j].bytes })
		for _, r := range rows {
			share := 0.0
			if totalBytes > 0 {
				share = float64(r.bytes) / float64(totalBytes) * 100
			}
			fmt.Fprintf(w, "  %s\t%d\t%s\t%.1f%%\n", r.enc, r.rects, humanBytes(r.bytes), share)
		}
		_ = w.Flush()
	}

	fmt.Printf("\n== Extensions acknowledged by the server ==\n")
	if len(stats.AckedPseudoEncodings) == 0 {
		fmt.Println("  (none observed)")
	} else {
		var acked []string
		for enc := range stats.AckedPseudoEncodings {
			acked = append(acked, enc.String())
		}
		sort.Strings(acked)
		for _, a := range acked {
			fmt.Printf("  %s\n", a)
		}
	}
	if *audio {
		if stats.AckedPseudoEncodings[rfb.EncodingQEMUAudio] {
			fmt.Println("  -> QEMU audio IS available (the VM has a sound device and VNC audiodev)")
		} else {
			fmt.Println("  -> QEMU audio NOT acknowledged (no sound device on the VM, or no audiodev on the VNC display)")
		}
	}

	fmt.Printf("\n== Throughput ==\n")
	seconds := elapsed.Seconds()
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  Updates:\t%d (%.1f/s)\n", stats.Updates, float64(stats.Updates)/seconds)
	fmt.Fprintf(w, "  Rectangles:\t%d\n", stats.Rects)
	fmt.Fprintf(w, "  Bytes read:\t%s (%s/s, %s)\n",
		humanBytes(stats.BytesRead),
		humanBytes(uint64(float64(stats.BytesRead)/seconds)),
		humanBits(uint64(float64(stats.BytesRead)*8/seconds)))
	fmt.Fprintf(w, "  Decode time:\t%s (%.1f%% of wall clock)\n",
		stats.DecodeTime.Round(time.Millisecond),
		float64(stats.DecodeTime)/float64(elapsed)*100)
	if len(resizes) > 0 {
		fmt.Fprintf(w, "  Resizes:\t%s\n", strings.Join(resizes, " -> "))
	}
	fmt.Fprintf(w, "  Cursor updates:\t%d\n", cursorShapes)
	fmt.Fprintf(w, "  Non-black pixels:\t%d of %d\n", nonBlack, width*height)
	if bellCount > 0 {
		fmt.Fprintf(w, "  Bells:\t%d\n", bellCount)
	}
	if cutTexts > 0 {
		fmt.Fprintf(w, "  Clipboard messages:\t%d\n", cutTexts)
	}
	return w.Flush()
}

// buildEncodings assembles the advertised encoding list for the probe.
func buildEncodings(override string, quality, compress int, audio bool) ([]rfb.Encoding, error) {
	var encs []rfb.Encoding
	if override == "" {
		encs = append(encs, rfb.DefaultEncodings...)
	} else {
		for _, part := range strings.Split(override, ",") {
			enc, ok := encodingByName(strings.TrimSpace(part))
			if !ok {
				return nil, fmt.Errorf("unknown encoding %q", part)
			}
			encs = append(encs, enc)
		}
	}
	// Quality and compression are pseudo-encodings, so they ride the same list.
	// Without a quality level QEMU never emits JPEG at all, which is exactly
	// the behaviour this probe is here to measure.
	if quality >= 0 {
		encs = append(encs, rfb.QualityLevel(quality))
	}
	if compress >= 0 {
		encs = append(encs, rfb.CompressLevel(compress))
	}
	if audio {
		encs = append(encs, rfb.EncodingQEMUAudio)
	}
	return encs, nil
}

func encodingByName(name string) (rfb.Encoding, bool) {
	known := []rfb.Encoding{
		rfb.EncodingRaw, rfb.EncodingCopyRect, rfb.EncodingRRE, rfb.EncodingCoRRE,
		rfb.EncodingHextile, rfb.EncodingZlib, rfb.EncodingTight, rfb.EncodingZRLE,
		rfb.EncodingTRLE, rfb.EncodingCursor, rfb.EncodingXCursor, rfb.EncodingCursorPos,
		rfb.EncodingDesktopSize, rfb.EncodingExtendedDesktopSize, rfb.EncodingLastRect,
		rfb.EncodingQEMUAudio, rfb.EncodingQEMUExtendedKeyEvent, rfb.EncodingQEMULEDState,
		rfb.EncodingContinuousUpdates, rfb.EncodingFence, rfb.EncodingExtendedClipboard,
	}
	for _, e := range known {
		if strings.EqualFold(e.String(), name) {
			return e, true
		}
	}
	return 0, false
}

func securityName(t uint8) string {
	switch t {
	case 1:
		return "None (1)"
	case 2:
		return "VNC Authentication (2)"
	default:
		return fmt.Sprintf("%d", t)
	}
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

func humanBits(n uint64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2f Gbit/s", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.2f Mbit/s", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.2f kbit/s", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d bit/s", n)
	}
}
