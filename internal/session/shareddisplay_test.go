// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

// --- fakes ------------------------------------------------------------------

// closableLink behaves like the production transport: closing it ends the
// read loop, which is what SetRole relies on to bounce a live stream.
type closableLink struct {
	*fakeLink
}

func newClosableLink() *closableLink { return &closableLink{fakeLink: newFakeLink()} }

func (l *closableLink) Close() error {
	_ = l.fakeLink.Close()
	l.drop(io.ErrClosedPipe)
	return nil
}

// sharedStep is one scripted outcome for the shared-display dialer.
type sharedStep struct {
	link Link
	err  error
}

// sharedDial records the role/force of every attempt and replays scripted
// outcomes like scriptedDialer.
type sharedDial struct {
	mu     sync.Mutex
	steps  []sharedStep
	calls  int
	roles  []string
	forces []bool
	// gate, when non-nil, blocks the next dial until releaseGate closes it:
	// the way a test changes the role mid-dial.
	gate chan struct{}
	// called signals every attempt, so a test can wait until a dial is
	// actually in flight before racing it.
	called chan struct{}
}

func newSharedDial(steps ...sharedStep) *sharedDial {
	return &sharedDial{steps: steps, called: make(chan struct{}, 64)}
}

func (d *sharedDial) dial(_ context.Context, _ rfb.Config, role string, force bool) (Link, error) {
	d.mu.Lock()
	gate := d.gate
	d.mu.Unlock()
	if gate != nil {
		// Signal before parking, so the test knows this dial is in flight.
		select {
		case d.called <- struct{}{}:
		default:
		}
		<-gate
	}

	d.mu.Lock()
	s := d.steps[min(d.calls, len(d.steps)-1)]
	d.calls++
	d.roles = append(d.roles, role)
	d.forces = append(d.forces, force)
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

// holdNext makes the next dial block until releaseGate.
func (d *sharedDial) holdNext() {
	d.mu.Lock()
	d.gate = make(chan struct{})
	d.mu.Unlock()
}

// releaseGate unblocks a dial parked by holdNext.
func (d *sharedDial) releaseGate() {
	d.mu.Lock()
	gate := d.gate
	d.gate = nil
	d.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

func (d *sharedDial) attempts() (roles []string, forces []bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.roles...), append([]bool(nil), d.forces...)
}

func (d *sharedDial) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func sharedTestOptions(d *sharedDial, rec *recorder) SharedDisplayOptions {
	return SharedDisplayOptions{
		Policy:         fastPolicy(),
		InUsePoll:      time.Millisecond,
		UpdateInterval: -1,
		Dial:           d.dial,
		OnState:        rec.record,
	}
}

func startShared(t *testing.T, ctx context.Context, opts SharedDisplayOptions) *SharedDisplay {
	t.Helper()
	s, err := DialSharedDisplay(ctx, opts)
	if err != nil {
		t.Fatalf("DialSharedDisplay: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func waitSharedState(t *testing.T, s *SharedDisplay, want State) {
	t.Helper()
	waitFor(t, fmt.Sprintf("state %v", want), func() bool {
		got, _ := s.State()
		return got == want
	})
}

// --- tests ------------------------------------------------------------------

func TestDialSharedDisplayRequiresADialer(t *testing.T) {
	if _, err := DialSharedDisplay(context.Background(), SharedDisplayOptions{}); err == nil {
		t.Fatal("expected an error without a dial function")
	}
}

func TestSharedDisplayConnectsAsObserver(t *testing.T) {
	link := newClosableLink()
	d := newSharedDial(sharedStep{link: link})
	rec := &recorder{}
	s := startShared(t, context.Background(), sharedTestOptions(d, rec))

	if _, live, err := s.Attach(context.Background()); err != nil {
		t.Fatalf("Attach: %v", err)
	} else if live.Err() != nil {
		t.Error("attachment context is already done")
	}

	roles, forces := d.attempts()
	if len(roles) != 1 || roles[0] != kwclient.DisplayRoleObserver || forces[0] {
		t.Fatalf("first dial = %v/%v, want observer without force", roles, forces)
	}
	wantStates(t, rec, StateConnecting, StateConnected)
}

func TestSharedDisplaySetRoleRedialsImmediately(t *testing.T) {
	observer := newClosableLink()
	controller := newClosableLink()
	d := newSharedDial(sharedStep{link: observer}, sharedStep{link: controller})
	rec := &recorder{}
	s := startShared(t, context.Background(), sharedTestOptions(d, rec))

	if _, _, err := s.Attach(context.Background()); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	s.SetRole(kwclient.DisplayRoleController, false)

	// The old stream is closed out and the new role is dialled at once — a
	// role change is not a failure and pays no backoff.
	waitFor(t, "controller dial", func() bool { return d.count() == 2 })
	roles, forces := d.attempts()
	if roles[1] != kwclient.DisplayRoleController || forces[1] {
		t.Fatalf("second dial = %s force=%v, want controller without force", roles[1], forces[1])
	}
	if _, closes := observer.snapshot(); closes == 0 {
		t.Error("the observer stream was not closed on the role change")
	}

	// The new generation attaches underneath the same caller contract.
	if _, live, err := s.Attach(context.Background()); err != nil {
		t.Fatalf("Attach after role change: %v", err)
	} else if live.Err() != nil {
		t.Error("attachment context is already done")
	}

	// The bump is idempotent: restating the intent must not bounce the stream.
	s.SetRole(kwclient.DisplayRoleController, false)
	time.Sleep(10 * time.Millisecond)
	if got := d.count(); got != 2 {
		t.Fatalf("a same-role SetRole redialled (%d dials)", got)
	}
}

func TestSharedDisplayForceIsConsumedByTheSuccessfulDial(t *testing.T) {
	observer := newClosableLink()
	d := newSharedDial(sharedStep{link: observer}, sharedStep{link: newClosableLink()}, sharedStep{link: newClosableLink()})
	rec := &recorder{}
	s := startShared(t, context.Background(), sharedTestOptions(d, rec))

	if _, _, err := s.Attach(context.Background()); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// The takeover reconnect dials with force exactly once.
	s.SetRole(kwclient.DisplayRoleController, true)
	waitFor(t, "forced controller dial", func() bool { return d.count() == 2 })
	waitSharedState(t, s, StateConnected)

	// A later transport drop reconnects the same role without force:
	// re-sending it would revoke the control lease we now hold.
	_, forces := d.attempts()
	if !forces[1] {
		t.Fatal("the takeover dial did not carry force")
	}
	d.mu.Lock()
	controller := d.steps[1].link.(*closableLink)
	d.mu.Unlock()
	controller.drop(io.EOF)

	waitFor(t, "unforced reconnect", func() bool { return d.count() == 3 })
	_, forces = d.attempts()
	if forces[2] {
		t.Fatal("the reconnect after a drop still carried force")
	}
	roles, _ := d.attempts()
	if roles[2] != kwclient.DisplayRoleController {
		t.Fatalf("the reconnect dialled %s, want the held controller role", roles[2])
	}
}

func TestSharedDisplaySetRoleMidDialNeverPublishesTheStaleRole(t *testing.T) {
	stale := newClosableLink()
	fresh := newClosableLink()
	d := newSharedDial(sharedStep{link: stale}, sharedStep{link: fresh})
	d.holdNext()
	rec := &recorder{}
	s := startShared(t, context.Background(), sharedTestOptions(d, rec))

	// The first dial is parked in flight when the intent moves on.
	<-d.called
	s.SetRole(kwclient.DisplayRoleController, false)
	d.releaseGate()

	waitFor(t, "redial with the new role", func() bool { return d.count() == 2 })
	roles, _ := d.attempts()
	if roles[1] != kwclient.DisplayRoleController {
		t.Fatalf("second dial = %s, want controller", roles[1])
	}
	// The observer stream completed too late to be of use: it is closed
	// without ever becoming the live generation.
	if _, closes := stale.snapshot(); closes == 0 {
		t.Fatal("the stale observer stream was not closed")
	}
	if _, closes := fresh.snapshot(); closes != 0 {
		t.Fatal("the fresh controller stream was closed")
	}
	waitSharedState(t, s, StateConnected)
}

func TestSharedDisplayBusyControlLeasePollsSlowly(t *testing.T) {
	d := newSharedDial(
		sharedStep{err: fmt.Errorf("dial shared display: %w", kwclient.ErrControllerPresent)},
		sharedStep{link: newClosableLink()},
	)
	rec := &recorder{}
	s := startShared(t, context.Background(), sharedTestOptions(d, rec))
	s.SetRole(kwclient.DisplayRoleController, true)

	waitSharedState(t, s, StateConnected)
	if !rec.has(StateDisplayInUse) {
		t.Fatalf("a held control lease never surfaced as display-in-use: %v", rec.seen())
	}
}

func TestSharedDisplayUnknownParticipantIsFatal(t *testing.T) {
	d := newSharedDial(sharedStep{err: fmt.Errorf("dial shared display: %w", kwclient.ErrParticipantNotFound)})
	rec := &recorder{}
	s := startShared(t, context.Background(), sharedTestOptions(d, rec))

	waitSharedState(t, s, StateFailed)
	if _, _, err := s.Attach(context.Background()); err == nil {
		t.Fatal("Attach on a failed session did not return the failure")
	}
	if got := d.count(); got != 1 {
		t.Fatalf("a gone participant was redialled (%d dials)", got)
	}
}

func TestSharedDisplayCloseEndsTheSession(t *testing.T) {
	link := newClosableLink()
	d := newSharedDial(sharedStep{link: link})
	rec := &recorder{}
	s := startShared(t, context.Background(), sharedTestOptions(d, rec))

	if _, _, err := s.Attach(context.Background()); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	waitSharedState(t, s, StateClosed)
	if _, _, err := s.Attach(context.Background()); err == nil {
		t.Fatal("Attach on a closed session did not fail")
	}
	if got := d.count(); got != 1 {
		t.Fatalf("Close still redialled (%d dials)", got)
	}
}

// A drop with no role change behind it is an ordinary reconnect: the intent
// is unchanged, so the same role is dialled again after the backoff.
func TestSharedDisplayReconnectKeepsTheRole(t *testing.T) {
	observer := newClosableLink()
	controller := newClosableLink()
	reconnected := newClosableLink()
	d := newSharedDial(sharedStep{link: observer}, sharedStep{link: controller}, sharedStep{link: reconnected})
	rec := &recorder{}
	s := startShared(t, context.Background(), sharedTestOptions(d, rec))

	if _, _, err := s.Attach(context.Background()); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	s.SetRole(kwclient.DisplayRoleController, true)
	waitFor(t, "controller dial", func() bool { return d.count() == 2 })
	waitSharedState(t, s, StateConnected)

	controller.drop(io.EOF)
	waitFor(t, "reconnect", func() bool { return d.count() == 3 })

	roles, forces := d.attempts()
	if roles[2] != kwclient.DisplayRoleController {
		t.Fatalf("reconnect dialled %s, want controller", roles[2])
	}
	if forces[2] {
		t.Fatal("the reconnect still carried the consumed takeover force")
	}
	if !rec.has(StateReconnecting) {
		t.Fatalf("the drop never surfaced as reconnecting: %v", rec.seen())
	}
}
