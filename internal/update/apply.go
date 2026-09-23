// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrNeedsElevation means the install directory is not writable (a
// Program-Files-style install): re-run elevated or defer, never half-install.
var ErrNeedsElevation = errors.New("update: install directory needs elevated permissions")

// ErrTranslocated means the app is running translocated from a disk image or
// the bare download: the install is read-only or not where the user thinks.
// Move the app somewhere permanent first.
var ErrTranslocated = errors.New("update: running from a translocated or read-only location")

// PendingName is the marker file an install carries between swap and first
// successful relaunch. It holds the expected tag; [ConfirmPending] consumes
// it (see [Apply]).
const PendingName = ".pending-update"

// BackupSuffix is appended to the previous generation kept beside the new
// binaries until the new build confirms itself on relaunch.
const BackupSuffix = ".prev"

// Layout is the installed files an update swaps.
type Layout struct {
	// Dir holds the binaries: beside the shell on Linux/Windows, inside
	// Kube Workspaces.app/Contents/MacOS on macOS.
	Dir string
	// Shell is the running shell binary.
	Shell string
	// WebChild is the installed webview child, or "" when the install has
	// none (developer copies, windows/arm64).
	WebChild string
	// Windows selects .exe conventions. It derives from the executable's
	// own name, not the host GOOS, so the flow is testable on any host.
	Windows bool
}

// LayoutForExe resolves the install layout from the running executable's
// path. The web child is whatever sits beside it under the release name;
// anything else in the directory is left alone.
func LayoutForExe(exePath string) (Layout, error) {
	if exePath == "" {
		return Layout{}, fmt.Errorf("update: no executable path")
	}
	abs, err := filepath.Abs(exePath)
	if err != nil {
		return Layout{}, fmt.Errorf("update: resolve executable: %w", err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return Layout{}, err
	}
	var l Layout
	l.Dir = filepath.Dir(abs)
	l.Shell = abs
	l.Windows = strings.HasSuffix(strings.ToLower(filepath.Base(abs)), ".exe")
	child := filepath.Join(l.Dir, WebChildBinary)
	if l.Windows {
		child += ".exe"
	}
	if fi, err := os.Stat(child); err == nil && !fi.IsDir() {
		l.WebChild = child
	}
	return l, nil
}

// backupPath is where the previous generation waits out confirmation.
func backupPath(p string) string { return p + BackupSuffix }

// Apply swaps staged binaries into the install that exePath runs from.
//
// The order is deliberate: the staged shell must first report wantTag via
// verify (production runs `new-binary version`; tests inject a fake), and
// only then does anything move. The previous generation is kept as *.prev
// until [ConfirmPending] sees the new build alive on relaunch. Any failure
// after the backup restores it, so Apply never leaves a half-updated install
// behind — it either fully swaps or fully rolls back.
func Apply(staged Staged, exePath, wantTag string, verify func(binary, wantTag string) error) error {
	layout, err := LayoutForExe(exePath)
	if err != nil {
		return err
	}
	if err := osTranslocated(layout.Shell); err != nil {
		return err
	}
	lock := filepath.Join(layout.Dir, ".update-lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		return fmt.Errorf("update: cannot acquire install lock (another update or interrupted transaction): %w", err)
	}
	defer func() { _ = os.Remove(lock) }()
	if _, err := os.Stat(filepath.Join(layout.Dir, PendingName)); !os.IsNotExist(err) {
		return fmt.Errorf("update: launch the installed graphical shell to confirm the previous update first")
	}
	if _, err := os.Stat(filepath.Join(layout.Dir, journalName)); !os.IsNotExist(err) {
		return fmt.Errorf("update: interrupted transaction needs recovery")
	}
	for _, p := range []string{layout.Shell, childTarget(layout)} {
		if _, err := os.Lstat(backupPath(p)); !os.IsNotExist(err) {
			return fmt.Errorf("update: previous backup still exists at %s; restart or recover it first", backupPath(p))
		}
	}
	if layout.WebChild != "" && !staged.HasChild {
		return fmt.Errorf("update: release lacks the installed web child")
	}
	if verify == nil {
		verify = VerifyVersion
	}
	// Self-check before touching the install: a staged binary that does not
	// even report the expected version must never reach the swap.
	if err := verify(staged.Shell, wantTag); err != nil {
		return fmt.Errorf("update: staged build failed its version check: %w", err)
	}

	// Stage the new files next to the install first, so a cross-device or
	// permission failure happens before the current binaries move.
	pending, err := stageForInstall(staged, layout)
	if err != nil {
		return err
	}
	j := journal{HadChild: layout.WebChild != "", NewChild: pending.child != ""}
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(layout.Dir, journalName), data, 0o600); err != nil {
		_ = os.Remove(pending.shell)
		if pending.child != "" {
			_ = os.Remove(pending.child)
		}
		return err
	}
	// From here on, failures roll back: restore() puts the backups back and
	// drops the pending marker.
	restore := func(cause error) error {
		rerr := rollback(layout, pending)
		if rerr != nil {
			return fmt.Errorf("update: install failed (%v) and rollback failed: %w", cause, rerr)
		}
		_ = os.Remove(filepath.Join(layout.Dir, journalName))
		return fmt.Errorf("update: install failed, previous version restored: %w", cause)
	}

	if err := os.Rename(layout.Shell, backupPath(layout.Shell)); err != nil {
		_ = os.Remove(pending.shell)
		_ = os.Remove(pending.child)
		_ = os.Remove(filepath.Join(layout.Dir, journalName))
		return mapAccessError(layout.Dir, err)
	}
	if err := os.Rename(pending.shell, layout.Shell); err != nil {
		return restore(mapAccessError(layout.Dir, err))
	}
	if pending.child != "" {
		if layout.WebChild != "" {
			if err := os.Rename(layout.WebChild, backupPath(layout.WebChild)); err != nil {
				return restore(mapAccessError(layout.Dir, err))
			}
		}
		if err := os.Rename(pending.child, childTarget(layout)); err != nil {
			return restore(mapAccessError(layout.Dir, err))
		}
	}
	if !layout.Windows {
		for _, p := range []string{layout.Shell, layout.WebChild} {
			if p == "" {
				continue
			}
			if err := os.Chmod(p, 0o755); err != nil {
				return restore(err)
			}
		}
	}
	marker := filepath.Join(layout.Dir, PendingName)
	if err := verify(layout.Shell, wantTag); err != nil {
		return restore(err)
	}
	if err := os.WriteFile(marker, []byte(wantTag+"\n"), 0o600); err != nil {
		return restore(err)
	}
	_ = os.Remove(filepath.Join(layout.Dir, journalName))
	return nil
}

