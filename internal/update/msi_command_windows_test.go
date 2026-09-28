// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"os/exec"
	"strings"
	"testing"
)

func TestMSICommandOverridesWindowsQuoting(t *testing.T) {
	args := msiArgs(`C:\cache\new.msi`, `C:\Program Files\Kube Workspaces\`, true, `C:\cache\msi-update.log`)
	cmd := exec.Command("msiexec.exe", args...)
	if err := configureMSICommand(cmd, args); err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr == nil || !strings.Contains(cmd.SysProcAttr.CmdLine, `INSTALLDIR="C:\Program Files\Kube Workspaces\"`) {
		t.Fatalf("missing MSI-specific command line: %+v", cmd.SysProcAttr)
	}
}
