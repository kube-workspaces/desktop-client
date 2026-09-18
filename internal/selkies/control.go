// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package selkies

import (
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

// Control wire constants. The contract is pinned to Revision: the JS client
// (addons/selkies-web-core/lib/input.js) emits these messages and the server
// (src/selkies/input_handler.py) parses them, both at that revision.
const (
	// keyHeartbeatInterval is the JS _KEY_HEARTBEAT_INTERVAL: the held-key
	// refresh cadence that keeps the server's auto-release sweep from reaping
	// keys still physically down.
	keyHeartbeatInterval = 100 * time.Millisecond
	// writeTimeout bounds one control write, matching the probe's send budget.
	writeTimeout = time.Second
	// simpleClipboardMax is the largest payload carried in a single base64
	// frame; larger transfers switch to the multipart cws/cwd/cwe verbs.
	simpleClipboardMax = 512 * 1024
	// clipboardChunkSize bounds one multipart clipboard chunk.
	clipboardChunkSize = 64 * 1024
)

// Wire pointer-button bits. The client swaps bits 1 and 2 of the browser's
// MouseEvent.buttons mask before sending (_wireButtons), so the wire mask
// numbers bit 1 as middle and bit 2 as right, exactly as the server's X11
// read-back expects. Bits 3/4/6/7 ride scroll pulses, never held buttons.
const (
	wireButtonLeft   = 1 << 0
	wireButtonMiddle = 1 << 1
	wireButtonRight  = 1 << 2
	// The client pulses the wheel on bits 3 (down) and 4 (up):
	// _triggerMouseWheel names its button 3 for 'down' and 4 for 'up', and the
	// server re-reads bit 3 as wheel-down (X button 5) and bit 4 as wheel-up
	// (X button 4). The action names on the server are the client button, not
	// the physical direction.
	wireScrollUp    = 1 << 4
	wireScrollDown  = 1 << 3
	wireScrollLeft  = 1 << 6
	wireScrollRight = 1 << 7
)

// numpad tables translate keypad keysyms to their main-keyboard equivalents
// depending on the guest Num Lock state (JS NumpadTranslations_NumLockOn/Off).
// A keypad keysym absent from the table for the current state is sent as
// written, mirroring the browser.
var (
	numpadOn = map[keysym.Keysym]keysym.Keysym{
		keysym.KPSpace:     keysym.Space,
		keysym.KPEnter:     keysym.Return,
		keysym.KPEqual:     keysym.Equal,
		keysym.KPMultiply:  keysym.FromRune('*'),
		keysym.KPAdd:       keysym.Plus,
		keysym.KPSeparator: keysym.Comma,
		keysym.KPSubtract:  keysym.Minus,
		keysym.KPDecimal:   keysym.Period,
		keysym.KPDivide:    keysym.Slash,
		keysym.KP0:         keysym.FromRune('0'),
		keysym.KP1:         keysym.FromRune('1'),
		keysym.KP2:         keysym.FromRune('2'),
		keysym.KP3:         keysym.FromRune('3'),
		keysym.KP4:         keysym.FromRune('4'),
		keysym.KP5:         keysym.FromRune('5'),
		keysym.KP6:         keysym.FromRune('6'),
		keysym.KP7:         keysym.FromRune('7'),
		keysym.KP8:         keysym.FromRune('8'),
		keysym.KP9:         keysym.FromRune('9'),
	}
	numpadOff = map[keysym.Keysym]keysym.Keysym{
		keysym.KPHome:     keysym.Home,
		keysym.KPUp:       keysym.Up,
		keysym.KPPageUp:   keysym.PageUp,
		keysym.KPLeft:     keysym.Left,
		keysym.KPBegin:    keysym.Clear,
		keysym.KPRight:    keysym.Right,
		keysym.KPEnd:      keysym.End,
		keysym.KPDown:     keysym.Down,
		keysym.KPPageDown: keysym.PageDown,
		keysym.KPInsert:   keysym.Insert,
		keysym.KPDelete:   keysym.Delete,
		keysym.KPEnter:    keysym.Return,
	}
)

// Control sends input and control verbs over one Tier 1 Selkies WebSocket link.
//
// It is the interactive counterpart of [Probe]: Probe measures the media path,
// while Control drives the guest. Control serializes every write on one link,
// tracks which keysyms it has told the guest are held (so a release always
// matches the press, and the kh heartbeat can refresh them), tracks the wire
// pointer mask so scroll pulses preserve held buttons, and tracks the guest
// Num Lock belief so keypad translation stays in step with the guest.
//
// Control is not safe for use from multiple goroutines without external
// coordination on Key/Pointer ordering, except that the heartbeat goroutine
// itself is internal and serialized with the same lock. Callers own the
// WebSocket lifecycle: the send side never closes conn.
type Control struct {
	conn *websocket.Conn

	mu sync.Mutex
	// held is the wire "kd" set in press order, in exactly the form the
	// heartbeat echoes. Release uses the pressed value, so a keypad key whose
	// translation is state-dependent is released as it was sent.
	held []keysym.Keysym
	// pointerMask is the last wire button mask sent, for scroll-pulse restores.
	pointerMask int
	// numlock is the client's belief about the guest Num Lock state. It is
	// seeded at construction and toggled on each sent NumLock press.
	numlock bool
	// transferID numbers multipart clipboard transfers on this link.
	transferID uint64
	// heartbeat bookkeeping: the kh goroutine is running exactly when
	// heartRunning is true, restarted lazily on the next press.
	heartRunning bool
	closed       bool
	stop         chan struct{}
	workers      sync.WaitGroup
	lastInput    time.Time
}

// NewControl returns a Control sending on conn. numLockOn seeds the guest Num
// Lock belief used for keypad translation; pass false when unknown (the
// browser reports the OS state; a desktop client normally tracks the first
// NumLock toggle its user presses).
func NewControl(conn *websocket.Conn, numLockOn bool) *Control {
	return &Control{conn: conn, numlock: numLockOn, stop: make(chan struct{})}
}

// Close stops and joins held-key heartbeats without taking socket ownership.
// Close the transport first when a write may be blocked. Further sends fail.
func (c *Control) Close() error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.held = nil
		close(c.stop)
	}
	c.mu.Unlock()
	c.workers.Wait()
	return nil
}

