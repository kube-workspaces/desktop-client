// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// logShowsAdminBlock reports whether the installer log shows MSI error
// 1730 ("you must be an Administrator"): a per-machine change attempted
// without administrator rights, which a silent unattended run can never
// satisfy with a prompt. Seen live when a major upgrade's nested removal
// of older per-machine products runs unelevated.
//
// The verbose log may be UTF-16 (as msiexec writes it) or plain text;
// stripping NUL bytes makes the ASCII-subset search work for either
// encoding. Logs are capped: a runaway log must not balloon the helper.
func logShowsAdminBlock(log string) bool {
	const maxLogScan = 8 << 20
	b, err := os.ReadFile(log)
	if err != nil || len(b) == 0 || len(b) > maxLogScan {
		return false
	}
	if bytes.IndexByte(b, 0) >= 0 {
		b = bytes.ReplaceAll(b, []byte{0}, nil)
	}
	return strings.Contains(string(b), "Error 1730")
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
	if err := configureMSICommand(cmd, args); err != nil {
		return err
	}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return &msiExitError{Code: exit.ExitCode()}
		}
		return err
	}
	return nil
}

// msiexec does not use CommandLineToArgvW quoting (see os/exec.Command).
// In particular, public properties need NAME="value with spaces", not
// "NAME=value with spaces". Backslashes are literal, including a directory's
// trailing separator; do not apply CRT backslash doubling here.
func msiCommandLine(exe string, args []string) (string, error) {
	quote := func(s string) (string, error) {
		if strings.ContainsAny(s, "\"\x00\r\n") {
			return "", fmt.Errorf("update: invalid character in installer argument")
		}
		return `"` + s + `"`, nil
	}
	program, err := quote(exe)
	if err != nil {
		return "", err
	}
	parts := []string{program}
	for _, arg := range args {
		prefix, value := "", arg
		if strings.HasPrefix(arg, "INSTALLDIR=") {
			prefix, value = "INSTALLDIR=", strings.TrimPrefix(arg, "INSTALLDIR=")
		}
		if prefix != "" || strings.ContainsAny(value, " \t") {
			value, err = quote(value)
			if err != nil {
				return "", err
			}
		} else if strings.ContainsAny(value, "\"\x00\r\n") {
			return "", fmt.Errorf("update: invalid character in installer argument")
		}
		parts = append(parts, prefix+value)
	}
	return strings.Join(parts, " "), nil
}

// applyMSI runs the staged installer and proves the result: the installed
// shell must report the release tag afterwards, the same self-check the
// archive flow applies before and after its swap (see [Apply]). A nil verify
// means [VerifyVersion].
//
// Silent msiexec never prompts: a per-machine install that needs rights it
// does not have fails instead of asking. Exit 1925 is the one code that
// means the rights genuinely were not available, and it maps to
// [ErrNeedsElevation]. MSI error 1730 in the log means the same story one
// level down (a nested removal of an older per-machine product attempted
// unelevated — the fate of every silent per-machine major upgrade, seen
// live in v0.8.0) and maps there too, naming the staged package to run by
// hand. Per-machine updates therefore elevate before msiexec ever runs
// (see the update-helper-runmsi leg); by the time applyMSI executes, the
// rights question is settled and any failure here is real.
func applyMSI(pkg *MSIPackage, verify func(binary, wantTag string) error) error {
	log := filepath.Join(pkg.Work, "msi-update.log")
	err := execMsiexec(msiArgs(pkg.Path, pkg.Dir, pkg.MachineScope, log))
	diagnostic := "Windows Installer did not create a log; staged package: " + pkg.Path
	if _, statErr := os.Stat(log); statErr == nil {
		diagnostic = "see " + log
	}
	var xe *msiExitError
	if errors.As(err, &xe) {
		switch xe.Code {
		case 1641, 3010:
			// Installed, reboot requested: the files are the new ones.
			// (Unlikely — the caller quits first — but not a failure.)
			err = nil
		case 1602:
			return fmt.Errorf("update: installer was cancelled (%s)", diagnostic)
		case 1639:
			return fmt.Errorf("update: Windows Installer rejected the command line (exit code 1639; %s)", diagnostic)
		case 1925:
			return fmt.Errorf("%w in %s: approve the administrator prompt, or run the update from an elevated terminal (%s)",
				ErrNeedsElevation, pkg.Dir, diagnostic)
		default:
			if logShowsAdminBlock(log) {
				return fmt.Errorf("%w for a per-machine install: run %s manually and approve the administrator prompt, then relaunch (%s)",
					ErrNeedsElevation, pkg.Path, diagnostic)
			}
			return fmt.Errorf("update: installer failed with exit code %d (%s)", xe.Code, diagnostic)
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
