// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/agent"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/media"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

// ErrAgentInputPending marks guest input that has no transport yet: the
// premium agent session carries video, audio and resize, but the guest has
// no input-injection backend, so keys/pointer/clipboard cannot cross.
// View-only premium is the honest maximum until injection lands; the error
// stays typed so the UI can explain instead of dropping keystrokes.
var ErrAgentInputPending = errors.New("agent input injection not yet implemented")

// agentInput forwards window events to the held agent session. Resize is
// real (paired ACKs); everything else reports the pending state.
type agentInput struct {
	mu      sync.Mutex
	session *agent.Session
}

func (in *agentInput) attach(session *agent.Session) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.session = session
}

func (in *agentInput) Key(sym keysym.Keysym, down bool) error { return ErrAgentInputPending }
func (in *agentInput) Pointer(x, y int, mask rfb.ButtonMask) error {
	return ErrAgentInputPending
}
func (in *agentInput) Wheel(dx, dy int) error { return ErrAgentInputPending }
func (in *agentInput) SetClipboard(text string) error {
	return ErrAgentInputPending
}
func (in *agentInput) ResetKeys() error { return ErrAgentInputPending }

// RunAgentPremium opens the premium agent window: ticket, bridge, admit,
// then H.264/Opus decode into the shared Tier-1 presenter. Transport
// selection stays with the caller (explicit opt-in until the API reports
// transport truth); failures classify like Tier 1 — refusal/busy/auth
// never fall back silently, anything else may.
//
// Premium v1 is video + audio + resize. Guest input injection does not
// exist yet, so the window is observer-grade for typing: keys report
// ErrAgentInputPending instead of vanishing.
func RunAgentPremium(ctx context.Context, client *kwclient.Client, ns, name, participant string, be viewer.Backend, cfg Tier1Config) error {
	if be == nil {
		return fmt.Errorf("session: agent premium has no window")
	}
	if err := requireDecoder(); err != nil {
		return err
	}
	input := &agentInput{}
	runErr := viewer.RunTier1(ctx, be, input, agentProduce(client, ns, name, participant, input, cfg.Audio), viewer.Tier1Config{
		Title:        cfg.Title,
		Width:        cfg.Width,
		Height:       cfg.Height,
		Fullscreen:   cfg.Fullscreen,
		ScaleQuality: cfg.ScaleQuality,
		NoVSync:      cfg.NoVSync,
		NoResize:     cfg.NoResize,
		Transport:    "Agent",
		Audio:        cfg.Audio,
		Logf:         cfg.logf,
	})
	return MapAgentResult(ctx, runErr)
}

// AgentLive is one live premium window owned by the multi-window pump,
// mirroring [Tier1Live].
type AgentLive struct {
	// Detached is the window the pump steps. It is never nil.
	Detached *viewer.Tier1Detached
}

// OpenAgentPremium opens a premium window without driving it, for the
// multi-window pump. Same ownership/step/close contract as [OpenTier1].
func OpenAgentPremium(ctx context.Context, client *kwclient.Client, ns, name, participant string, be viewer.Backend, cfg Tier1Config) (*AgentLive, error) {
	if be == nil {
		return nil, fmt.Errorf("session: agent premium has no window")
	}
	if err := requireDecoder(); err != nil {
		return nil, err
	}
	input := &agentInput{}
	det, err := viewer.OpenTier1Detached(ctx, be, input, agentProduce(client, ns, name, participant, input, cfg.Audio), viewer.Tier1Config{
		Title:        cfg.Title,
		Width:        cfg.Width,
		Height:       cfg.Height,
		Fullscreen:   cfg.Fullscreen,
		ScaleQuality: cfg.ScaleQuality,
		NoVSync:      cfg.NoVSync,
		NoResize:     cfg.NoResize,
		Transport:    "Agent",
		Audio:        cfg.Audio,
		Logf:         cfg.logf,
	})
	if err != nil {
		return nil, err
	}
	return &AgentLive{Detached: det}, nil
}