// pendingFiles are the new binaries parked in the install dir under temp
// names, ready for the rename swap.
type pendingFiles struct {
	shell, child string
}

// stageForInstall copies the staged binaries into the install dir under
// .pending-* names. Copying (not renaming) keeps the stage usable for retry,
// and keeps the swap itself to renames inside one directory.
func stageForInstall(staged Staged, layout Layout) (pendingFiles, error) {
	var p pendingFiles
	p.shell = filepath.Join(layout.Dir, ".pending-shell")
	if layout.Windows && !strings.HasSuffix(strings.ToLower(p.shell), ".exe") {
		p.shell += ".exe"
	}
	if err := copyFile(staged.Shell, p.shell, 0o755); err != nil {
		return pendingFiles{}, mapAccessError(layout.Dir, err)
	}
	if err := preserveAttrs(p.shell, layout.Shell); err != nil {
		_ = os.Remove(p.shell)
		return pendingFiles{}, err
	}
	if staged.HasChild {
		p.child = filepath.Join(layout.Dir, ".pending-webchild")
		if layout.Windows && !strings.HasSuffix(strings.ToLower(p.child), ".exe") {
			p.child += ".exe"
		}
		if err := copyFile(staged.WebChild, p.child, 0o755); err != nil {
			_ = os.Remove(p.shell)
			return pendingFiles{}, mapAccessError(layout.Dir, err)
		}
		if layout.WebChild != "" {
			if err := preserveAttrs(p.child, layout.WebChild); err != nil {
				_ = os.Remove(p.shell)
				_ = os.Remove(p.child)
				return pendingFiles{}, err
			}
		}
	}
	return p, nil
}

