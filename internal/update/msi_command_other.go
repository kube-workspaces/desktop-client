// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package update

import "os/exec"

func configureMSICommand(_ *exec.Cmd, _ []string) error { return nil }
