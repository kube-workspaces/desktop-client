// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
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
	// NoResize keeps the guest resolution fixed while resizing the window.
	NoResize bool
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

	requests := make(chan struct{}, 1)
	input := &tier1Input{}
	runErr := viewer.RunTier1(ctx, be, input, func(ctx context.Context, sink *viewer.Tier1Sink) error {
		dial := func(ctx context.Context) (*websocket.Conn, error) {
			return client.DialSelkies(ctx, ns, name, agentBase)
		}
		conn, started, err := dialTier1Display(ctx, cfg.startupTimeout(), sink, requests, dial, func(ctx context.Context) error {
			return client.Tier1Takeover(ctx, ns, name)
		})
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		return runTier1Generations(ctx, conn, input, sink, cfg, started, dial)
	}, viewer.Tier1Config{
		Title:        cfg.Title,
		Width:        cfg.Width,
		Height:       cfg.Height,
		Fullscreen:   cfg.Fullscreen,
		ScaleQuality: cfg.ScaleQuality,
		NoVSync:      cfg.NoVSync,
		NoResize:     cfg.NoResize,
		Takeover: func() {
			select {
			case requests <- struct{}{}:
			default:
			}
		},
		Audio: cfg.Audio,
		Logf:  cfg.logf,
	})
	return MapTier1Result(ctx, runErr)
}

// MapTier1Result classifies a Tier 1 terminal result the way [RunTier1]
// classifies its return, so the detached (multi-window) path and the
// blocking path cannot disagree about what a failure means:
//
//   - nil: the user quit or ctx was cancelled; the caller ends the session.
//   - an error wrapping [ErrNoFallback]: do not fall back — the agent refused
//     the session, credentials were rejected, or the display is owned
//     elsewhere.
//   - any other error: a recoverable failure the caller may recover from by
//     opening a Tier 0 session.
func MapTier1Result(ctx context.Context, runErr error) error {
	if runErr == nil || ctx.Err() != nil {
		return nil
	}
	if errors.Is(runErr, selkies.ErrRefused) {
		return fmt.Errorf("%w: %v", ErrNoFallback, runErr)
	}
	return runErr
}

// Tier1Live is one live Tier 1 window owned by the multi-window pump: the
// detached presenter plus the session produce worker behind it.
type Tier1Live struct {
	// Detached is the window the pump steps. It is never nil.
	Detached *viewer.Tier1Detached
}

// OpenTier1 opens a Tier 1 window without driving it, for the multi-window
// pump that steps every live window cooperatively. The caller feeds routed
// events to Detached.Step, waits on Detached.IdleWait, closes with
// Detached.Close, and classifies the terminal result with
// [MapTier1Result]. It must be called from the goroutine that owns the main
// OS thread, like every [viewer.Backend] window.
func OpenTier1(ctx context.Context, client *kwclient.Client, ns, name, agentBase string, be viewer.Backend, cfg Tier1Config) (*Tier1Live, error) {
	if be == nil {
		return nil, fmt.Errorf("session: Tier 1 has no window")
	}

	requests := make(chan struct{}, 1)
	input := &tier1Input{}
	det, err := viewer.OpenTier1Detached(ctx, be, input, func(ctx context.Context, sink *viewer.Tier1Sink) error {
		dial := func(ctx context.Context) (*websocket.Conn, error) {
			return client.DialSelkies(ctx, ns, name, agentBase)
		}
		conn, started, err := dialTier1Display(ctx, cfg.startupTimeout(), sink, requests, dial, func(ctx context.Context) error {
			return client.Tier1Takeover(ctx, ns, name)
		})
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		return runTier1Generations(ctx, conn, input, sink, cfg, started, dial)
	}, viewer.Tier1Config{
		Title:        cfg.Title,
		Width:        cfg.Width,
		Height:       cfg.Height,
		Fullscreen:   cfg.Fullscreen,
		ScaleQuality: cfg.ScaleQuality,
		NoVSync:      cfg.NoVSync,
		NoResize:     cfg.NoResize,
		Takeover: func() {
			select {
			case requests <- struct{}{}:
			default:
			}
		},
		Audio: cfg.Audio,
		Logf:  cfg.logf,
	})
	if err != nil {
		return nil, err
	}
	return &Tier1Live{Detached: det}, nil
}

