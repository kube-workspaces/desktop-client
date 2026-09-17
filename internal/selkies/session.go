// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package selkies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/media"
)

// SessionConfig tunes an interactive Tier 1 session.
//
// Interactive sessions deliberately do not lock manual resolution: that mode
// forces the server to 1024x768 and hides the guest's real display. The guest
// keeps its current mode; window resizes are driven by explicit `r,<W>x<H>`
// messages through [Control.Resize].
type SessionConfig struct {
	// Audio requests Opus audio (START_AUDIO instead of STOP_AUDIO). Decode
	// failures are best-effort: a missing audio device disables audio, it does
	// not fail the session.
	Audio bool
	// NumLockOn seeds the Control adapter's guest Num Lock belief.
	NumLockOn bool
	// VideoBitrateKbps is the requested H.264 bitrate. Zero means 8000.
	VideoBitrateKbps int
	// Framerate is the requested encoding rate. Zero means 30.
	Framerate int
	// StartupTimeout bounds the MODE-to-first-video-frame handshake. Zero
	// means 10 seconds. On expiry the session fails so the caller can fall
	// back to Tier 0.
	StartupTimeout time.Duration
	// Logf receives diagnostics; nil silences them.
	Logf func(format string, args ...any)
	// VideoDec and AudioDec override the native decoders so tests can run
	// without the runtime FFmpeg/Opus libraries. Nil means "use the native
	// path".
	VideoDec VideoDecoder
	AudioDec AudioDecoder
}

func (c SessionConfig) framerate() int {
	if c.Framerate <= 0 {
		return 30
	}
	return c.Framerate
}

func (c SessionConfig) bitrate() int {
	if c.VideoBitrateKbps <= 0 {
		return 8000
	}
	return c.VideoBitrateKbps
}

func (c SessionConfig) startupTimeout() time.Duration {
	if c.StartupTimeout <= 0 {
		return 10 * time.Second
	}
	return c.StartupTimeout
}

func (c SessionConfig) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// VideoDecoder decodes one complete H.264 Annex-B access unit. Close must be
// safe to call once, serialized with Decode.
type VideoDecoder interface {
	Decode([]byte) (*image.RGBA, error)
	Close()
}

// AudioDecoder decodes one Opus packet into little-endian stereo PCM.
type AudioDecoder interface {
	Decode([]byte) ([]byte, error)
	Close()
}

// Sink receives decoded media and parsed control events from [Session.Run].
// Callbacks run on the session's goroutine and must not block for long: they
// are the only thing pacing the read loop.
type Sink struct {
	// Video receives decoded frames. It is nil-safe.
	Video func(*image.RGBA)
	// Audio receives decoded PCM. It is nil-safe.
	Audio func([]byte)
	// Control receives parsed server control events (clipboard pushes, cursor
	// shapes, settings, ...). Nil disables it.
	Control func(ControlEvent)
}

func (s Sink) video(frame *image.RGBA) {
	if s.Video != nil {
		s.Video(frame)
	}
}

func (s Sink) audio(pcm []byte) {
	if s.Audio != nil {
		s.Audio(pcm)
	}
}

func (s Sink) control(evt ControlEvent) {
	if s.Control != nil {
		s.Control(evt)
	}
}

// Session is a live interactive Tier 1 connection: the handshake and read loop
// behind a [Control] input adapter.
//
// Constructing a Session only requires a dialled socket, so a caller can build
// it before committing to the transport. [Session.Run] performs the MODE/SETTINGS
// handshake, waits for the first H.264 keyframe within the startup budget, then
// dispatches decoded frames and audio to the [Sink] until the context is
// cancelled, the peer closes, or a protocol error ends the session.
//
// [Session.Control] is safe to call before, during and after [Session.Run]: its
// writes are serialized with the session's own setup and ACK writes on the one
// socket, the way the reference client's one WS link carries both.
//
// A Session owns conn only during [Session.Run]: the send side never closes it
// and the caller remains responsible for its lifecycle. Run is safe to call and
// join once.
type Session struct {
	conn *websocket.Conn
	cfg  SessionConfig
	sink Sink

	ctrl   *Control
	parser ControlParser

	runOnce sync.Once
	ran     bool
	runErr  error
}

// NewSession builds an interactive session on conn. No I/O happens until Run.
func NewSession(conn *websocket.Conn, cfg SessionConfig, sink Sink) *Session {
	return &Session{
		conn: conn,
		cfg:  cfg,
		sink: sink,
		ctrl: NewControl(conn, cfg.NumLockOn),
	}
}

// Control returns the input adapter for this session.
func (s *Session) Control() *Control { return s.ctrl }

// Started reports whether Run has been called.
func (s *Session) Started() bool { return s.ran }

func (s *Session) settings() (string, error) {
	payload, err := json.Marshal(map[string]any{
		"displayId": "primary",
		"encoder":   "h264enc", "use_cpu": true, "video_streaming_mode": true,
		"framerate":         s.cfg.framerate(),
		"rate_control_mode": "cbr", "video_bitrate": s.cfg.bitrate() * 1000,
		"audio_bitrate": 128000, "audioRedundancy": false,
	})
	if err != nil {
		return "", err
	}
	return "SETTINGS," + string(payload), nil
}

// Run drives the session until ctx is cancelled or an error ends it. Run is
// safe to call more than once: only the first call performs I/O, and every call
// returns the same result.
func (s *Session) Run(ctx context.Context) error {
	s.runOnce.Do(func() {
		s.ran = true
		s.runErr = s.run(ctx)
	})
	return s.runErr
}

