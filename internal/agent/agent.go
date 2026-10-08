package agent

// Framed-protocol client for the workspace-agent bridge served by the proxy
// at /proxy/{namespace}/{name}/agent/.
//
// One agent frame travels as one WebSocket binary message in each direction
// (control tag 0x00 + u32 length + JSON envelope; media kinds 0x01/0x02).
// The client performs hello → attach(ticket) → control, mirroring the
// server's single-controller discipline: admission refusal, busy and
// unconfigured images surface as typed errors from the handshake, never as
// silent fallbacks.

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

const (
	// maxControlBytes and maxMediaBytes mirror the wire spec caps enforced
	// by the proxy at the edge.
	maxControlBytes = 1024 * 1024
	maxMediaBytes   = 8*1024*1024 + 5
)

// Envelope is one JSON control message.
type Envelope struct {
	Protocol        string         `json:"protocol"`
	ProtocolVersion int            `json:"protocolVersion"`
	SessionID       string         `json:"sessionId"`
	Generation      uint64         `json:"generation"`
	Sequence        uint64         `json:"sequence"`
	SentAtNs        uint64         `json:"sentAtNs"`
	Channel         string         `json:"channel"`
	Type            string         `json:"type"`
	Payload         map[string]any `json:"payload"`
}

// TransportState is the badge state for future chrome: exactly premium,
// console or degraded, with a machine-readable reason. This package only
// ever reports the agent leg; the caller combines it with the console path.
type TransportState int

const (
	// StatePremium means admitted on the agent transport with flow confirmed.
	StatePremium TransportState = iota
	// StateConsole means the agent path is unavailable; use Tier 0.
	StateConsole
	// StateDegraded means the agent path failed after admission or is
	// unusable for this client; surface the reason, never guess.
	StateDegraded
)

// Reason codes for non-premium states.
const (
	ReasonNone             = ""
	ReasonUnconfigured     = "agent-unconfigured"
	ReasonBusy             = "ownership-busy"
	ReasonTicketRefused    = "ticket-refused"
	ReasonCodecUnsupported = "codec-unsupported"
	ReasonTransportLost    = "transport-lost"
)

// ErrRefused marks a guest attach rejection (bad/expired binding): the
// viewer must re-attach with a fresh ticket, never replay or fall back
// silently around ownership.
var ErrRefused = errors.New("agent attach refused by guest")

// ErrProtocol marks a wire violation (bad tag, oversize, malformed
// envelope): the session is unusable, close it.
var ErrProtocol = errors.New("agent wire protocol violation")

var ErrClipboardUnavailable = errors.New("guest agent text clipboard unavailable")
var ErrInputUnavailable = errors.New("guest agent input unavailable")

const MaxClipboardBytes = 64 * 1024

// Counters mirrors the session telemetry the indicator contract requires.
type Counters struct {
	ControlFrames uint64
	MediaFrames   uint64
	MediaBytes    uint64
	VideoFrames   uint64
	VideoBytes    uint64
	AudioFrames   uint64
	AudioBytes    uint64
	ResizeACKs    uint64
	Keyframes     uint64
}

// Ticket is the decoded attach grant. SessionID arrives empty from the API
// ("whoever holds the live claim") and MUST be stamped with the hello claim
// before attach — the guest rejects unstamped tickets. Decode with
// DecodeTicket; never hand-assemble.
type Ticket struct {
	WorkspaceUID        string `json:"workspaceUid"`
	WorkspaceGeneration string `json:"workspaceGeneration"`
	SessionID           string `json:"sessionId"`
	Participant         string `json:"participant"`
	Role                string `json:"role"`
	ControlEpoch        uint64 `json:"controlEpoch"`
	Audience            string `json:"audience"`
	ExpiresAtNs         int64  `json:"expiresAtNs"`
}

// DecodeTicket splits id.payload.signature and returns the session id plus
// the payload claim. Signature verification belongs to the proxy edge (HMAC
// keys); the viewer checks binding shape only.
func DecodeTicket(signed string) (string, Ticket, error) {
	var ticket Ticket
	parts := strings.SplitN(signed, ".", 3)
	if len(parts) != 3 || parts[0] == "" {
		return "", ticket, fmt.Errorf("agent: malformed ticket")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ticket, fmt.Errorf("agent: ticket payload encoding: %w", err)
	}
	if err := json.Unmarshal(raw, &ticket); err != nil {
		return "", ticket, fmt.Errorf("agent: ticket payload shape: %w", err)
	}
	if ticket.WorkspaceUID == "" || ticket.Audience == "" || ticket.ExpiresAtNs <= 0 {
		return "", ticket, fmt.Errorf("agent: ticket missing binding")
	}
	return parts[0], ticket, nil
}

