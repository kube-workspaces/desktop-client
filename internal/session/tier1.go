// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"math/rand/v2"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/selkies"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// ErrNoFallback marks a Tier 1 failure that must not be routed around by a
// Tier 0 attempt: the agent explicitly refused the session, credentials were
// rejected, or the display is owned by another session. Callers test with
// errors.Is and surface the error instead of falling back.
var ErrNoFallback = errors.New("tier 1 refused; not falling back")

// tier1EstablishmentBudget bounds the Tier 1 handshake and first-frame wait
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
	// Decoder factories create fresh state for every reconnect. An injected
	// decoder above is single-use; supply a factory to support recovery with
	// custom decoders. Native decoders are recreated automatically.
	NewVideoDecoder func() selkies.VideoDecoder
	NewAudioDecoder func() selkies.AudioDecoder
	// RecoveryBudget bounds two reconnect attempts after a live drop. Zero
	// means ten seconds. A negative value disables reconnect (fixed sessions).
	RecoveryBudget time.Duration
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

// RunTier1 runs an interactive Tier 1 (Selkies) session to completion in be,
// dialling the in-guest agent through the instance proxy and presenting the
// decoded media as it arrives. Both the GUI shell and the connect command run
// this from the goroutine that owns the window, exactly as they run the RFB
// viewer.
//
// The connection lifecycle is contained here: a live drop gets up to two
// reconnect attempts within ten seconds in the same window. Sockets, input
// workers and decoder state are closed before another generation starts.
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

	started := time.Now()
	dialCtx, dialCancel := context.WithTimeout(ctx, cfg.startupTimeout())
	conn, err := client.DialSelkies(dialCtx, ns, name, agentBase)
	dialCancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		if noFallbackDial(err) {
			return fmt.Errorf("%w: %v", ErrNoFallback, err)
		}
		return fmt.Errorf("tier 1: dial %s/%s: %w", ns, name, err)
	}
	defer func() { _ = conn.Close() }()
	input := &tier1Input{}
	runErr := viewer.RunTier1(ctx, be, input, func(ctx context.Context, sink *viewer.Tier1Sink) error {
		return runTier1Generations(ctx, conn, input, sink, cfg, started, func(ctx context.Context) (*websocket.Conn, error) {
			return client.DialSelkies(ctx, ns, name, agentBase)
		})
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

// runTier1Generations is the sole owner of transport/decoder generations.
// A live drop gets at most two attempts within one total budget. Failure before
// the first frame falls back immediately; refusal never tries another route.
func runTier1Generations(ctx context.Context, conn *websocket.Conn, input *tier1Input, sink *viewer.Tier1Sink,
	cfg Tier1Config, started time.Time, dial func(context.Context) (*websocket.Conn, error)) error {
	budget := cfg.RecoveryBudget
	if budget == 0 {
		budget = 10 * time.Second
	}
	deadline := started.Add(cfg.startupTimeout())
	var recoveryDeadline time.Time
	attempts, generation := 0, 0
	var lastErr error
	for {
		active := false
		if conn != nil {
			if generation > 0 && ((cfg.VideoDec != nil && cfg.NewVideoDecoder == nil) ||
				(cfg.Audio && cfg.AudioDec != nil && cfg.NewAudioDecoder == nil)) {
				_ = conn.Close()
				return fmt.Errorf("tier 1: reconnect requires fresh injected decoders")
			}
			var sess *selkies.Session
			sess = selkies.NewSession(conn, selkies.SessionConfig{
				Audio: cfg.Audio, NumLockOn: cfg.NumLockOn,
				StartupTimeout: max(time.Nanosecond, time.Until(deadline)),
				VideoDec:       cfg.VideoDec, AudioDec: cfg.AudioDec, Logf: cfg.logf,
				NewVideoDecoder: cfg.NewVideoDecoder, NewAudioDecoder: cfg.NewAudioDecoder,
			}, selkies.Sink{
				Video: func(frame *image.RGBA) {
					if !active {
						// Reset guest input before publishing the new generation.
						if err := sess.Control().ResetKeys(); err != nil {
							_ = conn.Close()
							return
						}
						if err := sess.Control().Pointer(0, 0, 0); err != nil {
							_ = conn.Close()
							return
						}
						input.attach(sess.Control(), conn.Close)
						active = true
						cfg.logf("Tier 1 active (generation %d)", generation+1)
					}
					sink.Video(frame)
				},
				Audio: func(pcm []byte) {
					if active {
						sink.Audio(pcm)
					}
				},
				Control: func(evt selkies.ControlEvent) {
					if evt.Kind == selkies.EventClipboard && !evt.Clipboard.Binary {
						sink.GuestClipboard(evt.Clipboard.Text)
					}
				},
			})
			lastErr = sess.Run(ctx)
			_ = conn.Close() // unblock any failed input write before detaching
			input.attach(nil, nil)
			generation++
		}
		if ctx.Err() != nil {
			return nil
		}
		if lastErr == nil {
			lastErr = io.EOF
		}
		if errors.Is(lastErr, selkies.ErrRefused) || noFallbackDial(lastErr) {
			return fmt.Errorf("%w: %v", ErrNoFallback, lastErr)
		}
		if active {
			recoveryDeadline = time.Now().Add(budget)
			attempts = 0
			sink.Reconnecting()
		}
		if budget < 0 || recoveryDeadline.IsZero() || attempts >= 2 || time.Now().After(recoveryDeadline) {
			return fmt.Errorf("tier 1 unavailable; Tier 0 fallback: %w", lastErr)
		}
		attempts++
		cfg.logf("Tier 1 reconnect %d/2 after %v", attempts, lastErr)
		// Capped jitter avoids every disconnected viewer redialling together.
		wait := min(time.Duration(100+rand.IntN(150))*time.Millisecond*time.Duration(attempts), time.Until(recoveryDeadline))
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		deadline = time.Now().Add(cfg.startupTimeout())
		if recoveryDeadline.Before(deadline) {
			deadline = recoveryDeadline
		}
		dialCtx, cancel := context.WithDeadline(ctx, deadline)
		conn, lastErr = dial(dialCtx)
		cancel()
	}
}

// noFallbackDial reports whether a dial failure must not be routed around by a
// Tier 0 attempt. Per the plan, a rejected credential or an ownership conflict
// must not be evaded through an alternative transport.
func noFallbackDial(err error) bool {
	return errors.Is(err, kwclient.ErrUnauthorized) ||
		errors.Is(err, kwclient.ErrForbidden) ||
		errors.Is(err, kwclient.ErrSessionInUse)
}
