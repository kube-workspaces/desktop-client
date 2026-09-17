// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package selkies

import (
	"context"
	"fmt"
	"image"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/media"
)

// MediaSink receives Go-owned decoded buffers. Callbacks run on the decoding
// worker, never the SDL thread. A sink must bound its presentation queues.
type MediaSink struct {
	Video func(*image.RGBA)
	Audio func([]byte)
}

// DecodeStats distinguishes decoding from receiving and from presentation.
type DecodeStats struct {
	VideoFrames  uint64  `json:"decoded_video_frames"`
	AudioSamples uint64  `json:"decoded_audio_samples_per_channel"`
	DecodeMS     float64 `json:"decode_ms"`
}

// ProbeMedia exercises native decoding against the pinned wire protocol. It
// owns conn even if decoder initialization fails. This remains a diagnostic,
// not the automatic interactive transport selector or an ownership claim.
func ProbeMedia(ctx context.Context, conn *websocket.Conn, cfg ProbeConfig, sink MediaSink) (ProbeStats, DecodeStats, error) {
	defer func() { _ = conn.Close() }()
	var decoded DecodeStats
	video, err := media.NewH264()
	if err != nil {
		return ProbeStats{}, decoded, err
	}
	defer video.Close()
	var audio *media.Opus
	if cfg.Audio {
		audio, err = media.NewOpus()
		if err != nil {
			return ProbeStats{}, decoded, err
		}
		defer audio.Close()
	}
	stats, err := probe(ctx, conn, cfg, func(p Packet) error {
		started := time.Now()
		defer func() { decoded.DecodeMS += time.Since(started).Seconds() * 1000 }()
		if p.Kind == Audio {
			pcm, err := audio.Decode(p.Payload)
			if err != nil {
				return err
			}
			decoded.AudioSamples += uint64(len(pcm) / 4)
			if sink.Audio != nil {
				sink.Audio(pcm)
			}
			return nil
		}
		frame, err := video.Decode(p.Payload)
		if err != nil {
			return err
		}
		if frame == nil {
			return nil
		}
		if frame.Rect.Dx() != int(p.Width) || frame.Rect.Dy() != int(p.Height) {
			return fmt.Errorf("%w: decoded dimensions disagree with wire header", ErrMalformed)
		}
		decoded.VideoFrames++
		if sink.Video != nil {
			sink.Video(frame)
		}
		return nil
	})
	if err == nil && (decoded.VideoFrames < 2 || cfg.Audio && decoded.AudioSamples == 0) {
		err = fmt.Errorf("%w: no sustained decoded media", ErrMalformed)
	}
	return stats, decoded, err
}
