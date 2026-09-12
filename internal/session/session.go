// Package session connects the platform API client to the RFB protocol client.
//
// It owns the small amount of glue that turns "a workspace name" into "a live
// framebuffer": dial the API's VNC bridge, wrap the WebSocket as a byte stream,
// and run the RFB handshake over it. Both the CLI tools and the graphical
// viewer build on this.
package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

// Session is a live RFB session against a VM workspace.
type Session struct {
	transport *wsio.Conn
	conn      *rfb.Conn
	workspace string
	namespace string
}

// Dial opens a VNC session to a workspace.
//
// The workspace must be a running VM: the API rejects the bridge for other
// workspace types, and refuses a second display session with 409 because the
// KubeVirt VNC console is single-session. Those come back as
// kwclient.ErrNotVM and kwclient.ErrSessionInUse respectively, before any
// WebSocket upgrade happens.
func Dial(ctx context.Context, client *kwclient.Client, namespace, name string, cfg rfb.Config) (*Session, error) {
	ws, err := client.DialVNC(ctx, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("dial vnc bridge for %s/%s: %w", namespace, name, err)
	}

	transport := wsio.New(ws)
	conn, err := rfb.NewConn(transport, cfg)
	if err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("rfb handshake with %s/%s: %w", namespace, name, err)
	}
	return &Session{transport: transport, conn: conn, workspace: name, namespace: namespace}, nil
}

// Conn returns the underlying RFB connection.
func (s *Session) Conn() *rfb.Conn { return s.conn }

// Workspace returns the workspace name this session is attached to.
func (s *Session) Workspace() string { return s.workspace }

// Namespace returns the workspace's namespace.
func (s *Session) Namespace() string { return s.namespace }

// Run drives the RFB read loop until the context is cancelled or the stream
// ends. It returns nil on a clean close.
func (s *Session) Run(ctx context.Context) error {
	err := s.conn.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// Close tears down the session, releasing the server's single-session slot.
func (s *Session) Close() error { return s.transport.Close() }

// RequestUpdate asks for one framebuffer update covering the whole screen.
//
// Pass incremental=false to force a full repaint. That is what a freshly
// established connection needs: the server tracks what it has already sent per
// connection, so an incremental request on a new link describes changes to a
// framebuffer we do not have.
func (s *Session) RequestUpdate(incremental bool) error {
	return s.conn.RequestUpdate(incremental)
}

// RequestUpdates starts a goroutine that keeps asking for incremental
// framebuffer updates.
//
// RFB is pull-based: the server sends nothing until asked, and each request is
// answered by at most one update. A client must therefore re-request
// continuously or the screen freezes. Requesting on a timer rather than
// immediately on each update keeps a fast server from monopolising the link and
// gives the adaptive-quality controller a natural place to throttle.
func (s *Session) RequestUpdates(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 16 * time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.conn.RequestUpdate(true); err != nil {
					return
				}
			}
		}
	}()
}