func (s *Session) run(ctx context.Context) error {
	video, err := s.videoDecoder()
	if err != nil {
		return err
	}
	defer video.Close()
	var audio AudioDecoder
	if s.cfg.Audio {
		audio, err = s.audioDecoder()
		if err != nil {
			s.cfg.logf("selkies: audio decode unavailable (%v); continuing without audio", err)
		} else {
			defer audio.Close()
		}
	}

	conn := s.conn
	conn.SetReadLimit(MaxMessageBytes)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()

	settings, err := s.settings()
	if err != nil {
		return err
	}

	messages := make(chan incoming, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			kind, data, readErr := conn.ReadMessage()
			select {
			case messages <- incoming{kind, data, readErr}:
			case <-ctx.Done():
				return
			}
			if readErr != nil {
				return
			}
		}
	}()
	defer func() {
		cancel()
		_ = conn.Close()
		<-readerDone
	}()

	write := func(text string) error { return s.ctrl.Send(text) }
	timer := time.NewTimer(s.cfg.startupTimeout())
	defer timer.Stop()
	ackTick := time.NewTicker(keyHeartbeatInterval)
	defer ackTick.Stop()
	var gotMode, haveVideo bool
	var frameID uint16
	var frameAt time.Time

	emit := func(message incoming) error {
		if message.err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var closed *websocket.CloseError
			if errors.As(message.err, &closed) {
				return fmt.Errorf("selkies: peer closed WebSocket (code %d)", closed.Code)
			}
			return fmt.Errorf("selkies: read: %w", message.err)
		}
		switch message.kind {
		case websocket.TextMessage:
			if !utf8.Valid(message.data) {
				return fmt.Errorf("%w: invalid control UTF-8", ErrMalformed)
			}
			text := string(message.data)
			if strings.HasPrefix(text, "KILL ") || strings.HasPrefix(text, "AUTH_ERROR") {
				return errors.New("selkies: agent refused session")
			}
			if text == "AUDIO_DISABLED" {
				s.cfg.logf("selkies: agent has no audio device")
				return nil
			}
			if strings.HasPrefix(text, "MODE ") {
				if text != "MODE websockets" {
					return fmt.Errorf("%w: expected WebSocket mode", ErrUnsupported)
				}
				if !gotMode {
					gotMode = true
					commands := []string{settings, "START_VIDEO", "STOP_AUDIO"}
					if s.cfg.Audio {
						commands[2] = "START_AUDIO"
					}
					for _, command := range commands {
						if err := write(command); err != nil {
							return fmt.Errorf("selkies: setup write: %w", err)
						}
					}
				}
				return nil
			}
			if evt, ok := s.parser.Parse(text); ok {
				s.sink.control(evt)
			}
			return nil

		case websocket.BinaryMessage:
			if !gotMode {
				return fmt.Errorf("%w: media before MODE", ErrMalformed)
			}
			p, err := ParseBinary(message.data)
			if err != nil {
				return err
			}
			if p.Kind == Audio {
				if audio != nil {
					pcm, err := audio.Decode(p.Payload)
					if err != nil {
						return fmt.Errorf("selkies: opus decode: %w", err)
					}
					s.sink.audio(pcm)
				}
				return nil
			}
			haveVideo, frameID, frameAt = true, p.FrameID, time.Now()
			if len(p.Payload) == 0 {
				return nil // video heartbeat
			}
			if p.Y != 0 {
				return fmt.Errorf("%w: expected full-frame h264enc", ErrUnsupported)
			}
			frame, err := video.Decode(p.Payload)
			if err != nil {
				return fmt.Errorf("selkies: H.264 decode: %w", err)
			}
			if frame == nil {
				return nil
			}
			if frame.Rect.Dx() != int(p.Width) || frame.Rect.Dy() != int(p.Height) {
				return fmt.Errorf("%w: decoded dimensions disagree with wire header", ErrMalformed)
			}
			s.sink.video(frame)
			return nil

		default:
			return fmt.Errorf("%w: WebSocket message kind", ErrUnsupported)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if !haveVideo {
				return errors.New("selkies: startup timed out waiting for H.264 video")
			}
			// The deadline only gates first video; later connect budget is
			// the reconnect supervisor's.
			timer.Stop()
		case <-ackTick.C:
			if haveVideo {
				// The server uses ACKs for both backpressure and liveness;
				// repeat the latest ID when idle. This acknowledges receipt,
				// not presentation. Serialized with input on the same link.
				if err := write(fmt.Sprintf("CLIENT_FRAME_ACK %d %.3f", frameID, time.Since(frameAt).Seconds()*1000)); err != nil {
					return fmt.Errorf("selkies: ACK write: %w", err)
				}
			}
		case message := <-messages:
			if err := emit(message); err != nil {
				return err
			}
		}
	}
}

// videoDecoder builds the session's video decoder, preferring an injected
// override (for tests) and otherwise the native FFmpeg binding.
func (s *Session) videoDecoder() (VideoDecoder, error) {
	if s.cfg.VideoDec != nil {
		return s.cfg.VideoDec, nil
	}
	return media.NewH264()
}

// audioDecoder builds the session's audio decoder, preferring an injected
// override (for tests) and otherwise the native Opus binding.
func (s *Session) audioDecoder() (AudioDecoder, error) {
	if s.cfg.AudioDec != nil {
		return s.cfg.AudioDec, nil
	}
	return media.NewOpus()
}
