// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Command agent-probe is the headless premium-transport diagnostic: attach
// a workspace guest agent through the proxy bridge, demand a keyframe,
// decode H.264/Opus with the native libraries, and report what flowed.
// It never opens a window and never falls back — a failed probe is a
// failed probe, not a Tier-0 session.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/agent"
	"github.com/kube-workspaces/desktop-client/internal/cmdutil"
	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/media"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

func main() {
	runtime.LockOSThread()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agent-probe:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent-probe", flag.ContinueOnError)
	server := fs.String("server", "", "platform HTTP(S) origin (uses KW_SESSION from environment)")
	profile := fs.String("profile", "", "stored profile (used when --server is omitted)")
	resize := fs.String("resize", "", "request and verify a guest-pixel mode, WIDTHxHEIGHT (allow at least 42s duration)")
	namespace := fs.String("namespace", "", "workspace namespace")
	workspace := fs.String("workspace", "", "workspace name")
	duration := fs.Duration("duration", 10*time.Second, "media read window after admission")
	decode := fs.Bool("decode", true, "decode H.264/Opus with native libraries (proves decodability, not just bytes)")
	present := fs.Bool("present", false, "open the real SDL agent viewer and verify rendering/audio queueing for the duration")
	resizeGuest := fs.Bool("resize-guest", false, "enable guest resolution changes during the presentation proof")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-probe --server ORIGIN --namespace NS --workspace NAME [--duration D] [--decode=false]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *namespace == "" || *workspace == "" {
		fs.Usage()
		return fmt.Errorf("agent-probe: --server, --namespace and --workspace are required")
	}
	var client *kwclient.Client
	var err error
	if *server == "" {
		client, _, err = cmdutil.For(kwclient.Version, *profile)
	} else {
		token := os.Getenv("KW_SESSION")
		if token == "" {
			return fmt.Errorf("agent-probe: KW_SESSION is not set")
		}
		client, err = kwclient.New(*server, kwclient.WithToken(token))
	}
	if err != nil {
		return err
	}
	var requested connection.DisplayMode
	if *resize != "" {
		w, h, ok := strings.Cut(*resize, "x")
		requested.Width, _ = strconv.Atoi(w)
		requested.Height, _ = strconv.Atoi(h)
		if !ok || !requested.Valid() || !*decode {
			return fmt.Errorf("--resize requires a valid WIDTHxHEIGHT and decoding")
		}
	}
	if *present {
		return presentAgent(ctx, client, *namespace, *workspace, *duration, *resizeGuest, requested)
	}
	ticket, err := client.AgentAttach(ctx, *namespace, *workspace, "agent-probe")
	if err != nil {
		return fmt.Errorf("agent-probe: attach: %w", err)
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.AgentRelease(releaseCtx, *namespace, *workspace, ticket.ID); err != nil {
			fmt.Fprintln(os.Stderr, "agent-probe: release:", err)
		}
	}()
	conn, resp, err := client.DialAgentWS(ctx, *namespace, *workspace)
	if err != nil {
		return fmt.Errorf("agent-probe: dial: %w", err)
	}
	_ = resp
	session, err := agent.Attach(wsio.New(conn), ticket.Ticket)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("agent-probe: handshake: %w", err)
	}
	defer func() { _ = session.Close() }()
	w, h := session.CaptureSize()
	fmt.Printf("capture=%dx%d modes=%v resize_available=%v video=%+v\n", w, h, session.DisplayModes(), session.ResizeAvailable(), session.VideoDetails())
	if requested.Valid() {
		if !session.ResizeAvailable() {
			return fmt.Errorf("agent-probe: guest resize unavailable")
		}
		if err := session.RequestResize("probe-resize", requested.Width, requested.Height); err != nil {
			return err
		}
	}
	var video *media.H264
	var audio *media.Opus
	if *decode {
		video, err = media.NewH264()
		if err != nil {
			_ = session.Close()
			return fmt.Errorf("agent-probe: H.264 decoder unavailable: %w", err)
		}
		defer video.Close()
		audio, err = media.NewOpus()
		if err != nil {
			_ = session.Close()
			return fmt.Errorf("agent-probe: Opus decoder unavailable: %w", err)
		}
		defer audio.Close()
	}
	var videoFrames, audioFrames int64
	var videoBytes, audioBytes int64
	var decodedVideo, decodedAudio int64
	var resizeACK, resizeFrame bool
	if err := session.Keyframe(); err != nil {
		_ = session.Close()
		return fmt.Errorf("agent-probe: keyframe: %w", err)
	}
	runErr := session.Run(agent.Callbacks{
		OnResizeResult: func(id string, payload map[string]any) {
			fmt.Printf("resize_id=%s actual=%v reason=%v codec_reconfigured=%v idr_sent=%v\n", id, payload["actual"], payload["reason"], payload["codecReconfigured"], payload["idrSent"])
			actual, _ := payload["actual"].(map[string]any)
			w, h := agent.Dimensions(actual)
			resizeACK = id == "probe-resize" && w == requested.Width && h == requested.Height && payload["codecReconfigured"] == true && payload["idrSent"] == true
		},
		OnVideo: func(payload []byte) error {
			videoFrames++
			videoBytes += int64(len(payload))
			if video == nil {
				return nil
			}
			frame, err := video.Decode(payload)
			if err != nil {
				return fmt.Errorf("agent-probe: H.264 decode: %w", err)
			}
			if frame != nil {
				decodedVideo++
				if requested.Valid() && (frame.Rect.Dx() == requested.Width || frame.Rect.Dx() == (requested.Width+15)&^15) && (frame.Rect.Dy() == requested.Height || frame.Rect.Dy() == (requested.Height+15)&^15) {
					resizeFrame = true
				}
			}
			return nil
		},
		OnAudio: func(payload []byte) error {
			audioFrames++
			audioBytes += int64(len(payload))
			if audio == nil {
				return nil
			}
			if _, err := audio.Decode(payload); err != nil {
				return fmt.Errorf("agent-probe: Opus decode: %w", err)
			}
			decodedAudio++
			return nil
		},
	}, time.Now().Add(*duration))
	_ = session.Close()
	if runErr != nil {
		return fmt.Errorf("agent-probe: session: %w", runErr)
	}
	status, err := client.AgentSessionStatus(ctx, *namespace, *workspace)
	if err != nil {
		return fmt.Errorf("agent-probe: status: %w", err)
	}
	counters := session.Counters()
	fmt.Printf("admitted=true video_frames=%d video_bytes=%d decoded_video=%d audio_frames=%d audio_bytes=%d decoded_audio=%d control=%d api_active=%v api_sessions=%d\n",
		videoFrames, videoBytes, decodedVideo, audioFrames, audioBytes, decodedAudio,
		counters.ControlFrames, status.Active, status.Sessions)
	if videoFrames == 0 {
		return fmt.Errorf("agent-probe: no video flowed")
	}
	if requested.Valid() {
		fmt.Printf("resize_ack_verified=%v resize_frame_verified=%v\n", resizeACK, resizeFrame)
		if !resizeACK || !resizeFrame {
			return fmt.Errorf("agent-probe: requested mode was not confirmed by guest readback and decoded video")
		}
	}
	return nil
}
