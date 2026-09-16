package rfb

import (
	"bufio"
	"context"
	"crypto/des" //nolint:gosec // VNC authentication is specified in terms of DES.
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config configures a Conn. The zero value is usable: it negotiates
// PreferredPixelFormat, requests a shared session and advertises
// DefaultEncodings.
type Config struct {
	// Password is used only if the server demands VNC authentication. The
	// KubeVirt console does not (it offers "None" and relies on the API bridge
	// for authorization), but a password lets this package talk to ordinary
	// VNC servers, which is useful for local testing.
	Password string

	// Shared asks the server not to disconnect other clients. Defaults to true;
	// set ExclusiveSession to override.
	ExclusiveSession bool

	// PixelFormat overrides PreferredPixelFormat. Leave nil unless you have a
	// reason: the default is the one with a zero-cost conversion path.
	PixelFormat *PixelFormat

	// Encodings is the encoding preference list sent after the handshake.
	// Defaults to DefaultEncodings.
	Encodings []Encoding

	// Quality enables adaptive Tight quality/compression and lossless idle
	// refresh. Start with DefaultQualityConfig. The transport must implement
	// io.Closer; Run closes it on exit and joins the controller's writer.
	Quality *QualityConfig

	// Decoders overrides the decoder set. Defaults to DefaultDecoders.
	Decoders []Decoder

	// OnFramebufferUpdate is called after each complete FramebufferUpdate with
	// the regions that changed. The framebuffer must not be retained past the
	// callback; clone it if you need to.
	OnFramebufferUpdate func(fb *Framebuffer, damage []Rect)

	// OnResize is called when the server changes the framebuffer size, either
	// via DesktopSize or ExtendedDesktopSize.
	OnResize func(width, height int)

	// OnCutText is called when the server sends clipboard text.
	OnCutText func(text string)

	// OnBell is called when the server rings the bell.
	OnBell func()

	// OnLEDState is called when the guest's keyboard lock indicators change.
	OnLEDState func(state LEDState)

	// OnRect, when set, is called for every rectangle after it is decoded.
	// This is a diagnostic hook: it is how the probe tool reports what the
	// server actually sent, which is the only way to discover a server's real
	// capabilities.
	OnRect func(enc Encoding, r Rect, bytes uint64)

	// OnCursor is called when the server sends a new cursor shape via the
	// Cursor pseudo-encoding. image is RGBA of size w*h*4; hotX/hotY are the
	// hotspot. A zero-sized cursor means "hide the pointer".
	OnCursor func(image []byte, w, h, hotX, hotY int)

	// AudioFormat opts into the QEMU audio extension: EncodingQEMUAudio is
	// added to the encoding list during the handshake, and once the caller has
	// seen AudioOK() the format is sent with SetAudioFormat and EnableAudio,
	// after which the server streams PCM batches to OnAudio. Nil keeps audio
	// off. A connection never receives audio it has not asked for, and QEMU
	// only accepts the audio messages after it has acknowledged the encoding,
	// so nothing is sent until AudioOK().
	AudioFormat *AudioFormat

	// OnAudio is called for each batch of PCM samples the server streams. data
	// holds the raw bytes in the negotiated AudioFormat and must not be
	// retained past the callback; copy it if you need it later. It is only
	// ever called for a connection that enabled audio via AudioFormat.
	OnAudio func(data []byte)
}

func (c *Config) pixelFormat() PixelFormat {
	if c.PixelFormat != nil {
		return *c.PixelFormat
	}
	return PreferredPixelFormat
}

// DefaultEncodings is the encoding list the client advertises by default.
//
// Order matters: it is the client's preference, most-preferred first. Tight is
// first because it is the only encoding QEMU will use JPEG with, and JPEG is
// what makes photographic and video content affordable. The pseudo-encodings
// that follow are feature advertisements, not choices.
var DefaultEncodings = []Encoding{
	EncodingTight,
	EncodingZRLE,
	EncodingHextile,
	EncodingCopyRect,
	EncodingRaw,

	EncodingExtendedDesktopSize,
	EncodingDesktopSize,
	EncodingLastRect,
	EncodingCursor,
	EncodingCursorPos,
	EncodingQEMUExtendedKeyEvent,
	EncodingQEMULEDState,
}

// Stats records connection-level counters. They drive both the probe tool and,
// later, the adaptive-quality controller.
type Stats struct {
	BytesRead       uint64
	Updates         uint64
	Rects           uint64
	RectsByEncoding map[Encoding]uint64
	BytesByEncoding map[Encoding]uint64
	DecodeTime      time.Duration
	// AckedPseudoEncodings records pseudo-encodings the server echoed back as
	// zero-sized rectangles. That echo is the only way a client learns an
	// extension was accepted, so it is the ground truth for capability
	// detection.
	AckedPseudoEncodings map[Encoding]bool
}

// Conn is a connected RFB client session.
//
// One goroutine runs the read loop (Run); any goroutine may send input events.
// Writes are serialised internally.
type Conn struct {
	cfg      Config
	rw       io.ReadWriter
	r        *bufio.Reader
	counting *countingReader

	writeMu sync.Mutex
	w       *bufio.Writer

	fbMu sync.RWMutex
	fb   *Framebuffer

	pixels   *PixelReader
	decoders map[Encoding]Decoder

	serverName  string
	serverPF    PixelFormat
	securityTyp uint8

	// audioMu guards audioFmt; SetAudioFormat runs on the sending goroutine
	// while the read loop consumes it, so the two do not share the write lock.
	audioMu  sync.RWMutex
	audioFmt *AudioFormat

	statsMu sync.Mutex
	stats   Stats

	// quality is the adaptive quality controller for this connection, or nil
	// when Config.Quality was not set.
	quality *qualityController

	closed atomic.Bool
}

// ServerName returns the desktop name reported by the server. For KubeVirt this
// looks like "QEMU (namespace_workspace-name)".
func (c *Conn) ServerName() string { return c.serverName }

// ServerPixelFormat returns the pixel format the server advertised at
// ServerInit, before any SetPixelFormat was applied.
func (c *Conn) ServerPixelFormat() PixelFormat { return c.serverPF }

// SecurityType returns the security type that was negotiated.
func (c *Conn) SecurityType() uint8 { return c.securityTyp }

// Stats returns a snapshot of the connection counters.
func (c *Conn) Stats() Stats {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	out := c.stats
	out.BytesRead = c.counting.Count()
	out.RectsByEncoding = make(map[Encoding]uint64, len(c.stats.RectsByEncoding))
	for k, v := range c.stats.RectsByEncoding {
		out.RectsByEncoding[k] = v
	}
	out.BytesByEncoding = make(map[Encoding]uint64, len(c.stats.BytesByEncoding))
	for k, v := range c.stats.BytesByEncoding {
		out.BytesByEncoding[k] = v
	}
	out.AckedPseudoEncodings = make(map[Encoding]bool, len(c.stats.AckedPseudoEncodings))
	for k, v := range c.stats.AckedPseudoEncodings {
		out.AckedPseudoEncodings[k] = v
	}
	return out
}

// Size returns the current framebuffer dimensions.
func (c *Conn) Size() (width, height int) {
	c.fbMu.RLock()
	defer c.fbMu.RUnlock()
	return c.fb.Width, c.fb.Height
}

// WithFramebuffer runs fn with the framebuffer locked for reading. This is how
// a renderer safely snapshots or uploads pixels while the read loop is running.
func (c *Conn) WithFramebuffer(fn func(fb *Framebuffer)) {
	c.fbMu.RLock()
	defer c.fbMu.RUnlock()
	fn(c.fb)
}

// PixelReader exposes the negotiated pixel converter, for decoders.
func (c *Conn) PixelReader() *PixelReader { return c.pixels }

// reader exposes the buffered reader to decoders.
func (c *Conn) reader() *bufio.Reader { return c.r }

// framebuffer exposes the framebuffer to decoders. Callers must already hold
// the write side of fbMu, which the read loop does for the duration of an
// update.
func (c *Conn) framebuffer() *Framebuffer { return c.fb }

// NewConn performs the RFB handshake over rw and returns a ready connection.
//
// rw is typically an adapter over a WebSocket, since that is how the
// kube-workspaces API exposes the KubeVirt console, but any stream works.
func NewConn(rw io.ReadWriter, cfg Config) (*Conn, error) {
	if cfg.Quality != nil {
		if _, ok := rw.(io.Closer); !ok {
			return nil, fmt.Errorf("rfb: adaptive quality requires a closeable transport")
		}
		qcfg, err := normalizeQualityConfig(*cfg.Quality)
		if err != nil {
			return nil, err
		}
		cfg.Quality = &qcfg
	}
	counting := &countingReader{r: rw}
	c := &Conn{
		cfg:      cfg,
		rw:       rw,
		counting: counting,
		r:        bufio.NewReaderSize(counting, 64*1024),
		w:        bufio.NewWriterSize(rw, 16*1024),
		stats: Stats{
			RectsByEncoding:      map[Encoding]uint64{},
			BytesByEncoding:      map[Encoding]uint64{},
			AckedPseudoEncodings: map[Encoding]bool{},
		},
	}

	decoders := cfg.Decoders
	if decoders == nil {
		decoders = DefaultDecoders()
	}
	c.decoders = make(map[Encoding]Decoder, len(decoders))
	for _, d := range decoders {
		c.decoders[d.Encoding()] = d
	}

	if err := c.handshake(); err != nil {
		return nil, err
	}
	return c, nil
}

// handshake runs version negotiation, security, ClientInit and ServerInit, then
// applies the client's preferred pixel format and encoding list.
func (c *Conn) handshake() error {
	if err := c.versionHandshake(); err != nil {
		return err
	}
	if err := c.securityHandshake(); err != nil {
		return err
	}
	if err := c.initHandshake(); err != nil {
		return err
	}

	pf := c.cfg.pixelFormat()
	if err := c.SetPixelFormat(pf); err != nil {
		return fmt.Errorf("rfb: set pixel format: %w", err)
	}
	encs := c.cfg.Encodings
	if encs == nil {
		encs = DefaultEncodings
	}
	if c.cfg.AudioFormat != nil {
		// Audio is opt-in: the pseudo-encoding is only advertised when this
		// connection has a sink to play it. QEMU acknowledges it with a
		// payload-free rectangle only when the display actually has an
		// audiodev attached; the format and enable messages are sent from
		// AudioOK(), which observes that ack. Sending them blind is a
		// protocol error on a server without audio and drops the link.
		encs = append(append([]Encoding(nil), encs...), EncodingQEMUAudio)
		c.setAudioFormat(*c.cfg.AudioFormat)
	}
	if c.cfg.Quality != nil {
		// Preserve the full feature list (including audio), and advertise the
		// opening tier in the first SetEncodings, before any update request.
		c.quality = newQualityController(c, *c.cfg.Quality, encs)
		encs = c.quality.encodingsFor(c.quality.appliedTier)
	}
	if err := c.SetEncodings(encs); err != nil {
		return fmt.Errorf("rfb: set encodings: %w", err)
	}
	return nil
}

func (c *Conn) versionHandshake() error {
	buf := make([]byte, 12)
	if err := readFull(c.r, buf); err != nil {
		return fmt.Errorf("rfb: read protocol version: %w", err)
	}
	version := string(buf)
	if !strings.HasPrefix(version, "RFB ") {
		return fmt.Errorf("rfb: not an RFB server, got %q", version)
	}
	// We only speak 3.8. Servers advertising a higher version must accept a
	// lower one from the client (RFC 6143 §7.1.1); servers advertising 3.3
	// would need a different security handshake, which we reject explicitly
	// rather than misparse.
	if version < "RFB 003.007\n" {
		return fmt.Errorf("rfb: server protocol version %q is too old (need 3.7+)", strings.TrimSpace(version))
	}
	if _, err := c.rw.Write([]byte(ProtocolVersion)); err != nil {
		return fmt.Errorf("rfb: write protocol version: %w", err)
	}
	return nil
}

func (c *Conn) securityHandshake() error {
	var count uint8
	if err := binary.Read(c.r, binary.BigEndian, &count); err != nil {
		return fmt.Errorf("rfb: read security type count: %w", err)
	}
	if count == 0 {
		reason, err := c.readString32()
		if err != nil {
			return fmt.Errorf("rfb: read security failure reason: %w", err)
		}
		return fmt.Errorf("rfb: server refused connection: %s", reason)
	}
	types := make([]byte, count)
	if err := readFull(c.r, types); err != nil {
		return fmt.Errorf("rfb: read security types: %w", err)
	}

	// Prefer None: the KubeVirt bridge authorises at the HTTP layer, so the RFB
	// stream itself is unauthenticated by design.
	chosen := secInvalid
	for _, t := range types {
		if t == secNone {
			chosen = secNone
			break
		}
	}
	if chosen == secInvalid {
		for _, t := range types {
			if t == secVNCAuth {
				chosen = secVNCAuth
				break
			}
		}
	}
	if chosen == secInvalid {
		return fmt.Errorf("rfb: no mutually supported security type (server offered %v)", types)
	}
	c.securityTyp = chosen

	if _, err := c.rw.Write([]byte{chosen}); err != nil {
		return fmt.Errorf("rfb: write security type: %w", err)
	}

	if chosen == secVNCAuth {
		if err := c.vncAuth(); err != nil {
			return err
		}
	}

	var result uint32
	if err := binary.Read(c.r, binary.BigEndian, &result); err != nil {
		return fmt.Errorf("rfb: read security result: %w", err)
	}
	if result != 0 {
		reason, err := c.readString32()
		if err != nil {
			return fmt.Errorf("rfb: authentication failed (and reason unreadable: %w)", err)
		}
		return fmt.Errorf("rfb: authentication failed: %s", reason)
	}
	return nil
}

// vncAuth answers the classic 16-byte DES challenge.
func (c *Conn) vncAuth() error {
	challenge := make([]byte, 16)
	if err := readFull(c.r, challenge); err != nil {
		return fmt.Errorf("rfb: read auth challenge: %w", err)
	}
	block, err := des.NewCipher(vncAuthKey(c.cfg.Password)) //nolint:gosec // protocol-mandated
	if err != nil {
		return fmt.Errorf("rfb: build auth cipher: %w", err)
	}
	response := make([]byte, 16)
	block.Encrypt(response[:8], challenge[:8])
	block.Encrypt(response[8:], challenge[8:])
	if _, err := c.rw.Write(response); err != nil {
		return fmt.Errorf("rfb: write auth response: %w", err)
	}
	return nil
}

// vncAuthKey builds the DES key from a password: at most 8 bytes, zero padded,
// with the bits of each byte reversed. The bit reversal is a quirk of the
// original VNC implementation that every server has since had to reproduce.
func vncAuthKey(password string) []byte {
	key := make([]byte, 8)
	copy(key, password)
	for i, b := range key {
		var r byte
		for bit := 0; bit < 8; bit++ {
			if b&(1<<bit) != 0 {
				r |= 1 << (7 - bit)
			}
		}
		key[i] = r
	}
	return key
}

func (c *Conn) initHandshake() error {
	shared := byte(1)
	if c.cfg.ExclusiveSession {
		shared = 0
	}
	if _, err := c.rw.Write([]byte{shared}); err != nil {
		return fmt.Errorf("rfb: write client init: %w", err)
	}

	header := make([]byte, 24)
	if err := readFull(c.r, header); err != nil {
		return fmt.Errorf("rfb: read server init: %w", err)
	}
	width := int(binary.BigEndian.Uint16(header[0:]))
	height := int(binary.BigEndian.Uint16(header[2:]))
	pf, err := unmarshalPixelFormat(header[4:20])
	if err != nil {
		return err
	}
	c.serverPF = pf
	nameLen := binary.BigEndian.Uint32(header[20:])
	if nameLen > 1<<20 {
		return fmt.Errorf("rfb: implausible desktop name length %d", nameLen)
	}
	name := make([]byte, nameLen)
	if err := readFull(c.r, name); err != nil {
		return fmt.Errorf("rfb: read desktop name: %w", err)
	}
	c.serverName = string(name)
	c.fb = NewFramebuffer(width, height)
	return nil
}

func (c *Conn) readString32() (string, error) {
	var n uint32
	if err := binary.Read(c.r, binary.BigEndian, &n); err != nil {
		return "", err
	}
	if n > 1<<20 {
		return "", fmt.Errorf("rfb: implausible string length %d", n)
	}
	buf := make([]byte, n)
	if err := readFull(c.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// --- client -> server messages ---------------------------------------------

// send writes a complete client message under the write lock.
func (c *Conn) send(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed.Load() {
		return net(ErrClosed)
	}
	if c.quality != nil && payload[0] == msgFramebufferUpdateRequest {
		c.quality.noteRequest(time.Now())
	}
	if _, err := c.w.Write(payload); err != nil {
		return err
	}
	return c.w.Flush()
}

// ErrClosed is returned when using a Conn whose read loop has exited.
var ErrClosed = errors.New("rfb: connection closed")

func net(err error) error { return err }

// SetPixelFormat asks the server to send pixels in the given format and
// updates the local converter to match.
func (c *Conn) SetPixelFormat(pf PixelFormat) error {
	reader, err := NewPixelReader(pf)
	if err != nil {
		return err
	}
	buf := make([]byte, 4, 20)
	buf[0] = msgSetPixelFormat
	buf = append(buf, pf.marshal()...)
	if err := c.send(buf); err != nil {
		return err
	}
	c.pixels = reader
	return nil
}

// SetEncodings sends the client's encoding preference list.
func (c *Conn) SetEncodings(encs []Encoding) error {
	buf := make([]byte, 4, 4+4*len(encs))
	buf[0] = msgSetEncodings
	binary.BigEndian.PutUint16(buf[2:], uint16(len(encs)))
	for _, e := range encs {
		var tmp [4]byte
		binary.BigEndian.PutUint32(tmp[:], uint32(e))
		buf = append(buf, tmp[:]...)
	}
	return c.send(buf)
}

// SetAudioFormat tells QEMU the sample format to resample the guest's sound
// card output to, and remembers it so the read loop can size incoming audio
// batches. It only takes effect once EnableAudio follows; QEMU reads the
// current format when the capture starts.
//
// fm must be [AudioFormat.Valid]: QEMU treats an invalid format as a protocol
// error and disconnects the client rather than ignoring it.
//
// It must only be called once AudioOK() reports the server acknowledged the
// audio encoding; QEMU otherwise treats the message as a protocol error.
func (c *Conn) SetAudioFormat(fm AudioFormat) error {
	if !fm.Valid() {
		return fmt.Errorf("rfb: invalid audio format %s", fm)
	}
	c.setAudioFormat(fm)
	buf := make([]byte, 4, 10)
	buf[0] = msgQEMU
	buf[1] = qemuSubAudio
	binary.BigEndian.PutUint16(buf[2:], qemuAudioSetFormat)
	buf = append(buf, fm.marshal()...)
	return c.send(buf)
}

// EnableAudio tells QEMU to start streaming guest audio. It must be called
// after SetAudioFormat and only once AudioOK() is true; QEMU otherwise
// desynchronises the session.
func (c *Conn) EnableAudio() error {
	buf := []byte{msgQEMU, qemuSubAudio, 0, 0}
	binary.BigEndian.PutUint16(buf[2:], qemuAudioEnable)
	return c.send(buf)
}

// DisableAudio tells QEMU to stop streaming guest audio. QEMU answers with
// qemuAudioEnd and keeps the capture idle until the next EnableAudio.
func (c *Conn) DisableAudio() error {
	buf := []byte{msgQEMU, qemuSubAudio, 0, 0}
	binary.BigEndian.PutUint16(buf[2:], qemuAudioDisable)
	return c.send(buf)
}

// AudioFormat returns the format this connection is configured for, and
// whether audio was requested at all.
func (c *Conn) AudioFormat() (AudioFormat, bool) {
	c.audioMu.RLock()
	defer c.audioMu.RUnlock()
	if c.audioFmt == nil {
		return AudioFormat{}, false
	}
	return *c.audioFmt, true
}

// AudioOK reports whether the server has acknowledged the audio encoding. It
// is the ground-truth gate for SetAudioFormat and EnableAudio: QEMU echoes the
// -259 encoding as a payload-free rectangle only when its display was created
// with an audiodev, so until AudioOK() there is no sound card to capture. The
// ack arrives asynchronously after the handshake, so this usually reports
// false for the first few updates of a connection.
func (c *Conn) AudioOK() bool {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	return c.stats.AckedPseudoEncodings[EncodingQEMUAudio]
}

// setAudioFormat stores the format without touching the wire; callers that
// want to send it use SetAudioFormat.
func (c *Conn) setAudioFormat(fm AudioFormat) {
	c.audioMu.Lock()
	c.audioFmt = &fm
	c.audioMu.Unlock()
}

// RequestUpdate asks for a framebuffer update covering the whole screen.
// Incremental requests ask only for what changed, which is what a client sends
// continuously; a non-incremental request forces a full repaint and is needed
// after a resize or a reconnect.
func (c *Conn) RequestUpdate(incremental bool) error {
	w, h := c.Size()
	return c.RequestUpdateRect(incremental, Rect{0, 0, uint16(w), uint16(h)})
}

// RequestUpdateRect asks for an update covering just r.
func (c *Conn) RequestUpdateRect(incremental bool, r Rect) error {
	buf := make([]byte, 10)
	buf[0] = msgFramebufferUpdateRequest
	buf[1] = boolByte(incremental)
	binary.BigEndian.PutUint16(buf[2:], r.X)
	binary.BigEndian.PutUint16(buf[4:], r.Y)
	binary.BigEndian.PutUint16(buf[6:], r.Width)
	binary.BigEndian.PutUint16(buf[8:], r.Height)
	return c.send(buf)
}

// QualityInterval returns the adaptive controller's recommended update request
// cadence, or zero when disabled. Callers may sample this in their request loop.
func (c *Conn) QualityInterval() time.Duration {
	if c.quality == nil {
		return 0
	}
	return time.Duration(c.quality.interval.Load())
}

// KeyEvent sends a key press or release. key is an X11 keysym.
func (c *Conn) KeyEvent(key uint32, down bool) error {
	buf := make([]byte, 8)
	buf[0] = msgKeyEvent
	buf[1] = boolByte(down)
	binary.BigEndian.PutUint32(buf[4:], key)
	return c.send(buf)
}

// PointerEvent sends an absolute pointer position and button state.
func (c *Conn) PointerEvent(x, y uint16, mask ButtonMask) error {
	buf := make([]byte, 6)
	buf[0] = msgPointerEvent
	buf[1] = byte(mask)
	binary.BigEndian.PutUint16(buf[2:], x)
	binary.BigEndian.PutUint16(buf[4:], y)
	return c.send(buf)
}

// CutText sends clipboard text to the server. RFB's base clipboard message is
// latin-1 only; characters outside it are replaced with '?'.
func (c *Conn) CutText(text string) error {
	encoded := toLatin1(text)
	buf := make([]byte, 8, 8+len(encoded))
	buf[0] = msgClientCutText
	binary.BigEndian.PutUint32(buf[4:], uint32(len(encoded)))
	buf = append(buf, encoded...)
	return c.send(buf)
}

// SetDesktopSize asks the server to resize the remote screen, the client half
// of the ExtendedDesktopSize extension. For KubeVirt this reaches QEMU, which
// updates the virtio-gpu EDID so the guest can follow.
//
// The server may refuse; the outcome arrives asynchronously as an
// ExtendedDesktopSize rectangle.
func (c *Conn) SetDesktopSize(width, height uint16) error {
	// One screen, id 0, covering the whole framebuffer. Servers that track
	// multiple screens expect the full screen list; we only ever drive one.
	buf := make([]byte, 8+16)
	buf[0] = msgSetDesktopSize
	binary.BigEndian.PutUint16(buf[2:], width)
	binary.BigEndian.PutUint16(buf[4:], height)
	buf[6] = 1 // number of screens
	binary.BigEndian.PutUint32(buf[8:], 0)
	binary.BigEndian.PutUint16(buf[12:], 0)
	binary.BigEndian.PutUint16(buf[14:], 0)
	binary.BigEndian.PutUint16(buf[16:], width)
	binary.BigEndian.PutUint16(buf[18:], height)
	binary.BigEndian.PutUint32(buf[20:], 0)
	return c.send(buf)
}

func toLatin1(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xff {
			r = '?'
		}
		out = append(out, byte(r))
	}
	return out
}

// --- server -> client loop --------------------------------------------------

// Run reads and dispatches server messages until the context is cancelled, the
// stream ends, or a protocol error occurs. It returns nil on a clean EOF.
func (c *Conn) Run(ctx context.Context) (runErr error) {
	defer c.closed.Store(true)
	if c.quality != nil {
		qctx, cancel := context.WithCancel(ctx)
		qdone := make(chan error, 1)
		closer := c.rw.(io.Closer) // checked by NewConn
		go func() {
			err := c.quality.run(qctx)
			if err != nil {
				_ = closer.Close() // wake the read loop on a failed tuning write
			}
			qdone <- err
		}()
		defer func() {
			cancel()
			c.closed.Store(true)
			_ = closer.Close() // also releases a blocked controller write on EOF
			if err := <-qdone; err != nil && ctx.Err() == nil {
				runErr = fmt.Errorf("rfb: adaptive quality: %w", err)
			}
		}()
	}

	// Cancellation works by closing the transport, which unblocks the read.
	// The caller owns the transport, so it is their Close we rely on; we just
	// stop dispatching.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			if closer, ok := c.rw.(io.Closer); ok {
				_ = closer.Close()
			}
		case <-done:
		}
	}()

	for {
		msgType, err := c.r.ReadByte()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return fmt.Errorf("rfb: read message type: %w", err)
		}

		switch msgType {
		case msgFramebufferUpdate:
			if err := c.readFramebufferUpdate(); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
		case msgSetColourMapEntries:
			if err := c.readColourMap(); err != nil {
				return err
			}
		case msgBell:
			if c.cfg.OnBell != nil {
				c.cfg.OnBell()
			}
		case msgServerCutText:
			var pad [3]byte
			if err := readFull(c.r, pad[:]); err != nil {
				return fmt.Errorf("rfb: read cut text padding: %w", err)
			}
			text, err := c.readString32()
			if err != nil {
				return fmt.Errorf("rfb: read cut text: %w", err)
			}
			if c.cfg.OnCutText != nil {
				c.cfg.OnCutText(text)
			}
		case msgQEMU:
			if err := c.readQEMU(); err != nil {
				return err
			}
		default:
			// There is no framing, so an unknown message type means the stream
			// is desynchronised and cannot be recovered.
			return fmt.Errorf("rfb: unknown server message type %d", msgType)
		}
	}
}

