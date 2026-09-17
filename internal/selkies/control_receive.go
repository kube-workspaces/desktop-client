// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package selkies

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

// ControlEventKind identifies which server-to-client control message was parsed.
type ControlEventKind int

const (
	// EventMode carries the transport mode the server selected at connect.
	EventMode ControlEventKind = iota + 1
	// EventCursor carries a cursor shape (handle 0 = hide).
	EventCursor
	// EventClipboard carries a server-to-client clipboard push (text or binary).
	EventClipboard
	// EventClipboardReply tags a clipboard push as the answer to a prior
	// client "cr" (request by this connection), so the GUI may cache it
	// instead of pasting without the user invoking paste.
	EventClipboardReply
	// EventSettings is the initial JSON server_settings payload.
	EventSettings
	// EventSystem is a catch-all verb the GUI routes to notification
	// handling (KILL, AUDIO_DISABLED, command_done, ...).
	EventSystem
)

// ControlEvent is one parsed server-to-client control message.
type ControlEvent struct {
	Kind ControlEventKind
	// Mode is the transport mode for EventMode ("websockets").
	Mode string
	// Cursor for EventCursor.
	Cursor CursorShape
	// Clipboard for EventClipboard.
	Clipboard ClipboardData
	// ReplyTo is the originating verb for EventClipboardReply.
	ReplyTo string
	// Settings for EventSettings.
	Settings map[string]any
	// Text for EventSystem (the raw unparsed verb, e.g. "AUDIO_DISABLED",
	// "command_done,<cmd>", "KILL ...").
	Text string
}

// CursorShape is a cursor image as the server sends it: a base64-encoded PNG,
// the hotspot, and a handle that changes when the image changes (0 = hide).
type CursorShape struct {
	Data   string `json:"curdata"` // base64-encoded PNG
	Width  int    `json:"width"`
	Height int    `json:"height"`
	HotX   int    `json:"hotx"`
	HotY   int    `json:"hoty"`
	Handle uint64 `json:"handle"`
}

// Visible reports whether this shape is not a hide (handle 0 or empty).
func (c CursorShape) Visible() bool { return c.Handle != 0 && c.Data != "" }

// ClipboardData carries one clipboard payload. For multipart server pushes
// (clipboard_start / clipboard_data / clipboard_finish), Data is populated on
// the EventClipboard event after the finish frame arrives.
type ClipboardData struct {
	// Text is the payload for text/plain data.
	Text string
	// MIME is the payload MIME type; empty means text/plain.
	MIME string
	// Data is the raw payload when MIME != "text/plain", or raw bytes for
	// any binary clipboard transfer.
	Data []byte
	// Binary reports whether this was a binary (non-text/plain) transfer.
	Binary bool
	// Tag holds the originating verb ("cr") if a clipboard_reply preceded
	// the payload, so the receiver can treat the response as cache-only
	// rather than triggering a paste.
	Tag string
}

// ControlParser decodes the server's text-control stream, including the
// multipart clipboard assembly. It is not safe for concurrent use.
type ControlParser struct {
	mu sync.Mutex
	// Clipboard assembly state. The server emits one multipart transfer at
	// a time on a connection, so a single slot is sufficient.
	inTransfer   bool
	transferMIME string
	transferBuf  bytes.Buffer
	reply        string // pending clipboard_reply verb
}

