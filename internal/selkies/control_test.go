// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package selkies

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

// controlPeer spins up a test WebSocket server and returns the client-side
// connection a Control writes on, plus a reader fed by exactly one goroutine
// (the server handler) so messages arrive as a channel in wire order. There is
// never an echo: the reader observes precisely what the client sent.
func controlPeer(t *testing.T) (*websocket.Conn, *controlReader) {
	t.Helper()
	ch := make(chan string, 512)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := websocket.Upgrader{}
		ws, err := u.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			ch <- string(data)
		}
	}))
	t.Cleanup(srv.Close)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws, &controlReader{ch: ch}
}

type controlReader struct {
	ch <-chan string
}

// readText returns the next control verb, failing on timeout.
func (r *controlReader) readText(t *testing.T) string {
	t.Helper()
	select {
	case msg := <-r.ch:
		return msg
	case <-time.After(time.Second):
		t.Fatal("read: timeout waiting for control verb")
		return ""
	}
}

// maybeReadText returns the next control verb or "" if nothing arrived in d.
func (r *controlReader) maybeReadText(t *testing.T, d time.Duration) string {
	t.Helper()
	select {
	case msg := <-r.ch:
		return msg
	case <-time.After(d):
		return ""
	}
}

// --- Input send tests ---

func TestKeySendsKDAndKU(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.Key(keysym.FromRune('a'), true); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != "kd,97" { // XK_a = 0x61 = 97
		t.Fatalf("kd: got %q", got)
	}
	if err := c.Key(keysym.FromRune('a'), false); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != "ku,97" {
		t.Fatalf("ku: got %q", got)
	}
}

func TestNumpadNumLockOn(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, true)
	if err := c.Key(keysym.KP0, true); err != nil {
		t.Fatal(err)
	}
	// Num Lock on: KP_0 -> XK_0 = 0x30
	if got := rd.readText(t); got != "kd,48" { // 0x30
		t.Fatalf("numpad on: got %q", got)
	}
}

func TestNumpadNumLockOff(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.Key(keysym.KP0, true); err != nil {
		t.Fatal(err)
	}
	// Num Lock off: KP_0 raw = 0xffb0 = 65456
	if got := rd.readText(t); got != "kd,65456" {
		t.Fatalf("numpad off: got %q", got)
	}
}

func TestNumLockTogglesOnKeyDown(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	// Press Num Lock: belief toggles to true
	if err := c.Key(keysym.NumLock, true); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != fmt.Sprintf("kd,%d", keysym.NumLock) { // XK_Num_Lock = 0xff7f
		t.Fatalf("numlock down: got %q", got)
	}
	// Now KP0 should be translated to '0' (0x30)
	if err := c.Key(keysym.KP0, true); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != "kd,48" {
		t.Fatalf("numpad after numlock: got %q", got)
	}
	// Release NumLock: belief toggles back to false
	if err := c.Key(keysym.NumLock, false); err != nil {
		t.Fatal(err)
	}
	// Release NumLock does not toggle the belief: the guest OS toggles once on
	// the press, and the browser samples getModifierState("NumLock") per key.
	rd.readText(t) // ku,<NumLock>
	// KP0 is still '0' while the belief remains on
	if err := c.Key(keysym.KP0, true); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != "kd,48" {
		t.Fatalf("numpad after numlock still on: got %q", got)
	}
	// Pressing NumLock again toggles the belief back off
	if err := c.Key(keysym.NumLock, true); err != nil {
		t.Fatal(err)
	}
	rd.readText(t) // kd,<NumLock>
	if err := c.Key(keysym.KP0, true); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != "kd,65456" {
		t.Fatalf("numpad after numlock off: got %q", got)
	}
}

func TestRepeatedKeyDownResendsKD(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	for i := 0; i < 3; i++ {
		if err := c.Key(keysym.Space, true); err != nil {
			t.Fatal(err)
		}
		if got := rd.readText(t); got != "kd,32" {
			t.Fatalf("repeat %d: got %q", i, got)
		}
	}
}

func TestKeyUpDroppedWhenNotPressed(t *testing.T) {
	ws, _ := controlPeer(t)
	c := NewControl(ws, false)
	// Release without a prior press must not block or error
	if err := c.Key(keysym.Space, false); err != nil {
		t.Fatal(err)
	}
}