func (c *Conn) readColourMap() error {
	head := make([]byte, 5)
	if err := readFull(c.r, head); err != nil {
		return fmt.Errorf("rfb: read colour map header: %w", err)
	}
	n := int(binary.BigEndian.Uint16(head[3:]))
	// We always negotiate true colour, so a colour map is unexpected; read and
	// discard it rather than desynchronise.
	if _, err := io.CopyN(io.Discard, c.r, int64(n)*6); err != nil {
		return fmt.Errorf("rfb: discard colour map: %w", err)
	}
	return nil
}

// readQEMU dispatches one message type 255. On this client the only QEMU
// server sub-type is audio, so a message that is not audio means the stream is
// desynchronised and cannot be recovered. The sub-type and command were
// already announced by the caller consuming the type byte; here we consume the
// remaining three header bytes plus whatever payload the command carries.
func (c *Conn) readQEMU() error {
	head := make([]byte, 3)
	if err := readFull(c.r, head); err != nil {
		return fmt.Errorf("rfb: read qemu message header: %w", err)
	}
	if head[0] != qemuSubAudio {
		return fmt.Errorf("rfb: unknown QEMU message sub-type %d", head[0])
	}
	switch cmd := binary.BigEndian.Uint16(head[1:]); cmd {
	case qemuAudioBegin, qemuAudioEnd:
		// Payload-free notifications that capture started or stopped.
		return nil
	case qemuAudioData:
		return c.readAudioData()
	default:
		return fmt.Errorf("rfb: unknown QEMU audio message %d", cmd)
	}
}

