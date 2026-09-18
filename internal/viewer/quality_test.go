package viewer

import (
	"context"
	"encoding/binary"
	"slices"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

func TestAdaptiveViewerWireLifecycle(t *testing.T) {
	v := New(newFakeBackend(100, 100), Config{AdaptiveQuality: true})
	base := v.RFBConfig(rfb.Config{AudioFormat: &rfb.AudioFormatPCM})
	if base.Quality == nil || base.Quality.IdleDelay != time.Second {
		t.Fatal("viewer did not enable the one-second idle controller")
	}
	// Short test windows, and a deliberate byte ceiling to model a saturated
	// link even with a tiny Tight fill rectangle.
	base.Quality.Window = 20 * time.Millisecond
	base.Quality.IdleDelay = 60 * time.Millisecond
	for i := range base.Quality.Tiers {
		base.Quality.Tiers[i].MaxBytesPerSec = 1
	}
	stream, srv := startFakeServer(t, 100, 100)
	c, err := rfb.NewConn(stream, base)
	if err != nil {
		t.Fatal(err)
	}
	nextEncodings := func() []rfb.Encoding {
		t.Helper()
		select {
		case encs := <-srv.encodings:
			return encs
		case <-time.After(2 * time.Second):
			t.Fatal("no SetEncodings")
			return nil
		}
	}
	if encs := nextEncodings(); !slices.Contains(encs, rfb.QualityLevel(8)) || !slices.Contains(encs, rfb.EncodingQEMUAudio) {
		t.Fatalf("opening tier/capabilities: %v", encs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	// One Tight fill, 100x100. This drives the real decoder and metrics path.
	fill := make([]byte, 20)
	fill[3] = 1
	binary.BigEndian.PutUint16(fill[8:], 100)
	binary.BigEndian.PutUint16(fill[10:], 100)
	binary.BigEndian.PutUint32(fill[12:], uint32(rfb.EncodingTight))
	fill[16], fill[17], fill[18], fill[19] = 0x80, 10, 20, 30
	if err := srv.send(fill); err != nil {
		t.Fatal(err)
	}
	if encs := nextEncodings(); !slices.Contains(encs, rfb.QualityLevel(6)) {
		t.Fatalf("no pressure downgrade: %v", encs)
	}
	for _, enc := range nextEncodings() {
		if enc >= rfb.QualityLevel(0) && enc <= rfb.QualityLevel(9) {
			t.Fatal("idle refresh still permits JPEG")
		}
	}
	req, ok := srv.next(t).(updateRequestMsg)
	if !ok || req.incremental || req.rect.Width != 100 || req.rect.Height != 100 {
		t.Fatalf("not a full lossless repaint: %+v", req)
	}
	if err := srv.send(fill); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-srv.msgs:
		t.Fatalf("idle refresh feedback: %+v", msg)
	case <-time.After(150 * time.Millisecond):
	}
	if c.QualityInterval() != base.Quality.IdleInterval {
		t.Fatal("idle pacing missing")
	}
	// A new connection must get fresh defaults, not the first generation's
	// tuned tiers/configuration. Fixed mode leaves the protocol untouched.
	fresh := v.RFBConfig(rfb.Config{})
	if fresh.Quality.Tiers[2].MaxBytesPerSec == 1 {
		t.Fatal("configuration leaked across reconnect")
	}
	fixed := New(newFakeBackend(100, 100), Config{}).RFBConfig(rfb.Config{})
	if fixed.Quality != nil {
		t.Fatal("fixed mode enabled controller")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("controller survived cancellation")
	}
}