// Session is one held agent transport: admitted controller plus counters.
// It owns no UI and no decoders; media bytes are counted here and handed to
// the presenter layer by the caller.
type Session struct {
	conn             *wsio.Conn
	session          string
	sequence         uint64
	counters         Counters
	writeMu          sync.Mutex
	pending          map[string]bool
	clipboard        bool
	telemetry        bool
	input            bool
	resize           bool
	clipboardPending map[string]bool
	lastSentNs       uint64
}

// readFrame parses one agent frame from the message stream.
func readFrame(reader io.Reader) (tag byte, body []byte, err error) {
	header := make([]byte, 5)
	if _, err = io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	length := int(binary.BigEndian.Uint32(header[1:5]))
	cap := maxControlBytes
	if header[0] != 0x00 {
		if header[0] != 0x01 && header[0] != 0x02 {
			return 0, nil, fmt.Errorf("agent: unknown media kind %d", header[0])
		}
		cap = maxMediaBytes
	}
	if length == 0 || length > cap {
		return 0, nil, fmt.Errorf("agent: frame size %d outside caps", length)
	}
	body = make([]byte, length)
	if _, err = io.ReadFull(reader, body); err != nil {
		return 0, nil, err
	}
	return header[0], body, nil
}

// writeControl sends one JSON envelope as a single binary message.
// Resize requests self-register for ACK pairing in Run.
func (s *Session) writeControl(messageType string, payload map[string]any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.sequence++
	if messageType == "resizeRequest" {
		if id, _ := payload["requestId"].(string); id != "" {
			if s.pending == nil {
				s.pending = map[string]bool{}
			}
			s.pending[id] = true
		}
	}
	stamp := uint64(time.Now().UnixNano())
	if stamp <= s.lastSentNs {
		stamp = s.lastSentNs + 1
	}
	s.lastSentNs = stamp
	body, err := json.Marshal(Envelope{
		Protocol: "kw-agent-v1", ProtocolVersion: 1,
		SessionID: s.session, Generation: 1, Sequence: s.sequence,
		SentAtNs: stamp,
		Channel:  "control", Type: messageType, Payload: payload,
	})
	if err != nil {
		return err
	}
	frame := append([]byte{0x00}, uint32be(len(body))...)
	frame = append(frame, body...)
	_, err = s.conn.Write(frame)
	return err
}

func uint32be(n int) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, uint32(n))
	return out
}

// readControl reads the next control envelope, skipping nothing: media
// frames are counted, not parsed here.
func (s *Session) readControl() (Envelope, error) {
	var message Envelope
	for {
		tag, body, err := readFrame(s.conn)
		if err != nil {
			return message, err
		}
		if tag != 0x00 {
			s.counters.MediaFrames++
			s.counters.MediaBytes += uint64(len(body))
			continue
		}
		if err := json.Unmarshal(body, &message); err != nil {
			return message, fmt.Errorf("agent: control frame is not an envelope: %w", err)
		}
		s.counters.ControlFrames++
		return message, nil
	}
}

// Attach performs hello → attach(ticket) and returns the admitted session.
// The signed ticket is decoded and stamped with the live hello claim
// before sending: the guest validates the ticket as a binding OBJECT and
// rejects raw strings (malformed-ticket) and unstamped claims alike.
// The caller owns closing: Bye is sent by Close.
func Attach(conn *wsio.Conn, signed string) (*Session, error) {
	_, ticket, err := DecodeTicket(signed)
	if err != nil {
		return nil, err
	}
	session := &Session{conn: conn}
	hello, err := session.readControl()
	if err != nil {
		return nil, fmt.Errorf("agent: hello: %w", err)
	}
	if hello.Type != "hello" || hello.Protocol != "kw-agent-v1" {
		return nil, fmt.Errorf("agent: unexpected greeting %q", hello.Type)
	}
	session.session = hello.SessionID
	session.clipboard, _ = hello.Payload["clipboardText"].(bool)
	session.telemetry, _ = hello.Payload["telemetryAvailable"].(bool)
	session.input, _ = hello.Payload["inputAvailable"].(bool)
	session.resize, _ = hello.Payload["resizeAvailable"].(bool)
	ticket.SessionID = hello.SessionID
	if err := session.writeControl("attach", map[string]any{"ticket": ticket}); err != nil {
		return nil, fmt.Errorf("agent: attach: %w", err)
	}
	result, err := session.readControl()
	if err != nil {
		return nil, fmt.Errorf("agent: attach result: %w", err)
	}
	if result.Type != "attachResult" {
		return nil, fmt.Errorf("agent: unexpected %q, want attachResult", result.Type)
	}
	admitted, _ := result.Payload["admitted"].(bool)
	if !admitted {
		reason, _ := result.Payload["reason"].(string)
		if reason == "" {
			reason = ReasonTicketRefused
		}
		return nil, fmt.Errorf("agent: refused (%s): %w", reason, ErrRefused)
	}
	return session, nil
}