// readAudioData consumes one batch of PCM samples: a u32 byte count followed
// by that many bytes in the negotiated format. The buffer is handed to the
// OnAudio callback, which must not retain it.
func (c *Conn) readAudioData() error {
	var head [4]byte
	if err := readFull(c.r, head[:]); err != nil {
		return fmt.Errorf("rfb: read audio data header: %w", err)
	}
	n := int(binary.BigEndian.Uint32(head[:]))
	// QEMU's capture path always bundles small batches. 1 MiB is a generous
	// sanity bound that catches a desynced stream before the allocation.
	if n <= 0 || n > 1<<20 {
		return fmt.Errorf("rfb: implausible audio batch of %d bytes", n)
	}
	c.audioMu.RLock()
	fm := c.audioFmt
	c.audioMu.RUnlock()
	if fm == nil {
		// Audio only flows after the client requested it, and the format is
		// stored before EnableAudio is ever sent, so this is a desync.
		return fmt.Errorf("rfb: audio data before a format was negotiated")
	}
	buf := make([]byte, n)
	if err := readFull(c.r, buf); err != nil {
		return fmt.Errorf("rfb: read audio data: %w", err)
	}
	if c.cfg.OnAudio != nil {
		c.cfg.OnAudio(buf)
	}
	return nil
}