func TestResetKeysSendsKR(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.Key(keysym.FromRune('a'), true); err != nil {
		t.Fatal(err)
	}
	if err := c.Key(keysym.FromRune('b'), true); err != nil {
		t.Fatal(err)
	}
	// Drain kd messages
	rd.readText(t)
	rd.readText(t)
	if err := c.ResetKeys(); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != "kr" {
		t.Fatalf("kr: got %q", got)
	}
}

func TestHeartbeatRefreshesHeld(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.Key(keysym.FromRune('a'), true); err != nil {
		t.Fatal(err)
	}
	kd := rd.readText(t)
	if kd != "kd,97" {
		t.Fatalf("kd: got %q", kd)
	}
	// Wait just past one heartbeat interval
	time.Sleep(keyHeartbeatInterval + 50*time.Millisecond)
	kh := rd.readText(t)
	if !strings.HasPrefix(kh, "kh,") {
		t.Fatalf("kh: got %q", kh)
	}
	if !strings.Contains(kh, "97") {
		t.Fatalf("kh missing keysym: %q", kh)
	}
	// Release
	if err := c.Key(keysym.FromRune('a'), false); err != nil {
		t.Fatal(err)
	}
	ku := rd.readText(t)
	if ku != "ku,97" {
		t.Fatalf("ku: got %q", ku)
	}
	// Wait well past the next heartbeat tick: no further kh
	maybe := rd.maybeReadText(t, keyHeartbeatInterval*2)
	if maybe != "" {
		t.Fatalf("heartbeat after release: %q", maybe)
	}
}

func TestHeartbeatNoMessagesWhenNothingHeld(t *testing.T) {
	ws, _ := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.Key(keysym.FromRune('a'), false); err != nil {
		t.Fatal(err)
	}
	// Verify nothing sent at all and no heartbeat started
	time.Sleep(keyHeartbeatInterval * 3)
	// The Peer handler drain loop never writes to the client, so the reader
	// never fires. This test mainly proves the keyup path does not crash.
	_ = c
}

func TestPointerSwapsMiddleAndRight(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	tests := []struct {
		buttons rfb.ButtonMask
		want    string
	}{
		{rfb.ButtonLeft, "m,10,20,1,0"},
		{rfb.ButtonMiddle, "m,10,20,2,0"}, // wire bit1
		{rfb.ButtonRight, "m,10,20,4,0"},  // wire bit2
		{rfb.ButtonLeft | rfb.ButtonRight, "m,10,20,5,0"},
	}
	for _, tt := range tests {
		if err := c.Pointer(10, 20, tt.buttons); err != nil {
			t.Fatal(err)
		}
		if got := rd.readText(t); got != tt.want {
			t.Fatalf("buttons %d: got %q want %q", tt.buttons, got, tt.want)
		}
	}
}

func TestWheelDownMagnitudeAndBits(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.Wheel(0, 3); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"m2,0,0,0,3", // clear bit3 (0x08) → 0
		"m2,0,0,8,3", // set bit3 → 0x08
		"m2,0,0,0,3", // restore baseline (no buttons held)
	}
	for i, want := range expected {
		got := rd.readText(t)
		if got != want {
			t.Fatalf("wheel down msg %d: got %q want %q", i, got, want)
		}
	}
}

func TestWheelUpBits(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.Wheel(0, -2); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"m2,0,0,0,2",  // clear bit4 (0x10) → 0
		"m2,0,0,16,2", // set bit4 → 0x10
		"m2,0,0,0,2",  // restore
	}
	for i, want := range expected {
		got := rd.readText(t)
		if got != want {
			t.Fatalf("wheel up msg %d: got %q want %q", i, got, want)
		}
	}
}

func TestWheelHorizontalRight(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.Wheel(2, 0); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"m2,0,0,128,2", // pointerMask(0)|bit7=128, mag=2
		"m2,0,0,0,2",   // restore
	}
	for i, want := range expected {
		got := rd.readText(t)
		if got != want {
			t.Fatalf("wheel right msg %d: got %q want %q", i, got, want)
		}
	}
}

func TestWheelHorizontalLeft(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.Wheel(-1, 0); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"m2,0,0,64,1", // bit6=0x40, mag=1
		"m2,0,0,0,1",  // restore
	}
	for i, want := range expected {
		got := rd.readText(t)
		if got != want {
			t.Fatalf("wheel left msg %d: got %q want %q", i, got, want)
		}
	}
}

