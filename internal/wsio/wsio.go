// Package wsio adapts a WebSocket connection to the io.ReadWriteCloser that
// stream-oriented protocols expect.
//
// The kube-workspaces API exposes the KubeVirt consoles as WebSocket bridges
// that relay a raw byte stream: /vnc carries RFB, /exec carries a serial
// console, /ssh carries an SSH session. None of those protocols know anything
// about WebSocket message boundaries, so this package flattens the message
// sequence back into a stream.
package wsio

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Conn presents a WebSocket as a byte stream.
//
// Concurrency follows gorilla/websocket's contract: one reader goroutine and
// one writer goroutine. Read is therefore NOT safe for concurrent use (the
// protocol's read loop owns it), while Write is serialised internally so UI
// goroutines can send input events freely.
type Conn struct {
	ws *websocket.Conn

	// readMu guards the read side so a stray concurrent Read cannot corrupt
	// the partially-consumed message.
	readMu sync.Mutex
	// cur is the in-progress message reader, nil when between messages.
	cur io.Reader

	writeMu sync.Mutex
	// writeTimeout bounds a single frame write so a wedged peer cannot block
	// an input event forever.
	writeTimeout time.Duration

	closeOnce sync.Once
	closeErr  error
}

// Option configures a Conn.
type Option func(*Conn)

// WithWriteTimeout bounds how long a single Write may block. Zero disables the
// deadline. Defaults to 10s.
func WithWriteTimeout(d time.Duration) Option {
	return func(c *Conn) { c.writeTimeout = d }
}

// New wraps ws. The returned Conn takes ownership: closing it closes ws.
func New(ws *websocket.Conn, opts ...Option) *Conn {
	c := &Conn{ws: ws, writeTimeout: 10 * time.Second}
	for _, o := range opts {
		o(c)
	}
	return c
}

// WebSocket exposes the underlying connection for callers that need protocol
// level access, such as sending a close frame with a specific code.
func (c *Conn) WebSocket() *websocket.Conn { return c.ws }

// Read fills p with bytes from the WebSocket, transparently advancing to the
// next message when the current one is exhausted.
//
// Control frames are handled by gorilla beneath this call. Both binary and
// text messages are treated as data: the bridges relay whatever the server
// sends, and the KubeVirt console sends binary, but a bridge that echoed a
// text frame should not break the stream.
func (c *Conn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	for {
		if c.cur == nil {
			_, r, err := c.ws.NextReader()
			if err != nil {
				return 0, translateErr(err)
			}
			c.cur = r
		}
		n, err := c.cur.Read(p)
		if n > 0 {
			// Deliberately return the short read rather than looping to fill p:
			// a stream consumer must not be made to wait for the next message.
			if errors.Is(err, io.EOF) {
				c.cur = nil
				err = nil
			}
			return n, translateErr(err)
		}
		if errors.Is(err, io.EOF) {
			// Message exhausted with nothing to show for it; move on.
			c.cur = nil
			continue
		}
		if err != nil {
			return 0, translateErr(err)
		}
	}
}

// Write sends p as a single binary WebSocket message.
//
// Protocol writers batch their own messages (an RFB client, for example,
// flushes a complete request), so one Write maps to one frame without any
// buffering of our own.
func (c *Conn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.writeTimeout > 0 {
		if err := c.ws.SetWriteDeadline(time.Now().Add(c.writeTimeout)); err != nil {
			return 0, translateErr(err)
		}
	}
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, translateErr(err)
	}
	return len(p), nil
}

// Close closes the WebSocket. It attempts a courteous close handshake first so
// the server's session registry releases the slot promptly, which matters
// because the KubeVirt VNC console is single-session: a connection left to time
// out would lock the display out of a reconnect.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.writeMu.Lock()
		_ = c.ws.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(time.Second),
		)
		c.writeMu.Unlock()
		c.closeErr = c.ws.Close()
	})
	return c.closeErr
}

// SetReadDeadline bounds how long a Read may block.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.ws.SetReadDeadline(t) }

// translateErr converts WebSocket close conditions into the io errors stream
// consumers expect, so a normal remote close reads as a clean EOF rather than
// an opaque protocol error.
func translateErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	if websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseNoStatusReceived,
	) {
		var ce *websocket.CloseError
		if errors.As(err, &ce) && ce.Text != "" {
			// Preserve the reason: the API uses it to say things like
			// "taken over by another user", which the UI surfaces verbatim.
			return fmt.Errorf("%w: %s", io.EOF, ce.Text)
		}
		return io.EOF
	}
	return err
}

// CloseReason extracts the close code and text from an error returned by Read
// or Write, if it was a WebSocket close. ok is false for other errors.
func CloseReason(err error) (code int, text string, ok bool) {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Code, ce.Text, true
	}
	return 0, "", false
}
