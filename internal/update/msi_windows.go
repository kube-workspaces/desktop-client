// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package update

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// uninstallKey is where Windows Installer registers Add/Remove Programs
// entries. Per-user installs normally register under HKCU, per-machine under
// HKLM — but a per-user product may still appear under HKLM, so the hive is
// never treated as the install scope (see [MsiInstall]).
const uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall`

// msiProductName is the DisplayName the MSI registers.
const msiProductName = "Kube Workspaces"

// DetectMsiInstall reports whether exePath runs from a directory owned by an
// installed Kube Workspaces MSI, or nil when it is a manual install.
//
// Detection reads the Add/Remove Programs registrations for our DisplayName
// with the WindowsInstaller marker and compares the recorded
// ARPINSTALLLOCATION against the executable's directory. MSIs that predate
// ARPINSTALLLOCATION fall back to a best-effort match (see [MsiInstall]).
// Per-key read failures are skipped: one corrupt uninstall entry must not
// break updating.
func DetectMsiInstall(exePath string) (*MsiInstall, error) {
	abs, err := filepath.Abs(exePath)
	if err != nil {
		return nil, fmt.Errorf("update: resolve executable: %w", err)
	}
	dir := filepath.Dir(abs)
	var cands []msiCandidate
	for _, hive := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		cands = append(cands, msiCandidates(hive)...)
	}
	if m := matchMsiCandidate(cands, dir); m != nil {
		return &MsiInstall{
			ProductCode:  m.ProductCode,
			Dir:          filepath.Clean(strings.TrimRight(strings.TrimSpace(m.InstallLocation), `/\`)),
			Version:      m.DisplayVersion,
			MachineScope: inferMachineScope(dir),
		}, nil
	}
	if len(cands) > 0 && legacyMsiDir(os.Getenv("LocalAppData"), os.Getenv("ProgramFiles"), dir) {
		return &MsiInstall{
			ProductCode:  cands[0].ProductCode,
			Version:      cands[0].DisplayVersion,
			MachineScope: inferMachineScope(dir),
			Legacy:       true,
		}, nil
	}
	return nil, nil
}

// msiCandidates lists this hive's uninstall entries claiming to be our MSI.
func msiCandidates(hive registry.Key) []msiCandidate {
	base, err := registry.OpenKey(hive, uninstallKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer func() { _ = base.Close() }()
	names, err := base.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}
	var out []msiCandidate
	for _, name := range names {
		key, err := registry.OpenKey(base, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		display, _, err := key.GetStringValue("DisplayName")
		if err != nil || display != msiProductName {
			_ = key.Close()
			continue
		}
		marker, _, err := key.GetIntegerValue("WindowsInstaller")
		if err != nil || marker != 1 {
			_ = key.Close()
			continue
		}
		loc, _, _ := key.GetStringValue("InstallLocation")
		ver, _, _ := key.GetStringValue("DisplayVersion")
		_ = key.Close()
		out = append(out, msiCandidate{ProductCode: name, InstallLocation: loc, DisplayVersion: ver})
	}
	return out
}
