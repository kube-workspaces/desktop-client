// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package selkies implements the Tier 1 transport diagnostic.
// It targets Selkies 2.0.0rc0, not the incompatible video framing on upstream
// main. ProbeMedia optionally decodes through internal/media; interactive
// input and normal GUI transport selection are not implemented here.
package selkies

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Revision identifies the upstream wire implementation this package targets.
const Revision = "2.0.0rc0/f5eb10c8b1bdbb9c8e0d8ed3deb8387bc566630e"

// MaxMessageBytes bounds one reassembled WebSocket message, including headers.
const MaxMessageBytes = 8 * 1024 * 1024

const (
	// Audio is plain Opus preceded by the message type and RED block count.
	Audio byte = 0x01
	// Video is a ten-byte stripe header followed by an H.264 Annex-B access unit.
	Video byte = 0x04
)

var (
	// ErrMalformed indicates a truncated, invalid, or excessive wire message.
	ErrMalformed = errors.New("selkies: malformed message")
	// ErrUnsupported indicates a wire feature this implementation did not negotiate.
	ErrUnsupported = errors.New("selkies: unsupported protocol feature")
)

// Packet borrows its Payload from the input buffer; callers must not mutate it
// while the packet is in use. Video dimensions describe a stripe, not necessarily
// the entire display. An empty video payload is a heartbeat, not a frame.
type Packet struct {
	Kind    byte
	Key     bool
	FrameID uint16
	Y       uint16
	Width   uint16
	Height  uint16
	Payload []byte
}

// ParseBinary parses the explicitly supported 2.0.0rc0 subset. RED and gzip
// must not be advertised by callers: they are intentionally unsupported here.
// See upstream addons/selkies-web-core/selkies-ws-core.js at Revision for framing.
func ParseBinary(data []byte) (Packet, error) {
	if len(data) == 0 || len(data) > MaxMessageBytes {
		return Packet{}, fmt.Errorf("%w: size %d", ErrMalformed, len(data))
	}
	p := Packet{Kind: data[0]}
	switch p.Kind {
	case Audio:
		if len(data) < 3 {
			return Packet{}, fmt.Errorf("%w: empty Opus packet", ErrMalformed)
		}
		if data[1] != 0 {
			return Packet{}, fmt.Errorf("%w: Opus RED was not negotiated", ErrUnsupported)
		}
		p.Payload = data[2:]
	case Video:
		if len(data) < 10 {
			return Packet{}, fmt.Errorf("%w: short video header", ErrMalformed)
		}
		// Newer Selkies adds a codec nibble and reference ID. Reject its
		// flags rather than feeding the extra header bytes into an H.264 decoder.
		if data[1] > 2 {
			return Packet{}, fmt.Errorf("%w: video flags 0x%02x", ErrUnsupported, data[1])
		}
		p.Key = data[1] == 1
		p.FrameID = binary.BigEndian.Uint16(data[2:4])
		p.Y = binary.BigEndian.Uint16(data[4:6])
		p.Width = binary.BigEndian.Uint16(data[6:8])
		p.Height = binary.BigEndian.Uint16(data[8:10])
		p.Payload = data[10:]
		if len(p.Payload) == 0 {
			return p, nil // Header-only heartbeat may have zero geometry.
		}
		if p.Width == 0 || p.Height == 0 || p.Width > 8192 || int(p.Y)+int(p.Height) > 8192 ||
			uint64(p.Width)*uint64(p.Height) > 16*1024*1024 {
			return Packet{}, fmt.Errorf("%w: video geometry", ErrMalformed)
		}
		if !annexB(p.Payload) {
			return Packet{}, fmt.Errorf("%w: expected H.264 Annex-B payload", ErrMalformed)
		}
	default:
		return Packet{}, fmt.Errorf("%w: binary opcode 0x%02x", ErrUnsupported, p.Kind)
	}
	return p, nil
}

func annexB(data []byte) bool {
	return len(data) >= 4 && data[0] == 0 && data[1] == 0 &&
		(data[2] == 1 || len(data) >= 5 && data[2] == 0 && data[3] == 1)
}
