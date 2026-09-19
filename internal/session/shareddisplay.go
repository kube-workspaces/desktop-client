// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/reconnect"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
	"github.com/kube-workspaces/desktop-client/internal/transport"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

// SharedDialFunc establishes one shared-display stream for a participant, in
// the given role. force asks the server to revoke the previous control lease
// when attaching as controller — the takeover reconnect after a forced REST
// acquire. A new cfg is supplied per attempt because the RFB handshake reads
// it once.
type SharedDialFunc func(ctx context.Context, cfg rfb.Config, role string, force bool) (Link, error)

// SharedDisplayOptions configures a [SharedDisplay]. The zero value is usable
// apart from Dial, which is required.
type SharedDisplayOptions struct {
	// Policy paces retries of transient failures. The zero value is
	// [reconnect.Default] with jitter off.
	Policy reconnect.Policy

	// InUsePoll is how often to retry while the control lease is still held
	// by the previous controller's fence. Zero means [DefaultInUsePoll].
	InUsePoll time.Duration

	// UpdateInterval paces framebuffer update requests on each connection.
	// Zero means [DefaultUpdateInterval].
	UpdateInterval time.Duration

	// OnState is called on every state change. It runs on the supervisor
	// goroutine and must not block; see [Options.OnState].
	OnState func(State, error)

	// Config returns the RFB configuration for the next connection. Nil
	// means a zero rfb.Config.
	Config func() rfb.Config

	// Dial establishes one stream. Required.
	Dial SharedDialFunc
}

// SharedDisplay keeps one participant's shared-display stream alive: it
// reconnects after transport failures like a [ReconnectingSession], and it
// re-dials in place when the participant's role changes, because an observer
// stream and a controller stream are different server-side attachments — the
// fence that gates input is created at connect time.
//
// A role change is not a reconnect: [SharedDisplay.SetRole] drops the live
// connection immediately so the supervisor re-dials with the new role, and
// the window (attached through the same ConnSource contract) swaps the stream
// underneath itself.
type SharedDisplay struct {
	dial      SharedDialFunc
	config    func() rfb.Config
	policy    reconnect.Policy
	inUsePoll time.Duration
	interval  time.Duration
	onState   func(State, error)

	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	state   State
	lastErr error
	// role and force are the dial intent. roleGen is bumped on every change,
	// so the supervisor can tell "the connection ended" apart from "the
	// intent moved on while this connection was live".
	role    string
	force   bool
	roleGen int
	gen     *sharedGeneration
	// changed is closed and replaced on every state, generation or intent
	// change; see [ReconnectingSession.changed].
	changed chan struct{}
}

// sharedGeneration is one live shared-display stream and what ends it.
type sharedGeneration struct {
	link   Link
	ctx    context.Context
	cancel context.CancelFunc
}

// DialSharedDisplay starts a supervised shared-display session. The initial
// role is observer: every client joins as an observer and is promoted later
// through the control endpoints, which is what [SharedDisplay.SetRole]
// re-dials for.
func DialSharedDisplay(ctx context.Context, opts SharedDisplayOptions) (*SharedDisplay, error) {
	if opts.Dial == nil {
		return nil, fmt.Errorf("session: shared display needs a dial function")
	}
	config := opts.Config
	if config == nil {
		config = func() rfb.Config { return rfb.Config{} }
	}
	inUsePoll := opts.InUsePoll
	if inUsePoll == 0 {
		inUsePoll = DefaultInUsePoll
	}
	interval := opts.UpdateInterval
	if interval == 0 {
		interval = DefaultUpdateInterval
	}

	runCtx, cancel := context.WithCancel(ctx)
	s := &SharedDisplay{
		dial:      opts.Dial,
		config:    config,
		policy:    opts.Policy,
		inUsePoll: inUsePoll,
		interval:  interval,
		onState:   opts.OnState,
		cancel:    cancel,
		done:      make(chan struct{}),
		state:     StateConnecting,
		role:      kwclient.DisplayRoleObserver,
		changed:   make(chan struct{}),
	}
	go s.run(runCtx)
	return s, nil
}

// SetRole changes the dial intent. A live connection is dropped immediately
// and the supervisor re-dials with the new role without backoff; a
// disconnected supervisor simply dials the new role next. force applies to
// the next successful dial only: it is the takeover flag, and re-sending it
// on an unrelated later reconnect would revoke our own control lease.
func (s *SharedDisplay) SetRole(role string, force bool) {
	s.mu.Lock()
	if s.role == role && s.force == force {
		s.mu.Unlock()
		return
	}
	s.role, s.force = role, force
	s.roleGen++
	gen := s.gen
	s.mu.Unlock()

	// Closing the transport ends the read loop, which is what returns the
	// supervisor to its dial step; cancelling the generation context tells
	// the attached viewer the same thing from its side.
	if gen != nil {
		gen.cancel()
		_ = gen.link.Close()
	}
	s.wake()
}

// Role returns the current dial intent.
func (s *SharedDisplay) Role() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.role
}

// State returns the current state and the error that caused it, if any.
func (s *SharedDisplay) State() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.lastErr
}

// Attach implements the viewer's ConnSource contract: it blocks until a
// stream is live and returns it with the context that ends when that stream
// does. See [ReconnectingSession.Attach].
func (s *SharedDisplay) Attach(ctx context.Context) (transport.Conn, context.Context, error) {
	for {
		s.mu.Lock()
		gen, state, lastErr, changed := s.gen, s.state, s.lastErr, s.changed
		s.mu.Unlock()

		if gen != nil {
			return gen.link.Conn(), gen.ctx, nil
		}
		if state.Terminal() {
			if lastErr != nil {
				return nil, nil, lastErr
			}
			return nil, nil, fmt.Errorf("session: shared display is %s", state)
		}

		select {
		case <-changed:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

// Close stops the supervisor and releases the stream. See
// [ReconnectingSession.Close].
func (s *SharedDisplay) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		<-s.done
	})
	return nil
}

