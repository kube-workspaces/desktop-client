// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"fmt"
	"sort"
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

// AgentCounters is the live guest-agent telemetry the debug card reports.
// Plain values (not internal/agent types) so the session package can serve
// them without an import cycle: the window owns the presenter, the session
// owns the transport.
type AgentCounters struct {
	CaptureW, CaptureH int
	ControlFrames      uint64
	ControlBytes       uint64
	MediaBytes         uint64
	VideoFrames        uint64
	VideoBytes         uint64
	AudioFrames        uint64
	AudioBytes         uint64
	ResizeACKs         uint64
	Keyframes          uint64
	DisplayModes       int
}

// agentDebugger is implemented by the session-owned agent input when the
// guest negotiated an interactive agent transport. Absent (nil, or
// !ok) means the window has no live agent session to report.
type agentDebugger interface {
	DebugCounters() (AgentCounters, bool)
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
		Tier: "Tier 0", Proto: "RFB over WebSocket",
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
		stats := v.conn.Stats()
		s.Capabilities.GuestResize = stats.AckedPseudoEncodings[rfb.EncodingExtendedDesktopSize]
		s.Capabilities.Audio = v.audioOpened
		s.AudioLive = v.audioOpened && !v.muted
		s.BytesIn, s.BytesOut = stats.BytesRead, stats.BytesWritten
		s.HasActivity = true
		width, height := v.conn.Size()
		s.Extra = [][2]string{
			{"Framebuffer", formatFramebuffer(width, height)},
			{"Updates / rects", formatCountPair(stats.Updates, stats.Rects)},
			{"Encodings", formatTopEncodings(stats.BytesByEncoding)},
		}
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

// formatFramebuffer renders guest dimensions for the debug card. A zero
// size means the framebuffer is not established yet, never 0x0.
func formatFramebuffer(width, height int) string {
	if width <= 0 || height <= 0 {
		return "unknown"
	}
	return fmt.Sprintf("%d×%d", width, height)
}

// formatTopEncodings names up to three encodings by received bytes for the
// debug card, most first. Control-only sessions report none yet.
func formatTopEncodings(bytesByEncoding map[rfb.Encoding]uint64) string {
	type entry struct {
		name  string
		bytes uint64
	}
	var entries []entry
	for enc, n := range bytesByEncoding {
		if n == 0 {
			continue
		}
		entries = append(entries, entry{name: enc.String(), bytes: n})
	}
	if len(entries) == 0 {
		return "none yet"
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].bytes > entries[j].bytes })
	top := entries
	if len(top) > 3 {
		top = top[:3]
	}
	parts := make([]string, 0, len(top))
	for _, e := range top {
		parts = append(parts, e.name+" "+connection.FormatBytes(e.bytes))
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += " · " + p
	}
	return out
}

// formatAgentMedia renders a frame/byte total for the debug card.
func formatAgentMedia(frames, bytes uint64) string {
	return fmt.Sprintf("%d frames · %s", frames, connection.FormatBytes(bytes))
}

// formatCountPair renders two related totals for the debug card.
func formatCountPair(first, second uint64) string {
	return fmt.Sprintf("%d / %d", first, second)
}

// formatCount renders one total for the debug card.
func formatCount(n uint64) string {
	return fmt.Sprintf("%d", n)
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
	proto := "Selkies H.264/Opus over WebSocket"
	if transport == "Agent" {
		proto = "kw-agent-v1 over WebSocket"
	}
	// Agent clipboard and resize are negotiated with the guest. SendInput
	// cannot implement Windows secure attention (Ctrl+Alt+Del), so that
	// special-key action stays unavailable even when ordinary input is enabled.
	specialKeys, clipboardSync := true, true
	guestResize := true
	if transport == "Agent" {
		specialKeys, clipboardSync = false, false
		if capable, ok := w.inp.(interface{ ClipboardAvailable() bool }); ok {
			clipboardSync = capable.ClipboardAvailable()
		}
		if capable, ok := w.inp.(interface{ ResizeAvailable() bool }); ok {
			guestResize = capable.ResizeAvailable()
		}
	}
	snap := connection.Snapshot{Workspace: w.opts.Title, Surface: connection.Desktop,
		State: state, Role: connection.Exclusive, Transport: transport,
		Tier: "Tier 1", Proto: proto,
		Clipboard: !w.clipboardDisabled && w.opts.ClipboardInterval >= 0, ResizeGuest: !w.opts.NoResize, Muted: w.muted,
		AudioLive:    w.audio != nil && !w.muted,
		Capabilities: connection.Capabilities{SpecialKeys: specialKeys, ClipboardSync: clipboardSync, GuestResize: guestResize, Audio: w.audio != nil}}
	if transport == "Agent" {
		if dbg, ok := w.inp.(agentDebugger); ok && dbg != nil {
			if counters, live := dbg.DebugCounters(); live {
				snap.BytesIn, snap.BytesOut = counters.MediaBytes, counters.ControlBytes
				snap.HasActivity = true
				snap.Extra = [][2]string{
					{"Capture", formatFramebuffer(counters.CaptureW, counters.CaptureH)},
					{"Video", formatAgentMedia(counters.VideoFrames, counters.VideoBytes)},
					{"Audio", formatAgentMedia(counters.AudioFrames, counters.AudioBytes)},
					{"Resize ACKs / keyframes", formatCountPair(counters.ResizeACKs, counters.Keyframes)},
					{"Control frames", formatCount(counters.ControlFrames)},
					{"Guest modes", formatCount(uint64(counters.DisplayModes))},
				}
			}
		}
	}
	return snap
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
