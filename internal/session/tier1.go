// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"image"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/selkies"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// ErrNoFallback marks a Tier 1 failure that must not be routed around by a
// Tier 0 attempt: the agent explicitly refused the session, credentials were
// rejected, or the display is owned by another session. Callers test with
// errors.Is and surface the error instead of falling back.
var ErrNoFallback = errors.New("tier 1 refused; not falling back")

// tier1EstablishmentSeconds bounds the Tier 1 handshake and first-frame wait
// before the caller gives up and files the attempt as a recoverable failure
// (plan §7.2: a 5-second establishment deadline).
const tier1EstablishmentBudget = 5 * time.Second

// Tier1Config carries the knobs for one interactive Tier 1 session.
type Tier1Config struct {
	// Title is the window title, typically "namespace/workspace".
	Title string
	// Audio enables Opus decode and playback when the guest streams audio.
	// The viewer disables output itself if the window has no audio device.
	Audio bool
	// NumLockOn seeds the Control adapter's guest Num-Lock belief used for
	// numeric keypad translation; pass false when unknown.
	NumLockOn bool
	// StartupTimeout bounds the MODE-to-first-video-frame handshake. Zero
	// means the plan's 5-second establishment budget.
	StartupTimeout time.Duration
	// Fullscreen starts the session fullscreen.
	Fullscreen bool
	// ScaleQuality selects the scaling filter. Empty means [viewer.ScaleLinear].
	ScaleQuality viewer.ScaleQuality
	// NoVSync disables presentation synchronisation (default: on).
	NoVSync bool
	// Width and Height are the initial window size in pixels. Zero means
	// 1280x800 until the first frame reveals the guest and the window is
	// fitted to it.
	Width, Height int
	// VideoDec and AudioDec override the native FFmpeg/Opus decoders. Nil
	// uses the native dynamic loads; an override lets a caller pin a codec
	// implementation (and the tests avoid the native libraries entirely).
	VideoDec selkies.VideoDecoder
	AudioDec selkies.AudioDecoder
	// Logf, if set, receives session diagnostics.
	Logf func(format string, args ...any)
}

func (c Tier1Config) startupTimeout() time.Duration {
	if c.StartupTimeout <= 0 {
		return tier1EstablishmentBudget
	}
	return c.StartupTimeout
}

func (c Tier1Config) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// RunTier1 runs one interactive Tier 1 (Selkies) session to completion in be,
// dialling the in-guest agent through the instance proxy and presenting the
// decoded media as it arrives. Both the GUI shell and the connect command run
// this from the goroutine that owns the window, exactly as they run the RFB
// viewer.
//
// The connection lifecycle is contained here: the agent socket is closed when
// the session ends, whatever the reason.
//
// Return classification (test with [errors.Is]):
//
//   - nil: the user quit or ctx was cancelled; the caller ends the connection.
//   - an error wrapping [ErrNoFallback]: do not fall back — the agent refused
//     the session, credentials were rejected, or the display is owned
//     elsewhere.
//   - any other error: a recoverable dial, negotiation, startup or decode
//     failure the caller may recover from by opening a Tier 0 session. Whether
//     Tier 1 is attempted at all is decided once per connection by the caller,
//     which is what makes the fallback sticky.
func RunTier1(ctx context.Context, client *kwclient.Client, ns, name, agentBase string, be viewer.Backend, cfg Tier1Config) error {
	if be == nil {
		return fmt.Errorf("session: Tier 1 has no window")
	}

	conn, err := client.DialSelkies(ctx, ns, name, agentBase)
	if err != nil {
		if noFallbackDial(err) {
			return fmt.Errorf("%w: %v", ErrNoFallback, err)
		}
		return fmt.Errorf("tier 1: dial %s/%s: %w", ns, name, err)
	}
	conn.SetReadLimit(selkies.MaxMessageBytes)
	// The session's send side never closes conn; the caller owns its lifecycle.
	defer func() { _ = conn.Close() }()

	// The sink is a late binding: the viewer hands it to the produce worker it
	// spawns, and that assignment happens-before the worker starts the session
	// (the go statement is the synchronisation edge), so the reads below on the
	// session's read-loop goroutine always see it.
	var tier1Sink *viewer.Tier1Sink
	sess := selkies.NewSession(conn, selkies.SessionConfig{
		Audio:          cfg.Audio,
		NumLockOn:      cfg.NumLockOn,
		StartupTimeout: cfg.startupTimeout(),
		VideoDec:       cfg.VideoDec,
		AudioDec:       cfg.AudioDec,
		Logf:           cfg.logf,
	}, selkies.Sink{
		Video: func(frame *image.RGBA) { tier1Sink.Video(frame) },
		Audio: func(pcm []byte) { tier1Sink.Audio(pcm) },
		Control: func(evt selkies.ControlEvent) {
			switch evt.Kind {
			case selkies.EventClipboard:
				// Only text reaches the host clipboard today; binary payloads
				// are dropped. The reference client pastes text on arrival,
				// and the host clipboard owns one text slot.
				if !evt.Clipboard.Binary && evt.Clipboard.Text != "" {
					tier1Sink.GuestClipboard(evt.Clipboard.Text)
				}
			case selkies.EventCursor, selkies.EventMode, selkies.EventSettings:
				// The session restores the guest's native cursor and logged the
				// rest; the window has nothing to do with them.
			}
		},
	})

	runErr := viewer.RunTier1(ctx, be, sess.Control(), func(ctx context.Context, sink *viewer.Tier1Sink) error {
		tier1Sink = sink
		return sess.Run(ctx)
	}, viewer.Tier1Config{
		Title:        cfg.Title,
		Width:        cfg.Width,
		Height:       cfg.Height,
		Fullscreen:   cfg.Fullscreen,
		ScaleQuality: cfg.ScaleQuality,
		NoVSync:      cfg.NoVSync,
		Audio:        cfg.Audio,
		Logf:         cfg.logf,
	})
	if runErr == nil {
		return nil
	}
	if errors.Is(runErr, selkies.ErrRefused) {
		return fmt.Errorf("%w: %v", ErrNoFallback, runErr)
	}
	return runErr
}

// noFallbackDial reports whether a dial failure must not be routed around by a
// Tier 0 attempt. Per the plan, a rejected credential or an ownership conflict
// must not be evaded through an alternative transport.
func noFallbackDial(err error) bool {
	return errors.Is(err, kwclient.ErrUnauthorized) ||
		errors.Is(err, kwclient.ErrForbidden) ||
		errors.Is(err, kwclient.ErrSessionInUse)
}