// childTarget is where the new web child belongs: the installed child's path
// when there is one, otherwise the release name beside the shell (an install
// gaining its first child).
func childTarget(layout Layout) string {
	if layout.WebChild != "" {
		return layout.WebChild
	}
	name := WebChildBinary
	if layout.Windows {
		name += ".exe"
	}
	return filepath.Join(layout.Dir, name)
}

// rollback undoes a failed Apply: pending files go away, backups move back,
// the marker is dropped. Best effort per step — it reports the first error
// but keeps trying, because a half-rolled-back install is the worst outcome.
func rollback(layout Layout, p pendingFiles) error {
	var first error
	fail := func(err error) {
		if err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	fail(os.Remove(p.shell))
	fail(os.Remove(p.child))
	if _, err := os.Stat(backupPath(layout.Shell)); err == nil {
		_ = os.Remove(layout.Shell)
		fail(os.Rename(backupPath(layout.Shell), layout.Shell))
	}
	if layout.WebChild != "" {
		if _, err := os.Stat(backupPath(layout.WebChild)); err == nil {
			_ = os.Remove(layout.WebChild)
			fail(os.Rename(backupPath(layout.WebChild), layout.WebChild))
		}
	}
	fail(os.Remove(filepath.Join(layout.Dir, PendingName)))
	// A staged child with no installed predecessor leaves its target behind
	// on rollback: it was never a backup, so remove it outright when a
	// backup does not exist to cover it.
	if layout.WebChild == "" && p.child != "" {
		fail(os.Remove(childTarget(layout)))
	}
	return first
}

// journalName records a multi-file transaction until version verification.
const journalName = ".update-journal"

type journal struct{ HadChild, NewChild bool }

// Recover completes rollback of an interrupted multi-file swap. It uses only
// fixed install paths, never paths supplied by the journal.
func Recover(exe string) error {
	l, err := LayoutForExe(exe)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(l.Dir, journalName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var j journal
	if err := json.Unmarshal(raw, &j); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(l.Dir, PendingName)); os.IsNotExist(err) {
		if j.HadChild {
			l.WebChild = childTarget(l)
		} else {
			l.WebChild = ""
		}
		p := pendingFiles{shell: filepath.Join(l.Dir, ".pending-shell")}
		if l.Windows {
			p.shell += ".exe"
		}
		if j.NewChild {
			p.child = filepath.Join(l.Dir, ".pending-webchild")
			if l.Windows {
				p.child += ".exe"
			}
		}
		if err := rollback(l, p); err != nil {
			return err
		}
	}
	_ = os.Remove(filepath.Join(l.Dir, ".update-lock"))
	return os.Remove(filepath.Join(l.Dir, journalName))
}

// ConfirmPending settles the previous update on startup. The updater writes
// a pending marker at swap time; the first launch of the new build calls
// this with its own version:
//   - marker matches: the new build is alive, backups and marker are removed;
//   - marker differs: something is wrong (or the user re-ran the old
//     binary), so the *.prev generation is kept and a notice for the user is
//     returned;
//   - no marker: nothing to do.
//
// It never deletes the running install's only fallback on a mismatch: a kept
// backup is a manual rescue path, not garbage.
func ConfirmPending(exePath, currentVersion string) (notice string, err error) {
	layout, err := LayoutForExe(exePath)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(layout.Dir, PendingName))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("update: read pending marker: %w", err)
	}
	if strings.TrimSpace(string(raw)) == strings.TrimSpace(currentVersion) && currentVersion != "" {
		for _, p := range []string{filepath.Join(layout.Dir, journalName), backupPath(layout.Shell), backupPath(childTarget(layout))} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return "", fmt.Errorf("update: cleanup will retry on next launch: %w", err)
			}
		}
		return "", os.Remove(filepath.Join(layout.Dir, PendingName))
	}
	kept := backupPath(layout.Shell)
	return fmt.Sprintf("the previous version is kept at %s (expected %s, running %s)",
		kept, strings.TrimSpace(string(raw)), currentVersion), nil
}

// VerifyVersion runs `binary version` and compares its trimmed stdout to
// wantTag. It is the production verify for [Apply]: the staged build proves
// it is the release it claims to be before it touches the install.
func VerifyVersion(binary, wantTag string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil {
		return fmt.Errorf("run staged version: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != wantTag {
		return fmt.Errorf("staged binary reports %q, want %q", got, wantTag)
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("write: %w", err)
	}
	return nil
}
