// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/reconnect"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

// --- fakes ------------------------------------------------------------------

// fakeLink is a Link that never touches a socket. Run blocks until the test
// releases it, which is what lets a test hold a "connection" open and then end
// it with a chosen error.
type fakeLink struct {
	mu       sync.Mutex
	requests []bool // incremental flag of every RequestUpdate, in order
	closes   int

	requestErr error
	runErr     error
	release    chan struct{}
	releaseOne sync.Once
}

func newFakeLink() *fakeLink {
	return &fakeLink{release: make(chan struct{})}
}

func (l *fakeLink) Conn() *rfb.Conn { return nil }

func (l *fakeLink) Run(ctx context.Context) error {
	select {
	case <-l.release:
		return l.runErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *fakeLink) RequestUpdate(incremental bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.requestErr != nil {
		return l.requestErr
	}
	l.requests = append(l.requests, incremental)
	return nil
}

func (l *fakeLink) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closes++
	return nil
}

// drop ends the connection with err.
func (l *fakeLink) drop(err error) {
	l.releaseOne.Do(func() {
		l.mu.Lock()
		l.runErr = err
		l.mu.Unlock()
		close(l.release)
	})
}

func (l *fakeLink) snapshot() (requests []bool, closes int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]bool(nil), l.requests...), l.closes
}

// step is one scripted outcome for the fake dialer.
type step struct {
	link *fakeLink
	err  error
}

// scriptedDialer replays steps in order, repeating the last one forever, and
// records how many times it was called and with which configs.
type scriptedDialer struct {
	mu      sync.Mutex
	steps   []step
	calls   int
	configs []rfb.Config
	called  chan struct{}
}

func newDialer(steps ...step) *scriptedDialer {
	return &scriptedDialer{steps: steps, called: make(chan struct{}, 64)}
}

func (d *scriptedDialer) dial(_ context.Context, cfg rfb.Config) (Link, error) {
	d.mu.Lock()
	s := d.steps[min(d.calls, len(d.steps)-1)]
	d.calls++
	d.configs = append(d.configs, cfg)
	d.mu.Unlock()

	select {
	case d.called <- struct{}{}:
	default:
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.link, nil
}

func (d *scriptedDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// recorder collects the state callback for later assertion.
type recorder struct {
	mu     sync.Mutex
	states []State
	errs   []error
}

func (r *recorder) record(s State, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, s)
	r.errs = append(r.errs, err)
}

func (r *recorder) seen() []State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]State(nil), r.states...)
}

// errorFor returns the first recorded error reported alongside the given
// state. Tests assert against this rather than polling State(), because a
// reconnect that succeeds immediately makes the failed state transient and
// therefore impossible to observe reliably.
func (r *recorder) errorFor(want State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, s := range r.states {
		if s == want && r.errs[i] != nil {
			return r.errs[i]
		}
	}
	return nil
}

func (r *recorder) has(want State) bool {
	for _, s := range r.seen() {
		if s == want {
			return true
		}
	}
	return false
}

// --- helpers ----------------------------------------------------------------

// fastPolicy retries immediately and deterministically, so the tests measure
// behaviour rather than the clock.
func fastPolicy() reconnect.Policy {
	return reconnect.Policy{Initial: time.Millisecond, Max: 2 * time.Millisecond, Multiplier: 1}
}

// testOptions returns options with the update ticker disabled, so that
// RequestUpdate assertions see only what the supervisor itself sent.
func testOptions(d *scriptedDialer, rec *recorder) Options {
	return Options{
		Policy:         fastPolicy(),
		InUsePoll:      time.Millisecond,
		UpdateInterval: -1,
		Dial:           d.dial,
		OnState:        rec.record,
	}
}

