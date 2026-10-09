// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"context"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

// recordInput captures every call the presenter made against the guest-facing
// input surface, so tests can assert the exact event translation without a
// transport.
type recordInput struct {
	mu      sync.Mutex
	keys    []keyCall
	pointer []pointerCall
	wheels  []wheelCall
	resizes []resizeCall
	clips   []string
	resets  int
}

type viewOnlyInput struct{ recordInput }

func (*viewOnlyInput) InputAvailable() bool  { return false }
func (*viewOnlyInput) ResizeAvailable() bool { return false }

func TestViewOnlyTierDoesNotTerminateOnRemoteInput(t *testing.T) {
	inp := &viewOnlyInput{}
	w := &tier1Window{inp: inp, haveFrame: true, opts: Tier1Config{Transport: "Agent"}}
	w.opts.applyDefaults()
	for _, event := range []Event{
		EventKey{Key: keysym.KeyReturn, Down: true},
		EventKey{Key: keysym.KeyReturn, Down: false},
		EventPointer{X: 1, Y: 1},
		EventWheel{DX: 1, DY: 1},
		EventResize{W: 1024, H: 768},
	} {
		if err := w.handleEvent(time.Now(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.applyGuestResize(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(inp.keys)+len(inp.pointer)+len(inp.wheels)+len(inp.resizes) != 0 {
		t.Fatal("view-only guest received injected input")
	}
	if w.connectionSnapshot().Capabilities.GuestResize {
		t.Fatal("unsupported guest resize advertised")
	}
	if err := w.handleKey(EventKey{Rune: 'q', Down: true, Mods: keysym.ModControl | keysym.ModAlt}); err != nil {
		t.Fatal(err)
	}
	if !w.quit {
		t.Fatal("local disconnect hotkey was blocked")
	}
}

type keyCall struct {
	sym  keysym.Keysym
	down bool
}
type pointerCall struct {
	x, y int
	mask rfb.ButtonMask
}
type wheelCall struct{ dx, dy int }
type resizeCall struct{ w, h int }

func (r *recordInput) Key(sym keysym.Keysym, down bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, keyCall{sym, down})
	return nil
}

func (r *recordInput) Pointer(x, y int, mask rfb.ButtonMask) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pointer = append(r.pointer, pointerCall{x, y, mask})
	return nil
}

func (r *recordInput) Wheel(dx, dy int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wheels = append(r.wheels, wheelCall{dx, dy})
	return nil
}

func (r *recordInput) Resize(w, h int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resizes = append(r.resizes, resizeCall{w, h})
	return nil
}

func (r *recordInput) SetClipboard(text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clips = append(r.clips, text)
	return nil
}

func (r *recordInput) ResetKeys() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resets++
	return nil
}

func (r *recordInput) resetCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resets
}

// TestTier1GrabsKeyboardInFullscreen mirrors the RFB viewer's contract: the
// grab follows fullscreen and focus, and asks for nothing when windowed.
func TestTier1GrabsKeyboardInFullscreen(t *testing.T) {
	be := newFakeBackend(1280, 800)
	w := &tier1Window{be: be, inp: &recordInput{}, focused: true}
	now := time.Now()

	if err := be.SetFullscreen(true); err != nil {
		t.Fatalf("fullscreen: %v", err)
	}
	w.syncGrab()
	if len(be.grabs) != 1 || !be.grabs[0] {
		t.Fatalf("grabs = %v in fullscreen, want [true]", be.grabs)
	}
	if err := w.handleEvent(now, EventFocus{Gained: false}); err != nil {
		t.Fatalf("focus loss: %v", err)
	}
	w.syncGrab()
	if grabs := be.grabs; len(grabs) != 2 || grabs[1] {
		t.Fatalf("grabs = %v after focus loss, want [true false]", grabs)
	}
}

// TestTier1WindowInstallsGuestCursor: a queued producer shape reaches the
// backend with its pixels, a hide hides, an empty queue asks for nothing,
// and a reconnect drops a shape that never made it to the window.
func TestTier1WindowInstallsGuestCursor(t *testing.T) {
	be := newFakeBackend(1280, 800)
	sink := &Tier1Sink{}
	w := &tier1Window{be: be, sink: sink, inp: &recordInput{}}

	pix := []byte{0x00, 0xff, 0x00, 0xff}
	sink.GuestCursor(&CursorShape{Pix: pix, W: 1, H: 1})
	w.syncCursor()
	got := be.lastCursor()
	if got == nil || got.W != 1 || got.H != 1 || len(got.Pix) != 4 || got.Pix[1] != 0xff {
		t.Fatalf("installed cursor = %+v, want the 1x1 green shape", got)
	}

	sink.GuestCursor(&CursorShape{})
	w.syncCursor()
	if got := be.lastCursor(); got == nil || !got.Hidden() {
		t.Fatalf("cursor = %+v after a hide, want hidden", got)
	}

	n := len(be.cursors)
	w.syncCursor()
	if len(be.cursors) != n {
		t.Fatal("empty queue installed a cursor")
	}

	sink.GuestCursor(&CursorShape{Pix: pix, W: 1, H: 1})
	sink.Reconnecting()
	w.syncCursor()
	if len(be.cursors) != n {
		t.Fatal("reconnect did not drop the pending shape")
	}
}

