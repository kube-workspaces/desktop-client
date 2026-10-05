// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// DeviceInfo is a revocable native credential's public metadata. Tokens are
// returned only at creation, never by ListDevices.
type DeviceInfo struct {
	DeviceID  string `json:"deviceId"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

// DeviceToken is the result of registering a device using a signed-in session.
type DeviceToken struct {
	Token     string `json:"token"`
	DeviceID  string `json:"device_id"`
	Name      string `json:"name"`
	ExpiresAt int64  `json:"expires_at"`
}

// DeviceName gives registrations a recognizable, bounded host label.
func DeviceName() string {
	name, _ := os.Hostname()
	name = strings.TrimSpace(name)
	if name == "" {
		return "Kube Workspaces desktop"
	}
	runes := []rune(name)
	for len(name) > 64 {
		runes = runes[:len(runes)-1]
		name = string(runes)
	}
	return name
}

// CreateDeviceToken registers a long-lived credential. It does not replace the
// client's token: callers must verify and persist the returned credential.
func (c *Client) CreateDeviceToken(ctx context.Context, name string) (*DeviceToken, error) {
	var out DeviceToken
	if err := c.doJSON(ctx, requestSpec{method: http.MethodPost, path: "/auth/device/create", body: map[string]string{"name": name}}, &out); err != nil {
		return nil, err
	}
	if out.Token == "" || out.DeviceID == "" {
		return nil, fmt.Errorf("kwclient: create device: missing credential")
	}
	return &out, nil
}

// ListDevices lists registrations visible to the current user.
func (c *Client) ListDevices(ctx context.Context) ([]DeviceInfo, error) {
	var out struct {
		Devices []DeviceInfo `json:"devices"`
	}
	if err := c.doJSON(ctx, requestSpec{method: http.MethodGet, path: "/auth/device/list", query: url.Values{"scope": {"own"}}}, &out); err != nil {
		return nil, err
	}
	return out.Devices, nil
}

// RevokeDevice invalidates a registration for subsequent authenticated requests.
func (c *Client) RevokeDevice(ctx context.Context, deviceID string) error {
	return c.doJSON(ctx, requestSpec{method: http.MethodPost, path: "/auth/device/revoke", body: map[string]string{"device_id": deviceID}}, nil)
}
