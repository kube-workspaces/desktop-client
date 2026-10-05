// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"net/http"
	"net/url"
)

// SSHKey is a public key registered for workspace cloud-init.
type SSHKey struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	KeyName     string `json:"key_name,omitempty"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// CreateSSHKeyPayload registers a public key; an empty namespace uses the API default.
type CreateSSHKeyPayload struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	KeyName   string `json:"key_name,omitempty"`
	PublicKey string `json:"public_key"`
}

// ListSSHKeys lists keys in the user's default namespace, matching the web profile.
func (c *Client) ListSSHKeys(ctx context.Context) ([]SSHKey, error) {
	var out []SSHKey
	err := c.doJSON(ctx, requestSpec{method: http.MethodGet, path: "/v1/sshkeys"}, &out)
	return out, err
}

// CreateSSHKey registers a public key for future workspace starts.
func (c *Client) CreateSSHKey(ctx context.Context, payload CreateSSHKeyPayload) (*SSHKey, error) {
	var out SSHKey
	if err := c.doJSON(ctx, requestSpec{method: http.MethodPost, path: "/v1/sshkeys", body: payload}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteSSHKey deletes a key in its namespace.
func (c *Client) DeleteSSHKey(ctx context.Context, namespace, name string) error {
	return c.doJSON(ctx, requestSpec{method: http.MethodDelete, path: "/v1/sshkeys/" + url.PathEscape(name), query: url.Values{"namespace": {namespace}}}, nil)
}

// ChangePassword updates the signed-in user's local password.
func (c *Client) ChangePassword(ctx context.Context, current, next string) error {
	return c.doJSON(ctx, requestSpec{method: http.MethodPost, path: "/auth/change-password", body: map[string]string{"currentPassword": current, "newPassword": next}}, nil)
}