// Send writes one raw control verb. It is the escape hatch for verbs the
// adapter does not model (START_VIDEO, gamepad js, SET_NATIVE_CURSOR_RENDERING,
// and so on), always serialized with input on the same link.
func (c *Control) Send(verb string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(verb)
}

// Key sends a key press or release by keysym. Repetition is delivered as
// further presses, and no guest release is sent for a key the adapter has not
// pressed; both match the JS client. A NumLock press also flips the adapter's
// belief, keeping keypad translation aligned with the guest.
//
// NoSymbol and VoidSymbol are ignored, so the input loop can pass every event
// through without filtering first.
func (c *Control) Key(sym keysym.Keysym, down bool) error {
	if sym == keysym.NoSymbol || sym == keysym.VoidSymbol {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return io.ErrClosedPipe
	}
	if sym == keysym.NumLock && down {
		c.numlock = !c.numlock
	}
	wire := c.translate(sym)
	if down {
		if !containsSym(c.held, wire) {
			c.held = append(c.held, wire)
		}
		if !c.heartRunning {
			c.heartRunning = true
			c.workers.Add(1)
			go c.heartbeat()
		}
		return c.writeLocked(fmt.Sprintf("kd,%d", wire))
	}
	release, held := c.findAndDelete(wire)
	if !held {
		return nil // a keyup for a code the client never pressed is dropped
	}
	return c.writeLocked(fmt.Sprintf("ku,%d", release))
}

// ResetKeys sends kr, the server's full keyboard release, and forgets the
// held set. Use it for a focus-lost cleanup or before disconnecting.
func (c *Control) ResetKeys() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held = c.held[:0]
	return c.writeLocked("kr")
}

// Pointer sends an absolute pointer position with the RFB button mask re-
// numbered onto the wire's bit layout (middle and right swap).
func (c *Control) Pointer(x, y int, buttons rfb.ButtonMask) error {
	wire := wireButtons(buttons)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pointerMask = wire
	return c.writeLocked(fmt.Sprintf("m,%d,%d,%d,0", x, y, wire))
}

