package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/agent"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/transport"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

// AgentClipboard adds text clipboard to an independent VNC console. It never
// takes over another agent seat and never runs or pastes commands itself.
type AgentClipboard struct {
	session *agent.Session
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	err     error
}

func OpenAgentClipboard(ctx context.Context, client *kwclient.Client, ns, name string, onText func(string)) (*AgentClipboard, error) {
	ticket, err := client.AgentAttach(ctx, ns, name, "rfb-clipboard")
	if err != nil {
		return nil, err
	}
	release := func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.AgentRelease(releaseCtx, ns, name, ticket.ID)
	}
	conn, _, err := client.DialAgentWS(ctx, ns, name)
	if err != nil {
		release()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	session, err := agent.Attach(wsio.New(conn), ticket.Ticket)
	if err != nil {
		_ = conn.Close()
		release()
		return nil, err
	}
	if !session.ClipboardAvailable() {
		_ = session.Close()
		release()
		return nil, agent.ErrClipboardUnavailable
	}
	runCtx, cancel := context.WithCancel(ctx)
	bridge := &AgentClipboard{session: session, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(bridge.done)
		defer cancel()
		defer release()
		defer session.Close() //nolint:errcheck // best-effort teardown
		var last *string
		err := session.Run(agent.Callbacks{OnClipboard: func(_ string, text *string, err error) error {
			if err != nil {
				return err
			}
			if text != nil && (last == nil || *text != *last) {
				value := *text
				last = &value
				if onText != nil {
					onText(value)
				}
			}
			return nil
		}}, time.Time{})
		bridge.mu.Lock()
		if bridge.err == nil {
			bridge.err = err
		}
		bridge.mu.Unlock()
	}()
	go func() {
		defer session.Close() //nolint:errcheck // also unblocks Run on cancellation
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		interval := time.Duration(ticket.TTLMs/2) * time.Millisecond
		if interval <= 0 {
			interval = 30 * time.Second
		}
		nextRenew := time.Now().Add(interval)
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := session.RequestClipboard(fmt.Sprintf("clipboard-get-%d", time.Now().UnixNano()), nil); err != nil {
					bridge.mu.Lock()
					bridge.err = err
					bridge.mu.Unlock()
					return
				}
				if !time.Now().Before(nextRenew) {
					if err := client.AgentRenew(runCtx, ns, name, ticket.ID); err != nil {
						bridge.mu.Lock()
						bridge.err = err
						bridge.mu.Unlock()
						return
					}
					nextRenew = time.Now().Add(interval)
				}
			}
		}
	}()
	return bridge, nil
}

func (b *AgentClipboard) SetText(text string) error {
	select {
	case <-b.done:
		b.mu.Lock()
		err := b.err
		b.mu.Unlock()
		if err != nil {
			return fmt.Errorf("agent clipboard ended: %w", err)
		}
		return agent.ErrClipboardUnavailable
	default:
		return b.session.RequestClipboard(fmt.Sprintf("clipboard-set-%d", time.Now().UnixNano()), &text)
	}
}

func (b *AgentClipboard) Close() {
	b.cancel()
	_ = b.session.Close()
	<-b.done
}

// WithAgentClipboard preserves all RFB operations except CutText, which is
// routed through the explicitly opted-in guest clipboard backend.
func WithAgentClipboard(source viewer.ConnSource, clipboard *AgentClipboard) viewer.ConnSource {
	return &clipboardSource{ConnSource: source, clipboard: clipboard}
}

type clipboardSource struct {
	viewer.ConnSource
	clipboard *AgentClipboard
}

func (s *clipboardSource) Attach(ctx context.Context) (transport.Conn, context.Context, error) {
	conn, liveCtx, err := s.ConnSource.Attach(ctx)
	if err != nil {
		return nil, nil, err
	}
	return &clipboardConn{Conn: conn, clipboard: s.clipboard}, liveCtx, nil
}

type clipboardConn struct {
	transport.Conn
	clipboard *AgentClipboard
}

func (c *clipboardConn) CutText(text string) error { return c.clipboard.SetText(text) }