// --- helpers ----------------------------------------------------------------

func waitForBool(t *testing.T, d time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// startTier1 launches RunTier1 with a canned transport: blocking produce worker
// that pushes exactly one frame once release is closed (nil means "now"), then
// waits out the session. frame nil keeps the loop in the connecting state.
func startTier1(t *testing.T, be Backend, inp Tier1Input, opts Tier1Config, release chan struct{}, frame *image.RGBA) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	produce := func(ctx context.Context, s *Tier1Sink) error {
		if release != nil {
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
		}
		if frame != nil {
			s.Video(frame)
		}
		<-ctx.Done()
		return nil
	}
	go func() { done <- RunTier1(ctx, be, inp, produce, opts) }()
	return cancel, done
}

func TestRunTier1ConnectingOverlayThenFirstFrame(t *testing.T) {
	t.Parallel()
	be := newFakeBackend(1280, 800)
	inp := &recordInput{}
	release := make(chan struct{})
	frame := image.NewRGBA(image.Rect(0, 0, 64, 64))
	cancel, done := startTier1(t, be, inp, Tier1Config{}, release, frame)

	// Before any frame: a dim "connecting" plate and no guest pixels.
	waitForBool(t, 2*time.Second, func() bool {
		be.mu.Lock()
		defer be.mu.Unlock()
		return len(be.overlays) > 0 && be.overlays[len(be.overlays)-1].Dim == overlayDim
	})
	be.mu.Lock()
	if n := len(be.presents); n == 0 || !be.presents[n-1].Empty() {
		t.Fatal("connecting present drew guest pixels")
	}
	be.mu.Unlock()

	close(release)
	waitForBool(t, 2*time.Second, func() bool { return be.uploadCount() > 0 })

	be.mu.Lock()
	if len(be.texSizes) == 0 || be.texSizes[0] != [2]int{64, 64} {
		t.Fatalf("texture not sized to guest: %v", be.texSizes)
	}
	var fr Rect
	if n := len(be.presents); n > 0 {
		fr = be.presents[n-1]
	}
	be.mu.Unlock()
	if fr.Empty() {
		t.Fatal("frame present is empty after first frame")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunTier1 returned %v on cancel, want nil", err)
	}
}

func TestRunTier1InputTranslationAndWheelSign(t *testing.T) {
	t.Parallel()
	be := newFakeBackend(1280, 800)
	inp := &recordInput{}
	frame := image.NewRGBA(image.Rect(0, 0, 1280, 800))
	cancel, done := startTier1(t, be, inp, Tier1Config{}, nil, frame)
	waitForBool(t, 2*time.Second, func() bool { return be.uploadCount() > 0 })

	be.push(EventPointer{X: 640, Y: 480, Buttons: ButtonLeft})
	waitForBool(t, 2*time.Second, func() bool {
		inp.mu.Lock()
		defer inp.mu.Unlock()
		return len(inp.pointer) >= 1
	})
	inp.mu.Lock()
	pc := inp.pointer[0]
	inp.mu.Unlock()
	if pc.x != 640 || pc.y != 480 || pc.mask != rfb.ButtonLeft {
		t.Fatalf("pointer %+v, want 640,480 + left", pc)
	}

	// Backend DY positive means scroll up; the wire convention (browser
	// deltas) is positive scroll down, so the presenter flips the axis.
	be.push(EventWheel{DX: 1, DY: 2})
	waitForBool(t, 2*time.Second, func() bool {
		inp.mu.Lock()
		defer inp.mu.Unlock()
		return len(inp.wheels) >= 1
	})
	inp.mu.Lock()
	w := inp.wheels[0]
	inp.mu.Unlock()
	if w.dx != 1 || w.dy != -2 {
		t.Fatalf("wheel %+v, want (1,-2)", w)
	}

	be.push(EventKey{Rune: 'a', Down: true})
	waitForBool(t, 2*time.Second, func() bool {
		inp.mu.Lock()
		defer inp.mu.Unlock()
		return len(inp.keys) >= 1
	})
	inp.mu.Lock()
	k := inp.keys[0]
	inp.mu.Unlock()
	if k.sym != keysym.FromRune('a') || !k.down {
		t.Fatalf("key %+v, want %d down", k, keysym.FromRune('a'))
	}

	// Focus loss releases the held key so the guest never believes it is
	// still held down.
	be.push(EventFocus{Gained: false})
	waitForBool(t, 2*time.Second, func() bool {
		inp.mu.Lock()
		defer inp.mu.Unlock()
		for _, k := range inp.keys {
			if k.sym == keysym.FromRune('a') && !k.down {
				return true
			}
		}
		return false
	})

	cancel()
	<-done
}

