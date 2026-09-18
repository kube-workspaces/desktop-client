package session

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/selkies"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

type recoveryBackend struct {
	fakeTier1Backend
	opens  atomic.Int32
	frames chan struct{}
}

func (b *recoveryBackend) Open(viewer.WindowOptions) error { b.opens.Add(1); return nil }
func (b *recoveryBackend) Upload(viewer.Rect, []byte, int) error {
	select {
	case b.frames <- struct{}{}:
	default:
	}
	return nil
}

type trackedDecoder struct {
	fakeVideoDec
	closed *atomic.Int32
}

func (d *trackedDecoder) Close() { d.closed.Add(1) }

func awaitTier1Socket(t *testing.T, p *selkiesPeer) *websocket.Conn {
	t.Helper()
	select {
	case c := <-p.conns:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("no Tier 1 connection")
		return nil
	}
}

func activateTier1(t *testing.T, p *selkiesPeer, c *websocket.Conn, b *recoveryBackend) {
	t.Helper()
	if err := writeText(c, "MODE websockets"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.waitText("START_VIDEO", time.Second); err != nil {
		t.Fatal(err)
	}
	msg := make([]byte, 19)
	msg[0], msg[1], msg[13] = selkies.Video, 1, 1
	for _, offset := range []int{6, 8, 14, 16} {
		binary.BigEndian.PutUint16(msg[offset:], 32)
	}
	if err := c.WriteMessage(websocket.BinaryMessage, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.frames:
	case <-time.After(time.Second):
		t.Fatal("first frame not presented")
	}
}

func TestTier1LiveRecovery(t *testing.T) {
	for _, outcome := range []string{"recover", "exhaust", "refuse", "cancel", "budget"} {
		t.Run(outcome, func(t *testing.T) {
			p, url := startSelkiesPeer(t)
			b := &recoveryBackend{frames: make(chan struct{}, 10)}
			var created, closed atomic.Int32
			cfg := baseTier1()
			cfg.StartupTimeout = 2 * time.Second
			cfg.RecoveryBudget = 3 * time.Second
			if outcome == "budget" {
				cfg.RecoveryBudget = 50 * time.Millisecond
			}
			cfg.NewVideoDecoder = func() selkies.VideoDecoder {
				created.Add(1)
				return &trackedDecoder{closed: &closed}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- RunTier1(ctx, tier1Client(t, url), "demo", "vm", "", b, cfg) }()
			first := awaitTier1Socket(t, p)
			activateTier1(t, p, first, b)
			_ = first.Close()
			switch outcome {
			case "recover":
				second := awaitTier1Socket(t, p)
				activateTier1(t, p, second, b)
				cancel()
			case "exhaust":
				for i := 0; i < 2; i++ {
					_ = awaitTier1Socket(t, p).Close()
				}
			case "refuse":
				second := awaitTier1Socket(t, p)
				if err := writeText(second, "KILL ownership revoked"); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			}
			select {
			case err := <-done:
				if outcome == "refuse" && !errors.Is(err, ErrNoFallback) {
					t.Fatalf("refusal bypass: %v", err)
				}
				if (outcome == "exhaust" || outcome == "budget") && (err == nil || errors.Is(err, ErrNoFallback)) {
					t.Fatalf("expected recoverable fallback: %v", err)
				}
				if (outcome == "recover" || outcome == "cancel") && err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("recovery exceeded budget/cancellation")
			}
			if created.Load() != closed.Load() {
				t.Fatalf("decoder leak: created %d closed %d", created.Load(), closed.Load())
			}
			if outcome == "exhaust" && created.Load() != 3 {
				t.Fatalf("want initial + 2 attempts, got %d", created.Load())
			}
			if outcome == "budget" && created.Load() != 1 {
				t.Fatal("attempt started after recovery budget")
			}
			if b.opens.Load() != 1 {
				t.Fatal("reconnect recreated the window")
			}
		})
	}
}

type failedTier1Input struct {
	viewer.Tier1Input
	calls int
}

func (f *failedTier1Input) ResetKeys() error { f.calls++; return io.EOF }

func TestTier1InputFailureSignalsSupervisor(t *testing.T) {
	i := &tier1Input{}
	if err := i.ResetKeys(); err != nil {
		t.Fatal("outage input should be discarded")
	}
	f := &failedTier1Input{}
	closed := 0
	i.attach(f, func() error { closed++; return nil })
	if err := i.ResetKeys(); err != nil {
		t.Fatal("input error bypassed recovery")
	}
	_ = i.ResetKeys()
	if closed != 1 || f.calls != 1 {
		t.Fatalf("stale writes/close: %d/%d", f.calls, closed)
	}
}

func TestTier1DialDeadline(t *testing.T) {
	// A proxy that accepts TCP but never upgrades must consume the same
	// establishment budget as MODE/first-frame, not the dialer's 45s default.
	// The existing startup test covers the post-upgrade half of this budget.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := tier1Client(t, "http://127.0.0.1:1")
	if err := RunTier1(ctx, client, "demo", "vm", "", &fakeTier1Backend{}, baseTier1()); err != nil {
		t.Fatalf("cancelled dial should end normally: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	started := time.Now()
	err := RunTier1(context.Background(), tier1Client(t, srv.URL), "demo", "vm", "", &fakeTier1Backend{}, baseTier1())
	if err == nil || errors.Is(err, ErrNoFallback) || time.Since(started) > time.Second {
		t.Fatalf("dial budget not enforced: elapsed %v, err %v", time.Since(started), err)
	}
}
