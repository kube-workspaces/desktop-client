// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
)

// MSIInstaller is the optional updater capability for MSI-managed Windows
// installs. The shell asserts it without widening [UpdateService], so the
// archive flow and its fakes stay untouched: when the assertion fails, or
// when detection reports a manual install ([ErrNotMSI]), everything behaves
// exactly as before.
type MSIInstaller interface {
	// DetectMSI reports the MSI ownership of the running install, or nil
	// for a manual install.
	DetectMSI() (*MsiInstall, error)
	// PrepareMSI downloads and verifies the release's .msi without touching
	// the installation. It returns [ErrNotMSI] for manual installs.
	PrepareMSI(ctx context.Context, rel *Release, progress func(int64)) (*MSIPackage, error)
}

// MSIPackage is a downloaded, checksum-verified Windows installer plus the
// install it targets. Close drops the staging directory; it is idempotent,
// so both the handoff helper (on success) and the caller (deferred) may call
// it.
type MSIPackage struct {
	// Path is the verified .msi file under Work.
	Path string
	// Tag is the release tag the MSI was downloaded for.
	Tag string
	// Dir is the current install directory, passed back as INSTALLDIR so an
	// update preserves even a customized location.
	Dir string
	// MachineScope selects the ALLUSERS=1 MST-style override for per-machine
	// installs; per-user installs pass MSIINSTALLPERUSER=1 instead. It changes
	// the installer's scope, not the client's rights: see [Installer.PrepareMSI]
	// for why the process running it needs no write access to Dir.
	MachineScope bool
	// Work is the staging directory owning Path (and the update log).
	Work string
}

// Close removes the staging directory.
func (m *MSIPackage) Close() { _ = os.RemoveAll(m.Work) }

// DetectMSI reports the MSI ownership of the install ExePath runs from.
func (in *Installer) DetectMSI() (*MsiInstall, error) {
	exe, err := in.exePath()
	if err != nil {
		return nil, err
	}
	detect := in.MSIDetect
	if detect == nil {
		detect = DetectMsiInstall
	}
	return detect(exe)
}

// PrepareMSI downloads the release's .msi for the configured platform and
// verifies it against the release's SHA256SUMS. It touches nothing in the
// install: the verified package is staged in the user cache ([CacheDir]), and
// the only thing checked here is that the recorded install location is a real
// directory.
//
// It deliberately does NOT probe whether this process can write that
// directory. An MSI update moves no bytes from the client: msiexec hands the
// install to the Windows Installer service, which runs as LocalSystem and
// performs the per-machine writes, raising its own UAC consent prompt when the
// install context needs one. A per-machine install in Program Files is
// therefore updatable from an unelevated shell, and a writability probe would
// reject exactly the installs that work — v0.6.2 shipped that probe and every
// "All users" install died at download time with [ErrNeedsElevation], which
// re-running elevated cannot fix, because it is not the client that writes.
// Elevation is an msiexec-time story, not a download-time one; see
// [applyMSI] for the one exit code that means it was actually needed.
//
// Like [Installer.Prepare], the trust anchor is SHA256SUMS over TLS, and an
// unconfigured signature step is accepted explicitly (see [Install]).
func (in *Installer) PrepareMSI(ctx context.Context, rel *Release, progress func(int64)) (_ *MSIPackage, retErr error) {
	if rel == nil {
		return nil, fmt.Errorf("update: no release to install")
	}
	goos, goarch := in.platform()
	inst, err := in.DetectMSI()
	if err != nil {
		return nil, err
	}
	if inst == nil || goos != "windows" {
		return nil, ErrNotMSI
	}
	if inst.Dir == "" {
		return nil, fmt.Errorf("update: MSI install location is unknown; reinstall the current MSI first")
	}
	asset, err := rel.MSIAssetFor(goos, goarch)
	if err != nil {
		return nil, err
	}
	u, err := neturl.Parse(asset.URL)
	if err != nil || filepath.Base(u.Path) != asset.Name {
		return nil, fmt.Errorf("update: asset URL does not match installer name")
	}
	// A recorded location that is not a directory is a real, actionable
	// failure (a moved or hand-edited registration): report it now, by name,
	// rather than after the download. Writability is deliberately NOT probed
	// here — see this function's doc comment.
	if fi, err := os.Stat(inst.Dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("update: MSI install location %s is not a directory; reinstall the current MSI", inst.Dir)
	}

	cache, err := CacheDir()
	if err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp(cache, "msi-install-*")
	if err != nil {
		return nil, fmt.Errorf("update: create staging dir: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(work)
		}
	}()
	archive, err := Download(ctx, in.Client, asset.URL, work, progress)
	if err != nil {
		return nil, err
	}
	sums, err := fetchSums(ctx, in.Client, rel)
	if err != nil {
		return nil, err
	}
	v := in.verifier()
	if err := v.VerifyChecksum(archive, sums, asset.Name); err != nil {
		return nil, err
	}
	if err := v.VerifySignature(archive, rel); err != nil && !errors.Is(err, ErrSignatureNotConfigured) {
		return nil, err
	}
	return &MSIPackage{Path: archive, Tag: rel.Tag, Dir: inst.Dir, MachineScope: inst.MachineScope, Work: work}, nil
}