// dialTier1Display keeps a contended display in the consent UI instead of
// attempting a different transport. Each dial has its own first-frame budget;
// time spent waiting for another owner is not agent establishment time.
func dialTier1Display(ctx context.Context, budget time.Duration, sink *viewer.Tier1Sink,
	requests <-chan struct{}, dial func(context.Context) (*websocket.Conn, error),
	takeover func(context.Context) error) (*websocket.Conn, time.Time, error) {
	busy := false
	defer sink.DisplayBusy(false)
	for {
		started := time.Now()
		dialCtx, cancel := context.WithTimeout(ctx, budget)
		conn, err := dial(dialCtx)
		cancel()
		if err == nil {
			return conn, started, nil
		}
		if ctx.Err() != nil {
			return nil, started, ctx.Err()
		}
		if !errors.Is(err, kwclient.ErrSessionInUse) {
			if busy || noFallbackDial(err) {
				return nil, started, fmt.Errorf("%w: %w", ErrNoFallback, err)
			}
			return nil, started, err
		}
		busy = true
		sink.DisplayBusy(true)
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, started, ctx.Err()
		case <-timer.C:
		case <-requests:
			timer.Stop()
			// Revocation is explicit and bounded. The server still enforces
			// its fencing interval before granting the next ownership claim.
			takeCtx, takeCancel := context.WithTimeout(ctx, budget)
			err := takeover(takeCtx)
			takeCancel()
			if err != nil {
				return nil, started, fmt.Errorf("%w: take over display: %w", ErrNoFallback, err)
			}
		}
	}
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
	// cursorShapes counts decoded cursor shapes this generation, and
	// cursorGuestVisible is the guest-cursor visibility last requested.
	// Together they converge the guest cursor exactly once per state
	// change: visible until the first decoded shape proves the window
	// renders its own, hidden after — whichever of first-video and
	// first-shape arrives first. Both reset per generation; the installed
	// window shape is left alone (frozen, like the frame).
	cursorShapes := 0
	cursorGuestVisible := true
	for {
		active := false
		if conn != nil {
			if generation > 0 && ((cfg.VideoDec != nil && cfg.NewVideoDecoder == nil) ||
				(cfg.Audio && cfg.AudioDec != nil && cfg.NewAudioDecoder == nil)) {
				_ = conn.Close()
				return fmt.Errorf("tier 1: reconnect requires fresh injected decoders")
			}
			var sess *selkies.Session
			// syncCursorVisible asks the guest to show its cursor until a
			// decoded shape proves the window renders its own, then hides
			// it — sending only on change, so steady state is silent.
			syncCursorVisible := func() {
				want := cursorShapes == 0
				if want == cursorGuestVisible {
					return
				}
				cursorGuestVisible = want
				if err := sess.Control().SetCursorVisible(want); err != nil {
					cfg.logf("Tier 1 cursor visibility: %v", err)
				}
			}
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
						syncCursorVisible()
					}
					sink.Video(frame)
				},
				Audio: func(pcm []byte) {
					if active {
						sink.Audio(pcm)
					}
				},
				Control: func(evt selkies.ControlEvent) {
					switch evt.Kind {
					case selkies.EventClipboard:
						if !evt.Clipboard.Binary {
							sink.GuestClipboard(evt.Clipboard.Text)
						}
					case selkies.EventCursor:
						shape, err := decodeCursorShape(evt.Cursor)
						if err != nil {
							cfg.logf("Tier 1 cursor shape: %v", err)
							return
						}
						cursorShapes++
						sink.GuestCursor(shape)
						syncCursorVisible()
					}
				},
			})
			lastErr = sess.Run(ctx)
			_ = conn.Close() // unblock any failed input write before detaching
			input.attach(nil, nil)
			generation++
			// The next generation renegotiates visibility from scratch;
			// the installed window shape stays frozen meanwhile.
			cursorShapes = 0
			cursorGuestVisible = true
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

// decodeCursorShape renders an agent cursor shape into window pixels. A
// hidden shape (handle 0 or empty) decodes to a pixel-less shape, which the
// window hides; anything the PNG decoder rejects is an error, never a
// half-installed cursor.
func decodeCursorShape(shape selkies.CursorShape) (*viewer.CursorShape, error) {
	out := &viewer.CursorShape{HotX: shape.HotX, HotY: shape.HotY}
	if !shape.Visible() {
		return out, nil
	}
	raw, err := base64.StdEncoding.DecodeString(shape.Data)
	if err != nil {
		return nil, fmt.Errorf("tier 1: cursor image: %w", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("tier 1: cursor image: %w", err)
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("tier 1: cursor image is %dx%d", w, h)
	}
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(rgba, rgba.Bounds(), img, bounds.Min, draw.Src)
	out.Pix, out.W, out.H = rgba.Pix, w, h
	return out, nil
}

// noFallbackDial reports whether a dial failure must not be routed around by a
// Tier 0 attempt. Per the plan, a rejected credential or an ownership conflict
// must not be evaded through an alternative transport.
func noFallbackDial(err error) bool {
	return errors.Is(err, kwclient.ErrUnauthorized) ||
		errors.Is(err, kwclient.ErrForbidden) ||
		errors.Is(err, kwclient.ErrSessionInUse)
}
