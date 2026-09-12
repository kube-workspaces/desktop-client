// Package rfb implements a client for the RFB (Remote Framebuffer) protocol,
// the protocol behind VNC, as specified by RFC 6143 plus the widely-deployed
// extensions this project needs.
//
// It is written for a specific server: the QEMU VNC server embedded in every
// KubeVirt virt-launcher pod, reached through the kube-workspaces API's
// /v1/workspaces/{name}/vnc WebSocket bridge. The bridge is a transparent relay
// of the raw RFB byte stream, so this package speaks RFB directly over a
// WebSocket message stream rather than a TCP socket (see Transport).
//
// Design notes:
//
//   - The framebuffer is kept in RGBA byte order so it can be handed to a GPU
//     texture without a conversion pass. To make that free we ask the server for
//     a pixel format whose little-endian byte order is already R,G,B,pad (see
//     PreferredPixelFormat).
//   - Decoders are pluggable (see Decoder) so the encoding set can be tuned at
//     runtime; that is what the adaptive-quality controller will drive.
package rfb

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// ProtocolVersion is the RFB version this client speaks. 3.8 is the version
// described by RFC 6143 and is what QEMU offers.
const ProtocolVersion = "RFB 003.008\n"

// Client-to-server message types (RFC 6143 §7.5).
const (
	msgSetPixelFormat           uint8 = 0
	msgSetEncodings             uint8 = 2
	msgFramebufferUpdateRequest uint8 = 3
	msgKeyEvent                 uint8 = 4
	msgPointerEvent             uint8 = 5
	msgClientCutText            uint8 = 6
	// msgSetDesktopSize is the client half of the ExtendedDesktopSize
	// extension: it asks the server to change the framebuffer (and, for QEMU,
	// the guest's display) size.
	msgSetDesktopSize uint8 = 251
)

// Server-to-client message types (RFC 6143 §7.6).
const (
	msgFramebufferUpdate   uint8 = 0
	msgSetColourMapEntries uint8 = 1
	msgBell                uint8 = 2
	msgServerCutText       uint8 = 3
)

// Security types (RFC 6143 §7.1.2).
const (
	secInvalid uint8 = 0
	secNone    uint8 = 1
	secVNCAuth uint8 = 2
)

// Encoding identifies a framebuffer encoding or pseudo-encoding. Pseudo-
// encodings are negative and are how a client advertises support for protocol
// extensions: the server silently ignores any it does not implement, so a
// client must treat every extension as unsupported until it sees evidence
// otherwise.
type Encoding int32

// Real encodings, in the order a client would typically prefer them.
const (
	EncodingRaw      Encoding = 0
	EncodingCopyRect Encoding = 1
	EncodingRRE      Encoding = 2
	EncodingCoRRE    Encoding = 4
	EncodingHextile  Encoding = 5
	EncodingZlib     Encoding = 6
	EncodingTight    Encoding = 7
	EncodingZlibHex  Encoding = 8
	EncodingTRLE     Encoding = 15
	EncodingZRLE     Encoding = 16
)

// Pseudo-encodings.
const (
	EncodingCursor              Encoding = -239
	EncodingXCursor             Encoding = -240
	EncodingCursorPos           Encoding = -232
	EncodingDesktopSize         Encoding = -223
	EncodingLastRect            Encoding = -224
	EncodingExtendedDesktopSize Encoding = -308
	EncodingFence               Encoding = -312
	EncodingContinuousUpdates   Encoding = -313
	// EncodingExtendedClipboard is registered as the unsigned value
	// 0xc0a1e5ce; as the int32 the protocol actually puts on the wire that is
	// -1063131698.
	EncodingExtendedClipboard Encoding = -1063131698

	// QEMU-specific pseudo-encodings. EncodingQEMUAudio is how a client asks
	// QEMU to stream guest audio in-band; QEMU only acknowledges it when it was
	// started with an audiodev attached to the VNC display.
	EncodingQEMUPointerMotionChange Encoding = -257
	EncodingQEMUExtendedKeyEvent    Encoding = -258
	EncodingQEMUAudio               Encoding = -259
	EncodingQEMULEDState            Encoding = -261
)

// QualityLevel returns the JPEG quality-level pseudo-encoding for level
// (0..9), where 0 is the smallest/ugliest and 9 the largest/best. Sending this
// is what switches QEMU's Tight encoder into its JPEG path at all: with no
// quality level set, QEMU never emits JPEG rectangles.
func QualityLevel(level int) Encoding {
	if level < 0 {
		level = 0
	}
	if level > 9 {
		level = 9
	}
	return Encoding(-32 + level)
}