func (c *Conn) readFramebufferUpdate() error {
	var latency, decodeTime time.Duration
	// Count consumed protocol bytes, not transport read-ahead, which may also
	// contain audio or a later update. The message type has already been read.
	beforeUpdate := c.counting.Count() - uint64(c.r.Buffered())
	if c.quality != nil {
		latency = c.quality.beginUpdate(time.Now())
	}
	head := make([]byte, 3)
	if err := readFull(c.r, head); err != nil {
		return fmt.Errorf("rfb: read update header: %w", err)
	}
	numRects := int(binary.BigEndian.Uint16(head[1:]))

	c.fbMu.Lock()
	defer c.fbMu.Unlock()

	for i := 0; i < numRects; i++ {
		hdr := make([]byte, 12)
		if err := readFull(c.r, hdr); err != nil {
			return fmt.Errorf("rfb: read rect header: %w", err)
		}
		r := Rect{
			X:      binary.BigEndian.Uint16(hdr[0:]),
			Y:      binary.BigEndian.Uint16(hdr[2:]),
			Width:  binary.BigEndian.Uint16(hdr[4:]),
			Height: binary.BigEndian.Uint16(hdr[6:]),
		}
		enc := Encoding(int32(binary.BigEndian.Uint32(hdr[8:])))

		before := c.counting.Count()
		start := time.Now()

		stop, err := c.decodeRect(enc, r)
		if err != nil {
			return err
		}
		decodeTime += time.Since(start)

		if c.cfg.OnRect != nil {
			c.cfg.OnRect(enc, r, c.counting.Count()-before)
		}

		c.statsMu.Lock()
		c.stats.Rects++
		c.stats.RectsByEncoding[enc]++
		c.stats.BytesByEncoding[enc] += c.counting.Count() - before
		c.stats.DecodeTime += time.Since(start)
		c.statsMu.Unlock()

		if stop {
			break
		}
	}

	c.statsMu.Lock()
	c.stats.Updates++
	c.statsMu.Unlock()

	if c.cfg.OnFramebufferUpdate != nil || c.quality != nil {
		damage := c.fb.TakeDamage()
		if c.quality != nil {
			bytes := c.counting.Count() - uint64(c.r.Buffered()) - beforeUpdate + 1
			c.quality.noteUpdate(damage, bytes, decodeTime, latency, uint64(c.fb.Width)*uint64(c.fb.Height))
		}
		if c.cfg.OnFramebufferUpdate != nil {
			c.cfg.OnFramebufferUpdate(c.fb, damage)
		}
	}
	return nil
}

