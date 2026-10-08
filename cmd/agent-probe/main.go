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
	"syscall"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/agent"
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
	namespace := fs.String("namespace", "", "workspace namespace")
	workspace := fs.String("workspace", "", "workspace name")
	duration := fs.Duration("duration", 10*time.Second, "media read window after admission")
	decode := fs.Bool("decode", true, "decode H.264/Opus with native libraries (proves decodability, not just bytes)")
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
	if *server == "" || *namespace == "" || *workspace == "" {
		fs.Usage()
		return fmt.Errorf("agent-probe: --server, --namespace and --workspace are required")
	}
	token := os.Getenv("KW_SESSION")
	if token == "" {
		return fmt.Errorf("agent-probe: KW_SESSION is not set")
	}
	client, err := kwclient.New(*server, kwclient.WithToken(token))
	if err != nil {
		return err
	}
	ticket, err := client.AgentAttach(ctx, *namespace, *workspace, "agent-probe")
	if err != nil {
		return fmt.Errorf("agent-probe: attach: %w", err)
	}
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
	if err := session.Keyframe(); err != nil {
		_ = session.Close()
		return fmt.Errorf("agent-probe: keyframe: %w", err)
	}
	runErr := session.Run(agent.Callbacks{
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
	return client.AgentRelease(ctx, *namespace, *workspace, ticket.ID)
}