// CompressLevel returns the zlib compression-level pseudo-encoding for level
// (0..9). Higher costs more server CPU for a smaller stream.
func CompressLevel(level int) Encoding {
	if level < 0 {
		level = 0
	}
	if level > 9 {
		level = 9
	}
	return Encoding(-256 + level)
}

// String renders well-known encodings by name, for logs and the probe tool.
func (e Encoding) String() string {
	switch e {
	case EncodingRaw:
		return "Raw"
	case EncodingCopyRect:
		return "CopyRect"
	case EncodingRRE:
		return "RRE"
	case EncodingCoRRE:
		return "CoRRE"
	case EncodingHextile:
		return "Hextile"
	case EncodingZlib:
		return "Zlib"
	case EncodingTight:
		return "Tight"
	case EncodingZlibHex:
		return "ZlibHex"
	case EncodingTRLE:
		return "TRLE"
	case EncodingZRLE:
		return "ZRLE"
	case EncodingCursor:
		return "Cursor"
	case EncodingXCursor:
		return "XCursor"
	case EncodingCursorPos:
		return "CursorPos"
	case EncodingDesktopSize:
		return "DesktopSize"
	case EncodingLastRect:
		return "LastRect"
	case EncodingExtendedDesktopSize:
		return "ExtendedDesktopSize"
	case EncodingFence:
		return "Fence"
	case EncodingContinuousUpdates:
		return "ContinuousUpdates"
	case EncodingExtendedClipboard:
		return "ExtendedClipboard"
	case EncodingQEMUPointerMotionChange:
		return "QEMUPointerMotionChange"
	case EncodingQEMUExtendedKeyEvent:
		return "QEMUExtendedKeyEvent"
	case EncodingQEMUAudio:
		return "QEMUAudio"
	case EncodingQEMULEDState:
		return "QEMULEDState"
	}
	switch {
	case e >= -32 && e <= -23:
		return fmt.Sprintf("JPEGQuality%d", int(e)+32)
	case e >= -256 && e <= -247:
		return fmt.Sprintf("CompressLevel%d", int(e)+256)
	}
	return fmt.Sprintf("Encoding(%d)", int32(e))
}

// PixelFormat describes how a pixel is laid out on the wire (RFC 6143 §7.4).
type PixelFormat struct {
	BitsPerPixel uint8
	Depth        uint8
	BigEndian    bool
	TrueColor    bool
	RedMax       uint16
	GreenMax     uint16
	BlueMax      uint16
	RedShift     uint8
	GreenShift   uint8
	BlueShift    uint8
}

// PreferredPixelFormat is the format this client asks the server to use.
//
// The shifts are chosen so that, read little-endian, the four bytes of a pixel
// are already R, G, B, unused — identical to Go's image.RGBA layout and to what
// a GPU texture wants. That turns the hot path (Raw rectangles, Tight's
// full-colour sub-rectangles) into a copy with an alpha fixup instead of a
// per-pixel shift/mask dance.
var PreferredPixelFormat = PixelFormat{
	BitsPerPixel: 32,
	Depth:        24,
	BigEndian:    false,
	TrueColor:    true,
	RedMax:       255,
	GreenMax:     255,
	BlueMax:      255,
	RedShift:     0,
	GreenShift:   8,
	BlueShift:    16,
}

// BytesPerPixel returns the on-the-wire size of one pixel.
func (pf PixelFormat) BytesPerPixel() int { return int(pf.BitsPerPixel) / 8 }

// IsRGBA reports whether this format's little-endian byte order is exactly
// R,G,B,pad, which enables the fast copy path in the decoders.
func (pf PixelFormat) IsRGBA() bool {
	return pf.BitsPerPixel == 32 && pf.Depth == 24 && !pf.BigEndian && pf.TrueColor &&
		pf.RedMax == 255 && pf.GreenMax == 255 && pf.BlueMax == 255 &&
		pf.RedShift == 0 && pf.GreenShift == 8 && pf.BlueShift == 16
}

// SupportsCompactTPIXEL reports whether Tight's 3-byte "TPIXEL" compaction
// applies: it does when the format is 32bpp true-colour with depth 24 and all
// three maxima are 255, in which case the padding byte is omitted.
func (pf PixelFormat) SupportsCompactTPIXEL() bool {
	return pf.BitsPerPixel == 32 && pf.Depth == 24 && pf.TrueColor &&
		pf.RedMax == 255 && pf.GreenMax == 255 && pf.BlueMax == 255
}