func start(t *testing.T, ctx context.Context, opts Options) *ReconnectingSession {
	t.Helper()
	r, err := DialReconnecting(ctx, nil, "ns", "vm", rfb.Config{}, opts)
	if err != nil {
		t.Fatalf("DialReconnecting: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// waitFor blocks until cond holds, failing the test if it never does. Polling
// keeps the tests free of internal synchronisation hooks.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Microsecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitState(t *testing.T, r *ReconnectingSession, want State) {
	t.Helper()
	waitFor(t, fmt.Sprintf("state %v", want), func() bool {
		got, _ := r.State()
		return got == want
	})
}

// --- tests ------------------------------------------------------------------

func TestDialReconnectingRequiresAClientOrADialer(t *testing.T) {
	if _, err := DialReconnecting(context.Background(), nil, "ns", "vm", rfb.Config{}, Options{}); err == nil {
		t.Fatal("expected an error with neither a client nor a dial function")
	}
}

func TestReconnectingSessionRetriesTransientErrors(t *testing.T) {
	link := newFakeLink()
	d := newDialer(
		step{err: io.EOF},
		step{err: fmt.Errorf("dial vnc bridge: %w", kwclient.ErrUnavailable)},
		step{err: fmt.Errorf("dial vnc bridge: %w", kwclient.ErrNotFound)},
		step{link: link},
	)
	rec := &recorder{}
	r := start(t, context.Background(), testOptions(d, rec))

	conn, live, err := r.Attach(context.Background())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if conn != nil {
		t.Errorf("Conn() = %v, want nil for a fake link", conn)
	}
	if live.Err() != nil {
		t.Error("attachment context is already done")
	}
	if got := d.count(); got != 4 {
		t.Errorf("dial called %d times, want 4", got)
	}

	// Connecting once, then a reconnect wait per failed attempt, then
	// connected. The first state must be Connecting so a caller that wires up
	// OnState sees the whole story.
	wantStates(t, rec, StateConnecting, StateReconnecting, StateReconnecting, StateReconnecting, StateConnected)
}

func TestReconnectingSessionDoesNotRetryPermanentErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"unauthorized", fmt.Errorf("dial vnc bridge: %w", kwclient.ErrUnauthorized)},
		{"forbidden", fmt.Errorf("dial vnc bridge: %w", kwclient.ErrForbidden)},
		{"not a vm", fmt.Errorf("dial vnc bridge: %w", kwclient.ErrNotVM)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDialer(step{err: tt.err})
			rec := &recorder{}
			r := start(t, context.Background(), testOptions(d, rec))

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _, err := r.Attach(ctx)
			if !errors.Is(err, tt.err) {
				t.Fatalf("Attach error = %v, want %v", err, tt.err)
			}

			waitState(t, r, StateFailed)
			if got := d.count(); got != 1 {
				t.Errorf("dial called %d times, want exactly 1: a permanent failure must not be retried", got)
			}
			wantStates(t, rec, StateConnecting, StateFailed)
		})
	}
}

// A busy display must use the slow fixed poll, never the backoff curve: the
// backoff here is an hour, so a session that took the wrong path would hang.
func TestReconnectingSessionUsesTheSlowPathForABusyDisplay(t *testing.T) {
	link := newFakeLink()
	d := newDialer(
		step{err: fmt.Errorf("dial vnc bridge: %w", kwclient.ErrSessionInUse)},
		step{err: fmt.Errorf("dial vnc bridge: %w", kwclient.ErrSessionInUse)},
		step{link: link},
	)
	rec := &recorder{}
	opts := testOptions(d, rec)
	opts.Policy = reconnect.Policy{Initial: time.Hour, Max: time.Hour, Multiplier: 1}
	opts.InUsePoll = time.Millisecond

	r := start(t, context.Background(), opts)
	if _, _, err := r.Attach(context.Background()); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	wantStates(t, rec, StateConnecting, StateDisplayInUse, StateDisplayInUse, StateConnected)
	// The reason must reach the callback, not just the state: the UI has to be
	// able to say who is holding the display up.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !errors.Is(rec.errs[1], kwclient.ErrSessionInUse) {
		t.Errorf("state error = %v, want it to wrap %v", rec.errs[1], kwclient.ErrSessionInUse)
	}
}

