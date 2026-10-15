// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/session"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// fakeAgentDet stands in for *viewer.Tier1Detached. Its Step mirrors the real
// window: when the producer has ended, Step reports the terminal error and
// Closed reports true in the same pass.
type fakeAgentDet struct {
	stepErr error
	closed  bool
	result  error
}

func (f *fakeAgentDet) Step(time.Time, []viewer.Event) error { return f.stepErr }
func (f *fakeAgentDet) IdleWait(time.Time) time.Duration     { return 0 }
func (f *fakeAgentDet) Closed() bool                         { return f.closed }
func (f *fakeAgentDet) Result() error                        { return f.result }
func (f *fakeAgentDet) CloseDisposition() connection.CloseDisposition {
	return connection.Park
}
func (f *fakeAgentDet) Close()                  {}
func (f *fakeAgentDet) Backend() viewer.Backend { return nil }
func (f *fakeAgentDet) ReleaseInput()           {}

func newAgentWindow(det agentDetached) *agentLiveWindow {
	return &agentLiveWindow{
		handle: &agentHandle{
			ws: kwclient.Workspace{Namespace: "ns", Name: "vm"},
		},
		det: det,
	}
}

// A premium window whose producer ends with ErrAgentConsoleRequired must be
// classified as a console handoff, not surfaced as a window failure. The
// producer error arrives through Step (the det reaps it), so Step must not
// return it before the terminal result is mapped. Regression guard: the old
// ordering returned the raw error and the pump closed the session instead of
// dialing the console.
func TestAgentWindowConsoleRequiredIsHandoffNotFailure(t *testing.T) {
	w := newAgentWindow(&fakeAgentDet{
		stepErr: session.ErrAgentConsoleRequired,
		closed:  true,
		result:  session.ErrAgentConsoleRequired,
	})

	err := w.Step(context.Background(), time.Now(), nil)
	if err != nil {
		t.Fatalf("Step returned %v; a console requirement must not surface as a window error", err)
	}
	if !w.closed {
		t.Fatalf("window not closed after the handoff decision")
	}
	// The handoff is entered with a nil API client, so dialing the console
	// fails: the result is that dial error, never the raw console sentinel.
	if w.result == nil {
		t.Fatalf("expected the handoff dial error to be recorded")
	}
	if errors.Is(w.result, session.ErrAgentConsoleRequired) {
		t.Fatalf("result = %v; the console sentinel leaked through the handoff", w.result)
	}
}

// A typed desktop boundary chooses the console label by reason: the secure
// desktop is protected, a higher-integrity foreground window is the
// interactive console. Both arm the automatic return.
func TestAgentWindowHandoffConsoleLabelByReason(t *testing.T) {
	cases := []struct {
		reason    string
		protected bool
	}{
		{"protected-desktop", true},
		{"higher-integrity-window", false},
	}
	for _, tc := range cases {
		var got SessionOptions
		called := false
		w := newAgentWindow(&fakeAgentDet{
			closed: true,
			result: &session.ConsoleRequiredError{Reason: tc.reason},
		})
		w.dialConsole = func(_ context.Context, opts SessionOptions) (*exclusiveHandle, error) {
			got, called = opts, true
			return nil, errors.New("no console in test")
		}
		if err := w.Step(context.Background(), time.Now(), nil); err != nil {
			t.Fatalf("%s: Step returned %v", tc.reason, err)
		}
		if !called {
			t.Fatalf("%s: console was not dialed", tc.reason)
		}
		if got.ProtectedConsole != tc.protected {
			t.Fatalf("%s: ProtectedConsole=%v, want %v", tc.reason, got.ProtectedConsole, tc.protected)
		}
		if got.switchTransport == nil {
			t.Fatalf("%s: console must expose the Agent/Console toggle", tc.reason)
		}
	}
}

// A premium window whose producer ends with a recoverable transport error
// falls back to Tier 0 (the console), exactly like the blocking path. The
// classification must reach MapAgentResult; the raw error must not surface as
// a window failure.
func TestAgentWindowTransportErrorFallsBack(t *testing.T) {
	boom := errors.New("transport lost")
	w := newAgentWindow(&fakeAgentDet{stepErr: boom, closed: true, result: boom})

	if err := w.Step(context.Background(), time.Now(), nil); err != nil {
		t.Fatalf("Step returned %v; a closed window's error belongs in Result", err)
	}
	if !w.closed || w.result == nil || errors.Is(w.result, boom) {
		t.Fatalf("closed=%v result=%v, want a Tier 0 fallback attempt", w.closed, w.result)
	}
}

// ErrNoFallback ends the session with the underlying error: refusal, busy and
// auth never dial the console.
func TestAgentWindowNoFallbackEndsSession(t *testing.T) {
	refused := fmt.Errorf("%w: account is busy", session.ErrNoFallback)
	w := newAgentWindow(&fakeAgentDet{stepErr: refused, closed: true, result: refused})

	if err := w.Step(context.Background(), time.Now(), nil); err != nil {
		t.Fatalf("Step returned %v; a closed window's error belongs in Result", err)
	}
	if !w.closed || !errors.Is(w.result, session.ErrNoFallback) {
		t.Fatalf("closed=%v result=%v, want the no-fallback error", w.closed, w.result)
	}
}