// decodeRect dispatches one rectangle. It returns stop=true for LastRect, which
// tells the server's "unknown number of rectangles" form to end the update.
func (c *Conn) decodeRect(enc Encoding, r Rect) (stop bool, err error) {
	switch enc {
	case EncodingLastRect:
		c.noteAck(enc)
		return true, nil

	case EncodingDesktopSize:
		c.resize(int(r.Width), int(r.Height))
		return false, nil

	case EncodingExtendedDesktopSize:
		if err := c.readExtendedDesktopSize(r); err != nil {
			return false, err
		}
		return false, nil

	case EncodingCursor:
		if err := c.readCursor(r); err != nil {
			return false, err
		}
		return false, nil

	case EncodingCursorPos:
		// Position-only update; nothing to read and nothing we act on yet.
		c.noteAck(enc)
		return false, nil

	case EncodingQEMULEDState:
		// QEMU reports the guest's keyboard LEDs as a 1x1 rectangle followed by
		// a single state byte. The payload MUST be consumed: leaving it in the
		// stream desynchronises everything after it.
		c.noteAck(enc)
		state, err := c.r.ReadByte()
		if err != nil {
			return false, fmt.Errorf("rfb: read LED state: %w", err)
		}
		if c.cfg.OnLEDState != nil {
			c.cfg.OnLEDState(LEDState(state))
		}
		return false, nil
	}

	if d, ok := c.decoders[enc]; ok {
		clipped := c.fb.Clip(r)
		if err := d.Decode(c, r); err != nil {
			return false, fmt.Errorf("rfb: decode %s rect %s: %w", enc, r, err)
		}
		c.fb.MarkDamaged(clipped)
		return false, nil
	}

	// Some pseudo-encodings arrive purely as an acknowledgement that the server
	// accepted an extension, with no payload at all. Those are safe to skip.
	//
	// Note the rectangle is NOT necessarily zero-sized: QEMU's
	// send_ext_key_event_ack and send_ext_audio_ack both report the full client
	// width and height, so the size tells us nothing and the encoding number
	// alone decides.
	//
	// This is an explicit allowlist rather than "anything negative has no
	// payload". That blanket assumption is how a client silently desynchronises:
	// a pseudo-encoding that does carry data (QEMU's LED state, for one) leaves
	// its bytes in the stream to be misread as the next message type. Failing
	// loudly on an unrecognised encoding is far easier to diagnose.
	if zeroPayloadPseudoEncodings[enc] {
		c.noteAck(enc)
		return false, nil
	}

	return false, fmt.Errorf("rfb: no decoder for encoding %s (rect %s)", enc, r)
}