// Resize requests a guest mode; the ACK pairs by request id. A NACK reason
// (e.g. display-backend-p0-gated) is returned, never hidden.
func (s *Session) Resize(id string, width, height uint32) (actual map[string]any, err error) {
	if err := s.writeControl("resizeRequest", map[string]any{
		"requestId": id, "width": width, "height": height,
	}); err != nil {
		return nil, err
	}
	ack, err := s.readControl()
	if err != nil {
		return nil, err
	}
	if ack.Type != "resizeAck" || ack.Payload["requestId"] != id {
		return nil, fmt.Errorf("agent: unpaired resize ack")
	}
	s.counters.ResizeACKs++
	return ack.Payload, nil
}

// Keyframe requests an IDR; demands coalesce server-side.
func (s *Session) Keyframe() error {
	if err := s.writeControl("keyframeRequest", map[string]any{}); err != nil {
		return err
	}
	s.counters.Keyframes++
	return nil
}

// RequestResize asks the guest to adopt a mode; the ACK pairs by request
// id in Run (or the synchronous Resize for lock-step diagnostics). Width
// and height ride along for servers that honor them; pairing is by id.
func (s *Session) RequestResize(requestID string, width, height int) error {
	return s.writeControl("resizeRequest", map[string]any{
		"requestId": requestID, "width": width, "height": height,
	})
}

func (s *Session) TelemetryAvailable() bool { return s.telemetry }
func (s *Session) RequestTelemetry() error  { return s.writeControl("telemetry", map[string]any{}) }

func (s *Session) InputAvailable() bool  { return s.input }
func (s *Session) ResizeAvailable() bool { return s.resize }

func (s *Session) Key(keysym uint32, down bool) error {
	if !s.input {
		return ErrInputUnavailable
	}
	validUnicode := keysym > 0x01000000 && keysym <= 0x0110FFFF && utf8.ValidRune(rune(keysym-0x01000000))
	if keysym > 0x0010FFFF && !validUnicode {
		return fmt.Errorf("agent: keysym out of range")
	}
	return s.writeControl("input", map[string]any{"kind": "key", "keysym": keysym, "down": down})
}

func (s *Session) Pointer(x, y int, buttons uint8) error {
	if !s.input {
		return ErrInputUnavailable
	}
	if x < 0 || y < 0 || x > 1_000_000 || y > 1_000_000 {
		return fmt.Errorf("agent: pointer out of range")
	}
	return s.writeControl("input", map[string]any{"kind": "pointer", "x": x, "y": y, "buttons": buttons})
}

func (s *Session) Wheel(dx, dy int) error {
	if !s.input {
		return ErrInputUnavailable
	}
	if dx < -1000 || dx > 1000 || dy < -1000 || dy > 1000 {
		return fmt.Errorf("agent: wheel out of range")
	}
	return s.writeControl("input", map[string]any{"kind": "wheel", "dx": dx, "dy": dy})
}

// Close sends Bye and closes the transport. The server releases the seat.
func (s *Session) Close() error {
	_ = s.writeControl("bye", map[string]any{})
	return s.conn.Close()
}

// Callbacks receives decoded session events. Implementations must be
// non-blocking (called on the read loop); heavy work goes to queues.
// A returned error fails the session loudly — corrupted media never
// limps along silently.
type Callbacks struct {
	OnTelemetry func(payload map[string]any)
	// OnVideo gets one complete H.264 access unit per call.
	OnVideo func(payload []byte) error
	// OnAudio gets one Opus packet per call.
	OnAudio func(payload []byte) error
	// OnResizeAck reports requested vs actual dimensions or a reason.
	OnResizeAck func(requestID string, reason string)
	// OnResizeResult preserves the actual mode and encoder/IDR completion flags.
	OnResizeResult func(requestID string, payload map[string]any)
	// OnClipboard receives paired text reads or set acknowledgements.
	// nil text means no text format (get) or successful write (set).
	OnClipboard func(requestID string, text *string, err error) error
}