// A rate limit is also a "slow down" answer, but it is not worth telling the
// user their display is busy.
func TestReconnectingSessionRateLimitUsesTheSlowPathButNotTheInUseState(t *testing.T) {
	link := newFakeLink()
	d := newDialer(
		step{err: fmt.Errorf("dial vnc bridge: %w", kwclient.ErrRateLimited)},
		step{link: link},
	)
	rec := &recorder{}
	opts := testOptions(d, rec)
	opts.Policy = reconnect.Policy{Initial: time.Hour, Max: time.Hour, Multiplier: 1}
	opts.InUsePoll = time.Millisecond

	r := start(t, context.Background(), opts)
	if _, _, err := r.Attach(context.Background()); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if rec.has(StateDisplayInUse) {
		t.Errorf("states = %v, want no display-in-use state for a rate limit", rec.seen())
	}
}

func TestReconnectingSessionInUseTimeout(t *testing.T) {
	d := newDialer(step{err: fmt.Errorf("dial vnc bridge: %w", kwclient.ErrSessionInUse)})
	rec := &recorder{}
	opts := testOptions(d, rec)
	opts.InUsePoll = time.Millisecond
	opts.InUseTimeout = 10 * time.Millisecond

	r := start(t, context.Background(), opts)
	waitState(t, r, StateFailed)

	_, err := r.State()
	if !errors.Is(err, kwclient.ErrSessionInUse) {
		t.Errorf("failure error = %v, want it to wrap %v", err, kwclient.ErrSessionInUse)
	}
}

func TestReconnectingSessionNegativeInUseTimeoutIsImmediatelyFatal(t *testing.T) {
	d := newDialer(step{err: fmt.Errorf("dial vnc bridge: %w", kwclient.ErrSessionInUse)})
	rec := &recorder{}
	opts := testOptions(d, rec)
	opts.InUseTimeout = -1

	r := start(t, context.Background(), opts)
	waitState(t, r, StateFailed)
	if got := d.count(); got != 1 {
		t.Errorf("dial called %d times, want 1", got)
	}
}

func TestReconnectingSessionMaxAttempts(t *testing.T) {
	d := newDialer(step{err: io.EOF})
	rec := &recorder{}
	opts := testOptions(d, rec)
	opts.Policy = reconnect.Policy{Initial: time.Millisecond, Max: time.Millisecond, Multiplier: 1, MaxAttempts: 3}

	r := start(t, context.Background(), opts)
	waitState(t, r, StateFailed)

	if got := d.count(); got != 3 {
		t.Errorf("dial called %d times, want 3", got)
	}
	_, err := r.State()
	if !errors.Is(err, io.EOF) {
		t.Errorf("failure error = %v, want it to wrap io.EOF", err)
	}
}

// The framebuffer is gone after a reconnect, so every connection must open
// with a non-incremental request. This is the assertion that a "reconnect"
// which leaves the user staring at a frozen pre-disconnect image would fail.
func TestReconnectingSessionRequestsFullUpdateOnEveryConnection(t *testing.T) {
	first, second := newFakeLink(), newFakeLink()
	d := newDialer(step{link: first}, step{link: second})
	rec := &recorder{}
	r := start(t, context.Background(), testOptions(d, rec))

	_, live, err := r.Attach(context.Background())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Drop the first connection the way a server restart would.
	first.drop(io.EOF)
	<-live.Done()

	waitFor(t, "the second connection", func() bool { return d.count() == 2 })
	waitState(t, r, StateConnected)

	for name, link := range map[string]*fakeLink{"first": first, "second": second} {
		requests, _ := link.snapshot()
		if len(requests) != 1 || requests[0] {
			t.Errorf("%s connection sent %v, want exactly one non-incremental request", name, requests)
		}
	}
	// The dead connection's transport must be released, or the server's
	// single display slot stays taken.
	if _, closes := first.snapshot(); closes != 1 {
		t.Errorf("first link closed %d times, want 1", closes)
	}

	wantStates(t, rec, StateConnecting, StateConnected, StateReconnecting, StateConnected)
}

