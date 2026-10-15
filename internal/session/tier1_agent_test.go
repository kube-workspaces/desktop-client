package session

import (
	"context"
	"errors"
	"image"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/agent"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

func TestMapAgentResultClasses(t *testing.T) {
	if err := MapAgentResult(context.Background(), nil); err != nil {
		t.Fatalf("nil must end cleanly: %v", err)
	}
	for _, err := range []error{
		agent.ErrRefused,
		kwclient.ErrSessionInUse,
		kwclient.ErrUnauthorized,
		kwclient.ErrForbidden,
	} {
		if got := MapAgentResult(context.Background(), err); !errors.Is(got, ErrNoFallback) {
			t.Fatalf("%v must never fall back, got %v", err, got)
		}
	}
	transport := errors.New("transport lost")
	if got := MapAgentResult(context.Background(), transport); !errors.Is(got, transport) || errors.Is(got, ErrNoFallback) {
		t.Fatalf("recoverable failure must pass through for Tier 0, got %v", got)
	}
}

func TestProtectedDesktopConsoleRequirementIsTypedAndPostAdmission(t *testing.T) {
	// Only the secure desktop (protected-desktop) hands off. A higher-integrity
	// foreground window keeps the premium session; see below.
	for _, payload := range []map[string]any{
		{"session": map[string]any{"desktop": map[string]any{"requiresConsole": true, "reason": "protected-desktop"}}},
		{"captureSuspended": true, "session": map[string]any{"desktop": map[string]any{"requiresConsole": true, "reason": "protected-desktop"}}},
	} {
		err := agentConsoleRequirement(payload)
		if !errors.Is(err, ErrAgentConsoleRequired) {
			t.Fatalf("want console handoff, got %v", err)
		}
		if errors.Is(MapAgentResult(context.Background(), err), ErrNoFallback) {
			t.Fatal("desktop boundary must permit authorized console")
		}
	}
	for _, payload := range []map[string]any{
		nil, {}, {"inputDropped": 10}, {"captureSuspended": "true"},
		{"captureSuspended": true},
		{"session": map[string]any{"desktop": map[string]any{"requiresConsole": true, "reason": "ticket-refused"}}},
		{"session": map[string]any{"desktop": map[string]any{"requiresConsole": false, "reason": "higher-integrity-window"}}},
		// The agent can still capture a higher-integrity foreground window, so
		// it stays on the premium session: only input is UIPI-blocked there.
		{"session": map[string]any{"desktop": map[string]any{"requiresConsole": true, "reason": "higher-integrity-window"}}},
	} {
		if err := agentConsoleRequirement(payload); err != nil {
			t.Fatalf("must not infer console from untyped/unknown telemetry: %v", err)
		}
	}
}

func TestAgentDesktopStateParsesTypedClassification(t *testing.T) {
	cases := []struct {
		name     string
		payload  map[string]any
		required bool
		reason   string
	}{
		{"protected", map[string]any{"session": map[string]any{"desktop": map[string]any{"requiresConsole": true, "reason": "protected-desktop"}}}, true, "protected-desktop"},
		{"higher-integrity", map[string]any{"session": map[string]any{"desktop": map[string]any{"requiresConsole": true, "reason": "higher-integrity-window"}}}, true, "higher-integrity-window"},
		{"clear", map[string]any{"session": map[string]any{"desktop": map[string]any{"requiresConsole": false, "reason": ""}}}, false, ""},
		{"missing", map[string]any{}, false, ""},
		{"nil", nil, false, ""},
		{"wrong-shape", map[string]any{"session": "nope"}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			required, reason := agentDesktopState(tc.payload)
			if required != tc.required || reason != tc.reason {
				t.Fatalf("got (%v, %q), want (%v, %q)", required, reason, tc.required, tc.reason)
			}
		})
	}
}

// The premium window reads the desktop classification off the shared input to
// draw the blocked-input banner. Detaching the session must clear it so a
// stale "blocked" reading cannot outlive the transport.
func TestAgentInputReportsAndClearsDesktopState(t *testing.T) {
	input := &agentInput{}
	if required, reason := input.DesktopRequired(); required || reason != "" {
		t.Fatalf("fresh input reported (%v, %q), want clear", required, reason)
	}
	input.setDesktopState(true, "higher-integrity-window")
	if required, reason := input.DesktopRequired(); !required || reason != "higher-integrity-window" {
		t.Fatalf("blocked state not recorded: (%v, %q)", required, reason)
	}
	input.attach(nil)
	if required, _ := input.DesktopRequired(); required {
		t.Fatal("detaching the session must clear the desktop state")
	}
}

func TestAgentFrameCropsPaddingWithoutApplyingStaleResizeDimensions(t *testing.T) {
	frame := image.NewRGBA(image.Rect(0, 0, 1920, 1088))
	cropped := cropAgentFrame(frame, 1920, 1080)
	if cropped.Rect.Dx() != 1920 || cropped.Rect.Dy() != 1080 || cropped.Stride != frame.Stride {
		t.Fatalf("actual guest dimensions not preserved: %v", cropped.Rect)
	}
	if cropAgentFrame(frame, 1280, 800) != frame {
		t.Fatal("stale dimensions cropped a new-mode frame")
	}
	if cropAgentFrame(frame, 0, 0) != frame {
		t.Fatal("unavailable metadata changed the frame")
	}
}

func TestAgentInputIsHonest(t *testing.T) {
	input := &agentInput{}
	if err := input.Key(0, true); !errors.Is(err, ErrAgentInputPending) {
		t.Fatalf("keys must report pending, got %v", err)
	}
	if err := input.Pointer(1, 2, 0); !errors.Is(err, ErrAgentInputPending) {
		t.Fatalf("pointer must report pending, got %v", err)
	}
	if err := input.Resize(800, 600); !errors.Is(err, ErrAgentInputPending) {
		t.Fatalf("resize without a session must report pending, got %v", err)
	}
	if err := input.ResetKeys(); err != nil {
		t.Fatalf("view-only focus cleanup must not kill the stream: %v", err)
	}
	if _, live := input.DebugCounters(); live {
		t.Fatal("sessionless input must not report live counters")
	}
	input.attach(nil)
	if _, live := input.DebugCounters(); live {
		t.Fatal("detached input must not report live counters")
	}
}
