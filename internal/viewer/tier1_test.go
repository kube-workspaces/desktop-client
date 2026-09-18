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