// A clean close with a reason ("taken over by another user") must reach the
// state callback: it is the only explanation the user gets.
func TestReconnectingSessionSurfacesTheCloseReason(t *testing.T) {
	first, second := newFakeLink(), newFakeLink()
	d := newDialer(step{link: first}, step{link: second})
	rec := &recorder{}
	r := start(t, context.Background(), testOptions(d, rec))

	_, live, err := r.Attach(context.Background())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	first.drop(&websocket.CloseError{Code: websocket.CloseGoingAway, Text: "taken over by another user"})
	<-live.Done()

	waitFor(t, "the close reason to be reported", func() bool {
		return rec.errorFor(StateReconnecting) != nil
	})
	if got := rec.errorFor(StateReconnecting); !strings.Contains(got.Error(), "taken over by another user") {
		t.Errorf("state error = %v, want it to carry the close reason", got)
	}
}

func TestReconnectingSessionRestartsTheUpdateTicker(t *testing.T) {
	first, second := newFakeLink(), newFakeLink()
	d := newDialer(step{link: first}, step{link: second})
	rec := &recorder{}
	opts := testOptions(d, rec)
	opts.UpdateInterval = time.Millisecond

	r := start(t, context.Background(), opts)
	_, live, err := r.Attach(context.Background())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	waitFor(t, "incremental updates on the first connection", func() bool {
		requests, _ := first.snapshot()
		return len(requests) > 2
	})

	first.drop(io.EOF)
	<-live.Done()

	// The ticker belongs to the connection: a new one must start, and the old
	// one must have stopped rather than keep writing to a dead link.
	waitFor(t, "incremental updates on the second connection", func() bool {
		requests, _ := second.snapshot()
		return len(requests) > 2
	})
	settled, _ := first.snapshot()
	time.Sleep(20 * time.Millisecond)
	if now, _ := first.snapshot(); len(now) != len(settled) {
		t.Errorf("first connection still receiving update requests: %d then %d", len(settled), len(now))
	}

	// Whatever the ticker did, the first request on each connection is the
	// full repaint.
	for name, link := range map[string]*fakeLink{"first": first, "second": second} {
		requests, _ := link.snapshot()
		if len(requests) == 0 || requests[0] {
			t.Errorf("%s connection's first request = %v, want non-incremental", name, requests)
		}
	}
}

func TestReconnectingSessionStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	link := newFakeLink()
	d := newDialer(step{link: link})
	rec := &recorder{}
	r := start(t, ctx, testOptions(d, rec))

	_, live, err := r.Attach(context.Background())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	cancel()
	select {
	case <-live.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the attachment context was not cancelled")
	}
	waitState(t, r, StateClosed)

	if _, closes := link.snapshot(); closes != 1 {
		t.Errorf("link closed %d times, want 1", closes)
	}
	if got := d.count(); got != 1 {
		t.Errorf("dial called %d times, want 1: a cancelled session must not reconnect", got)
	}
}

func TestReconnectingSessionCloseIsPromptAndIdempotent(t *testing.T) {
	link := newFakeLink()
	d := newDialer(step{link: link})
	rec := &recorder{}
	r := start(t, context.Background(), testOptions(d, rec))

	if _, _, err := r.Attach(context.Background()); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Close()
		_ = r.Close() // idempotent, and must not block on the second call
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return")
	}

	if state, _ := r.State(); state != StateClosed {
		t.Errorf("state after Close = %v, want %v", state, StateClosed)
	}
	if _, closes := link.snapshot(); closes != 1 {
		t.Errorf("link closed %d times, want 1", closes)
	}
	// Close must not race a reconnect into existence.
	if got := d.count(); got != 1 {
		t.Errorf("dial called %d times, want 1", got)
	}
	if r.Conn() != nil {
		t.Error("Conn() returned a connection after Close")
	}
}

func TestAttachRespectsItsOwnContext(t *testing.T) {
	// The session never connects; Attach must still return when its caller
	// gives up.
	d := newDialer(step{err: io.EOF})
	rec := &recorder{}
	opts := testOptions(d, rec)
	opts.Policy = reconnect.Policy{Initial: time.Hour, Max: time.Hour, Multiplier: 1}
	r := start(t, context.Background(), opts)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := r.Attach(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Attach error = %v, want %v", err, context.DeadlineExceeded)
	}
}

