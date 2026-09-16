// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package selkies

import (
	"encoding/binary"
	"errors"
	"testing"
)

// Independently constructed from the 2.0.0rc0 reference client's documented
// offsets. Payload bytes are synthetic framing fixtures, not decodable video.
func videoPacket(id uint16, key bool, payload []byte) []byte {
	b := make([]byte, 10, 10+len(payload))
	b[0] = Video
	if key {
		b[1] = 1
	}
	binary.BigEndian.PutUint16(b[2:4], id)
	binary.BigEndian.PutUint16(b[6:8], 1920)
	binary.BigEndian.PutUint16(b[8:10], 1080)
	return append(b, payload...)
}

func TestParseBinary(t *testing.T) {
	data := []byte{4, 1, 0x12, 0x34, 0, 16, 0x07, 0x80, 0, 32, 0, 0, 0, 1, 0x65}
	p, err := ParseBinary(data)
	if err != nil || !p.Key || p.FrameID != 0x1234 || p.Y != 16 || p.Width != 1920 || p.Height != 32 || len(p.Payload) != 5 {
		t.Fatalf("packet=%+v, err=%v", p, err)
	}
	p.Payload[4] = 0x41
	if data[14] != 0x41 {
		t.Fatal("payload should borrow the input buffer")
	}
	for _, data := range [][]byte{{1, 0, 0xf8, 0xff, 0xfe}, videoPacket(65535, false, nil)} {
		if _, err := ParseBinary(data); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseBinaryRejects(t *testing.T) {
	newWire := videoPacket(1, true, []byte{0, 0, 1, 0x65})
	newWire[1] = 0x11 // main's codec nibble is incompatible with the release.
	badSize := videoPacket(1, true, []byte{0, 0, 1, 0x65})
	badSize[6], badSize[7] = 0xff, 0xff
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"empty", nil, ErrMalformed},
		{"short header", []byte{4, 1, 0, 1}, ErrMalformed},
		{"empty Opus", []byte{1, 0}, ErrMalformed},
		{"RED unnegotiated", []byte{1, 1, 0, 0, 0}, ErrUnsupported},
		{"gzip unnegotiated", []byte{5, 1, 2}, ErrUnsupported},
		{"JPEG not H264", []byte{3, 0, 0, 1, 0, 0, 0xff}, ErrUnsupported},
		{"future wire", newWire, ErrUnsupported},
		{"geometry overflow", badSize, ErrMalformed},
		{"not AnnexB", videoPacket(1, true, []byte{1, 2, 3, 4, 5}), ErrMalformed},
		{"oversize", make([]byte, MaxMessageBytes+1), ErrMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseBinary(tt.data); !errors.Is(err, tt.want) {
				t.Fatalf("got %v; want %v", err, tt.want)
			}
		})
	}
}

func FuzzParseBinary(f *testing.F) {
	f.Add([]byte{1, 0, 0xf8, 0xff, 0xfe})
	f.Add(videoPacket(1, true, []byte{0, 0, 0, 1, 0x65}))
	f.Add(videoPacket(65535, false, nil))
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := ParseBinary(b)
		if err != nil {
			return
		}
		if len(p.Payload) > len(b) || (p.Kind != Video && p.Kind != Audio) {
			t.Fatal("invalid accepted packet")
		}
	})
}
