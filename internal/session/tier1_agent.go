// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"image"
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

// ErrAgentInputPending marks an unavailable or not-yet-attached input backend.
var ErrAgentInputPending = errors.New("agent input injection unavailable")

// agentInput forwards window events to the held agent session. Resize is
// capability-gated and paired; input is sent only when the guest advertises it.
type agentInput struct {
	mu                      sync.Mutex
	session                 *agent.Session
	keys                    map[keysym.Keysym]bool
	x, y                    int
	videoWidth, videoHeight int
}

func (in *agentInput) attach(session *agent.Session) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.session = session
	in.keys = make(map[keysym.Keysym]bool)
	in.videoWidth, in.videoHeight = 0, 0
	if session != nil {
		in.videoWidth, in.videoHeight = session.CaptureSize()
	}
}

func cropAgentFrame(frame *image.RGBA, width, height int) *image.RGBA {
	if width < 1 || height < 1 || width > 8192 || height > 8192 {
		return frame
	}
	// An IDR can precede its resize ACK. Do not apply stale old-mode dimensions
	// to a newly sized frame while waiting for the matching result.
	if frame.Rect.Dx() != (width+15)&^15 || frame.Rect.Dy() != (height+15)&^15 {
		return frame
	}
	rect := image.Rect(frame.Rect.Min.X, frame.Rect.Min.Y, frame.Rect.Min.X+width, frame.Rect.Min.Y+height)
	return frame.SubImage(rect).(*image.RGBA)
}

func (in *agentInput) Key(sym keysym.Keysym, down bool) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.session == nil {
		return ErrAgentInputPending
	}
	if err := in.session.Key(uint32(sym), down); err != nil {
		return err
	}
	if in.keys == nil {
		in.keys = make(map[keysym.Keysym]bool)
	}
	if down {
		in.keys[sym] = true
	} else {
		delete(in.keys, sym)
	}
	return nil
}
func (in *agentInput) Pointer(x, y int, mask rfb.ButtonMask) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.session == nil {
		return ErrAgentInputPending
	}
	in.x, in.y = x, y
	return in.session.Pointer(x, y, uint8(mask))
}
func (in *agentInput) Wheel(dx, dy int) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.session == nil {
		return ErrAgentInputPending
	}
	return in.session.Wheel(dx, dy)
}
func (in *agentInput) InputAvailable() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.session != nil && in.session.InputAvailable()
}
func (in *agentInput) ResizeAvailable() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.session != nil && in.session.ResizeAvailable()
}
func (in *agentInput) SetClipboard(text string) error {
	in.mu.Lock()
	session := in.session
	in.mu.Unlock()
	if session == nil {
		return ErrAgentInputPending
	}
	return session.RequestClipboard(fmt.Sprintf("clipboard-set-%d", time.Now().UnixNano()), &text)
}

func (in *agentInput) ClipboardAvailable() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.session != nil && in.session.ClipboardAvailable()
}

// Focus loss releases held keys and mouse buttons at the last pointer position.
func (in *agentInput) ResetKeys() error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.session == nil || !in.session.InputAvailable() {
		return nil
	}
	var result error
	for key := range in.keys {
		result = errors.Join(result, in.session.Key(uint32(key), false))
	}
	clear(in.keys)
	return errors.Join(result, in.session.Pointer(in.x, in.y, 0))
}

