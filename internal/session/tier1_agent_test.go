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
