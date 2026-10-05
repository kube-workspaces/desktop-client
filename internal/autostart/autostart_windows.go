// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package autostart

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvalKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
	valueName   = "Kube Workspaces"
)

func (Native) Status() (Status, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = key.Close() }()
	value, _, err := key.GetStringValue(valueName)
	if errors.Is(err, registry.ErrNotExist) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	status := Status{Enabled: value != ""}
	// Task Manager / Settings owns StartupApproved. Respect a user's OS-level
	// veto; never edit this undocumented binary value to bypass it.
	approval, err := registry.OpenKey(registry.CURRENT_USER, approvalKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = approval.Close() }()
	data, _, err := approval.GetBinaryValue(valueName)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return Status{}, err
	}
	status.NeedsApproval = status.Enabled && len(data) > 0 && (data[0] == 3 || data[0] == 7)
	return status, nil
}

func (n Native) SetEnabled(enabled bool) (Status, error) {
	if !enabled {
		key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return Status{}, nil
		}
		if err != nil {
			return Status{}, err
		}
		defer func() { _ = key.Close() }()
		if err = key.DeleteValue(valueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return Status{}, err
		}
		return Status{}, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return Status{}, err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = key.Close() }()
	if err = key.SetStringValue(valueName, syscall.EscapeArg(exe)+" shell"); err != nil {
		return Status{}, err
	}
	return n.Status()
}