// The RFB handshake reads its configuration once, so a consumer that installs
// per-connection callbacks needs a fresh one for every attempt.
func TestReconnectingSessionAsksForAFreshConfig(t *testing.T) {
	first, second := newFakeLink(), newFakeLink()
	d := newDialer(step{link: first}, step{link: second})
	rec := &recorder{}
	opts := testOptions(d, rec)

	var configs int
	var mu sync.Mutex
	opts.Config = func() rfb.Config {
		mu.Lock()
		defer mu.Unlock()
		configs++
		return rfb.Config{Password: fmt.Sprintf("gen-%d", configs)}
	}

	r := start(t, context.Background(), opts)
	_, live, err := r.Attach(context.Background())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	first.drop(io.EOF)
	<-live.Done()
	waitFor(t, "the second connection", func() bool { return d.count() == 2 })

	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.configs) < 2 || d.configs[0].Password != "gen-1" || d.configs[1].Password != "gen-2" {
		t.Errorf("configs = %v, want a freshly built one per attempt", d.configs)
	}
}

func TestReconnectingSessionAccessors(t *testing.T) {
	d := newDialer(step{link: newFakeLink()})
	rec := &recorder{}
	r := start(t, context.Background(), testOptions(d, rec))
	if got, want := r.Namespace(), "ns"; got != want {
		t.Errorf("Namespace() = %q, want %q", got, want)
	}
	if got, want := r.Workspace(), "vm"; got != want {
		t.Errorf("Workspace() = %q, want %q", got, want)
	}
}

func TestStateString(t *testing.T) {
	tests := []struct {
		state State
		want  string
	}{
		{StateConnecting, "connecting"},
		{StateConnected, "connected"},
		{StateReconnecting, "reconnecting"},
		{StateDisplayInUse, "display in use"},
		{StateFailed, "failed"},
		{StateClosed, "closed"},
		{State(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("State(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
		if got, want := tt.state.Terminal(), tt.state == StateFailed || tt.state == StateClosed; got != want {
			t.Errorf("State(%d).Terminal() = %v, want %v", tt.state, got, want)
		}
	}
}

// --- small helpers ----------------------------------------------------------

// wantStates asserts the exact sequence of states reported to OnState,
// waiting for it rather than sampling: the supervisor runs on its own
// goroutine and the last transition may still be in flight.
func wantStates(t *testing.T, rec *recorder, want ...State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var got []State
	for time.Now().Before(deadline) {
		if got = rec.seen(); equalStates(got, want) {
			return
		}
		time.Sleep(200 * time.Microsecond)
	}
	t.Errorf("states = %v, want %v", got, want)
}

func equalStates(got, want []State) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// A connection that cannot even ask for its first frame is no use to anybody:
// it must be closed and retried rather than handed to a consumer.
func TestReconnectingSessionRetriesWhenTheFullUpdateFails(t *testing.T) {
	broken, good := newFakeLink(), newFakeLink()
	broken.requestErr = errors.New("write: broken pipe")
	d := newDialer(step{link: broken}, step{link: good})
	rec := &recorder{}
	r := start(t, context.Background(), testOptions(d, rec))

	if _, _, err := r.Attach(context.Background()); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, closes := broken.snapshot(); closes != 1 {
		t.Errorf("broken link closed %d times, want 1", closes)
	}
	wantStates(t, rec, StateConnecting, StateReconnecting, StateConnected)
}

// A server that closes the stream cleanly reports no error at all, and the
// message the user ends up seeing must still say something.
func TestReconnectingSessionNamesACleanDisconnect(t *testing.T) {
	link := newFakeLink()
	d := newDialer(step{link: link})
	rec := &recorder{}
	opts := testOptions(d, rec)
	opts.Policy = reconnect.Policy{Initial: time.Millisecond, Max: time.Millisecond, Multiplier: 1, MaxAttempts: 1}

	r := start(t, context.Background(), opts)
	if _, _, err := r.Attach(context.Background()); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	link.drop(nil)

	waitState(t, r, StateFailed)
	_, err := r.State()
	if err == nil || !strings.Contains(err.Error(), "closed by the server") {
		t.Errorf("failure error = %v, want it to name the clean close", err)
	}
}