// RunAgentPremium opens the premium agent window: ticket, bridge, admit,
// then H.264/Opus decode into the shared Tier-1 presenter. Transport
// selection stays with the caller (explicit opt-in until the API reports
// transport truth); failures classify like Tier 1 — refusal/busy/auth
// never fall back silently, anything else may.
//
// Input and resize remain view-only when the guest does not advertise them.
func RunAgentPremium(ctx context.Context, client *kwclient.Client, ns, name, participant string, be viewer.Backend, cfg Tier1Config) error {
	if be == nil {
		return fmt.Errorf("session: agent premium has no window")
	}
	if err := requireDecoder(); err != nil {
		return err
	}
	input := &agentInput{}
	runErr := viewer.RunTier1(ctx, be, input, agentProduce(client, ns, name, participant, input, cfg.Audio, cfg.logf), viewer.Tier1Config{
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
	det, err := viewer.OpenTier1Detached(ctx, be, input, agentProduce(client, ns, name, participant, input, cfg.Audio, cfg.logf), viewer.Tier1Config{
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
func agentProduce(client *kwclient.Client, ns, name, participant string, input *agentInput, wantAudio bool, logf func(string, ...any)) func(context.Context, *viewer.Tier1Sink) error {
	return func(ctx context.Context, sink *viewer.Tier1Sink) error {
		return produceAgent(ctx, client, ns, name, participant, input, sink, wantAudio, logf)
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
func produceAgent(ctx context.Context, client *kwclient.Client, ns, name, participant string, input *agentInput, sink *viewer.Tier1Sink, wantAudio bool, logf func(string, ...any)) error {
	ticket, err := client.AgentAttach(ctx, ns, name, participant)
	if err != nil {
		return err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.AgentRelease(releaseCtx, ns, name, ticket.ID)
	}()
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
	defer input.attach(nil)
	defer session.Close() //nolint:errcheck // release on every initialization failure
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-done:
		}
	}()
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
	clipboardCtx, stopClipboard := context.WithCancel(ctx)
	defer stopClipboard()
	if session.ClipboardAvailable() || session.TelemetryAvailable() {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-clipboardCtx.Done():
					return
				case <-ticker.C:
					if session.TelemetryAvailable() {
						if err := session.RequestTelemetry(); err != nil {
							_ = session.Close()
							return
						}
					}
					if session.ClipboardAvailable() {
						if err := session.RequestClipboard(fmt.Sprintf("clipboard-get-%d", time.Now().UnixNano()), nil); err != nil {
							_ = session.Close()
							return
						}
					}
				}
			}
		}()
	}
	var lastClipboard *string
	runErr := session.Run(agent.Callbacks{
		OnTelemetry: func(payload map[string]any) {
			if logf != nil {
				logf("agent telemetry: input=%v dropped=%v session=%v", payload["inputEvents"], payload["inputDropped"], payload["session"])
			}
		},
		OnResizeResult: func(id string, payload map[string]any) {
			if actual, ok := payload["actual"].(map[string]any); ok && payload["codecReconfigured"] == true && payload["idrSent"] == true {
				if width, height := agent.Dimensions(actual); width != 0 && height != 0 {
					input.mu.Lock()
					input.videoWidth, input.videoHeight = width, height
					input.mu.Unlock()
				}
			}
			if logf != nil {
				logf("agent resize %s: requested=%v actual=%v codecReconfigured=%v idrSent=%v reason=%v",
					id, payload["requested"], payload["actual"], payload["codecReconfigured"], payload["idrSent"], payload["reason"])
			}
		},
		OnClipboard: func(_ string, text *string, err error) error {
			if err != nil {
				return err
			}
			if text != nil && (lastClipboard == nil || *lastClipboard != *text) {
				copy := *text
				lastClipboard = &copy
				sink.GuestClipboard(copy)
			}
			return nil
		},
		OnVideo: func(payload []byte) error {
			frame, err := video.Decode(payload)
			if err != nil {
				return fmt.Errorf("agent premium: H.264 decode: %w", err)
			}
			if frame == nil {
				return nil
			}
			input.mu.Lock()
			width, height := input.videoWidth, input.videoHeight
			input.mu.Unlock()
			sink.Video(cropAgentFrame(frame, width, height))
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
				_ = session.Close()
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
	if !session.ResizeAvailable() {
		return ErrAgentInputPending
	}
	return session.RequestResize(fmt.Sprintf("resize-%dx%d-%d", w, h, time.Now().UnixMilli()), w, h)
}
