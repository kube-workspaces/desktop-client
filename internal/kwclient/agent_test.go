// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentPath(t *testing.T) {
	got, err := AgentPath("alice", "desktop-0")
	if err != nil || got != "/proxy/alice/desktop-0/agent/" {
		t.Fatalf("path = %q, %v", got, err)
	}
	for _, name := range []string{"", "../bob", "a/b", "-a", "a-"} {
		if _, err := AgentPath("alice", name); err == nil {
			t.Errorf("accepted invalid workspace %q", name)
		}
	}
}

func TestAgentTicketLifecycle(t *testing.T) {
	var bodies []map[string]any
	var sessionHeaders []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing bearer credential on %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(raw) > 0 {
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("ticket calls require a JSON body (%s %s)", r.Method, r.URL.Path)
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("ticket body not JSON: %v", err)
			}
		} else if r.URL.Path != "/v1/workspaces/desktop-0/agent/status" {
			// The real API (Goa) rejects bodiless POSTs with
			// missing-payload; the fake enforces the same contract.
			t.Errorf("ticket POSTs require a JSON body (%s %s)", r.Method, r.URL.Path)
		}
		bodies = append(bodies, body)
		sessionHeaders = append(sessionHeaders, r.Header.Get("X-KW-Agent-Session"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/desktop-0/agent/attach":
			_, _ = w.Write([]byte(`{"id":"sess-1","ticket":"sess-1.eyJ3b3Jrc3BhY2VVaWQiOiJ3cy0xIn0.c2ln","ttl_ms":60000,"protocol":1}`))
		case "/v1/workspaces/desktop-0/agent/renew":
			_, _ = w.Write([]byte(`{"ttl_ms":60000,"protocol":1}`))
		case "/v1/workspaces/desktop-0/agent/release":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/workspaces/desktop-0/agent/status":
			_, _ = w.Write([]byte(`{"active":true,"sessions":1}`))
		default:
			t.Errorf("unexpected agent path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL, WithToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ticket, err := c.AgentAttach(ctx, "alice", "desktop-0", "laptop")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if ticket.ID != "sess-1" || ticket.Protocol != 1 || ticket.TTLMs != 60000 {
		t.Fatalf("ticket: %+v", ticket)
	}
	if err := c.AgentRenew(ctx, "alice", "desktop-0", ticket.ID); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if err := c.AgentRelease(ctx, "alice", "desktop-0", ticket.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	status, err := c.AgentSessionStatus(ctx, "alice", "desktop-0")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Active || status.Sessions != 1 {
		t.Fatalf("status: %+v", status)
	}
	// Renew/release correlate on the session id in body AND header.
	if len(bodies) != 4 || len(sessionHeaders) != 4 {
		t.Fatalf("calls = %d", len(bodies))
	}
	for _, index := range []int{1, 2} {
		if bodies[index]["session_id"] != "sess-1" || sessionHeaders[index] != "sess-1" {
			t.Fatalf("call %d missing session correlation: %v / %q", index, bodies[index], sessionHeaders[index])
		}
	}
}

func TestAgentTicketProtocolGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","ticket":"x.y.z","ttl_ms":0,"protocol":99}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, WithToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AgentAttach(context.Background(), "alice", "desktop-0", ""); err == nil {
		t.Fatal("unsupported ticket protocol must fail")
	}
}

func TestDialAgentWSBusyMaps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/alice/desktop-0/agent/" {
			t.Errorf("unexpected bridge path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	c, err := New(srv.URL, WithToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.DialAgentWS(context.Background(), "alice", "desktop-0")
	if !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("busy must surface ErrAgentBusy, got %v", err)
	}
}
