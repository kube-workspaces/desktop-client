// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package transport defines the shared interface for remote desktop connections.
package transport

import (
	"context"

	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

// Conn is a live remote desktop connection.
type Conn interface {
	// Run drives the read loop until the context is cancelled or the stream
	// ends. It returns nil on a clean close.
	Run(ctx context.Context) error

	// Close tears down the connection, releasing the server's session slot.
	Close() error

	// Size returns the current guest display dimensions in pixels.
	Size() (w, h int)

	// ServerName returns the desktop name reported by the server.
	ServerName() string

	// ServerPixelFormat returns the pixel format the server advertised at
	// startup.
	ServerPixelFormat() rfb.PixelFormat

	// SecurityType returns the security type that was negotiated.
	SecurityType() uint8

	// PixelReader returns the negotiated pixel converter.
	PixelReader() *rfb.PixelReader

	// SetDesktopSize asks the guest to resize its display.
	SetDesktopSize(w, h uint16) error

	// KeyEvent sends a physical key transition to the guest.
	KeyEvent(keysym uint32, down bool) error

	// PointerEvent sends a pointer transition (movement or button change).
	PointerEvent(x, y uint16, mask rfb.ButtonMask) error

	// CutText sends clipboard text to the guest.
	CutText(text string) error

	// WithFramebuffer calls fn with the current framebuffer. The framebuffer
	// must not be retained past the call.
	WithFramebuffer(fn func(*rfb.Framebuffer))

	// Stats returns a snapshot of the connection counters.
	Stats() rfb.Stats

	// RequestUpdate asks for a full or incremental repaint.
	RequestUpdate(incremental bool) error

	// AudioFormat returns the negotiated audio format, if any.
	AudioFormat() (rfb.AudioFormat, bool)

	// AudioOK reports whether the guest is ready to stream audio.
	AudioOK() bool

	// SetAudioFormat configures the audio stream.
	SetAudioFormat(fmt rfb.AudioFormat) error

	// EnableAudio asks the guest to start streaming audio.
	EnableAudio() error
}

// Config collects the callbacks used by a transport to deliver media and
// lifecycle events to the viewer.
type Config struct {
	OnFramebufferUpdate func(fb *rfb.Framebuffer, damage []rfb.Rect)
	OnResize            func(width, height int)
	OnCutText           func(text string)
	OnAudio             func(data []byte)
}
