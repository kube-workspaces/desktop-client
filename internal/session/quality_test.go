package session

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/transport"
)

type pacedConn struct {
	transport.Conn
	interval atomic.Int64
}

func (c *pacedConn) QualityInterval() time.Duration { return time.Duration(c.interval.Load()) }

type pacedLink struct {
	*fakeLink
	conn  *pacedConn
	ticks chan time.Time
}

func (l *pacedLink) Conn() transport.Conn { return l.conn }
func (l *pacedLink) RequestUpdate(bool) error {
	l.ticks <- time.Now()
	return nil
}

func TestAdaptiveRequestCadence(t *testing.T) {
	c := &pacedConn{}
	c.interval.Store(int64(10 * time.Millisecond))
	if got := updateInterval(c, time.Second); got != time.Second {
		t.Fatalf("overrode explicit rate cap: %v", got)
	}
	if got := updateInterval(nil, time.Second); got != time.Second {
		t.Fatalf("non-adaptive connection interval: %v", got)
	}
	l := &pacedLink{fakeLink: newFakeLink(), conn: c, ticks: make(chan time.Time, 100)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { requestUpdates(ctx, l, time.Millisecond); close(done) }()
	select {
	case <-l.ticks:
	case <-time.After(time.Second):
		t.Fatal("no active request")
	}
	c.interval.Store(int64(80 * time.Millisecond))
	// One timer may already have been scheduled with the active interval.
	var last time.Time
	for i := 0; i < 4; i++ {
		select {
		case now := <-l.ticks:
			if i == 3 && now.Sub(last) < 70*time.Millisecond {
				t.Fatalf("idle cadence not consumed: %v", now.Sub(last))
			}
			last = now
		case <-time.After(time.Second):
			t.Fatal("request loop stalled")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request loop survived its generation")
	}
}
