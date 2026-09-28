// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMSICommandLinePropertyQuoting(t *testing.T) {
	for _, machine := range []bool{false, true} {
		for _, dir := range []string{`C:\Program Files\Kube Workspaces`, `C:\Users\Chris Fordham\Apps\Kube Workspaces\`, `C:\Apps\KW`} {
			args := msiArgs(`C:\Users\Chris Fordham\new.msi`, dir, machine, `C:\User Cache\msi-update.log`)
			got, err := msiCommandLine(`C:\Windows\System32\msiexec.exe`, args)
			if err != nil {
				t.Fatal(err)
			}
			scope := "MSIINSTALLPERUSER=1"
			if machine {
				scope = "ALLUSERS=1"
			}
			want := `"C:\Windows\System32\msiexec.exe" /i "C:\Users\Chris Fordham\new.msi" /qn /norestart /l*v "C:\User Cache\msi-update.log" ` + scope + ` INSTALLDIR="` + dir + `"`
			if got != want {
				t.Fatalf("command line:\n got %s\nwant %s", got, want)
			}
		}
	}
	if _, err := msiCommandLine("msiexec.exe", []string{"INSTALLDIR=bad\"path"}); err == nil {
		t.Fatal("accepted an embedded quote")
	}
}

func TestMSIFailureOnlyLinksExistingLog(t *testing.T) {
	old := execMsiexec
	t.Cleanup(func() { execMsiexec = old })
	execMsiexec = func([]string) error { return &msiExitError{Code: 1639} }
	pkg := &MSIPackage{Work: t.TempDir(), Path: `C:\cache\new.msi`}
	err := applyMSI(pkg, nil)
	if err == nil || !strings.Contains(err.Error(), "rejected the command line") || !strings.Contains(err.Error(), "did not create a log") || strings.Contains(err.Error(), "see ") {
		t.Fatalf("missing-log diagnostic: %v", err)
	}
	log := filepath.Join(pkg.Work, "msi-update.log")
	if err := os.WriteFile(log, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyMSI(pkg, nil); err == nil || !strings.Contains(err.Error(), "see "+log) {
		t.Fatalf("existing-log diagnostic: %v", err)
	}
	execMsiexec = func([]string) error { return errors.New("launch failed") }
	if err := applyMSI(pkg, nil); err == nil || err.Error() != "launch failed" {
		t.Fatalf("launch error: %v", err)
	}
}