// agentProduce holds one admitted session per generation: ticket, bridge,
// decode, renew. Shared by the blocking and detached window paths so they
// cannot disagree about what a premium session is.
func agentProduce(client *kwclient.Client, ns, name, participant string, input *agentInput, wantAudio bool) func(context.Context, *viewer.Tier1Sink) error {
	return func(ctx context.Context, sink *viewer.Tier1Sink) error {
		return produceAgent(ctx, client, ns, name, participant, input, sink, wantAudio)
	}
}

// MapAgentResult classifies a premium terminal result: nil/ctx-done ends,
// refusal/busy/auth never fall back, anything else may recover via Tier 0.
func MapAgentResult(ctx context.Context, runErr error) error {
	if runErr == nil || ctx.Err() != nil {
		return nil
	}
	if errors.Is(runErr, agent.ErrRefused) ||
		errors.Is(runErr, kwclient.ErrSessionInUse) ||
		errors.Is(runErr, kwclient.ErrUnauthorized) ||
		errors.Is(runErr, kwclient.ErrForbidden) {
		return fmt.Errorf("%w: %v", ErrNoFallback, runErr)
	}
	return runErr
}

// produceAgent holds one admitted session: ticket, bridge, decode, renew.
func produceAgent(ctx context.Context, client *kwclient.Client, ns, name, participant string, input *agentInput, sink *viewer.Tier1Sink, wantAudio bool) error {
	ticket, err := client.AgentAttach(ctx, ns, name, participant)
	if err != nil {
		return err
	}
	conn, _, err := client.DialAgentWS(ctx, ns, name)
	if err != nil {
		return err
	}
	session, err := agent.Attach(wsio.New(conn), ticket.Ticket)
	if err != nil {
		_ = conn.Close()
		return err
	}
	input.attach(session)
	video, err := media.NewH264()
	if err != nil {
		_ = session.Close()
		return err
	}
	defer video.Close()
	var audio *media.Opus
	if wantAudio {
		audio, err = media.NewOpus()
		if err != nil {
			_ = session.Close()
			return err
		}
		defer audio.Close()
	}
	renewCtx, stopRenew := context.WithCancel(context.Background())
	defer stopRenew()
	go renewAgent(renewCtx, client, ns, name, ticket, session)
	if err := session.Keyframe(); err != nil {
		_ = session.Close()
		return err
	}
	runErr := session.Run(agent.Callbacks{
		OnVideo: func(payload []byte) error {
			frame, err := video.Decode(payload)
			if err != nil {
				return fmt.Errorf("agent premium: H.264 decode: %w", err)
			}
			if frame == nil {
				return nil
			}
			sink.Video(frame)
			return nil
		},
		OnAudio: func(payload []byte) error {
			if audio == nil {
				return nil
			}
			pcm, err := audio.Decode(payload)
			if err != nil {
				return fmt.Errorf("agent premium: Opus decode: %w", err)
			}
			sink.Audio(pcm)
			return nil
		},
	}, time.Time{})
	_ = session.Close()
	return runErr
}

// renewAgent keeps the API-side session alive at half TTL; a failed renew
// ends the produce (the supervisor decides about Tier 0, never a silent
// re-attach here).
func renewAgent(ctx context.Context, client *kwclient.Client, ns, name string, ticket *kwclient.AgentTicket, session *agent.Session) {
	interval := time.Duration(ticket.TTLMs/2) * time.Millisecond
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := client.AgentRenew(ctx, ns, name, ticket.ID); err != nil {
				return
			}
		}
	}
}

// Resize asks the guest to adopt the window size; the ACK pairs by id.
func (in *agentInput) Resize(w, h int) error {
	in.mu.Lock()
	session := in.session
	in.mu.Unlock()
	if session == nil {
		return ErrAgentInputPending
	}
	return session.RequestResize(fmt.Sprintf("resize-%dx%d-%d", w, h, time.Now().UnixMilli()), w, h)
}
