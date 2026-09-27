// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrNotMSI means the running binary is not MSI-managed: a manual zip/tarball
// install that keeps the historical binary-swap update. Callers branch on it
// with errors.Is to choose the installer flow instead.
var ErrNotMSI = errors.New("update: installation is not MSI-managed")

// MsiInstall describes a Windows installation owned by the Kube Workspaces
// MSI (found via its Add/Remove Programs registration).
type MsiInstall struct {
	// ProductCode is the installed product's ProductCode (uninstall key name).
	ProductCode string
	// Dir is the install location the MSI recorded (ARPINSTALLLOCATION).
	// Legacy installs predate that property and leave it empty; see Legacy.
	Dir string
	// Version is the registered DisplayVersion.
	Version string
	// MachineScope reports a per-machine (ALLUSERS=1) install. It is inferred
	// from the install location, not the registry hive: Windows Installer may
	// register even a per-user product under HKLM.
	MachineScope bool
	// Legacy reports a best-effort match for an MSI that predates
	// ARPINSTALLLOCATION: a registered Kube Workspaces product exists and the
	// binary runs from a canonical install directory. Custom-directory
	// legacy installs are not detectable — reinstall the current MSI first.
	Legacy bool
}

// msiCandidate is one Add/Remove Programs entry claiming to be our product.
type msiCandidate struct {
	ProductCode     string
	InstallLocation string
	DisplayVersion  string
}

// matchMsiCandidate returns the candidate whose recorded install location is
// exeDir, comparing case-insensitively with separators and trailing slashes
// normalized (Windows paths compare case-insensitively).
func matchMsiCandidate(cands []msiCandidate, exeDir string) *msiCandidate {
	want := normalizeMsiDir(exeDir)
	if want == "" {
		return nil
	}
	for i := range cands {
		if cands[i].InstallLocation == "" {
			continue
		}
		if normalizeMsiDir(cands[i].InstallLocation) == want {
			return &cands[i]
		}
	}
	return nil
}

// normalizeMsiDir canonicalizes a directory for comparison: case, slashes
// and trailing separators are ignored. It is separator-agnostic on purpose
// so Windows paths compare correctly even in Linux-hosted tests.
func normalizeMsiDir(dir string) string {
	dir = strings.TrimSpace(dir)
	dir = strings.ReplaceAll(dir, "\\", "/")
	dir = strings.TrimRight(dir, "/")
	if dir == "" {
		return ""
	}
	return strings.ToLower(dir)
}

// legacyMsiDir reports whether exeDir is one of the canonical install
// directories: the per-user Programs folder or the per-machine Program
// Files folder. localAppData and programFiles are the OS folders; tests
// inject fixtures, production passes the real ones.
func legacyMsiDir(localAppData, programFiles, exeDir string) bool {
	want := normalizeMsiDir(exeDir)
	if want == "" {
		return false
	}
	for _, base := range []string{localAppData, programFiles} {
		if base == "" {
			continue
		}
		if want == normalizeMsiDir(base+"/Programs/Kube Workspaces") ||
			want == normalizeMsiDir(base+"/Kube Workspaces") {
			return true
		}
	}
	return false
}

// inferMachineScope reports whether dir looks like a per-machine location:
// under a Program Files root. Anything else (user profile paths, custom
// dirs) counts as per-user; the writability probe at install time is the
// backstop for exotic layouts.
func inferMachineScope(dir string) bool {
	pf := []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")}
	want := normalizeMsiDir(dir)
	if want == "" {
		return false
	}
	for _, root := range pf {
		root = normalizeMsiDir(root)
		if root == "" {
			continue
		}
		if want == root || strings.HasPrefix(want, root+"/") {
			return true
		}
	}
	return false
}

// MSIAssetName maps a release tag and architecture to the Windows installer
// file name — the MSI asset contract in code, mirroring [AssetName]. Only
// Windows ships MSIs; anything else is an error, never a silent fallback.
func MSIAssetName(tag, goarch string) (string, error) {
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("update: unsupported architecture %q", goarch)
	}
	if _, err := Parse(tag); err != nil {
		return "", fmt.Errorf("update: cannot name an MSI for tag: %w", err)
	}
	return fmt.Sprintf("kube-workspaces-%s-windows-%s.msi", tag, goarch), nil
}

// MSIAssetFor returns the download URL of this Windows architecture's .msi
// in rel, or an error when the release carries no such asset. Non-Windows
// platforms always error: their updates stay on the archive flow.
func (r *Release) MSIAssetFor(goos, goarch string) (Asset, error) {
	if r == nil {
		return Asset{}, fmt.Errorf("update: no release to pick an MSI from")
	}
	if goos != "windows" {
		return Asset{}, fmt.Errorf("update: no MSI for platform %q", goos)
	}
	want, err := MSIAssetName(r.Tag, goarch)
	if err != nil {
		return Asset{}, err
	}
	for _, a := range r.Assets {
		if a.Name == want {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("update: release %s has no asset %q", r.Tag, want)
}
