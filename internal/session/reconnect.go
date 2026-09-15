// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/reconnect"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

// State is what a [ReconnectingSession] is currently doing.
type State int

const (
	// StateConnecting is the first connection attempt.
	StateConnecting State = iota
	// StateConnected means a handshaken RFB connection is live.
	StateConnected
	// StateReconnecting means the session dropped or an attempt failed, and
	// the supervisor is waiting out a backoff delay before trying again.
	StateReconnecting
	// StateDisplayInUse means the workspace's single VNC display is held by
	// another client. The session is not broken and is not backing off: it is
	// polling slowly and will attach as soon as the slot is released. See
	// [Classify] for why this is a state of its own.
	StateDisplayInUse
	// StateFailed is terminal: the last error is not worth retrying, or the
	// retry budget ran out. The session performs no further work.
	StateFailed
	// StateClosed is terminal: the session was closed, or its context was
	// cancelled.
	StateClosed
)

// String implements fmt.Stringer.
func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateReconnecting:
		return "reconnecting"
	case StateDisplayInUse:
		return "display in use"
	case StateFailed:
		return "failed"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// Terminal reports whether no further state change can occur.
func (s State) Terminal() bool { return s == StateFailed || s == StateClosed }

// Link is one established RFB connection: a completed handshake plus the
// transport underneath it.
//
// It exists so that [ReconnectingSession] can be driven by a fake in tests —
// the supervisor logic is where the interesting decisions live, and it should
// not need a VNC server to exercise them. [*Session] is the production
// implementation.
type Link interface {
	// Conn returns the handshaken RFB connection.
	Conn() *rfb.Conn
	// Run drives the read loop until ctx is cancelled or the stream ends. It
	// returns nil on a clean close.
	Run(ctx context.Context) error
	// RequestUpdate asks for a framebuffer update covering the whole screen.
	RequestUpdate(incremental bool) error
	// Close releases the transport, and with it the server's session slot.
	Close() error
}

var _ Link = (*Session)(nil)

// DialFunc establishes one [Link]. cfg is the RFB configuration for this
// generation; a caller that rebuilds its callbacks per connection supplies it
// through [Options.Config].
type DialFunc func(ctx context.Context, cfg rfb.Config) (Link, error)

// Defaults for [Options].
const (
	// DefaultUpdateInterval paces framebuffer update requests. It matches
	// [Session.RequestUpdates].
	DefaultUpdateInterval = 16 * time.Millisecond

	// DefaultInUsePoll is how often a session waiting for a busy display tries
	// again. Seven seconds sits in the middle of the five-to-ten second band
	// that is slow enough not to fight the client that holds the slot, and
	// fast enough that handing the display over feels immediate.
	DefaultInUsePoll = 7 * time.Second

	// stableConnection is how long a connection must last before it counts as
	// a success for the purposes of the retry budget, so that a server which
	// accepts and instantly drops connections cannot flatten the backoff
	// curve.
	stableConnection = 10 * time.Second
)

// Options configures a [ReconnectingSession]. The zero value is usable.
type Options struct {
	// Policy paces retries of transient failures. The zero value is
	// [reconnect.Default] with jitter off; use reconnect.Default() explicitly
	// to get jitter, which is what production code should do.
	Policy reconnect.Policy

	// InUsePoll is how often to retry while the display is held by another
	// client. Zero means [DefaultInUsePoll].
	InUsePoll time.Duration

	// InUseTimeout bounds how long the session will wait for a busy display in
	// one stretch, measured from the first refusal and reset by any successful
	// connection. Zero means wait indefinitely, which is right for an
	// interactive client: only the user knows when to give up. A negative
	// value makes a busy display fatal immediately, for callers that would
	// rather fail than queue.
	InUseTimeout time.Duration

	// UpdateInterval paces framebuffer update requests on each connection.
	// Zero means [DefaultUpdateInterval]; a negative value disables the
	// automatic requests entirely, leaving them to the caller.
	UpdateInterval time.Duration

	// OnState is called on every state change, with the error that caused it
	// where there was one. It runs on the supervisor goroutine and must not
	// block; it must not call [ReconnectingSession.Close] either, since that
	// waits for the supervisor to stop.
	OnState func(State, error)

	// Config returns the RFB configuration for the next connection. It is
	// called immediately before each dial, so a consumer that must install
	// fresh per-connection callbacks (the handshake reads rfb.Config once and
	// keeps it) can do so here. Nil means reuse the configuration passed to
	// [DialReconnecting].
	Config func() rfb.Config

	// Dial overrides how a connection is established. Nil means dial the
	// workspace's VNC bridge through the client. It exists for tests.
	Dial DialFunc
}

