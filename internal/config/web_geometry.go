// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WebGeometryPath isolates each web child's settings from the shell and other
// children. The identity never includes a session grant URL or credential.
func WebGeometryPath(profile, server, workspace string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	identity, err := json.Marshal([]string{profile, server, workspace})
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "web-geometry", fmt.Sprintf("%x.json", sha256.Sum256(identity))), nil
}

// LoadWebGeometry reads a child-owned native rectangle. Missing records are
// normal on first launch; malformed records are ignored by the caller.
func LoadWebGeometry(path string) (WindowRect, error) {
	var r WindowRect
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return WindowRect{}, err
	}
	if !r.Valid() {
		return WindowRect{}, fmt.Errorf("invalid web window rectangle")
	}
	return r, nil
}

// Valid bounds native inputs before they cross an integer-sized host API.
func (r WindowRect) Valid() bool {
	const limit = 1 << 24
	return r.Width > 0 && r.Height > 0 && r.Width <= limit && r.Height <= limit &&
		r.X >= -limit && r.X <= limit && r.Y >= -limit && r.Y <= limit
}

// SaveWebGeometry atomically replaces just this child's rectangle. It never
// reads or rewrites config.json, which may be changing in the shell process.
func SaveWebGeometry(path string, r WindowRect) error {
	if !r.Valid() {
		return fmt.Errorf("invalid web window rectangle")
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".geometry-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
