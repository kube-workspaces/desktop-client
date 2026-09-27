// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package update

// DetectMsiInstall is a non-Windows stub: only Windows ships MSIs, so every
// other platform keeps the archive update flow unconditionally.
func DetectMsiInstall(string) (*MsiInstall, error) { return nil, nil }