// ReconnectingSession keeps a VNC session to one workspace alive across
// transient failures.
//
// It owns a connect → run → classify → wait → reconnect loop on its own
// goroutine. Consumers do not hold a connection: they take one out with
// [ReconnectingSession.Attach], which also hands back the lifetime of that
// particular connection, because the *rfb.Conn changes on every reconnect and
// a stale one silently stops working.
type ReconnectingSession struct {
	namespace string
	workspace string

	dial      DialFunc
	config    func() rfb.Config
	policy    reconnect.Policy
	inUsePoll time.Duration
	inUseMax  time.Duration
	interval  time.Duration
	onState   func(State, error)

	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	state   State
	lastErr error
	gen     *generation
	// changed is closed and replaced on every state or generation change, so
	// any number of waiters can be woken without a per-waiter registry.
	changed chan struct{}
	// retry signals the supervisor to re-dial immediately when waiting for a
	// busy display. It is buffered to avoid losing calls from goroutines that
	// happen to call it at just the wrong time.
	retry chan struct{}
}

// generation is one live connection and the context that bounds it.
type generation struct {
	link Link
	ctx  context.Context
}

// DialReconnecting starts a supervised VNC session against a workspace.
//
// It returns as soon as the supervisor is running: connecting is asynchronous
// because a reconnecting session cannot usefully block here. The first attempt
// may legitimately take minutes if the display is busy, and the caller needs
// its state callback wired up before that starts. Use
// [ReconnectingSession.Attach] to wait for a usable connection, and to learn
// about a failure that is not worth retrying.
//
// cfg is used for every connection unless [Options.Config] is set. The caller
// owns the returned session and must Close it, which is what releases the
// server's single display slot.
func DialReconnecting(ctx context.Context, client *kwclient.Client, namespace, name string, cfg rfb.Config, opts Options) (*ReconnectingSession, error) {
	dial := opts.Dial
	if dial == nil {
		if client == nil {
			return nil, fmt.Errorf("session: no API client and no dial function")
		}
		dial = func(ctx context.Context, cfg rfb.Config) (Link, error) {
			return Dial(ctx, client, namespace, name, cfg)
		}
	}
	config := opts.Config
	if config == nil {
		config = func() rfb.Config { return cfg }
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
	r := &ReconnectingSession{
		namespace: namespace,
		workspace: name,
		dial:      dial,
		config:    config,
		policy:    opts.Policy,
		inUsePoll: inUsePoll,
		inUseMax:  opts.InUseTimeout,
		interval:  interval,
		onState:   opts.OnState,
		cancel:    cancel,
		done:      make(chan struct{}),
		state:     StateConnecting,
		changed:   make(chan struct{}),
		retry:     make(chan struct{}, 1),
	}
	go r.run(runCtx)
	return r, nil
}

// Workspace returns the workspace name this session is attached to.
func (r *ReconnectingSession) Workspace() string { return r.workspace }

// Namespace returns the workspace's namespace.
func (r *ReconnectingSession) Namespace() string { return r.namespace }

// Conn returns the live RFB connection, or nil while there is none.
//
// The returned connection is only valid until the session reconnects. Code
// that runs for the length of a connection should use
// [ReconnectingSession.Attach] instead, which says when its connection ends.
func (r *ReconnectingSession) Conn() *rfb.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gen == nil {
		return nil
	}
	return r.gen.link.Conn()
}

// State returns the current state and the error that caused it, if any.
func (r *ReconnectingSession) State() (State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, r.lastErr
}