func TestRunTier1GuestResizeIsDebounced(t *testing.T) {
	t.Parallel()
	be := newFakeBackend(1280, 800)
	inp := &recordInput{}
	frame := image.NewRGBA(image.Rect(0, 0, 1280, 800))
	release := make(chan struct{})
	cancel, done := startTier1(t, be, inp, Tier1Config{}, release, frame)
	close(release)
	waitForBool(t, 2*time.Second, func() bool { return be.uploadCount() > 0 })

	be.resize(900, 700)
	// The debounce floor is 500ms; a request within 200ms means no debounce.
	time.Sleep(200 * time.Millisecond)
	inp.mu.Lock()
	early := len(inp.resizes)
	inp.mu.Unlock()
	if early > 0 {
		t.Fatal("guest resize sent before the debounce elapsed")
	}
	waitForBool(t, 2*time.Second, func() bool {
		inp.mu.Lock()
		defer inp.mu.Unlock()
		return len(inp.resizes) >= 1
	})
	inp.mu.Lock()
	rc := inp.resizes[0]
	inp.mu.Unlock()
	if rc.w != 900 || rc.h != 700 {
		t.Fatalf("guest resize %dx%d, want 900x700", rc.w, rc.h)
	}

	cancel()
	<-done
}

func TestRunTier1ClipboardBothWays(t *testing.T) {
	t.Parallel()
	be := newFakeBackend(1280, 800)
	inp := &recordInput{}
	sink := &Tier1Sink{}
	frame := image.NewRGBA(image.Rect(0, 0, 64, 64))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	produce := func(ctx context.Context, s *Tier1Sink) error {
		s.Video(frame)
		s.GuestClipboard("from-guest")
		<-ctx.Done()
		return nil
	}
	go func() { done <- RunTier1(ctx, be, inp, produce, Tier1Config{}) }()
	_ = sink
	waitForBool(t, 2*time.Second, func() bool { return be.uploadCount() > 0 })

	// A guest push lands on the host clipboard through the window loop.
	waitForBool(t, 2*time.Second, func() bool {
		be.mu.Lock()
		defer be.mu.Unlock()
		for _, s := range be.clipboardSet {
			if s == "from-guest" {
				return true
			}
		}
		return false
	})

	// A host change is polled to the guest.
	be.SetClipboard("hostside")
	waitForBool(t, 2*time.Second, func() bool {
		inp.mu.Lock()
		defer inp.mu.Unlock()
		for _, c := range inp.clips {
			if c == "hostside" {
				return true
			}
		}
		return false
	})

	// The guest push must not be echoed straight back by the next poll.
	cancel()
	<-done
}

func TestRunTier1AudioPlaysDecodedPCM(t *testing.T) {
	t.Parallel()
	be := &audioBackend{fakeBackend: newFakeBackend(1280, 800)}
	inp := &recordInput{}
	frame := image.NewRGBA(image.Rect(0, 0, 64, 64))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	produce := func(ctx context.Context, s *Tier1Sink) error {
		s.Audio([]byte{1, 2, 3, 4})
		s.Video(frame)
		<-ctx.Done()
		return nil
	}
	go func() { done <- RunTier1(ctx, be, inp, produce, Tier1Config{Audio: true}) }()

	waitForBool(t, 2*time.Second, func() bool { return be.audioOpenedCount() > 0 })
	be.audioMu.Lock()
	if be.gotFmt == nil || be.gotFmt.Channels != 2 || be.gotFmt.SampleRate != 48000 ||
		be.gotFmt.BytesPerSample != 2 || !be.gotFmt.LittleEndian {
		t.Fatalf("audio format %+v, want stereo s16le 48kHz", be.gotFmt)
	}
	be.audioMu.Unlock()
	waitForBool(t, 2*time.Second, func() bool {
		be.audioMu.Lock()
		defer be.audioMu.Unlock()
		return len(be.played) == 4
	})

	cancel()
	<-done
}