// intent reads the current dial parameters.
func (s *SharedDisplay) intent() (role string, force bool, roleGen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.role, s.force, s.roleGen
}

// roleChanged reports whether the intent moved on from roleGen.
func (s *SharedDisplay) roleChanged(roleGen int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.roleGen != roleGen
}

// clearForce consumes the takeover flag once its dial succeeded.
func (s *SharedDisplay) clearForce(roleGen int) {
	s.mu.Lock()
	if s.roleGen == roleGen {
		s.force = false
	}
	s.mu.Unlock()
}

// run is the supervisor loop.
func (s *SharedDisplay) run(ctx context.Context) {
	defer close(s.done)
	s.notify(StateConnecting, nil)

	var (
		attempt    int
		inUseSince time.Time
	)
	for {
		if ctx.Err() != nil {
			s.setState(StateClosed, nil)
			return
		}

		role, force, roleGen := s.intent()
		link, err := s.dial(ctx, s.config(), role, force)
		if err == nil {
			// The intent moved while dialling: this stream describes the old
			// role, so it must never go live.
			if s.roleChanged(roleGen) {
				_ = link.Close()
				continue
			}
			s.clearForce(roleGen)
			started := time.Now()
			err = s.serve(ctx, link)
			if ctx.Err() != nil {
				s.setState(StateClosed, nil)
				return
			}
			if s.roleChanged(roleGen) {
				// A role change ended this connection on purpose: re-dial at
				// once, with a clean retry budget and a "connecting" overlay
				// rather than a reconnecting one.
				attempt = 0
				inUseSince = time.Time{}
				s.setState(StateConnecting, nil)
				continue
			}
			if time.Since(started) >= stableConnection {
				attempt = 0
			}
			err = annotate(err)
		} else if ctx.Err() != nil {
			s.setState(StateClosed, nil)
			return
		} else if s.roleChanged(roleGen) {
			continue
		}

		switch Classify(err) {
		case RetryNever:
			s.setState(StateFailed, err)
			return

		case RetrySlow:
			if inUseSince.IsZero() {
				inUseSince = time.Now()
			}
			s.setState(stateFor(err), err)
			if !sleep(ctx, s.inUsePoll) {
				s.setState(StateClosed, nil)
				return
			}

		default: // RetryBackoff
			attempt++
			if s.policy.Exhausted(attempt) {
				s.setState(StateFailed, fmt.Errorf("giving up after %d attempt(s): %w", attempt, orUnknown(err)))
				return
			}
			s.setState(StateReconnecting, err)
			if !sleep(ctx, s.policy.Backoff(attempt)) {
				s.setState(StateClosed, nil)
				return
			}
		}
	}
}

// serve runs one stream to its end and returns the reason it ended.
func (s *SharedDisplay) serve(ctx context.Context, link Link) error {
	genCtx, genCancel := context.WithCancel(ctx)
	defer genCancel()

	// See ReconnectingSession.serve: a fresh stream has no shared history,
	// so the first request is a full frame.
	if err := link.RequestUpdate(false); err != nil {
		_ = link.Close()
		return fmt.Errorf("request full update after connect: %w", err)
	}

	s.connected(&sharedGeneration{link: link, ctx: genCtx, cancel: genCancel})

	if s.interval > 0 {
		go requestUpdates(genCtx, link, s.interval)
	}

	err := link.Run(ctx)

	genCancel()
	s.disconnected()
	_ = link.Close()
	return err
}

// connected publishes a generation and announces it; see
// [ReconnectingSession.connected].
func (s *SharedDisplay) connected(gen *sharedGeneration) {
	s.mu.Lock()
	s.gen = gen
	s.state = StateConnected
	s.lastErr = nil
	s.mu.Unlock()
	s.announce(StateConnected, nil)
}

// disconnected retracts the current generation; see
// [ReconnectingSession.disconnected].
func (s *SharedDisplay) disconnected() {
	s.mu.Lock()
	s.gen = nil
	s.mu.Unlock()
	s.wake()
}

// setState records a state and its cause, and announces it. See
// [ReconnectingSession.setState].
func (s *SharedDisplay) setState(state State, err error) {
	s.mu.Lock()
	if s.state.Terminal() {
		s.mu.Unlock()
		return
	}
	s.state, s.lastErr = state, err
	s.mu.Unlock()
	s.announce(state, err)
}

// announce runs the state callback and then releases every waiter.
func (s *SharedDisplay) announce(state State, err error) {
	s.notify(state, err)
	s.wake()
}

// wake releases every waiter by closing and replacing the broadcast channel.
func (s *SharedDisplay) wake() {
	s.mu.Lock()
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

// notify runs the state callback outside the lock.
func (s *SharedDisplay) notify(state State, err error) {
	if s.onState != nil {
		s.onState(state, err)
	}
}

// SharedLink wraps an established shared-display WebSocket stream as a
// [Link], running the RFB handshake over it.
func SharedLink(ws *websocket.Conn, cfg rfb.Config) (Link, error) {
	wsConn := wsio.New(ws)
	conn, err := rfb.NewConn(wsConn, cfg)
	if err != nil {
		_ = wsConn.Close()
		return nil, fmt.Errorf("rfb handshake on the shared display: %w", err)
	}
	return &Session{transport: wsConn, conn: conn}, nil
}