// A live premium window keeps stepping and reports any mid-flight step error.
func TestAgentWindowLiveStepErrorSurfaces(t *testing.T) {
	boom := errors.New("draw failed")
	w := newAgentWindow(&fakeAgentDet{stepErr: boom, closed: false})

	if err := w.Step(context.Background(), time.Now(), nil); !errors.Is(err, boom) {
		t.Fatalf("Step returned %v, want the live step error", err)
	}
	if w.closed {
		t.Fatalf("live window must not be marked closed")
	}
}

// fakeConsole stands in for the Tier 0 live window the console fallback opens.
type fakeConsole struct {
	closed bool
}

func (f *fakeConsole) Step(context.Context, time.Time, []viewer.Event) error { return nil }
func (f *fakeConsole) IdleWait(time.Time) time.Duration                      { return 0 }
func (f *fakeConsole) Closed() bool                                          { return f.closed }
func (f *fakeConsole) Result() error                                         { return nil }
func (f *fakeConsole) CloseDisposition() connection.CloseDisposition         { return connection.Park }
func (f *fakeConsole) Close()                                                { f.closed = true }
func (f *fakeConsole) WindowBackend() viewer.Backend                         { return nil }
func (f *fakeConsole) Raise() error                                          { return nil }
func (f *fakeConsole) ReleaseInput()                                         {}

// The toolbar toggle must move a premium window to the console, and back.
// Regression guard for the follow-up where only a launch-time flag could
// switch transports.
func TestAgentWindowToggleSwitchesTransport(t *testing.T) {
	var toConsole, toAgent int
	w := newAgentWindow(&fakeAgentDet{})
	w.handle.opts.Logf = nil
	w.dialConsole = func(context.Context, SessionOptions) (*exclusiveHandle, error) {
		toConsole++
		return nil, errors.New("no console in test")
	}
	w.dialAgent = func(context.Context) (agentDetached, error) {
		toAgent++
		return &fakeAgentDet{}, nil
	}

	// Agent -> console: a manual toggle while the premium window is live.
	w.wantToggle.Store(true)
	if err := w.Step(context.Background(), time.Now(), nil); err != nil {
		t.Fatalf("agent->console Step returned %v", err)
	}
	if toConsole != 1 || toAgent != 0 {
		t.Fatalf("agent->console dials: console=%d agent=%d, want 1/0", toConsole, toAgent)
	}
	if !w.closed {
		t.Fatalf("a console dial that fails must end the session window")
	}

	// Console -> agent: resume a console-mode window and toggle back.
	w2 := &agentLiveWindow{handle: &agentHandle{
		ws:   kwclient.Workspace{Namespace: "ns", Name: "vm"},
		opts: SessionOptions{Logf: nil},
	}}
	w2.fb = &fakeConsole{}
	w2.dialConsole = func(context.Context, SessionOptions) (*exclusiveHandle, error) {
		toConsole++
		return nil, errors.New("no console in test")
	}
	w2.dialAgent = func(context.Context) (agentDetached, error) {
		toAgent++
		return &fakeAgentDet{}, nil
	}
	w2.wantToggle.Store(true)
	if err := w2.Step(context.Background(), time.Now(), nil); err != nil {
		t.Fatalf("console->agent Step returned %v", err)
	}
	if toAgent != 1 || toConsole != 1 {
		t.Fatalf("console->agent dials: console=%d agent=%d, want 1/1", toConsole, toAgent)
	}
	if w2.fb != nil || w2.det == nil {
		t.Fatalf("console->agent must leave the agent window live: fb=%v det=%v", w2.fb, w2.det)
	}
}

// The watcher returns to the agent only after a required->clear transition, so
// a user who opens the console with no condition present is not yanked back,
// while a UAC handoff returns when the secure desktop clears.
func TestAgentWindowAutoReturnOnlyAfterConditionClears(t *testing.T) {
	// A manual console visit with no condition: clear reading never arms it.
	w := &agentLiveWindow{}
	w.desktopChanged(false, "")
	if w.wantAgent.Load() {
		t.Fatal("a clear desktop must not auto-return a user-chosen console")
	}

	// A condition seen, then cleared: auto-return fires.
	w.desktopChanged(true, "protected-desktop")
	if w.wantAgent.Load() {
		t.Fatal("a required desktop must not return yet")
	}
	w.desktopChanged(false, "")
	if !w.wantAgent.Load() {
		t.Fatal("clearing a seen condition must request the agent")
	}
}

// An auto-return request on a console window drives the same switch as the
// toolbar toggle.
func TestAgentWindowAutoReturnSwitchesToAgent(t *testing.T) {
	w := &agentLiveWindow{handle: &agentHandle{
		ws:   kwclient.Workspace{Namespace: "ns", Name: "vm"},
		opts: SessionOptions{},
	}}
	w.fb = &fakeConsole{}
	w.dialAgent = func(context.Context) (agentDetached, error) {
		return &fakeAgentDet{}, nil
	}
	w.wantAgent.Store(true)
	if err := w.Step(context.Background(), time.Now(), nil); err != nil {
		t.Fatalf("auto-return Step returned %v", err)
	}
	if w.det == nil {
		t.Fatal("auto-return must open the premium window")
	}
	if w.fb != nil {
		t.Fatal("auto-return must tear down the console window")
	}
}