func TestRunTier1QuitResetsKeys(t *testing.T) {
	t.Parallel()
	be := newFakeBackend(1280, 800)
	inp := &recordInput{}
	cancel, done := startTier1(t, be, inp, Tier1Config{}, nil, nil)
	_ = cancel

	be.push(EventQuit{})
	if err := <-done; err != nil {
		t.Fatalf("RunTier1 returned %v on quit, want nil", err)
	}
	if n := inp.resetCount(); n < 1 {
		t.Fatal("ResetKeys not called on teardown")
	}
}

func TestRunTier1ProducerErrorPropagates(t *testing.T) {
	t.Parallel()
	be := newFakeBackend(1280, 800)
	inp := &recordInput{}
	want := context.Canceled
	frame := image.NewRGBA(image.Rect(0, 0, 64, 64))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- RunTier1(ctx, be, inp, func(ctx context.Context, s *Tier1Sink) error {
			time.Sleep(50 * time.Millisecond)
			s.Video(frame)
			return want
		}, Tier1Config{})
	}()
	select {
	case err := <-done:
		if err != want {
			t.Fatalf("RunTier1 returned %v, want the producer's error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunTier1 did not return the producer error")
	}
}

func TestTier1NoResize(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		inp := &recordInput{}
		w := &tier1Window{inp: inp, haveFrame: true, opts: Tier1Config{NoResize: disabled}}
		w.opts.applyDefaults()
		now := time.Now()
		if err := w.handleEvent(now, EventResize{W: 1024, H: 768}); err != nil {
			t.Fatal(err)
		}
		if err := w.applyGuestResize(now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if (len(inp.resizes) == 0) != disabled {
			t.Fatalf("NoResize=%v: guest resize calls %v", disabled, inp.resizes)
		}
		if w.winW != 1024 || w.winH != 768 {
			t.Fatal("host window size was not updated")
		}
	}
}

func TestTier1TakeoverOnlyOnBusyEnter(t *testing.T) {
	inp := &recordInput{}
	sink := &Tier1Sink{}
	calls := 0
	w := &tier1Window{inp: inp, sink: sink, opts: Tier1Config{Takeover: func() { calls++ }}}
	w.opts.applyDefaults()
	for _, busy := range []bool{false, true} {
		sink.DisplayBusy(busy)
		before := len(inp.keys)
		for _, e := range []EventKey{
			{Key: keysym.KeyReturn, Down: true},
			{Key: keysym.KeyReturn, Down: true, Repeat: true},
			{Key: keysym.KeyReturn, Down: false},
		} {
			if err := w.handleKey(e); err != nil {
				t.Fatal(err)
			}
		}
		if busy && (calls != 1 || len(inp.keys) != before) {
			t.Fatal("busy Enter must request takeover once and swallow both key edges")
		}
		if !busy && calls != 0 {
			t.Fatal("takeover requested outside busy state")
		}
	}
}

func TestTier1DefersResizeAndClipboardUntilReady(t *testing.T) {
	be := newFakeBackend(1280, 800)
	if err := be.SetClipboard("copied while connecting"); err != nil {
		t.Fatal(err)
	}
	inp := &recordInput{}
	sink := &Tier1Sink{}
	w := &tier1Window{be: be, inp: inp, sink: sink}
	w.opts.applyDefaults()
	now := time.Now()
	w.scheduleGuestResize(now, 1024, 768)
	now = now.Add(time.Second)
	for _, recovering := range []bool{false, true} {
		w.haveFrame = recovering
		sink.reconnecting.Store(recovering)
		if err := w.applyGuestResize(now); err != nil {
			t.Fatal(err)
		}
		if err := w.syncGuestClipboard(now); err != nil {
			t.Fatal(err)
		}
		if !w.resizePending || w.hostClip != "" || len(inp.resizes) != 0 || len(inp.clips) != 0 {
			t.Fatal("input consumed while transport unavailable")
		}
	}
	sink.reconnecting.Store(false)
	if err := w.applyGuestResize(now); err != nil {
		t.Fatal(err)
	}
	if err := w.syncGuestClipboard(now); err != nil {
		t.Fatal(err)
	}
	if len(inp.resizes) != 1 || len(inp.clips) != 1 || inp.clips[0] != "copied while connecting" {
		t.Fatalf("pending state not delivered: resize=%v clipboard=%v", inp.resizes, inp.clips)
	}
}