// Attach blocks until a connection is live and returns it together with a
// context that is cancelled when that particular connection ends — whether
// because it dropped, because the session was closed, or because the session
// failed.
//
// A consumer loop therefore reads: attach, drive the connection until the
// returned context is done, attach again. Attach reports an error when the
// session has failed for good or when ctx is cancelled first, so the loop
// terminates on its own.
func (r *ReconnectingSession) Attach(ctx context.Context) (*rfb.Conn, context.Context, error) {
	for {
		r.mu.Lock()
		gen, state, lastErr, changed := r.gen, r.state, r.lastErr, r.changed
		r.mu.Unlock()

		if gen != nil {
			return gen.link.Conn(), gen.ctx, nil
		}
		if state.Terminal() {
			if lastErr != nil {
				return nil, nil, lastErr
			}
			return nil, nil, fmt.Errorf("session: %s/%s is %s", r.namespace, r.workspace, state)
		}

		select {
		case <-changed:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

// Close stops the supervisor and releases the server's display slot.
//
// It blocks until the loop has exited and the transport is shut down, because
// the KubeVirt console is single-seat: returning before the slot is released
// would make an immediate reconnect fail with 409 against our own ghost.
func (r *ReconnectingSession) Close() error {
	r.closeOnce.Do(func() {
		r.cancel()
		<-r.done
	})
	return nil
}

// RetryNow asks a session waiting on a busy display (or backing off) to
// re-dial immediately. It is safe to call from any goroutine and never blocks.
// It is a nudge, not a command: calling it while a connection is live or the
// session is closing does nothing.
func (r *ReconnectingSession) RetryNow() {
	select {
	case r.retry <- struct{}{}:
	default:
	}
}

// run is the supervisor loop.
func (r *ReconnectingSession) run(ctx context.Context) {
	defer close(r.done)

	// The initial state is announced from here rather than from the
	// constructor so that a caller which wires up OnState cannot miss it.
	r.notify(StateConnecting, nil)

	var (
		attempt    int
		inUseSince time.Time
	)
	for {
		if ctx.Err() != nil {
			r.setState(StateClosed, nil)
			return
		}

		link, err := r.dial(ctx, r.config())
		if err == nil {
			inUseSince = time.Time{}
			started := time.Now()
			// serve returns the reason this connection ended. A nil reason is
			// still a disconnect, so it goes through the same classification.
			err = r.serve(ctx, link)
			if ctx.Err() != nil {
				r.setState(StateClosed, nil)
				return
			}
			// The attempt counter is only reset by a connection that actually
			// held. A server that accepts a connection and drops it
			// immediately would otherwise pin the backoff at its initial
			// value forever, and reconnecting once a second to something that
			// is visibly broken is exactly the behaviour backoff exists to
			// prevent.
			if time.Since(started) >= stableConnection {
				attempt = 0
			}
			err = annotate(err)
		}

		switch Classify(err) {
		case RetryNever:
			r.setState(StateFailed, err)
			return

		case RetrySlow:
			if r.inUseMax < 0 {
				r.setState(StateFailed, err)
				return
			}
			if inUseSince.IsZero() {
				inUseSince = time.Now()
			} else if r.inUseMax > 0 && time.Since(inUseSince) >= r.inUseMax {
				r.setState(StateFailed, fmt.Errorf("gave up after waiting %v: %w", r.inUseMax, err))
				return
			}
			r.setState(stateFor(err), err)
			if !sleepOrRetry(ctx, r, r.inUsePoll) {
				r.setState(StateClosed, nil)
				return
			}

		default: // RetryBackoff
			attempt++
			if r.policy.Exhausted(attempt) {
				r.setState(StateFailed, fmt.Errorf("giving up after %d attempt(s): %w", attempt, orUnknown(err)))
				return
			}
			r.setState(StateReconnecting, err)
			if !sleep(ctx, r.policy.Backoff(attempt)) {
				r.setState(StateClosed, nil)
				return
			}
		}
	}
}

// serve runs one connection to its end and returns the reason it ended.
func (r *ReconnectingSession) serve(ctx context.Context, link Link) error {
	// The generation context bounds everything that belongs to this
	// connection: the update ticker below, and whatever the consumer that
	// attached to it is doing.
	genCtx, genCancel := context.WithCancel(ctx)
	defer genCancel()

	// A brand new connection shares no history with the old one. The server
	// tracks what it has already sent per connection, so an incremental
	// request on a fresh link is answered relative to a framebuffer the server
	// believes it has sent and we do not have: the result is a window that
	// only repaints the parts of the screen that happen to change afterwards,
	// with the rest frozen on the pre-disconnect image. One non-incremental
	// request costs a full frame and makes the reconnect invisible.
	if err := link.RequestUpdate(false); err != nil {
		_ = link.Close()
		return fmt.Errorf("request full update after connect: %w", err)
	}

	r.connected(&generation{link: link, ctx: genCtx})

	if r.interval > 0 {
		go requestUpdates(genCtx, link, r.interval)
	}

	err := link.Run(ctx)

	// Order matters on the way out: stop the ticker and drop the generation
	// before closing the transport, so nothing hands a consumer a connection
	// that is already gone.
	genCancel()
	r.disconnected()
	_ = link.Close()
	return err
}

// requestUpdates keeps asking for incremental framebuffer updates.
//
// RFB is pull-based: the server sends nothing until asked, and answers each
// request with at most one update, so a client that stops asking freezes. This
// is [Session.RequestUpdates] bound to one connection's lifetime, and it is
// restarted from scratch on every reconnect.
func requestUpdates(ctx context.Context, link Link, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := link.RequestUpdate(true); err != nil {
				// The read loop sees the same failure and ends the
				// generation; there is nothing useful to report from here.
				return
			}
		}
	}
}

// connected publishes a generation and announces the new state.
func (r *ReconnectingSession) connected(gen *generation) {
	r.mu.Lock()
	r.gen = gen
	r.state = StateConnected
	r.lastErr = nil
	r.mu.Unlock()
	r.announce(StateConnected, nil)
}

// disconnected retracts the current generation without changing the state; the
// supervisor sets the next state once it has classified the failure.
func (r *ReconnectingSession) disconnected() {
	r.mu.Lock()
	r.gen = nil
	r.mu.Unlock()
	r.wake()
}

// setState records a state and its cause, and announces it.
func (r *ReconnectingSession) setState(state State, err error) {
	r.mu.Lock()
	if r.state.Terminal() {
		// A terminal state is final: a later close must not overwrite the
		// failure the user has to see.
		r.mu.Unlock()
		return
	}
	r.state, r.lastErr = state, err
	r.mu.Unlock()
	r.announce(state, err)
}

// announce runs the state callback and then releases everyone waiting on the
// change.
//
// The order is deliberate. Running the callback first means a consumer that
// wakes from Attach can never be handed a connection its own OnState has not
// been told about yet, which is the difference between a UI that shows
// "connected" and one that shows whatever it last saw. It is also why OnState
// must not block.
func (r *ReconnectingSession) announce(state State, err error) {
	r.notify(state, err)
	r.wake()
}

// wake releases every waiter by closing and replacing the broadcast channel.
func (r *ReconnectingSession) wake() {
	r.mu.Lock()
	close(r.changed)
	r.changed = make(chan struct{})
	r.mu.Unlock()
}

// notify runs the state callback outside the lock, so a callback that asks the
// session what it is doing cannot deadlock.
func (r *ReconnectingSession) notify(state State, err error) {
	if r.onState != nil {
		r.onState(state, err)
	}
}

// stateFor maps a slow-retry error to the state that explains it. A busy
// display is worth saying out loud; a rate limit is just a slow reconnect.
func stateFor(err error) State {
	if errors.Is(err, kwclient.ErrSessionInUse) {
		return StateDisplayInUse
	}
	return StateReconnecting
}

// annotate turns the reason a connection ended into something a user can read.
//
// A clean stream end carries no error at all, and a WebSocket close frame's
// reason text — which the API uses to say things like "taken over by another
// user" — is the most informative thing available when there is one.
func annotate(err error) error {
	if _, text, ok := CloseReason(err); ok && text != "" {
		return fmt.Errorf("server closed the session: %s: %w", text, err)
	}
	return err
}

// orUnknown gives a nil disconnect reason a name, so the error reported after
// an exhausted retry budget is not "giving up after 3 attempt(s): %!w(<nil>)".
func orUnknown(err error) error {
	if err == nil {
		return errors.New("connection closed by the server")
	}
	return err
}

// sleep waits for d, reporting false if ctx was cancelled first.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// sleepOrRetry waits for either d, ctx cancellation, or a signal on retry. It
// returns true when the timeout expires (and no cancel happened), and false when
// the context is cancelled. On retry it returns true immediately without waiting.
func sleepOrRetry(ctx context.Context, r *ReconnectingSession, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	case <-r.retry:
		// Retry was signalled; the caller will reconnect immediately. Return
		// true to signal that we are still "alive" and should continue the loop,
		// but without having waited out the poll interval.
		return true
	}
}
