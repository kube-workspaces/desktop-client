// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"context"
	"errors"
	"image"
	"sync"
	"time"
)

// MediaFrames is the bounded handoff from a decoder worker to the SDL thread.
// Only decoded video may be overwritten; encoded references never enter it.
// PCM is capped at 200 ms; overflow drops the stale queue as one discontinuity.
type MediaFrames struct {
	mu                         sync.Mutex
	frame                      *image.RGBA
	pcm                        []byte
	droppedVideo, droppedAudio uint64
}

func (q *MediaFrames) Video(frame *image.RGBA) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.frame != nil {
		q.droppedVideo++
	}
	q.frame = frame
}

func (q *MediaFrames) Audio(pcm []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	const maxPCM = 48000 * 4 / 5
	if len(pcm) > maxPCM {
		q.droppedAudio++
		return
	}
	if len(q.pcm)+len(pcm) > maxPCM {
		q.pcm = nil
		q.droppedAudio++
	}
	q.pcm = append(q.pcm, pcm...)
}

func (q *MediaFrames) take() (*image.RGBA, []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	frame, pcm := q.frame, q.pcm
	q.frame, q.pcm = nil, nil
	return frame, pcm
}

// pending reports whether a frame or PCM is waiting to be drained, which a
// render loop uses to decide whether it may block in an event wait.
func (q *MediaFrames) pending() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.frame != nil || len(q.pcm) > 0
}

// MediaPresentationStats counts actual backend presentation calls and queue
// drops; it does not assert physical-display fps, audible sync or underruns.
type MediaPresentationStats struct {
	Presented    uint64 `json:"presented_frames"`
	DroppedVideo uint64 `json:"dropped_decoded_frames"`
	DroppedAudio uint64 `json:"audio_queue_resets"`
}

// PlayMedia runs the native-media diagnostic on the caller's window thread.
// produce runs off-thread and must stop on cancellation. Worker completion is
// joined before the backend/audio device is closed, including on window exit.
func PlayMedia(ctx context.Context, be Backend, audio bool, produce func(context.Context, *MediaFrames) error) (stats MediaPresentationStats, err error) {
	if err = be.Open(WindowOptions{Title: "Kube Workspaces — Tier 1 media diagnostic", Width: 1280, Height: 720}); err != nil {
		return stats, err
	}
	defer be.Close()
	var sink AudioSink
	if audio {
		var ok bool
		sink, ok = be.(AudioSink)
		if !ok {
			return stats, errors.New("media viewer: backend has no audio sink")
		}
		if err = sink.OpenAudio(AudioFormat{Channels: 2, SampleRate: 48000, BytesPerSample: 2, LittleEndian: true}); err != nil {
			return stats, err
		}
		defer sink.CloseAudio()
	}
	ctx, cancel := context.WithCancel(ctx)
	q := &MediaFrames{}
	done := make(chan error, 1)
	go func() { done <- produce(ctx, q) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
		q.mu.Lock()
		stats.DroppedVideo, stats.DroppedAudio = q.droppedVideo, q.droppedAudio
		q.mu.Unlock()
	}()
	var width, height int
	var events []Event
	for {
		select {
		case <-ctx.Done():
			return stats, ctx.Err()
		case err = <-done:
			joined = true
			return stats, err
		default:
		}
		for _, ev := range be.PollEvents(events[:0]) {
			if _, quit := ev.(EventQuit); quit {
				return stats, context.Canceled
			}
		}
		frame, pcm := q.take()
		if sink != nil && len(pcm) > 0 {
			sink.PlayPCM(pcm)
		}
		if frame != nil {
			w, h := frame.Rect.Dx(), frame.Rect.Dy()
			if w != width || h != height {
				if err = be.SetTextureSize(w, h); err != nil {
					return stats, err
				}
				width, height = w, h
			}
			if err = be.Upload(Rect{W: w, H: h}, frame.Pix, frame.Stride); err != nil {
				return stats, err
			}
			bw, bh := be.Size()
			if err = be.Present(FitLetterbox(w, h, bw, bh), Overlay{}); err != nil {
				return stats, err
			}
			stats.Presented++
		}
		// Poll at most one display cadence; cancellation remains bounded.
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