func TestTier1CtrlAltDelShortcuts(t *testing.T) {
	for _, key := range []keysym.Key{keysym.KeyDelete, keysym.KeyEnd} {
		inp := &recordInput{}
		w := &tier1Window{inp: inp}
		w.opts.applyDefaults()
		for _, event := range []EventKey{
			{Key: key, Mods: keysym.ModControl | keysym.ModAlt, Down: true},
			{Key: key, Mods: keysym.ModControl | keysym.ModAlt, Down: true, Repeat: true},
			{Key: key, Down: false},
		} {
			if err := w.handleKey(event); err != nil {
				t.Fatal(err)
			}
		}
		sequence := keysym.ChordCtrlAltDel.Sequence()
		if len(inp.keys) != len(sequence) {
			t.Fatalf("key %v: got %v, want one Ctrl+Alt+Del chord", key, inp.keys)
		}
		for i, action := range sequence {
			if inp.keys[i] != (keyCall{action.Sym, action.Down}) {
				t.Fatalf("key %v: incorrect chord: %v", key, inp.keys)
			}
		}
	}
}

func TestTier1ReconnectKeepsFrameAndClearsGeneration(t *testing.T) {
	be := &audioBackend{fakeBackend: newFakeBackend(100, 100)}
	inp := &recordInput{}
	sink := &Tier1Sink{}
	w := &tier1Window{be: be, inp: inp, sink: sink, audio: be, winW: 100, winH: 100}
	if err := be.Open(WindowOptions{Width: 100, Height: 100}); err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	if err := be.OpenAudio(AudioFormat{Channels: 2, SampleRate: 48000, BytesPerSample: 2, LittleEndian: true}); err != nil {
		t.Fatal(err)
	}
	defer be.CloseAudio()
	sink.Video(image.NewRGBA(image.Rect(0, 0, 32, 32)))
	if err := w.presentFrame(time.Now()); err != nil {
		t.Fatal(err)
	}
	w.held = []keysym.Keysym{'a'}
	w.sentMask, w.sentAny = rfb.ButtonLeft, true
	sink.Audio([]byte{1, 2, 3, 4})
	sink.GuestClipboard("old generation")
	sink.Reconnecting()
	w.syncGeneration()
	if !w.haveFrame || w.texW != 32 || len(w.held) != 0 || w.sentAny || w.sentMask != 0 {
		t.Fatal("reconnect lost texture or retained input")
	}
	if _, pcm := sink.frames.take(); len(pcm) != 0 {
		t.Fatal("stale queued PCM")
	}
	if _, ok := sink.takeClipboard(); ok {
		t.Fatal("stale clipboard")
	}
	if be.audioOpenedCount() != 2 {
		t.Fatal("audio device queue was not reset")
	}
	if err := w.presentFrame(time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.ovKey.text, "Reconnecting") {
		t.Fatalf("missing reconnect overlay: %s", w.ovKey.text)
	}
	sink.Video(image.NewRGBA(image.Rect(0, 0, 32, 32)))
	if sink.reconnecting.Load() {
		t.Fatal("fresh video did not clear reconnect status")
	}
}

func TestInitialGuestSizeLadder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		hostW, hostH, wantW, wantH int
	}{
		{3840, 2160, 1920, 1080}, // bigger host still starts at 1080p
		{2560, 1440, 1920, 1080},
		{1920, 1200, 1920, 1080}, // 16:10 host takes the 1080p rung
		{1920, 1080, 1920, 1080},
		{1600, 900, 1600, 900},
		{1366, 768, 1366, 768},
		{1280, 800, 1280, 720}, // old fallback window maps to 720p
		{1280, 720, 1280, 720},
		{1024, 768, 1024, 576}, // 4:3 host gets an even 16:9 fit
		{800, 600, 800, 450},
		{100, 100, 640, 360}, // below any useful mode: floor
		{0, 0, 1920, 1080},
		{-1, 500, 1920, 1080},
	}
	for _, tc := range cases {
		w, h := initialGuestSize(tc.hostW, tc.hostH)
		if w != tc.wantW || h != tc.wantH {
			t.Errorf("initialGuestSize(%d,%d) = %dx%d, want %dx%d",
				tc.hostW, tc.hostH, w, h, tc.wantW, tc.wantH)
		}
		if w&1 != 0 || h&1 != 0 {
			t.Errorf("initialGuestSize(%d,%d) = %dx%d, want even dimensions",
				tc.hostW, tc.hostH, w, h)
		}
	}
	// Below the ladder the fallback fits an exact 16:9 frame into the host.
	for _, host := range [][2]int{{1024, 768}, {800, 600}, {100, 100}, {1920, 200}} {
		w, h := initialGuestSize(host[0], host[1])
		if w*9 != h*16 {
			t.Errorf("initialGuestSize(%d,%d) = %dx%d, want exact 16:9", host[0], host[1], w, h)
		}
		if w > host[0] && (w != 640 || h != 360) {
			t.Errorf("initialGuestSize(%d,%d) = %dx%d, want fit or floor", host[0], host[1], w, h)
		}
	}
}

