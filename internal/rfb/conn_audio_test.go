package rfb

import (
	"context"
	"encoding/binary"
	"io"
	stdnet "net"
	"testing"
	"time"
)

// TestAudioFormatMarshal pins the wire form of the QEMU audio format message:
// the six bytes that follow the msgQEMU header and the qemuAudioSetFormat
// command, per QEMU's protocol_client_msg VNC_MSG_CLIENT_QEMU_AUDIO handling.
func TestAudioFormatMarshal(t *testing.T) {
	got := AudioFormatPCM.marshal()
	want := []byte{
		3, 2, // S16, stereo
		0, 0, 0xac, 0x44, // 44100
	}
	if string(got) != string(want) {
		t.Fatalf("marshal = % x, want % x", got, want)
	}
}

// TestAudioFormatValid mirrors QEMU's validation: channels other than 1 or 2,
// a frequency above 48000 Hz, or an unknown format code is a protocol error
// that disconnects the client, so Valid must reject exactly those.
func TestAudioFormatValid(t *testing.T) {
	bad := []AudioFormat{
		{Format: 3, Channels: 0, SamplesPerSec: 44100},
		{Format: 3, Channels: 3, SamplesPerSec: 44100},
		{Format: 3, Channels: 2, SamplesPerSec: 0},
		{Format: 3, Channels: 2, SamplesPerSec: 48001},
		{Format: 6, Channels: 2, SamplesPerSec: 44100}, // unknown code
	}
	for i, fm := range bad {
		if fm.Valid() {
			t.Errorf("case %d: %s should be invalid", i, fm)
		}
	}
}

// peer is a connected client/server pair over an in-memory pipe.
type peer struct {
	client stdnet.Conn
	server stdnet.Conn
}

func newPeer(t *testing.T) peer {
	t.Helper()
	client, server := stdnet.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	return peer{client: client, server: server}
}

// feed runs the server half of the RFB handshake, reports the encodings the
// client advertised, then writes every byte-slice from msgs into the
// connection. An error anywhere is reported on errc; the goroutine ends when
// msgs is closed.
func feed(t *testing.T, p peer, width, height int, msgs chan []byte, gotEncodings chan []int32, errc chan error) {
	t.Helper()
	go func() {
		if _, err := io.WriteString(p.server, ProtocolVersion); err != nil {
			errc <- err
			return
		}
		var buf [12]byte
		if err := readFull(p.server, buf[:]); err != nil {
			errc <- err
			return
		}
		if _, err := p.server.Write([]byte{1, 1}); err != nil { // security types: None
			errc <- err
			return
		}
		if err := readFull(p.server, buf[:1]); err != nil {
			errc <- err
			return
		}
		if _, err := p.server.Write([]byte{0, 0, 0, 0}); err != nil { // security OK
			errc <- err
			return
		}
		if err := readFull(p.server, buf[:1]); err != nil { // ClientInit shared flag
			errc <- err
			return
		}
		name := "peer"
		init := make([]byte, 24, 24+len(name))
		binary.BigEndian.PutUint16(init[0:], uint16(width))
		binary.BigEndian.PutUint16(init[2:], uint16(height))
		init[4] = 32 // bits per pixel
		init[5] = 24 // depth
		init[7] = 1  // true colour
		binary.BigEndian.PutUint16(init[8:], 255)
		binary.BigEndian.PutUint16(init[10:], 255)
		binary.BigEndian.PutUint16(init[12:], 255)
		binary.BigEndian.PutUint32(init[20:], uint32(len(name)))
		init = append(init, name...)
		if _, err := p.server.Write(init); err != nil {
			errc <- err
			return
		}

		encs, err := drainClientMessages(p.server)
		if err != nil {
			errc <- err
			return
		}
		if gotEncodings != nil {
			gotEncodings <- encs
		}

		go func() { io.Copy(io.Discard, p.server) }() // drain client writes

		for m := range msgs {
			if _, err := p.server.Write(m); err != nil {
				errc <- err
				return
			}
		}
		errc <- nil
	}()
}

// drainClientMessages consumes SetPixelFormat and SetEncodings, returning the
// advertised encodings.
func drainClientMessages(r io.Reader) ([]int32, error) {
	var hdr [1]byte
	for {
		if err := readFull(r, hdr[:]); err != nil {
			return nil, err
		}
		switch hdr[0] {
		case 0: // SetPixelFormat
			if err := discardBytes(r, 19); err != nil {
				return nil, err
			}
		case 2: // SetEncodings
			var nbuf [3]byte
			if err := readFull(r, nbuf[:]); err != nil {
				return nil, err
			}
			n := int(binary.BigEndian.Uint16(nbuf[1:]))
			raw := make([]byte, n*4)
			if err := readFull(r, raw); err != nil {
				return nil, err
			}
			encs := make([]int32, n)
			for i := 0; i < n; i++ {
				encs[i] = int32(binary.BigEndian.Uint32(raw[i*4:]))
			}
			return encs, nil
		}
	}
}

