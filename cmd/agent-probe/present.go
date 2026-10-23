package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/session"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

type countedSDL struct {
	*viewer.SDLBackend
	uploads, presents, audioBytes int64
	requested                     connection.DisplayMode
	snapshot                      connection.Snapshot
	requestedSent                 bool
	texture                       connection.DisplayMode
	modePresented                 bool
}

func (b *countedSDL) SetTextureSize(w, h int) error {
	if err := b.SDLBackend.SetTextureSize(w, h); err != nil {
		return err
	}
	b.texture = connection.DisplayMode{Width: w, Height: h}
	return nil
}

// Drive the exact local toolbar action path through the real viewer loop.
func (b *countedSDL) UpdateConnection(s connection.Snapshot) { b.snapshot = s }
func (b *countedSDL) FilterConnectionEvents(_ time.Time, events []viewer.Event) []viewer.Event {
	if b.requested.Valid() && !b.requestedSent && b.snapshot.Availability(connection.ResolutionAction(b.requested)).Enabled {
		b.requestedSent = true
		return append(events, viewer.EventConnectionAction{Action: connection.ResolutionAction(b.requested)})
	}
	return events
}

func (b *countedSDL) Upload(rect viewer.Rect, pixels []byte, stride int) error {
	if err := b.SDLBackend.Upload(rect, pixels, stride); err != nil {
		return err
	}
	b.uploads++
	return nil
}

func (b *countedSDL) Present(rect viewer.Rect, overlay viewer.Overlay) error {
	if err := b.SDLBackend.Present(rect, overlay); err != nil {
		return err
	}
	if !rect.Empty() && b.uploads > 0 {
		b.presents++
		if b.requestedSent && b.texture == b.requested {
			b.modePresented = true
		}
	}
	return nil
}

func (b *countedSDL) PlayPCM(data []byte) {
	b.SDLBackend.PlayPCM(data)
	b.audioBytes += int64(len(data))
}

func presentAgent(ctx context.Context, client *kwclient.Client, ns, name string, duration time.Duration, resizeGuest bool, requested connection.DisplayMode) error {
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	backend := &countedSDL{SDLBackend: viewer.NewSDLBackend(), requested: requested}
	if requested.Valid() {
		resizeGuest = true
	}
	// Unpinned: the viewer opens at the fallback size, then steers a
	// resizable guest to the host-fitting 16:9 mode on the first frame
	// and follows it, instead of pinning 1280x800 against the guest.
	err := session.RunAgentPremium(ctx, client, ns, name, "agent-present-probe", backend, session.Tier1Config{
		Title: "Agent presentation proof", NoResize: !resizeGuest, Audio: true,
		Logf: func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	fmt.Printf("uploaded_frames=%d presented_frames=%d queued_audio_bytes=%d\n", backend.uploads, backend.presents, backend.audioBytes)
	if err != nil {
		return err
	}
	if backend.uploads == 0 || backend.presents == 0 {
		return fmt.Errorf("agent-probe: no guest pixels presented")
	}
	if requested.Valid() {
		fmt.Printf("resize_action_sent=%v requested_mode_presented=%v texture=%s\n", backend.requestedSent, backend.modePresented, backend.texture)
		if !backend.modePresented {
			return fmt.Errorf("agent-probe: requested guest mode never presented")
		}
		return nil
	}
	if backend.audioBytes == 0 {
		return fmt.Errorf("agent-probe: no PCM reached the playback device")
	}
	return nil
}