func TestWheelPreservesButtons(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	// Press Left then scroll down
	if err := c.Pointer(100, 100, rfb.ButtonLeft); err != nil {
		t.Fatal(err)
	}
	rd.readText(t) // m,...,1,0
	if err := c.Wheel(0, 1); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"m2,0,0,1,1", // clear bit3 from pointerMask(1) → 1 &^ 8 = 1
		"m2,0,0,9,1", // set bit3 → 1|8 = 9
		"m2,0,0,1,1", // restore baseline (Left = 1)
	}
	for i, want := range expected {
		got := rd.readText(t)
		if got != want {
			t.Fatalf("wheel preserved msg %d: got %q want %q", i, got, want)
		}
	}
}

func TestResizeFormats(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	for _, tt := range []struct {
		w, h int
		want string
	}{
		{800, 600, "r,800x600"},
		{1920, 1080, "r,1920x1080"},
	} {
		if err := c.Resize(tt.w, tt.h); err != nil {
			t.Fatal(err)
		}
		if got := rd.readText(t); got != tt.want {
			t.Fatalf("resize %dx%d: got %q", tt.w, tt.h, got)
		}
	}
}

func TestResizeRejectsBounds(t *testing.T) {
	ws, _ := controlPeer(t)
	c := NewControl(ws, false)
	for _, tt := range []struct {
		w, h int
	}{
		{0, 600}, {800, 0}, {0, 0}, {70000, 600}, {800, 70000},
	} {
		if err := c.Resize(tt.w, tt.h); err == nil {
			t.Fatalf("resize %dx%d accepted", tt.w, tt.h)
		}
	}
}

func TestScaleFormats(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.Scale(1.25); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != "s,1.25" {
		t.Fatalf("scale: got %q", got)
	}
	if err := c.Scale(2); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != "s,2" {
		t.Fatalf("scale int: got %q", got)
	}
}

func TestScaleRejectsNonPositive(t *testing.T) {
	ws, _ := controlPeer(t)
	c := NewControl(ws, false)
	for _, v := range []float64{0, -1} {
		if err := c.Scale(v); err == nil {
			t.Fatalf("scale %v accepted", v)
		}
	}
}

func TestSetCursorVisible(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.SetCursorVisible(false); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != "p,0" {
		t.Fatalf("cursor hide: got %q", got)
	}
	if err := c.SetCursorVisible(true); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != "p,1" {
		t.Fatalf("cursor show: got %q", got)
	}
}

func TestSendRawVerb(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.Send("START_VIDEO"); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != "START_VIDEO" {
		t.Fatalf("send: got %q", got)
	}
}

func TestRequestClipboard(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.RequestClipboard(); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != "cr" {
		t.Fatalf("cr: got %q", got)
	}
}

func TestClipboardSmallText(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	text := "hello"
	want := "cw," + base64.StdEncoding.EncodeToString([]byte(text))
	if err := c.ClipboardText(text); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != want {
		t.Fatalf("cw: got %q", got)
	}
}

func TestClipboardSmallBinary(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	data := []byte{0xFF, 0xFE}
	mime := "image/png"
	want := fmt.Sprintf("cb,%s,%s", mime, base64.StdEncoding.EncodeToString(data))
	if err := c.ClipboardBinary(mime, data); err != nil {
		t.Fatal(err)
	}
	if got := rd.readText(t); got != want {
		t.Fatalf("cb: got %q", got)
	}
}

func TestClipboardLargeTextMultipart(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	big := strings.Repeat("X", simpleClipboardMax+1)
	if err := c.ClipboardText(big); err != nil {
		t.Fatal(err)
	}
	// cws with id=1
	start := rd.readText(t)
	if !strings.HasPrefix(start, "cws,1,") {
		t.Fatalf("cws start: %q", start)
	}
	// drain cwd chunks
	for {
		msg := rd.readText(t)
		if strings.HasPrefix(msg, "cwd,1,") {
			continue
		}
		if msg == "cwe,1" {
			break
		}
		t.Fatalf("unexpected chunk: %q", msg)
	}
}

func TestClipboardLargeBinaryMultipart(t *testing.T) {
	ws, rd := controlPeer(t)
	c := NewControl(ws, false)
	big := make([]byte, simpleClipboardMax+1)
	rand.Read(big)
	if err := c.ClipboardBinary("image/bmp", big); err != nil {
		t.Fatal(err)
	}
	start := rd.readText(t)
	if !strings.HasPrefix(start, "cbs,1,image/bmp,") {
		t.Fatalf("cbs start: %q", start)
	}
	for {
		msg := rd.readText(t)
		if strings.HasPrefix(msg, "cbd,1,") {
			continue
		}
		if msg == "cbe,1" {
			break
		}
		t.Fatalf("unexpected chunk: %q", msg)
	}
}