func discardBytes(r io.Reader, n int) error {
	_, err := io.CopyN(io.Discard, r, int64(n))
	return err
}

// audioAckUpdate is a framebuffer update whose single rectangle is the
// payload-free audio acknowledgment QEMU sends after -259 is advertised.
func audioAckUpdate(width, height int) []byte {
	msg := []byte{0, 0, 0, 1} // framebuffer update, 1 rect
	msg = append(msg, 0, 0, 0, 0)
	msg = append(msg, byte(width>>8), byte(width), byte(height>>8), byte(height))
	var enc [4]byte
	encI32 := int32(EncodingQEMUAudio)
	binary.BigEndian.PutUint32(enc[:], uint32(encI32))
	msg = append(msg, enc[:]...)
	return msg
}

// audioData is a QEMU audio batch: type 255, sub-type 1, u16 2 (DATA), u32
// byte count, samples.
func audioData(payload []byte) []byte {
	msg := []byte{255, 1, 0, 2}
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(len(payload)))
	msg = append(msg, sz[:]...)
	return append(msg, payload...)
}

// TestConnAudioAdvertisingAndData covers the whole wire contract: the
// handshake advertises -259 without sending an audio format, the server's ack
// rectangle flips AudioOK, and audio batches routed through the read loop
// reach OnAudio byte-exact after EnableAudio.
func TestConnAudioAdvertisingAndData(t *testing.T) {
	p := newPeer(t)
	msgs := make(chan []byte, 4)
	encs := make(chan []int32, 1)
	errc := make(chan error, 1)
	feed(t, p, 640, 480, msgs, encs, errc)

	delivered := make(chan []byte, 4)
	cfg := Config{
		Encodings:   []Encoding{EncodingTight},
		AudioFormat: &AudioFormatPCM,
		OnAudio:     func(data []byte) { delivered <- data },
	}
	conn, err := NewConn(p.client, cfg)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	var encodings []int32
	select {
	case encodings = <-encs:
	case err := <-errc:
		t.Fatalf("server: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for advertised encodings")
	}
	if len(encodings) != 2 || encodings[1] != int32(EncodingQEMUAudio) {
		t.Fatalf("encodings advertised = %v, want [Tight, QEMUAudio]", encodings)
	}
	if got, ok := conn.AudioFormat(); !ok || got != AudioFormatPCM {
		t.Fatalf("AudioFormat() = %v, %v, want PCM, true", got, ok)
	}
	if conn.AudioOK() {
		t.Fatal("AudioOK() true before any ack")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- conn.Run(ctx) }()

	msgs <- audioAckUpdate(640, 480)
	waitAudioOK(t, conn)

	// AudioOK gates nothing on the client side other than the caller's choice
	// to enable; QEMU starts streaming only after EnableAudio. Enable it and
	// stream two batches.
	if err := conn.EnableAudio(); err != nil {
		t.Fatalf("EnableAudio: %v", err)
	}
	msgs <- audioData([]byte{0x11, 0x22, 0x33, 0x44})
	msgs <- audioData([]byte{0xaa, 0xbb})

	var audio []byte
	for len(audio) < 6 {
		select {
		case d := <-delivered:
			audio = append(audio, d...)
		case err := <-errc:
			t.Fatalf("server: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatalf("audio delivered %d bytes, want 6", len(audio))
		}
	}
	want := []byte{0x11, 0x22, 0x33, 0x44, 0xaa, 0xbb}
	if string(audio) != string(want) {
		t.Fatalf("audio = % x, want % x", audio, want)
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
	close(msgs)
	if err := <-errc; err != nil {
		t.Fatalf("server: %v", err)
	}
}

// waitAudioOK polls until the ack rectangle has been reflected.
func waitAudioOK(t *testing.T, conn *Conn) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !conn.AudioOK() {
		if time.Now().After(deadline) {
			t.Fatal("AudioOK() never became true")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestConnAudioDataBeforeFormatErrors checks that audio data arriving on a
// connection that never opted into audio (Config.AudioFormat nil, so no format
// is stored and no ENABLE was ever sent) fails loudly rather than misparsing
// the stream. QEMU would only ever do this if the client skipped the audio
// handshake, but a misbehaving peer must not desync the read loop.
func TestConnAudioDataBeforeFormatErrors(t *testing.T) {
	p := newPeer(t)
	msgs := make(chan []byte, 2)
	errc := make(chan error, 1)
	feed(t, p, 640, 480, msgs, nil, errc)

	conn, err := NewConn(p.client, Config{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	ctx1, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- conn.Run(ctx1) }()

	// An audio batch on a connection with no negotiated format has no size
	// basis; the read loop must fail the connection, not desync.
	msgs <- audioData([]byte{1, 2, 3, 4})
	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("Run returned nil for audio before format")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not fail on audio before format")
	}
	close(msgs)
	<-errc
}