// Wheel emits scroll notches as the JS client does: vertical pulses clear the
// direction bit in a baseline, set it for one rising edge, then restore the
// held buttons; horizontal pulses set and restore on a scroll-only bit. The
// sign convention is browser deltas: positive dy scrolls down, positive dx
// scrolls right. dz is ignored.
func (c *Control) Wheel(dx, dy int) error {
	if dx == 0 && dy == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	mag := func(n int) int {
		if n < 0 {
			n = -n
		}
		if n < 1 {
			n = 1
		}
		return n
	}
	if dy != 0 {
		bit := wireScrollDown
		if dy < 0 {
			bit = wireScrollUp
		}
		cleared := c.pointerMask &^ bit
		for _, mask := range []int{cleared, cleared | bit, c.pointerMask} {
			if err := c.writeLocked(fmt.Sprintf("m2,0,0,%d,%d", mask, mag(dy))); err != nil {
				return err
			}
		}
	}
	if dx != 0 {
		bit := wireScrollLeft
		if dx > 0 {
			bit = wireScrollRight
		}
		for _, mask := range []int{c.pointerMask | bit, c.pointerMask} {
			if err := c.writeLocked(fmt.Sprintf("m2,0,0,%d,%d", mask, mag(dx))); err != nil {
				return err
			}
		}
	}
	return nil
}

// Resize requests a guest resolution change, sent as the server expects
// (`r,<W>x<H>`). Callers should debounce: the guest reconfigures its X
// server, which is not a cheap operation.
func (c *Control) Resize(w, h int) error {
	if w < 1 || h < 1 || w > 1<<16 || h > 1<<16 {
		return fmt.Errorf("selkies: resize %dx%d out of bounds", w, h)
	}
	return c.Send(fmt.Sprintf("r,%dx%d", w, h))
}

// Scale sets the session DPI ratio (`s,<f>`), the primary display's only on
// the X11 backend this client targets.
func (c *Control) Scale(f float64) error {
	if f <= 0 {
		return fmt.Errorf("selkies: invalid scale %g", f)
	}
	return c.Send("s," + strconv.FormatFloat(f, 'f', -1, 64))
}

// SetCursorVisible asks the guest to hide its cursor so the client may draw
// its own (`p,0`), or restore it (`p,1`). It is the short verb the input
// handler treats identically to SET_NATIVE_CURSOR_RENDERING.
func (c *Control) SetCursorVisible(visible bool) error {
	v := 0
	if visible {
		v = 1
	}
	return c.Send(fmt.Sprintf("p,%d", v))
}

// SetClipboard sends text to the guest clipboard under its browser-facing
// name. It is the input-surface alias for [Control.ClipboardText], so a
// [Control] satisfies the viewer's Tier 1 input interface as-is.
func (c *Control) SetClipboard(text string) error { return c.ClipboardText(text) }

// ClipboardText sends UTF-8 text to the guest clipboard. Payloads above
// simpleClipboardMax use the multipart cws/cwd/cwe transfer so the single
// frame stays small.
func (c *Control) ClipboardText(text string) error {
	return c.clipboard([]byte(text), "")
}

// ClipboardBinary sends arbitrary data to the guest clipboard. Payloads above
// simpleClipboardMax use the multipart cbs/cbd/cbe transfer.
func (c *Control) ClipboardBinary(mime string, data []byte) error {
	return c.clipboard(data, mime)
}

// RequestClipboard asks the guest to push its current clipboard text.
func (c *Control) RequestClipboard() error {
	return c.Send("cr")
}

func (c *Control) clipboard(data []byte, mime string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transferID++
	id := strconv.FormatUint(c.transferID, 10)
	if len(data) > simpleClipboardMax {
		if mime == "" {
			if err := c.writeLocked(fmt.Sprintf("cws,%s,%d", id, len(data))); err != nil {
				return err
			}
		} else if err := c.writeLocked(fmt.Sprintf("cbs,%s,%s,%d", id, mime, len(data))); err != nil {
			return err
		}
		chunk := make([]byte, 0, clipboardChunkSize)
		for off := 0; off < len(data); off += clipboardChunkSize {
			end := off + clipboardChunkSize
			if end > len(data) {
				end = len(data)
			}
			chunk = append(chunk[:0], data[off:end]...)
			verb := "cwd"
			if mime != "" {
				verb = "cbd"
			}
			encoded := base64.StdEncoding.EncodeToString(chunk)
			if err := c.writeLocked(fmt.Sprintf("%s,%s,%s", verb, id, encoded)); err != nil {
				return err
			}
		}
		verb := "cwe"
		if mime != "" {
			verb = "cbe"
		}
		return c.writeLocked(fmt.Sprintf("%s,%s", verb, id))
	}
	if mime == "" {
		return c.writeLocked("cw," + base64.StdEncoding.EncodeToString(data))
	}
	return c.writeLocked(fmt.Sprintf("cb,%s,%s", mime, base64.StdEncoding.EncodeToString(data)))
}

