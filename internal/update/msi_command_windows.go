// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"os/exec"
	"syscall"
)

func configureMSICommand(cmd *exec.Cmd, args []string) error {
	line, err := msiCommandLine(cmd.Path, args)
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	return nil
}
