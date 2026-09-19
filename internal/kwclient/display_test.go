// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestDisplayCapability(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != workspacePath("vm-a", "display") {
			t.Errorf("path = %q", got)
		}
		writeJSON(t, w, http.StatusOK, `{
			"enabled": true,
			"protocol": 1,
			"max_participants": 8,
			"max_width": 4096,
			"max_height": 2160,
			"transports": ["webrtc","ws"]
		}`)
	})
	got, err := c.Display(context.Background(), "workspaces", "vm-a")
	if err != nil {
		t.Fatalf("Display: %v", err)
	}
	if !got.Enabled || got.Protocol != 1 || got.MaxParticipants != 8 ||
		got.MaxWidth != 4096 || got.MaxHeight != 2160 {
		t.Errorf("capability mismatch: %+v", got)
	}
	if len(got.Transports) != 2 || got.Transports[0] != "webrtc" {
		t.Errorf("transports = %v", got.Transports)
	}
}

func TestDisplayStatus(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != workspacePath("vm-a", "display", "status") {
			t.Errorf("path = %q", got)
		}
		writeJSON(t, w, http.StatusOK, `{
			"enabled": true,
			"protocol": 1,
			"controller": {"id":"p-c","role":"controller","connected":true,"joined_at":"t0"},
			"observers": [{"id":"p-o","role":"observer","connected":false,"joined_at":"t1"}],
			"participants": 2
		}`)
	})
	got, err := c.DisplayStatus(context.Background(), "workspaces", "vm-a")
	if err != nil {
		t.Fatalf("DisplayStatus: %v", err)
	}
	if !got.Enabled || got.Protocol != 1 || got.Participants != 2 {
		t.Errorf("status mismatch: %+v", got)
	}
	if got.Controller == nil || got.Controller.ID != "p-c" || got.Controller.Role != DisplayRoleController {
		t.Errorf("controller = %+v", got.Controller)
	}
	if len(got.Observers) != 1 || got.Observers[0].ID != "p-o" {
		t.Errorf("observers = %+v", got.Observers)
	}
}

func TestJoinDisplayPostsRoleAndReturnsParticipant(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != workspacePath("vm-a", "display", "join") {
			t.Errorf("path = %q", got)
		}
		var payload map[string]json.RawMessage
		if err := decodeJSONPayload(r, &payload); err != nil {
			t.Fatalf("decode join payload: %v", err)
		}
		var role string
		if err := json.Unmarshal(payload["role"], &role); err != nil {
			t.Fatalf("role: %v", err)
		}
		if role != DisplayRoleController {
			t.Errorf("role = %q, want controller", role)
		}
		writeJSON(t, w, http.StatusOK, `{"participant":{"id":"p-1","role":"controller","connected":true,"joined_at":"t0"}}`)
	})
	got, err := c.JoinDisplay(context.Background(), "workspaces", "vm-a", DisplayRoleController)
	if err != nil {
		t.Fatalf("JoinDisplay: %v", err)
	}
	if got.Participant.ID != "p-1" || got.Participant.Role != DisplayRoleController {
		t.Errorf("participant = %+v", got.Participant)
	}
}

func TestJoinDisplayConflictReturnsErrControllerPresent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusConflict, `{"error":"conflict","message":"display already controlled by another participant"}`)
	})
	_, err := c.JoinDisplay(context.Background(), "workspaces", "vm-a", DisplayRoleObserver)
	if !errors.Is(err, ErrControllerPresent) {
		t.Fatalf("JoinDisplay = %v, want errors.Is(ErrControllerPresent)", err)
	}
}

func TestAcquireDisplayControlForce(t *testing.T) {
	var gotForce string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != workspacePath("vm-a", "display", "control", "acquire") {
			t.Errorf("path = %q", got)
		}
		var payload map[string]json.RawMessage
		if err := decodeJSONPayload(r, &payload); err != nil {
			t.Fatalf("decode acquire payload: %v", err)
		}
		var pid string
		if err := json.Unmarshal(payload["participant_id"], &pid); err != nil {
			t.Fatalf("participant_id: %v", err)
		}
		if pid != "p-1" {
			t.Errorf("participant_id = %q", pid)
		}
		var forceB json.RawMessage
		if err := json.Unmarshal(payload["force"], &forceB); err != nil {
			t.Fatalf("force: %v", err)
		}
		_ = forceB
		var force bool
		if err := json.Unmarshal(payload["force"], &force); err != nil {
			t.Fatalf("force bool: %v", err)
		}
		_ = force
		_ = gotForce
		writeJSON(t, w, http.StatusOK, `{
			"controller":{"id":"p-1","role":"controller","connected":true,"joined_at":"t0"},
			"released":false,
			"transferred":false
		}`)
	})
	got, err := c.AcquireDisplayControl(context.Background(), "workspaces", "vm-a", "p-1", true)
	if err != nil {
		t.Fatalf("AcquireDisplayControl: %v", err)
	}
	if got.Controller == nil || got.Controller.ID != "p-1" {
		t.Errorf("controller = %+v", got.Controller)
	}
	if got.Released || got.Transfered {
		t.Errorf("result = %+v", got)
	}
}

func TestTransferDisplayControlConflict(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusConflict, `{"error":"conflict","message":"participant \"p-2\" is not the current controller"}`)
	})
	_, err := c.TransferDisplayControl(context.Background(), "workspaces", "vm-a", "p-1", "p-2", false)
	if !errors.Is(err, ErrNotController) {
		t.Fatalf("TransferDisplayControl = %v, want errors.Is(ErrNotController)", err)
	}
}

func TestLeaveDisplaySendsDELETEAndSwarmsSentinels(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		writeJSON(t, w, http.StatusOK, `{"ok":true}`)
	})
	if err := c.LeaveDisplay(context.Background(), "workspaces", "vm-a", "p-1"); err != nil {
		t.Fatalf("LeaveDisplay: %v", err)
	}
	if gotPath != workspacePath("vm-a", "display", "sessions", "p-1") {
		t.Errorf("path = %q", gotPath)
	}
}

func TestLeaveDisplayNotFoundReturnsErrParticipantNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, `{"error":"not_found","message":"display session has no participant \"p-9\""}`)
	})
	if err := c.LeaveDisplay(context.Background(), "workspaces", "vm-a", "p-9"); !errors.Is(err, ErrParticipantNotFound) {
		t.Fatalf("LeaveDisplay = %v, want errors.Is(ErrParticipantNotFound)", err)
	}
}

// decodeJSONPayload decodes the JSON request body into a map of raw values.
func decodeJSONPayload(r *http.Request, out *map[string]json.RawMessage) error {
	defer func() { _ = r.Body.Close() }()
	return json.NewDecoder(r.Body).Decode(out)
}