// zeroPayloadPseudoEncodings lists pseudo-encodings whose rectangles carry no
// bytes after the rectangle header, so they can be skipped safely.
var zeroPayloadPseudoEncodings = map[Encoding]bool{
	EncodingQEMUExtendedKeyEvent:    true,
	EncodingQEMUPointerMotionChange: true,
	// The audio acknowledgement is payload-free; actual audio data arrives as
	// a separate server message type, not as a rectangle.
	EncodingQEMUAudio: true,
}

func (c *Conn) noteAck(enc Encoding) {
	c.statsMu.Lock()
	c.stats.AckedPseudoEncodings[enc] = true
	c.statsMu.Unlock()
}

// resize reallocates the framebuffer after a server-driven size change. The
// caller must hold fbMu.
func (c *Conn) resize(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	if width == c.fb.Width && height == c.fb.Height {
		return
	}
	c.fb.Resize(width, height)
	if c.cfg.OnResize != nil {
		c.cfg.OnResize(width, height)
	}
}

// readExtendedDesktopSize handles the richer resize rectangle. Here r.X is the
// reason code and r.Y the result code; a non-zero result means the server
// refused a resize we asked for.
func (c *Conn) readExtendedDesktopSize(r Rect) error {
	c.noteAck(EncodingExtendedDesktopSize)

	head := make([]byte, 4)
	if err := readFull(c.r, head); err != nil {
		return fmt.Errorf("rfb: read extended desktop size header: %w", err)
	}
	screens := int(head[0])
	if _, err := io.CopyN(io.Discard, c.r, int64(screens)*16); err != nil {
		return fmt.Errorf("rfb: discard screen list: %w", err)
	}

	// reason 0 means the change came from the server or another client; any
	// other reason is a response to our own request, where result != 0 is a
	// refusal we must not act on.
	const reasonServerSide = 0
	if r.X != reasonServerSide && r.Y != 0 {
		return nil
	}
	c.resize(int(r.Width), int(r.Height))
	return nil
}