func TestTier1InitialResizeSteersGuest(t *testing.T) {
	be := newFakeBackend(1280, 800)
	inp := &recordInput{}
	sink := &Tier1Sink{}
	w := &tier1Window{be: be, inp: inp, sink: sink, winW: 1280, winH: 800, opts: Tier1Config{Transport: "Agent"}}
	w.opts.applyDefaults()
	sink.Video(image.NewRGBA(image.Rect(0, 0, 800, 600)))
	if err := w.presentFrame(time.Now()); err != nil {
		t.Fatal(err)
	}
	if !w.initialResizeDone {
		t.Fatal("one-shot steer did not arm on the first frame")
	}
	// A 1280x800 host takes the 720p rung: window follows, request debounces.
	if w.winW != 1280 || w.winH != 720 {
		t.Fatalf("window = %dx%d, want 1280x720", w.winW, w.winH)
	}
	if !w.resizePending || w.resizeW != 1280 || w.resizeH != 720 {
		t.Fatalf("pending resize = %dx%d/%v, want 1280x720/true", w.resizeW, w.resizeH, w.resizePending)
	}
	if err := w.applyGuestResize(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(inp.resizes) != 1 || inp.resizes[0] != (resizeCall{1280, 720}) {
		t.Fatalf("guest resize calls = %v, want one 1280x720", inp.resizes)
	}
	// The guest following through never re-arms the steer.
	sink.Video(image.NewRGBA(image.Rect(0, 0, 1280, 720)))
	if err := w.presentFrame(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := w.applyGuestResize(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(inp.resizes) != 1 {
		t.Fatalf("guest resize calls = %v, want no second request", inp.resizes)
	}
}

func TestTier1InitialResizeSkips(t *testing.T) {
	t.Parallel()
	boot := func() (*fakeBackend, *recordInput, *Tier1Sink, *tier1Window) {
		be := newFakeBackend(1280, 800)
		inp := &recordInput{}
		sink := &Tier1Sink{}
		w := &tier1Window{be: be, inp: inp, sink: sink, winW: 1280, winH: 800, opts: Tier1Config{Transport: "Agent"}}
		w.opts.applyDefaults()
		return be, inp, sink, w
	}
	firstFrame := func(w *tier1Window, sink *Tier1Sink, fw, fh int) {
		sink.Video(image.NewRGBA(image.Rect(0, 0, fw, fh)))
		if err := w.presentFrame(time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("matching guest fits without asking", func(t *testing.T) {
		be, inp, sink, w := boot()
		firstFrame(w, sink, 1280, 720)
		if w.resizePending || len(inp.resizes) != 0 {
			t.Fatalf("matching guest asked: pending=%v calls=%v", w.resizePending, inp.resizes)
		}
		if ww, wh := be.Size(); ww != 1280 || wh != 720 {
			t.Fatalf("window = %dx%d, want fitted 1280x720", ww, wh)
		}
	})

	t.Run("pinned window keeps fit-to-guest", func(t *testing.T) {
		_, inp, sink, w := boot()
		w.pinned = true
		firstFrame(w, sink, 800, 600)
		if w.resizePending || len(inp.resizes) != 0 {
			t.Fatalf("pinned window asked: pending=%v calls=%v", w.resizePending, inp.resizes)
		}
	})

	t.Run("fixed resolution keeps fit-to-guest", func(t *testing.T) {
		_, inp, sink, w := boot()
		w.opts.NoResize = true
		firstFrame(w, sink, 800, 600)
		if w.resizePending || len(inp.resizes) != 0 {
			t.Fatalf("fixed window asked: pending=%v calls=%v", w.resizePending, inp.resizes)
		}
	})

	t.Run("view-only guest is never asked", func(t *testing.T) {
		be := newFakeBackend(1280, 800)
		inp := &viewOnlyInput{}
		sink := &Tier1Sink{}
		w := &tier1Window{be: be, inp: inp, sink: sink, winW: 1280, winH: 800, opts: Tier1Config{Transport: "Agent"}}
		w.opts.applyDefaults()
		firstFrame(w, sink, 800, 600)
		if w.resizePending || len(inp.resizes) != 0 {
			t.Fatalf("view-only guest asked: pending=%v calls=%v", w.resizePending, inp.resizes)
		}
	})

	t.Run("selkies transport keeps fit-to-guest", func(t *testing.T) {
		be := newFakeBackend(1280, 800)
		inp := &recordInput{}
		sink := &Tier1Sink{}
		w := &tier1Window{be: be, inp: inp, sink: sink, winW: 1280, winH: 800}
		w.opts.applyDefaults()
		firstFrame(w, sink, 800, 600)
		if w.resizePending || len(inp.resizes) != 0 {
			t.Fatalf("selkies guest asked: pending=%v calls=%v", w.resizePending, inp.resizes)
		}
		if ww, wh := be.Size(); ww != 800 || wh != 600 {
			t.Fatalf("window = %dx%d, want fitted 800x600", ww, wh)
		}
	})

	t.Run("in-flight user resize is not overridden", func(t *testing.T) {
		_, inp, sink, w := boot()
		now := time.Now()
		if err := w.handleEvent(now, EventResize{W: 1024, H: 768}); err != nil {
			t.Fatal(err)
		}
		firstFrame(w, sink, 800, 600)
		if !w.resizePending || w.resizeW != 1024 || w.resizeH != 768 {
			t.Fatalf("user resize lost: pending=%v size=%dx%d", w.resizePending, w.resizeW, w.resizeH)
		}
		if err := w.applyGuestResize(now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if len(inp.resizes) != 1 || inp.resizes[0] != (resizeCall{1024, 768}) {
			t.Fatalf("guest resize calls = %v, want the user's 1024x768", inp.resizes)
		}
	})
}

// modesInput is a capable guest that also advertises hello.displayModes.
type modesInput struct {
	recordInput
	modes [][2]int
}

func (m *modesInput) ResizeAvailable() bool  { return true }
func (m *modesInput) DisplayModes() [][2]int { return m.modes }

func TestNearestMode(t *testing.T) {
	t.Parallel()
	table := [][2]int{{800, 600}, {1280, 800}, {1920, 1080}, {3840, 2136}}
	cases := []struct {
		maxW, maxH, wantW, wantH int
		sixteenNine              bool
		found                    bool
	}{
		{3840, 2160, 3840, 2136, false, true},
		{1920, 1080, 1920, 1080, false, true},
		{1920, 1080, 1920, 1080, true, true},
		{1282, 808, 1280, 800, false, true}, // window-chrome size snaps
		{1366, 768, 800, 600, false, true},
		{1366, 768, 0, 0, true, false}, // no exact 16:9 fits
		{100, 100, 0, 0, false, false},
	}
	for _, tc := range cases {
		w, h, found := nearestMode(table, tc.maxW, tc.maxH, tc.sixteenNine)
		if found != tc.found || w != tc.wantW || h != tc.wantH {
			t.Errorf("nearestMode(%dx%d,16:9=%v) = %dx%d/%v, want %dx%d/%v",
				tc.maxW, tc.maxH, tc.sixteenNine, w, h, found, tc.wantW, tc.wantH, tc.found)
		}
	}
	// Out-of-bounds and degenerate entries never qualify.
	junk := [][2]int{{0, 0}, {-800, 600}, {100000, 100000}, {1280, 0}}
	if _, _, found := nearestMode(junk, 99999, 99999, false); found {
		t.Fatal("junk modes qualified")
	}
}

func TestTier1InitialResizePrefersAdvertised(t *testing.T) {
	be := newFakeBackend(1280, 800)
	inp := &modesInput{modes: [][2]int{{800, 600}, {1280, 800}, {1920, 1080}}}
	sink := &Tier1Sink{}
	w := &tier1Window{be: be, inp: inp, sink: sink, winW: 1280, winH: 800, opts: Tier1Config{Transport: "Agent"}}
	w.opts.applyDefaults()
	sink.Video(image.NewRGBA(image.Rect(0, 0, 800, 600)))
	if err := w.presentFrame(time.Now()); err != nil {
		t.Fatal(err)
	}
	// The ladder would guess 1280x720 (unsupported); the advertised table
	// offers 1280x800, which fits and is guaranteed.
	if !w.resizePending || w.resizeW != 1280 || w.resizeH != 800 {
		t.Fatalf("pending resize = %dx%d/%v, want 1280x800/true", w.resizeW, w.resizeH, w.resizePending)
	}
	if err := w.applyGuestResize(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(inp.resizes) != 1 || inp.resizes[0] != (resizeCall{1280, 800}) {
		t.Fatalf("guest resize calls = %v, want one 1280x800", inp.resizes)
	}
}

func TestTier1ResizeSnapsToAdvertised(t *testing.T) {
	be := newFakeBackend(1920, 1080)
	inp := &modesInput{modes: [][2]int{{800, 600}, {1280, 800}, {1920, 1080}}}
	sink := &Tier1Sink{}
	w := &tier1Window{be: be, inp: inp, sink: sink, winW: 1920, winH: 1080,
		texW: 1920, texH: 1080, haveFrame: true, opts: Tier1Config{Transport: "Agent"}}
	w.opts.applyDefaults()
	now := time.Now()
	if err := w.handleEvent(now, EventResize{W: 1282, H: 808}); err != nil {
		t.Fatal(err)
	}
	if err := w.applyGuestResize(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(inp.resizes) != 1 || inp.resizes[0] != (resizeCall{1280, 800}) {
		t.Fatalf("guest resize calls = %v, want snapped 1280x800", inp.resizes)
	}
}

func TestTier1SinkGuestSizeRoundtrip(t *testing.T) {
	t.Parallel()
	var nilSink *Tier1Sink
	nilSink.GuestSize(800, 600) // must not panic on teardown paths
	if _, _, ok := nilSink.takeGuestSize(); ok {
		t.Fatal("nil sink reported a size")
	}
	sink := &Tier1Sink{}
	if _, _, ok := sink.takeGuestSize(); ok {
		t.Fatal("fresh sink reported a size")
	}
	sink.GuestSize(800, 600)
	sink.GuestSize(1280, 800) // only the latest matters
	if w, h, ok := sink.takeGuestSize(); !ok || w != 1280 || h != 800 {
		t.Fatalf("size = %dx%d/%v, want 1280x800/true", w, h, ok)
	}
	if _, _, ok := sink.takeGuestSize(); ok {
		t.Fatal("drained size reported twice")
	}
}

func TestTier1WindowFollowsGuestActual(t *testing.T) {
	be := newFakeBackend(1146, 736)
	inp := &recordInput{}
	sink := &Tier1Sink{}
	w := &tier1Window{be: be, inp: inp, sink: sink, winW: 1146, winH: 736,
		haveFrame: true, opts: Tier1Config{Transport: "Agent"}}
	w.opts.applyDefaults()
	now := time.Now()
	// Guest lands smaller; the next step shrinks the window to it.
	sink.GuestSize(800, 600)
	if err := w.step(now); err != nil {
		t.Fatal(err)
	}
	if w.winW != 800 || w.winH != 600 {
		t.Fatalf("window = %dx%d, want fitted 800x600", w.winW, w.winH)
	}
	if w.ackedW != 800 || w.ackedH != 600 {
		t.Fatalf("acked = %dx%d, want 800x600", w.ackedW, w.ackedH)
	}
	// The SetSize echo must not re-request the live size.
	if err := w.handleEvent(now, EventResize{W: 800, H: 600}); err != nil {
		t.Fatal(err)
	}
	if err := w.applyGuestResize(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(inp.resizes) != 0 {
		t.Fatalf("guest resize calls = %v, want none (already live)", inp.resizes)
	}
	// A genuinely new size still goes out.
	if err := w.handleEvent(now, EventResize{W: 1920, H: 1080}); err != nil {
		t.Fatal(err)
	}
	if err := w.applyGuestResize(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(inp.resizes) != 1 || inp.resizes[0] != (resizeCall{1920, 1080}) {
		t.Fatalf("guest resize calls = %v, want one 1920x1080", inp.resizes)
	}
}

func TestTier1WindowFollowsGuestActualSkips(t *testing.T) {
	t.Parallel()
	// Pinned windows record the ack but keep their explicit size.
	be := newFakeBackend(1146, 736)
	w := &tier1Window{be: be, inp: &recordInput{}, sink: &Tier1Sink{},
		winW: 1146, winH: 736, pinned: true, opts: Tier1Config{Transport: "Agent"}}
	w.opts.applyDefaults()
	w.onGuestSize(800, 600)
	if ww, wh := be.Size(); ww != 1146 || wh != 736 {
		t.Fatalf("pinned window = %dx%d, want kept 1146x736", ww, wh)
	}
	if w.ackedW != 800 || w.ackedH != 600 {
		t.Fatalf("pinned acked = %dx%d, want recorded 800x600", w.ackedW, w.ackedH)
	}
	// Fullscreen likewise keeps the compositor's size.
	be2 := newFakeBackend(1146, 736)
	if err := be2.SetFullscreen(true); err != nil {
		t.Fatal(err)
	}
	w2 := &tier1Window{be: be2, inp: &recordInput{}, sink: &Tier1Sink{},
		winW: 1146, winH: 736, opts: Tier1Config{Transport: "Agent"}}
	w2.opts.applyDefaults()
	w2.onGuestSize(800, 600)
	if ww, wh := be2.Size(); ww != 1146 || wh != 736 {
		t.Fatalf("fullscreen window = %dx%d, want kept 1146x736", ww, wh)
	}
	// Degenerate pushes are ignored.
	w3 := &tier1Window{be: newFakeBackend(100, 100), inp: &recordInput{}}
	w3.opts.applyDefaults()
	w3.onGuestSize(0, 0)
	if w3.ackedW != 0 || w3.ackedH != 0 {
		t.Fatal("degenerate size recorded an ack")
	}
}