func (pf PixelFormat) String() string {
	return fmt.Sprintf("%dbpp depth%d %s truecolor=%t r<<%d g<<%d b<<%d max(%d,%d,%d)",
		pf.BitsPerPixel, pf.Depth, endianName(pf.BigEndian), pf.TrueColor,
		pf.RedShift, pf.GreenShift, pf.BlueShift, pf.RedMax, pf.GreenMax, pf.BlueMax)
}

func endianName(big bool) string {
	if big {
		return "big-endian"
	}
	return "little-endian"
}

// marshal writes the 16-byte wire form of a pixel format.
func (pf PixelFormat) marshal() []byte {
	b := make([]byte, 16)
	b[0] = pf.BitsPerPixel
	b[1] = pf.Depth
	b[2] = boolByte(pf.BigEndian)
	b[3] = boolByte(pf.TrueColor)
	binary.BigEndian.PutUint16(b[4:], pf.RedMax)
	binary.BigEndian.PutUint16(b[6:], pf.GreenMax)
	binary.BigEndian.PutUint16(b[8:], pf.BlueMax)
	b[10] = pf.RedShift
	b[11] = pf.GreenShift
	b[12] = pf.BlueShift
	// b[13:16] is padding.
	return b
}

// unmarshalPixelFormat parses the 16-byte wire form.
func unmarshalPixelFormat(b []byte) (PixelFormat, error) {
	if len(b) < 16 {
		return PixelFormat{}, fmt.Errorf("rfb: short pixel format: %d bytes", len(b))
	}
	return PixelFormat{
		BitsPerPixel: b[0],
		Depth:        b[1],
		BigEndian:    b[2] != 0,
		TrueColor:    b[3] != 0,
		RedMax:       binary.BigEndian.Uint16(b[4:]),
		GreenMax:     binary.BigEndian.Uint16(b[6:]),
		BlueMax:      binary.BigEndian.Uint16(b[8:]),
		RedShift:     b[10],
		GreenShift:   b[11],
		BlueShift:    b[12],
	}, nil
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// Rect is a rectangle within the framebuffer.
type Rect struct {
	X, Y, Width, Height uint16
}

func (r Rect) String() string {
	return fmt.Sprintf("%dx%d+%d+%d", r.Width, r.Height, r.X, r.Y)
}

// Area returns the pixel count of the rectangle.
func (r Rect) Area() int { return int(r.Width) * int(r.Height) }

// Empty reports whether the rectangle covers no pixels.
func (r Rect) Empty() bool { return r.Width == 0 || r.Height == 0 }

// ButtonMask is the RFB pointer button bitmask (RFC 6143 §7.5.5). Bits 0-2 are
// left/middle/right; bits 3-6 are wheel up/down/left/right, which are reported
// as a press followed immediately by a release.
type ButtonMask uint8

const (
	ButtonLeft       ButtonMask = 1 << 0
	ButtonMiddle     ButtonMask = 1 << 1
	ButtonRight      ButtonMask = 1 << 2
	ButtonWheelUp    ButtonMask = 1 << 3
	ButtonWheelDown  ButtonMask = 1 << 4
	ButtonWheelLeft  ButtonMask = 1 << 5
	ButtonWheelRight ButtonMask = 1 << 6
)

// readFull is a small helper that reads exactly len(buf) bytes or returns an
// error. It exists to keep the decoders free of repetitive error wrapping.
func readFull(r io.Reader, buf []byte) error {
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return nil
}

// LEDState is the guest's keyboard lock indicator state, reported by QEMU
// through the LED state pseudo-encoding. A viewer uses it to keep the host's
// lock keys visually in step with the guest.
type LEDState uint8

const (
	// LEDScrollLock is set when the guest's scroll lock is on.
	LEDScrollLock LEDState = 1 << 0
	// LEDNumLock is set when the guest's num lock is on.
	LEDNumLock LEDState = 1 << 1
	// LEDCapsLock is set when the guest's caps lock is on.
	LEDCapsLock LEDState = 1 << 2
)

// Has reports whether a given indicator is lit.
func (s LEDState) Has(f LEDState) bool { return s&f != 0 }

// String renders the lit indicators, for logs and status bars.
func (s LEDState) String() string {
	var on []string
	if s.Has(LEDCapsLock) {
		on = append(on, "caps")
	}
	if s.Has(LEDNumLock) {
		on = append(on, "num")
	}
	if s.Has(LEDScrollLock) {
		on = append(on, "scroll")
	}
	if len(on) == 0 {
		return "none"
	}
	return strings.Join(on, "+")
}