// Run pumps the session until the peer closes, the deadline lapses, or a
// wire violation occurs. Control replies route internally (resize pairing);
// media fans out to callbacks. It returns on first error — callers release
// the premium generation before any console fallback.
func (s *Session) Run(callbacks Callbacks, deadline time.Time) error {
	for {
		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil
		}
		// A WebSocket read timeout permanently fails the connection, and a
		// partial framed read cannot be resumed at the next frame header.
		// Use only the overall deadline; unbounded runs are stopped by Close.
		if err := s.conn.SetReadDeadline(deadline); err != nil {
			return err
		}
		tag, body, err := readFrame(s.conn)
		if err != nil {
			if isTimeout(err) && !deadline.IsZero() && !time.Now().Before(deadline) {
				return nil
			}
			return err
		}
		switch tag {
		case 0x00:
			var message Envelope
			if err := json.Unmarshal(body, &message); err != nil {
				return fmt.Errorf("agent: control frame is not an envelope: %w", err)
			}
			s.counters.ControlFrames++
			if message.Type == "telemetry" {
				if callbacks.OnTelemetry != nil {
					callbacks.OnTelemetry(message.Payload)
				}
				continue
			}
			if message.Type == "clipboardResult" {
				id, _ := message.Payload["requestId"].(string)
				s.writeMu.Lock()
				_, paired := s.clipboardPending[id]
				delete(s.clipboardPending, id)
				s.writeMu.Unlock()
				if paired && callbacks.OnClipboard != nil {
					text, err := clipboardResult(message.Payload)
					if err := callbacks.OnClipboard(id, text, err); err != nil {
						return err
					}
				}
				continue
			}
			if message.Type != "resizeAck" {
				continue
			}
			s.counters.ResizeACKs++
			id, _ := message.Payload["requestId"].(string)
			s.writeMu.Lock()
			paired := s.pending[id]
			delete(s.pending, id)
			s.writeMu.Unlock()
			if !paired {
				continue
			}
			if callbacks.OnResizeResult != nil {
				callbacks.OnResizeResult(id, message.Payload)
			}
			if callbacks.OnResizeAck != nil {
				reason, _ := message.Payload["reason"].(string)
				callbacks.OnResizeAck(id, reason)
			}
		case 0x01:
			s.counters.MediaFrames++
			s.counters.MediaBytes += uint64(len(body))
			s.counters.VideoFrames++
			s.counters.VideoBytes += uint64(len(body))
			if callbacks.OnVideo != nil {
				if err := callbacks.OnVideo(body); err != nil {
					return err
				}
			}
		case 0x02:
			s.counters.MediaFrames++
			s.counters.MediaBytes += uint64(len(body))
			s.counters.AudioFrames++
			s.counters.AudioBytes += uint64(len(body))
			if callbacks.OnAudio != nil {
				if err := callbacks.OnAudio(body); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("agent: unknown media kind %d", tag)
		}
	}
}

// ProbeResult reports what a headless pass observed (no decode, no
// presentation — bytes and pairing only).
type ProbeResult struct {
	Admitted      bool
	ResizePaired  bool
	ResizeReason  string
	ControlFrames uint64
	VideoFrames   uint64
	VideoBytes    uint64
	AudioFrames   uint64
	AudioBytes    uint64
}

// Probe runs the full exchange on an attached session: keyframe demand (so
// capture opens on a fresh IDR), a resize round-trip, then a bounded media
// read. Stats come from the session counters (same source the badge reads).
func (s *Session) Probe(readFor time.Duration) (*ProbeResult, error) {
	var videoFrames, videoBytes, audioFrames, audioBytes uint64
	var resizePaired bool
	var resizeReason string
	paired := make(chan struct{}, 1)
	if err := s.Keyframe(); err != nil {
		return nil, err
	}
	if err := s.writeControl("resizeRequest", map[string]any{"requestId": "probe-resize-1"}); err != nil {
		return nil, err
	}
	err := s.Run(Callbacks{
		OnVideo: func(payload []byte) error {
			videoFrames++
			videoBytes += uint64(len(payload))
			return nil
		},
		OnAudio: func(payload []byte) error {
			audioFrames++
			audioBytes += uint64(len(payload))
			return nil
		},
		OnResizeAck: func(requestID string, reason string) {
			if requestID == "probe-resize-1" {
				resizePaired = true
				resizeReason = reason
				select {
				case paired <- struct{}{}:
				default:
				}
			}
		},
	}, time.Now().Add(readFor))
	if err != nil {
		return nil, err
	}
	return &ProbeResult{
		Admitted:      true,
		ResizePaired:  resizePaired,
		ResizeReason:  resizeReason,
		ControlFrames: s.counters.ControlFrames,
		VideoFrames:   videoFrames,
		VideoBytes:    videoBytes,
		AudioFrames:   audioFrames,
		AudioBytes:    audioBytes,
	}, nil
}

func isTimeout(err error) bool {
	type timeout interface{ Timeout() bool }
	var value timeout
	if errors.As(err, &value) {
		return value.Timeout()
	}
	return false
}

// Counters returns a copy of the session telemetry.
func (s *Session) Counters() Counters { return s.counters }

// SessionID is the server-minted claim echoed through this session.
func (s *Session) SessionID() string { return s.session }
