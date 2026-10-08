package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/session"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

type countedSDL struct {
	*viewer.SDLBackend
	uploads, presents, audioBytes int64
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
	}
	return nil
}

func (b *countedSDL) PlayPCM(data []byte) {
	b.SDLBackend.PlayPCM(data)
	b.audioBytes += int64(len(data))
}

func presentAgent(ctx context.Context, client *kwclient.Client, ns, name string, duration time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	backend := &countedSDL{SDLBackend: viewer.NewSDLBackend()}
	err := session.RunAgentPremium(ctx, client, ns, name, "agent-present-probe", backend, session.Tier1Config{
		Title: "Agent presentation proof", Width: 1280, Height: 800, NoResize: true, Audio: true,
		Logf: func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	fmt.Printf("uploaded_frames=%d presented_frames=%d queued_audio_bytes=%d\n", backend.uploads, backend.presents, backend.audioBytes)
	if err != nil {
		return err
	}
	if backend.uploads == 0 || backend.presents == 0 {
		return fmt.Errorf("agent-probe: no guest pixels presented")
	}
	if backend.audioBytes == 0 {
		return fmt.Errorf("agent-probe: no PCM reached the playback device")
	}
	return nil
}
