package session

import (
	"context"
	"errors"
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
}