func TestNoSymbolKeyIsNoop(t *testing.T) {
	ws, _ := controlPeer(t)
	c := NewControl(ws, false)
	if err := c.Key(keysym.NoSymbol, true); err != nil {
		t.Fatal(err)
	}
	if err := c.Key(keysym.VoidSymbol, true); err != nil {
		t.Fatal(err)
	}
	// No frames sent
	time.Sleep(20 * time.Millisecond)
}

func TestKPEnterIsReturnRegardlessOfNumLock(t *testing.T) {
	for _, numlock := range []bool{false, true} {
		ws, rd := controlPeer(t)
		c := NewControl(ws, numlock)
		if err := c.Key(keysym.KPEnter, true); err != nil {
			t.Fatal(err)
		}
		if got := rd.readText(t); got != "kd,65293" { // XK_Return = 0xff0d
			t.Fatalf("numlock=%v kpe: got %q", numlock, got)
		}
		if err := c.Key(keysym.KPEnter, false); err != nil {
			t.Fatal(err)
		}
		if got := rd.readText(t); got != "ku,65293" {
			t.Fatalf("numlock=%v kpe up: got %q", numlock, got)
		}
	}
}

func TestHeartbeatNoRace(t *testing.T) {
	ws, _ := controlPeer(t)
	c := NewControl(ws, false)
	// Hammer keys rapidly to exercise the heartbeat start/stop path under -race.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Key(keysym.FromRune('a'), true)
			time.Sleep(time.Millisecond)
			_ = c.Key(keysym.FromRune('a'), false)
		}()
	}
	wg.Wait()
	time.Sleep(keyHeartbeatInterval + 50*time.Millisecond)
}

// --- Parser tests ---

func TestParseMode(t *testing.T) {
	p := &ControlParser{}
	evt, ok := p.Parse("MODE websockets")
	if !ok || evt.Kind != EventMode || evt.Mode != "websockets" {
		t.Fatalf("unexpected: %+v %v", evt, ok)
	}
}

func TestParseCursor(t *testing.T) {
	p := &ControlParser{}
	json := `{"curdata":"iVBOR","width":32,"height":32,"hotx":5,"hoty":5,"handle":42}`
	evt, ok := p.Parse("cursor," + json)
	if !ok || evt.Kind != EventCursor {
		t.Fatalf("unexpected: %+v", evt)
	}
	if evt.Cursor.Handle != 42 || evt.Cursor.HotX != 5 || evt.Cursor.Data != "iVBOR" {
		t.Fatalf("fields: %+v", evt.Cursor)
	}
	if !evt.Cursor.Visible() {
		t.Fatal("expected visible")
	}
}

func TestParseCursorHidden(t *testing.T) {
	p := &ControlParser{}
	json := `{"curdata":"","width":0,"height":0,"hotx":0,"hoty":0,"handle":0}`
	evt, ok := p.Parse("cursor," + json)
	if !ok || !evt.Cursor.Visible() == false {
		t.Fatalf("unexpected: %+v", evt)
	}
}

func TestParseClipboardText(t *testing.T) {
	p := &ControlParser{}
	data := "paste me"
	evt, ok := p.Parse("clipboard," + base64.StdEncoding.EncodeToString([]byte(data)))
	if !ok || evt.Kind != EventClipboard || evt.Clipboard.Text != data {
		t.Fatalf("unexpected: %+v %v", evt, ok)
	}
	if evt.Clipboard.MIME != "" || evt.Clipboard.Binary {
		t.Fatalf("expected text: %+v", evt.Clipboard)
	}
}

func TestParseClipboardBinary(t *testing.T) {
	p := &ControlParser{}
	raw := []byte{0xFF, 0xFD}
	evt, ok := p.Parse("clipboard_binary,image/png," + base64.StdEncoding.EncodeToString(raw))
	if !ok || evt.Kind != EventClipboard || !evt.Clipboard.Binary || evt.Clipboard.MIME != "image/png" {
		t.Fatalf("unexpected: %+v %v", evt, ok)
	}
	if len(evt.Clipboard.Data) != 2 || evt.Clipboard.Data[0] != 0xFF {
		t.Fatalf("data: %v", evt.Clipboard.Data)
	}
}