// msiArgs builds the msiexec command line for an unattended update: quiet,
// no reboot (the caller quits first), a verbose log beside the package, the
// scope flag, and the current directory so custom locations survive.
func msiArgs(path, dir string, machine bool, log string) []string {
	args := []string{"/i", path, "/qn", "/norestart", "/l*v", log}
	if machine {
		args = append(args, "ALLUSERS=1")
	} else {
		args = append(args, "MSIINSTALLPERUSER=1")
	}
	return append(args, "INSTALLDIR="+dir)
}

// msiExitError is a completed msiexec run with a nonzero status.
type msiExitError struct{ Code int }

func (e *msiExitError) Error() string {
	return fmt.Sprintf("update: installer exited with code %d", e.Code)
}

// execMsiexec runs msiexec and reports its exit. Overridden in tests; the
// production path only ever runs on Windows (see [applyMSI]).
var execMsiexec = func(args []string) error {
	cmd := exec.Command("msiexec.exe", args...)
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return &msiExitError{Code: exit.ExitCode()}
		}
		return err
	}
	return nil
}

// applyMSI runs the staged installer and proves the result: the installed
// shell must report the release tag afterwards, the same self-check the
// archive flow applies before and after its swap (see [Apply]). A nil verify
// means [VerifyVersion].
//
// Windows Installer performs the writes as LocalSystem, so a per-machine
// install normally succeeds from an unelevated process and prompts for its own
// UAC consent when the install context requires it. Exit 1925 is the one code
// that means the rights genuinely were not available, and it maps to
// [ErrNeedsElevation] so the user is told to approve the prompt (or re-run
// elevated) instead of reading a bare exit code.
func applyMSI(pkg *MSIPackage, verify func(binary, wantTag string) error) error {
	log := filepath.Join(pkg.Work, "msi-update.log")
	err := execMsiexec(msiArgs(pkg.Path, pkg.Dir, pkg.MachineScope, log))
	var xe *msiExitError
	if errors.As(err, &xe) {
		switch xe.Code {
		case 1641, 3010:
			// Installed, reboot requested: the files are the new ones.
			// (Unlikely — the caller quits first — but not a failure.)
			err = nil
		case 1602:
			return fmt.Errorf("update: installer was cancelled (see %s)", log)
		case 1925:
			return fmt.Errorf("%w in %s: approve the administrator prompt, or run the update from an elevated terminal (see %s)",
				ErrNeedsElevation, pkg.Dir, log)
		default:
			return fmt.Errorf("update: installer failed with exit code %d (see %s)", xe.Code, log)
		}
	}
	if err != nil {
		return err
	}
	if verify == nil {
		verify = VerifyVersion
	}
	shell := filepath.Join(pkg.Dir, ShellBinary+".exe")
	if err := verify(shell, pkg.Tag); err != nil {
		return fmt.Errorf("update: installed build failed its version check: %w", err)
	}
	return nil
}