// translate maps a keysym for the wire: keypad keysyms follow the guest Num
// Lock belief. The caller holds mu.
func (c *Control) translate(sym keysym.Keysym) keysym.Keysym {
	if c.numlock {
		if s, ok := numpadOn[sym]; ok {
			return s
		}
		return sym
	}
	if s, ok := numpadOff[sym]; ok {
		return s
	}
	return sym
}

// findAndDelete removes the first occurrence of wire from held, returning the
// exact keysym that was pressed there (the value the release must echo) and
// whether anything was held. The caller holds mu.
func (c *Control) findAndDelete(wire keysym.Keysym) (keysym.Keysym, bool) {
	for i, h := range c.held {
		if h == wire {
			release := c.held[i]
			c.held = append(c.held[:i], c.held[i+1:]...)
			return release, true
		}
	}
	return 0, false
}

func containsSym(s []keysym.Keysym, v keysym.Keysym) bool {
	for _, h := range s {
		if h == v {
			return true
		}
	}
	return false
}

// heartbeat refreshes the held-set on the wire cadence the server's auto-
// release sweep expects, stopping itself once nothing is held. The browser
// does exactly this (_startKeyHeartbeat/_stopKeyHeartbeat); holding kh alive
// after a release would refresh keys the guest already got a ku for.
func (c *Control) heartbeat() {
	defer c.workers.Done()
	ticker := time.NewTicker(keyHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			c.mu.Lock()
			c.heartRunning = false
			c.mu.Unlock()
			return
		case <-ticker.C:
		}
		c.mu.Lock()
		var err error
		if len(c.held) != 0 && !c.closed {
			msg := "kh," + joinKeysyms(c.held)
			err = c.writeLocked(msg)
		}
		if len(c.held) == 0 || c.closed || err != nil {
			// Publish stopped under the same lock as Key's launch decision;
			// a press arriving here must start a new worker.
			c.heartRunning = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}
}

func joinKeysyms(s []keysym.Keysym) string {
	b := make([]byte, 0, 16*len(s))
	for i, k := range s {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, int64(k), 10)
	}
	return string(b)
}

func wireButtons(b rfb.ButtonMask) int {
	m := 0
	if b&rfb.ButtonLeft != 0 {
		m |= wireButtonLeft
	}
	// RFB numbers middle before right (RFC 6143 §7.5.5); the wire swaps them.
	if b&rfb.ButtonMiddle != 0 {
		m |= wireButtonMiddle
	}
	if b&rfb.ButtonRight != 0 {
		m |= wireButtonRight
	}
	return m
}

// writeLocked sends one text frame under the write budget. The caller holds mu.
func (c *Control) writeLocked(text string) error {
	if c.closed {
		return io.ErrClosedPipe
	}
	for _, prefix := range []string{"kd,", "ku,", "m,", "m2,", "r,", "cw", "cb"} {
		if strings.HasPrefix(text, prefix) {
			c.lastInput = time.Now()
			break
		}
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.TextMessage, []byte(text))
}

func (c *Control) lastInputAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastInput
}

// SetFramerate uses the pinned input_handler.py _arg_fps control, which calls
// on_set_fps(fps, display_id). It changes encoder cadence without restarting
// the stream or transplanting RFB encoding controls into H.264.
func (c *Control) SetFramerate(fps int) error {
	if fps < 1 || fps > 240 {
		return fmt.Errorf("selkies: framerate out of bounds: %d", fps)
	}
	return c.Send(fmt.Sprintf("_arg_fps,%d", fps))
}
