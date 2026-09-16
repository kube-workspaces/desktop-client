// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package selkies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

// ProbeConfig requests a single software-encoded full-frame stream. Connecting
// as the primary client changes the agent's capture settings and may resize its
// display: use a dedicated spike guest, not an occupied interactive session.
type ProbeConfig struct {
	Width          int           `json:"width"`
	Height         int           `json:"height"`
	FPS            int           `json:"fps"`
	BitrateKbps    int           `json:"bitrate_kbps"`
	Audio          bool          `json:"audio"`
	Takeover       bool          `json:"takeover"`
	StartupTimeout time.Duration `json:"startup_timeout_ns"`
	Duration       time.Duration `json:"duration_ns"`
}

// DefaultProbeConfig is the initial 1080p30 software H.264 measurement preset.
func DefaultProbeConfig() ProbeConfig {
	return ProbeConfig{Width: 1920, Height: 1080, FPS: 30, BitrateKbps: 8000,
		Audio: true, StartupTimeout: 15 * time.Second, Duration: 60 * time.Second}
}

// Validate checks the probe's resource and timing bounds before opening a socket.
func (c ProbeConfig) Validate() error {
	if c.Width < 16 || c.Height < 16 || c.Width > 4096 || c.Height > 4096 ||
		c.FPS < 8 || c.FPS > 120 || c.BitrateKbps < 100 || c.BitrateKbps > 100000 ||
		c.StartupTimeout <= 0 || c.Duration <= 0 {
		return errors.New("selkies: invalid probe configuration")
	}
	return nil
}

func (c ProbeConfig) settings() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	// Do not request clipboard contents, microphone, gzip, or Opus RED. The
	// latter needs timestamp/deduplication support before it can be advertised.
	payload, err := json.Marshal(map[string]any{
		"displayId": "primary", "displayScale": 1, "useCssScaling": false,
		"manual_resolution": true, "manual_width": c.Width, "manual_height": c.Height,
		"initialClientWidth": c.Width, "initialClientHeight": c.Height,
		"encoder": "h264enc", "use_cpu": true, "video_fullcolor": false,
		"video_streaming_mode": true, "use_paint_over_quality": false,
		"framerate": c.FPS, "rate_control_mode": "cbr", "video_bitrate": c.BitrateKbps,
		"audio_bitrate": 128000, "audioRedundancy": false,
	})
	if err != nil {
		return nil, err
	}
	return append([]byte("SETTINGS,"), payload...), nil
}

// ProbeStats counts received application payload, not wire/TLS bytes or decoded
// and presented frames. Rates use the interval beginning at the first keyframe;
// startup bytes are excluded. No packet contents or server text are retained.
type ProbeStats struct {
	Revision         string  `json:"protocol_revision"`
	ElapsedSeconds   float64 `json:"elapsed_seconds"`
	FirstKeyframeMS  float64 `json:"first_keyframe_ms"`
	ApplicationBytes uint64  `json:"application_bytes"`
	VideoBytes       uint64  `json:"video_payload_bytes"`
	AudioBytes       uint64  `json:"audio_payload_bytes"`
	VideoFrames      uint64  `json:"received_video_frames"`
	Keyframes        uint64  `json:"keyframes"`
	Heartbeats       uint64  `json:"video_heartbeats"`
	AudioPackets     uint64  `json:"opus_packets"`
	ControlMessages  uint64  `json:"control_messages"`
	Width            uint16  `json:"width"`
	Height           uint16  `json:"height"`
	ReceivedFPS      float64 `json:"received_fps"`
	ApplicationMbps  float64 `json:"application_mbps"`
}

type incoming struct {
	kind int
	data []byte
	err  error
}