// Parse decodes one text frame from the server and returns the matching
// event. Multipart start/data frames are accumulated silently; an
// EventClipboard is returned only after the finish frame arrives (or a
// simple single-frame transfer completes immediately). An unrecognised
// verb is delivered as EventSystem with its raw text.
func (p *ControlParser) Parse(text string) (ControlEvent, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.inTransfer {
		return p.parseMultipart(text)
	}

	// Assembly frames without an active transfer (e.g. after Reset) are
	// orphans and are dropped rather than surfaced as events.
	if text == "clipboard_finish" || strings.HasPrefix(text, "clipboard_data,") {
		return ControlEvent{}, false
	}

	switch {
	case strings.HasPrefix(text, "MODE "):
		return ControlEvent{
			Kind: EventMode,
			Mode: strings.TrimPrefix(text, "MODE "),
		}, true

	case strings.HasPrefix(text, "clipboard,"):
		data, err := base64.StdEncoding.DecodeString(text[len("clipboard,"):])
		if err != nil {
			return ControlEvent{}, false
		}
		evt := ControlEvent{
			Kind: EventClipboard,
			Clipboard: ClipboardData{
				Text: string(data),
				Tag:  p.reply,
			},
		}
		p.reply = ""
		return evt, true

	case strings.HasPrefix(text, "clipboard_binary,"):
		rest := text[len("clipboard_binary,"):]
		idx := strings.IndexByte(rest, ',')
		if idx < 0 {
			return ControlEvent{}, false
		}
		mime := rest[:idx]
		data, err := base64.StdEncoding.DecodeString(rest[idx+1:])
		if err != nil {
			return ControlEvent{}, false
		}
		evt := ControlEvent{
			Kind: EventClipboard,
			Clipboard: ClipboardData{
				MIME:   mime,
				Data:   data,
				Binary: true,
				Tag:    p.reply,
			},
		}
		p.reply = ""
		return evt, true

	case strings.HasPrefix(text, "clipboard_reply,"):
		p.reply = strings.TrimPrefix(text, "clipboard_reply,")
		return ControlEvent{}, false // tag only; the payload event carries it

	case strings.HasPrefix(text, "clipboard_start,"):
		rest := text[len("clipboard_start,"):]
		parts := strings.SplitN(rest, ",", 2)
		if len(parts) != 2 {
			return ControlEvent{}, false
		}
		total, err := strconv.Atoi(parts[1])
		if err != nil || total < 0 {
			return ControlEvent{}, false
		}
		p.inTransfer = true
		p.transferMIME = parts[0]
		p.transferBuf.Reset()
		p.transferBuf.Grow(total)
		return ControlEvent{}, false

	case strings.HasPrefix(text, "cursor,"):
		var shape CursorShape
		if err := json.Unmarshal([]byte(text[len("cursor,"):]), &shape); err != nil {
			return ControlEvent{}, false
		}
		return ControlEvent{Kind: EventCursor, Cursor: shape}, true

	default:
		return p.parseSystemOrJSON(text)
	}
}

// parseSystemOrJSON handles settings JSON payloads and unknown verbs.
func (p *ControlParser) parseSystemOrJSON(text string) (ControlEvent, bool) {
	if len(text) == 0 || text[0] != '{' {
		return ControlEvent{Kind: EventSystem, Text: text}, true
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		return ControlEvent{Kind: EventSystem, Text: text}, true
	}
	if envelope.Type == "server_settings" {
		var settings struct {
			Settings map[string]any `json:"settings"`
		}
		if err := json.Unmarshal([]byte(text), &settings); err != nil {
			return ControlEvent{Kind: EventSystem, Text: text}, true
		}
		return ControlEvent{Kind: EventSettings, Settings: settings.Settings}, true
	}
	return ControlEvent{Kind: EventSystem, Text: text}, true
}

// parseMultipart accumulates clipboard_data/finish frames and returns the
// assembled payload when complete. The caller holds mu.
func (p *ControlParser) parseMultipart(text string) (ControlEvent, bool) {
	switch {
	case strings.HasPrefix(text, "clipboard_data,"):
		chunk, err := base64.StdEncoding.DecodeString(text[len("clipboard_data,"):])
		if err != nil {
			p.inTransfer = false
			return ControlEvent{}, false
		}
		p.transferBuf.Write(chunk)
		return ControlEvent{}, false

	case text == "clipboard_finish":
		data := make([]byte, p.transferBuf.Len())
		copy(data, p.transferBuf.Bytes())
		binary := p.transferMIME != "" && p.transferMIME != "text/plain"
		evt := ControlEvent{
			Kind: EventClipboard,
			Clipboard: ClipboardData{
				MIME:   p.transferMIME,
				Binary: binary,
				Tag:    p.reply,
			},
		}
		if !binary {
			evt.Clipboard.Text = string(data)
		} else {
			evt.Clipboard.Data = data
		}
		p.inTransfer = false
		p.transferMIME = ""
		p.reply = ""
		return evt, true

	default:
		// Abort on a frame that does not belong to the transfer.
		p.inTransfer = false
		p.transferMIME = ""
		return ControlEvent{}, false
	}
}

// Reset discards any in-progress multipart state without yielding an event.
func (p *ControlParser) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inTransfer = false
	p.transferMIME = ""
	p.reply = ""
	p.transferBuf.Reset()
}