// readCursor handles the Cursor pseudo-encoding: an image plus a 1-bit mask
// describing the remote pointer, which the viewer draws locally so the cursor
// does not lag by a round trip.
func (c *Conn) readCursor(r Rect) error {
	c.noteAck(EncodingCursor)

	w, h := int(r.Width), int(r.Height)
	if w == 0 || h == 0 {
		if c.cfg.OnCursor != nil {
			c.cfg.OnCursor(nil, 0, 0, int(r.X), int(r.Y))
		}
		return nil
	}
	bpp := c.pixels.BytesPerPixel()
	pixels := make([]byte, w*h*bpp)
	if err := readFull(c.r, pixels); err != nil {
		return fmt.Errorf("rfb: read cursor pixels: %w", err)
	}
	maskStride := (w + 7) / 8
	mask := make([]byte, maskStride*h)
	if err := readFull(c.r, mask); err != nil {
		return fmt.Errorf("rfb: read cursor mask: %w", err)
	}
	if c.cfg.OnCursor == nil {
		return nil
	}

	rgba := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src := (y*w + x) * bpp
			dst := (y*w + x) * 4
			col := c.pixels.Color(pixels[src:])
			rgba[dst] = col.R
			rgba[dst+1] = col.G
			rgba[dst+2] = col.B
			if mask[y*maskStride+x/8]&(0x80>>(x%8)) != 0 {
				rgba[dst+3] = 0xff
			}
		}
	}
	c.cfg.OnCursor(rgba, w, h, int(r.X), int(r.Y))
	return nil
}

// countingReader tallies bytes read so the connection can report bandwidth per
// encoding without every decoder having to cooperate.
type countingReader struct {
	r io.Reader
	n atomic.Uint64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(uint64(n))
	return n, err
}

func (c *countingReader) Count() uint64 { return c.n.Load() }