// Probe owns and closes conn, including on cancellation and errors. It waits for
// MODE websockets before sending SETTINGS, then requires a keyframe within the
// startup budget. A successful result requires sustained video (at least two
// frames), and Opus when requested. It does not imply successful media decoding.
func Probe(ctx context.Context, conn *websocket.Conn, cfg ProbeConfig) (stats ProbeStats, err error) {
	defer func() { _ = conn.Close() }()
	settings, err := cfg.settings()
	if err != nil {
		return stats, err
	}
	stats.Revision = Revision
	started := time.Now()
	var measuring time.Time
	defer func() {
		if !measuring.IsZero() {
			stats.ElapsedSeconds = time.Since(measuring).Seconds()
			stats.ReceivedFPS = float64(stats.VideoFrames) / stats.ElapsedSeconds
			stats.ApplicationMbps = float64(stats.ApplicationBytes) * 8 / stats.ElapsedSeconds / 1e6
		}
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	conn.SetReadLimit(MaxMessageBytes)
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

	write := func(data []byte) error {
		if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
			return err
		}
		return conn.WriteMessage(websocket.TextMessage, data)
	}
	timer := time.NewTimer(cfg.StartupTimeout)
	defer timer.Stop()
	ackTick := time.NewTicker(100 * time.Millisecond)
	defer ackTick.Stop()
	var gotMode, haveFrame bool
	var frameID uint16
	var frameAt time.Time
	for {
		select {
		case <-ctx.Done():
			return stats, ctx.Err()
		case <-timer.C:
			if measuring.IsZero() {
				return stats, errors.New("selkies: startup timed out waiting for H.264 keyframe")
			}
			if stats.VideoFrames < 2 {
				return stats, errors.New("selkies: no sustained H.264 stream")
			}
			if cfg.Audio && stats.AudioPackets == 0 {
				return stats, errors.New("selkies: no Opus packets received")
			}
			return stats, nil
		case <-ackTick.C:
			if haveFrame {
				// Repeat the latest ID when idle: the server uses ACKs for both
				// backpressure and liveness. These ACK receipt, not presentation.
				ack := fmt.Sprintf("CLIENT_FRAME_ACK %d %.3f", frameID, time.Since(frameAt).Seconds()*1000)
				if err := write([]byte(ack)); err != nil {
					return stats, fmt.Errorf("selkies: ACK write: %w", err)
				}
			}
		case msg := <-messages:
			if msg.err != nil {
				if ctx.Err() != nil {
					return stats, ctx.Err()
				}
				// Close reasons are guest-controlled and can contain sensitive text.
				var closed *websocket.CloseError
				if errors.As(msg.err, &closed) {
					return stats, fmt.Errorf("selkies: peer closed WebSocket (code %d)", closed.Code)
				}
				return stats, fmt.Errorf("selkies: read: %w", msg.err)
			}
			if !measuring.IsZero() {
				stats.ApplicationBytes += uint64(len(msg.data))
			}
			switch msg.kind {
			case websocket.TextMessage:
				if !utf8.Valid(msg.data) {
					return stats, fmt.Errorf("%w: invalid control UTF-8", ErrMalformed)
				}
				text := string(msg.data)
				if !measuring.IsZero() {
					stats.ControlMessages++
				}
				if strings.HasPrefix(text, "KILL ") || strings.HasPrefix(text, "AUTH_ERROR") {
					return stats, errors.New("selkies: agent refused session")
				}
				if text == "AUDIO_DISABLED" && cfg.Audio {
					return stats, errors.New("selkies: agent audio disabled")
				}
				if strings.HasPrefix(text, "MODE ") {
					if text != "MODE websockets" {
						return stats, fmt.Errorf("%w: expected WebSocket mode", ErrUnsupported)
					}
					if !gotMode {
						gotMode = true
						commands := [][]byte{settings, []byte("START_VIDEO"), []byte("STOP_AUDIO")}
						if cfg.Audio {
							commands[2] = []byte("START_AUDIO")
						}
						for _, command := range commands {
							if err := write(command); err != nil {
								return stats, fmt.Errorf("selkies: setup write: %w", err)
							}
						}
					}
				}
			case websocket.BinaryMessage:
				if !gotMode {
					return stats, fmt.Errorf("%w: media before MODE", ErrMalformed)
				}
				p, err := ParseBinary(msg.data)
				if err != nil {
					return stats, err
				}
				if p.Kind == Audio {
					if !measuring.IsZero() {
						stats.AudioPackets++
						stats.AudioBytes += uint64(len(p.Payload))
					}
					continue
				}
				haveFrame, frameID, frameAt = true, p.FrameID, time.Now()
				if len(p.Payload) == 0 {
					if !measuring.IsZero() {
						stats.Heartbeats++
					}
					continue
				}
				if p.Y != 0 {
					return stats, fmt.Errorf("%w: expected full-frame h264enc", ErrUnsupported)
				}
				if measuring.IsZero() {
					if !p.Key {
						continue
					}
					measuring = time.Now()
					stats.FirstKeyframeMS = measuring.Sub(started).Seconds() * 1000
					stats.ApplicationBytes = uint64(len(msg.data))
					timer.Reset(cfg.Duration)
				}
				stats.Width, stats.Height = p.Width, p.Height
				stats.VideoFrames++
				stats.VideoBytes += uint64(len(p.Payload))
				if p.Key {
					stats.Keyframes++
				}
			default:
				return stats, fmt.Errorf("%w: WebSocket message kind", ErrUnsupported)
			}
		}
	}
}