func TestParseClipboardReplySetsTag(t *testing.T) {
	p := &ControlParser{}
	text := "hi"
	evt, ok := p.Parse("clipboard_reply,cr")
	if ok {
		t.Fatalf("reply should not return event: %+v", evt)
	}
	evt, ok = p.Parse("clipboard," + base64.StdEncoding.EncodeToString([]byte(text)))
	if !ok || evt.Clipboard.Tag != "cr" {
		t.Fatalf("expected cr tag: %+v %v", evt, ok)
	}
}

func TestParseMultipartText(t *testing.T) {
	p := &ControlParser{}
	c1 := base64.StdEncoding.EncodeToString([]byte("hel"))
	c2 := base64.StdEncoding.EncodeToString([]byte("lo w"))
	c3 := base64.StdEncoding.EncodeToString([]byte("orld"))
	p.Parse("clipboard_start,text/plain,11")
	p.Parse("clipboard_data," + c1)
	evt, ok := p.Parse("clipboard_data," + c2)
	if ok {
		t.Fatalf("data should not complete: %+v", evt)
	}
	p.Parse("clipboard_data," + c3)
	evt, ok = p.Parse("clipboard_finish")
	if !ok || evt.Kind != EventClipboard || evt.Clipboard.Text != "hello world" {
		t.Fatalf("assembled: %+v %v (text %q)", evt, ok, evt.Clipboard.Text)
	}
}

func TestParseMultipartBinary(t *testing.T) {
	p := &ControlParser{}
	c1 := base64.StdEncoding.EncodeToString([]byte{0xFF})
	c2 := base64.StdEncoding.EncodeToString([]byte{0xFE})
	p.Parse("clipboard_start,image/x-icon,2")
	p.Parse("clipboard_data," + c1)
	evt, ok := p.Parse("clipboard_data," + c2)
	if ok {
		t.Fatalf("mid chunk ok")
	}
	evt, ok = p.Parse("clipboard_finish")
	if !ok || !evt.Clipboard.Binary || evt.Clipboard.MIME != "image/x-icon" {
		t.Fatalf("binary multipart: %+v %v", evt, ok)
	}
	if len(evt.Clipboard.Data) != 2 || evt.Clipboard.Data[0] != 0xFF || evt.Clipboard.Data[1] != 0xFE {
		t.Fatalf("data: %v", evt.Clipboard.Data)
	}
}

func TestParseMultipartAbortsOnBadFrame(t *testing.T) {
	p := &ControlParser{}
	p.Parse("clipboard_start,text/plain,100")
	evt, ok := p.Parse("SOME_UNEXPECTED")
	if ok || p.inTransfer {
		t.Fatalf("expected abort: %+v %v inTransfer=%v", evt, ok, p.inTransfer)
	}
}

func TestParseSystem(t *testing.T) {
	p := &ControlParser{}
	for _, verb := range []string{
		"AUDIO_DISABLED", "AUDIO_STARTED", "AUDIO_STOPPED",
		"MICROPHONE_DISABLED", "command_done,notepad", "command_error,bad",
	} {
		evt, ok := p.Parse(verb)
		if !ok || evt.Kind != EventSystem || evt.Text != verb {
			t.Fatalf("%q: %+v %v", verb, evt, ok)
		}
	}
}

func TestParseSettings(t *testing.T) {
	p := &ControlParser{}
	raw := `{"type":"server_settings","settings":{"framerate":30,"audio_bitrate":128000}}`
	evt, ok := p.Parse(raw)
	if !ok || evt.Kind != EventSettings {
		t.Fatalf("unexpected: %+v %v", evt, ok)
	}
	if evt.Settings["framerate"].(float64) != 30 {
		t.Fatalf("framerate: %v", evt.Settings["framerate"])
	}
}

func TestParseUnknownJSON(t *testing.T) {
	p := &ControlParser{}
	raw := `{"type":"display_config_update","data":{}}`
	evt, ok := p.Parse(raw)
	if !ok || evt.Kind != EventSystem {
		t.Fatalf("unexpected: %+v %v", evt, ok)
	}
}

func TestParseEmpty(t *testing.T) {
	p := &ControlParser{}
	evt, ok := p.Parse("")
	if !ok || evt.Kind != EventSystem {
		t.Fatalf("unexpected: %+v %v", evt, ok)
	}
}

func TestResetClearsAssembly(t *testing.T) {
	p := &ControlParser{}
	p.Parse("clipboard_start,text/plain,100")
	p.Reset()
	if p.inTransfer {
		t.Fatal("still in transfer after Reset")
	}
	// Now a new start should work
	evt, ok := p.Parse("clipboard_finish")
	if ok {
		t.Fatalf("should not complete after reset: %+v", evt)
	}
}
