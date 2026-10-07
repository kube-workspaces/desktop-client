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
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"

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

// Counters mirrors the session telemetry the indicator contract requires.
type Counters struct {
	ControlFrames uint64
	MediaFrames   uint64
	MediaBytes    uint64
	ResizeACKs    uint64
	Keyframes     uint64
}

// Session is one held agent transport: admitted controller plus counters.
// It owns no UI and no decoders; media bytes are counted here and handed to
// the presenter layer by the caller.
type Session struct {
	conn     *wsio.Conn
	session  string
	sequence uint64
	counters Counters
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
func (s *Session) writeControl(messageType string, payload map[string]any) error {
	s.sequence++
	body, err := json.Marshal(Envelope{
		Protocol: "kw-agent-v1", ProtocolVersion: 1,
		SessionID: s.session, Generation: 1, Sequence: s.sequence,
		SentAtNs: uint64(time.Now().UnixNano()),
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
// The caller owns closing: Bye is sent by Close.
func Attach(conn *wsio.Conn, ticket string) (*Session, error) {
	session := &Session{conn: conn}
	hello, err := session.readControl()
	if err != nil {
		return nil, fmt.Errorf("agent: hello: %w", err)
	}
	if hello.Type != "hello" || hello.Protocol != "kw-agent-v1" {
		return nil, fmt.Errorf("agent: unexpected greeting %q", hello.Type)
	}
	session.session = hello.SessionID
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
		return nil, fmt.Errorf("agent: refused (%s)", reason)
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

// Close sends Bye and closes the transport. The server releases the seat.
func (s *Session) Close() error {
	_ = s.writeControl("bye", map[string]any{})
	return s.conn.Close()
}

// Counters returns a copy of the session telemetry.
func (s *Session) Counters() Counters { return s.counters }

// SessionID is the server-minted claim echoed through this session.
func (s *Session) SessionID() string { return s.session }
