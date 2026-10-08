// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"time"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

// ConnectionChrome is optional client-owned chrome around a content backend.
// UI implementations live outside viewer to keep transport/window loops free
// of widget dependencies. External and single-window pumps use the same filter.
type ConnectionChrome interface {
	UpdateConnection(connection.Snapshot)
	FilterConnectionEvents(time.Time, []Event) []Event
}

// EventConnectionAction is a local toolbar command, never remote input.
type EventConnectionAction struct{ Action connection.Action }

func (EventConnectionAction) isViewerEvent() {}

// FilterConnectionInput updates chrome from authoritative loop state before
// routing input. Backends without chrome preserve their existing behaviour.
func FilterConnectionInput(be Backend, now time.Time, s connection.Snapshot, events []Event) []Event {
	if chrome, ok := be.(ConnectionChrome); ok {
		chrome.UpdateConnection(s)
		return chrome.FilterConnectionEvents(now, events)
	}
	return events
}

// SDLBackendFor unwraps a decorated content backend for the one SDL event pump.
func SDLBackendFor(be Backend) (*SDLBackend, bool) {
	if wrapped, ok := be.(interface{ NativeBackend() Backend }); ok {
		be = wrapped.NativeBackend()
	}
	b, ok := be.(*SDLBackend)
	return b, ok
}

// ChromePresenter keeps toolbar pixels separate from status/consent overlays
// and draws all layers in a single presentation.
type ChromePresenter interface {
	SetChromeSize(int, int) error
	UploadChrome(Rect, []byte, int) error
	PresentChrome(Rect, Overlay, Rect) error
}

func ConnectionState(status Status) connection.State {
	switch status {
	case StatusLive:
		return connection.Connected
	case StatusConnecting:
		return connection.Connecting
	case StatusReconnecting:
		return connection.Reconnecting
	case StatusDisplayInUse:
		return connection.Busy
	default:
		return connection.Failed
	}
}

func (v *Viewer) connectionSnapshot() connection.Snapshot {
	status, _ := v.Status()
	s := connection.Snapshot{Workspace: v.cfg.Title, Surface: connection.Desktop,
		State: ConnectionState(status), Role: connection.Exclusive, Transport: "RFB",
		Clipboard: !v.clipboardDisabled, ResizeGuest: !v.resizeDisabled, Muted: v.muted,
		FPS: v.lastFPS, Kbps: v.lastKbps, HasMetrics: true,
		Capabilities: connection.Capabilities{SpecialKeys: true, TypeClipboard: true, ClipboardSync: true, SharedControl: v.controlAction() != nil}}
	if v.conn == nil && s.State == connection.Connected {
		s.State = connection.Connecting
	}
	if v.controlAction() != nil {
		s.Role = connection.Controller
	}
	if v.readOnly() {
		s.Role = connection.Observer
	}
	if v.conn != nil {
		s.Capabilities.GuestResize = v.conn.Stats().AckedPseudoEncodings[rfb.EncodingExtendedDesktopSize]
		s.Capabilities.Audio = v.audioOpened
	}
	return s
}

func (v *Viewer) connectionAction(a connection.Action) error {
	if !v.connectionSnapshot().Availability(a).Enabled {
		return nil
	}
	switch a {
	case connection.Fullscreen:
		return v.toggleFullscreen()
	case connection.Disconnect:
		v.RequestDisconnect()
	case connection.SpecialKeys:
		return v.sendChord(keysym.ChordCtrlAltDel)
	case connection.TypeClipboard:
		v.startClipboardTyping()
	case connection.SharedControl:
		if h := v.controlAction(); h != nil {
			go h()
		}
	case connection.ClipboardSync:
		v.clipboardDisabled = !v.clipboardDisabled
		v.hostClip, v.clipboardHint = "", true
	case connection.GuestResize:
		v.resizeDisabled = !v.resizeDisabled
		if !v.resizeDisabled {
			v.scheduleGuestResize(time.Now(), v.winW, v.winH)
		} else {
			v.resizePending = false
		}
	case connection.FitDesktop:
		v.resizeDisabled, v.resizePending = true, false
		v.updatePresent()
	case connection.Audio:
		v.muted = !v.muted
		v.audioSink.CloseAudio()
		v.audioOpened, v.audioEnabled = false, false
	}
	v.markPresent()
	return nil
}

func (w *tier1Window) connectionSnapshot() connection.Snapshot {
	state := connection.Connecting
	if w.haveFrame {
		state = connection.Connected
	}
	if w.sink != nil {
		if w.sink.reconnecting.Load() {
			state = connection.Reconnecting
		}
		if w.sink.busy.Load() {
			state = connection.Busy
		}
	}
	transport := w.opts.Transport
	if transport == "" {
		transport = "Selkies"
	}
	// Agent premium v1 is observer-grade: real resize, no guest input or
	// clipboard yet. The badge must not advertise what the transport
	// cannot do.
	specialKeys, clipboardSync := true, true
	if transport == "Agent" {
		specialKeys, clipboardSync = false, false
	}
	return connection.Snapshot{Workspace: w.opts.Title, Surface: connection.Desktop,
		State: state, Role: connection.Exclusive, Transport: transport,
		Clipboard: !w.clipboardDisabled && w.opts.ClipboardInterval >= 0, ResizeGuest: !w.opts.NoResize, Muted: w.muted,
		Capabilities: connection.Capabilities{SpecialKeys: specialKeys, ClipboardSync: clipboardSync, GuestResize: true, Audio: w.audio != nil}}
}

func (w *tier1Window) connectionAction(a connection.Action) error {
	if !w.connectionSnapshot().Availability(a).Enabled {
		return nil
	}
	switch a {
	case connection.Fullscreen:
		return w.runHotkey(EventKey{Key: w.opts.FullscreenKey})
	case connection.Disconnect:
		w.requestDisconnect()
	case connection.SpecialKeys:
		return w.sendChord(keysym.ChordCtrlAltDel)
	case connection.ClipboardSync:
		w.clipboardDisabled = !w.clipboardDisabled
		w.hostClip, w.clipHint = "", true
	case connection.GuestResize:
		w.opts.NoResize = !w.opts.NoResize
		if !w.opts.NoResize {
			w.scheduleGuestResize(time.Now(), w.winW, w.winH)
		} else {
			w.resizePending = false
		}
	case connection.FitDesktop:
		w.opts.NoResize, w.resizePending = true, false
	case connection.Audio:
		w.muted = !w.muted
		w.audio.CloseAudio()
		if err := w.audio.OpenAudio(AudioFormat{Channels: 2, SampleRate: 48000, BytesPerSample: 2, LittleEndian: true}); err != nil {
			w.opts.logf("audio disabled: %v", err)
			w.audio = nil
		}
	}
	return nil
}
