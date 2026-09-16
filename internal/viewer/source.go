// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"context"
	"fmt"
	"sync"

	"github.com/kube-workspaces/desktop-client/internal/transport"
)

// ConnSource yields the connections a viewer displays, one after another.
//
// This is the seam that lets the window outlive a connection. A reconnect
// necessarily produces a new *rfb.Conn — the handshake state, the encodings,
// the server's idea of what it has already sent are all per-connection — so a
// viewer that captured one for its lifetime could only be rebuilt per
// connection, taking the window (and binsdl.Load, and sdl.Quit) with it. The
// window is the one thing the user considers stable, so it is the one thing
// the viewer owns outright, and the connection is what gets swapped
// underneath.
//
// Why a source rather than an exported SetConn/Detach pair: the render loop
// owns everything it touches and runs on the thread that owns the window, so
// an externally-driven setter would need its own handshake to get a connection
// across that boundary safely — which is exactly what this interface does
// once, in one place. It also makes the shape of the contract explicit: a
// connection is only valid for as long as its context, which is the mistake
// every caller of a raw accessor makes.
//
// *session.ReconnectingSession satisfies this interface as it stands; the
// viewer deliberately does not import that package, so the coupling is
// structural and testable with a fake.
type ConnSource interface {
	// Attach blocks until a connection is live and returns it together with a
	// context that is cancelled when that particular connection ends.
	//
	// It must return promptly when ctx is cancelled: the viewer waits for it
	// before tearing the window down. An error is terminal — the viewer shows
	// it and exits — so a source that intends to retry must do so internally
	// rather than returning.
	Attach(ctx context.Context) (transport.Conn, context.Context, error)
}

// SingleConn adapts one already-established connection to [ConnSource], for
// callers that do not supervise reconnection.
//
// The connection's lifetime is the context passed to [Viewer.Run]: such a
// caller drives the RFB read loop itself and cancels that context when the
// loop ends. Once the connection has been handed over, Attach blocks until
// then, so a dead connection leaves the last frame frozen rather than
// silently closing the window.
func SingleConn(conn transport.Conn) ConnSource { return &singleConn{conn: conn} }

type singleConn struct {
	mu   sync.Mutex
	conn transport.Conn
}

func (s *singleConn) Attach(ctx context.Context) (transport.Conn, context.Context, error) {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()

	if conn != nil {
		return conn, ctx, nil
	}
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

// pump follows src and hands each connection to the render loop.
//
// It is the only goroutine besides the RFB read loop that touches the viewer,
// and it does so through the same mutex-guarded inbox, because the render loop
// must stay on the thread that owns the window and cannot block waiting for a
// connection that may take minutes to arrive.
func (v *Viewer) pump(ctx context.Context, src ConnSource) {
	for {
		conn, connCtx, err := src.Attach(ctx)
		if err != nil {
			// A cancelled context is the viewer shutting the pump down, not a
			// session failure worth reporting to the user.
			if ctx.Err() == nil {
				v.offerErr(err)
			}
			return
		}
		if conn == nil || connCtx == nil {
			v.offerErr(fmt.Errorf("viewer: connection source returned no connection"))
			return
		}
		v.offer(conn, connCtx)

		select {
		case <-connCtx.Done():
		case <-ctx.Done():
			return
		}
	}
}

// offer hands a new connection to the render loop, which picks it up at the
// top of its next iteration.
func (v *Viewer) offer(conn transport.Conn, connCtx context.Context) {
	v.inbox.Lock()
	v.inbox.nextConn, v.inbox.nextCtx, v.inbox.hasNext = conn, connCtx, true
	v.inbox.needsPresent = true
	v.inbox.Unlock()
	// The render loop may be parked in a blocking event wait; a connection
	// that took a minute to establish must not then wait on a timer.
	v.wake()
}

// offerErr reports that the source has given up for good.
func (v *Viewer) offerErr(err error) {
	v.inbox.Lock()
	if v.inbox.hasErr {
		v.inbox.Unlock()
		return
	}
	v.inbox.srcErr, v.inbox.hasErr = err, true
	v.inbox.needsPresent = true
	v.inbox.Unlock()
	v.wake()
}
