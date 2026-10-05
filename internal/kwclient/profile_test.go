// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestProfileRequests(t *testing.T) {
	var calls []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer credential" {
			t.Error("missing account credential")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/sshkeys":
			writeJSON(t, w, http.StatusOK, `[{"name":"laptop","namespace":"personal","key_name":"Laptop","public_key":"ssh-ed25519 AAAA","fingerprint":"SHA256:abc","created_at":"2026-10-05"}]`)
		case "POST /v1/sshkeys":
			var payload CreateSSHKeyPayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Name != "laptop" || payload.KeyName != "Laptop" || payload.PublicKey != "ssh-ed25519 AAAA" || payload.Namespace != "" {
				t.Errorf("payload=%+v", payload)
			}
			writeJSON(t, w, http.StatusCreated, `{"name":"laptop","namespace":"personal","public_key":"ssh-ed25519 AAAA"}`)
		case "DELETE /v1/sshkeys/laptop":
			if r.URL.Query().Get("namespace") != "personal" {
				t.Error("deletion missing namespace")
			}
			w.WriteHeader(http.StatusNoContent)
		case "GET /auth/device/list":
			if r.URL.Query().Get("scope") != "own" {
				t.Error("Profile must show only this user's devices, including for admins")
			}
			writeJSON(t, w, http.StatusOK, `{"devices":[{"deviceId":"device","name":"Laptop","createdAt":1,"expiresAt":2}]}`)
		case "POST /auth/change-password":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["currentPassword"] != "old" || payload["newPassword"] != "new-password-long" {
				t.Errorf("password payload incorrect")
			}
			writeJSON(t, w, http.StatusOK, `{"status":"ok"}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	c.SetToken("credential")
	ctx := context.Background()
	keys, err := c.ListSSHKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].Fingerprint != "SHA256:abc" || keys[0].KeyName != "Laptop" {
		t.Fatalf("keys=%+v err=%v", keys, err)
	}
	key, err := c.CreateSSHKey(ctx, CreateSSHKeyPayload{Name: "laptop", KeyName: "Laptop", PublicKey: "ssh-ed25519 AAAA"})
	if err != nil || key.Namespace != "personal" {
		t.Fatalf("created=%+v err=%v", key, err)
	}
	if err := c.DeleteSSHKey(ctx, key.Namespace, key.Name); err != nil {
		t.Fatal(err)
	}
	devices, err := c.ListDevices(ctx)
	if err != nil || len(devices) != 1 || devices[0].DeviceID != "device" {
		t.Fatalf("devices=%+v err=%v", devices, err)
	}
	if err := c.ChangePassword(ctx, "old", "new-password-long"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 5 {
		t.Fatalf("calls=%v", calls)
	}
}
