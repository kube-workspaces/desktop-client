package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type handoff struct {
	Prepared Prepared
	// MSI carries an installer update: when non-nil the helper runs msiexec
	// instead of the binary swap. It is a plain struct (no funcs) so the
	// handoff stays JSON-serializable.
	MSI    *msiHandoff
	Exe    string
	Parent int
	Args   []string
}

// elevationError is a failed attempt to relaunch the helper elevated
// (UAC consent prompt). Code 1223 (ERROR_CANCELLED) means the user
// dismissed the prompt; anything else is the platform refusing the launch.
type elevationError struct{ Code int }

func (e *elevationError) Error() string {
	if e.Code == 1223 {
		return "update: administrator approval was declined — the update did not start; approve the prompt to update, or run the downloaded installer manually"
	}
	return fmt.Sprintf("update: could not elevate the installer helper (code %d)", e.Code)
}

// elevateHelper relaunches the helper elevated and returns once the
// consent prompt resolves. Overridden in tests; the production function
// lives in helper_windows.go, with an unreachable stub in helper_unix.go
// (per-machine MSI updates are Windows-only).
var elevateHelper = platformElevateHelper

// elevatedDoneFile is the completion signal the elevated leg leaves in the
// staging directory: "0" for installed, anything else for failed. A file
// (rather than a process handle) because ShellExecute hands back no handle
// to wait on.
const elevatedDoneFile = "elevated.done"

// elevatedWaitTimeout bounds the waiter's wait for the elevated leg: slow
// disks and five stale removals can stretch an install to minutes, but a
// prompt left unanswered overnight must still end in a relaunched client,
// not a hung helper.
var elevatedWaitTimeout = 10 * time.Minute

// writeElevatedDone records the elevated leg's outcome atomically: temp
// file plus rename, so a polling waiter never reads a half-written code.
func writeElevatedDone(work string, code int) error {
	tmp := filepath.Join(work, elevatedDoneFile+".tmp")
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d", code)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(work, elevatedDoneFile))
}

// waitElevatedDone polls for the elevated leg's completion signal. A file
// that exists but does not parse yet is treated as in-flight (the write is
// atomic, but filesystems lie); only the timeout gives up.
func waitElevatedDone(work string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		if b, err := os.ReadFile(filepath.Join(work, elevatedDoneFile)); err == nil {
			var code int
			if _, serr := fmt.Sscanf(string(b), "%d", &code); serr == nil {
				return code, nil
			}
		}
		if time.Now().After(deadline) {
			return -1, fmt.Errorf("update: timed out waiting for the elevated installer")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// msiHandoff is the serializable half of an [MSIPackage].
type msiHandoff struct {
	Path, Tag, Dir string
	Machine        bool
	Work           string
}

// StartHelper runs a private copy of the current executable outside the install
// directory. It waits for this process to exit before swapping locked binaries.
// The caller must quit only after this succeeds and transfer staging ownership.
func StartHelper(p *Prepared, exe string, args []string) error {
	l, err := LayoutForExe(exe)
	if err != nil {
		return err
	}
	if err := osTranslocated(l.Shell); err != nil {
		return err
	}
	f, err := os.CreateTemp(l.Dir, ".update-write-test-*")
	if err != nil {
		return mapAccessError(l.Dir, err)
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	helper := filepath.Join(p.Work, "update-helper")
	if l.Windows {
		helper += ".exe"
	}
	if err := copyFile(exe, helper, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(handoff{Prepared: *p, Exe: l.Shell, Parent: os.Getpid(), Args: args})
	if err != nil {
		return err
	}
	job := filepath.Join(p.Work, "handoff.json")
	if err := os.WriteFile(job, data, 0o600); err != nil {
		return err
	}
	cmd := exec.Command(helper, "update-helper", job)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		_ = os.Remove(helper)
		_ = os.Remove(job)
		return fmt.Errorf("start update helper: %w", err)
	}
	_ = cmd.Process.Release()
	return nil
}

// StartMSIHelper stages a detached installer update: a private copy of the
// current executable waits for this process to exit, runs the verified .msi
// unattended, proves the installed build, and relaunches the client (unless
// args is empty, as in the CLI flow). Ownership transfers like [StartHelper].
func StartMSIHelper(p *MSIPackage, exe string, args []string) error {
	l, err := LayoutForExe(exe)
	if err != nil {
		return err
	}
	if err := osTranslocated(l.Shell); err != nil {
		return err
	}
	helper := filepath.Join(p.Work, "update-helper")
	if l.Windows {
		helper += ".exe"
	}
	if err := copyFile(exe, helper, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(handoff{MSI: &msiHandoff{
		Path: p.Path, Tag: p.Tag, Dir: p.Dir, Machine: p.MachineScope, Work: p.Work,
	}, Exe: l.Shell, Parent: os.Getpid(), Args: args})
	if err != nil {
		return err
	}
	job := filepath.Join(p.Work, "handoff.json")
	if err := os.WriteFile(job, data, 0o600); err != nil {
		return err
	}
	// A per-machine install cannot update itself unelevated: nested
	// removal of older products fails with MSI error 1730 and the whole
	// transaction rolls back. Elevate first, while the user is watching —
	// the consent prompt lands seconds after their "restart to update"
	// click, and a dismissal returns here with the app still open instead
	// of stranding a quit plus a failed update.
	if p.MachineScope {
		if err := elevateHelper(helper, []string{"update-helper-runmsi", job}); err != nil {
			_ = os.Remove(helper)
			_ = os.Remove(job)
			return err
		}
	}
	cmd := exec.Command(helper, "update-helper", job)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		_ = os.Remove(helper)
		_ = os.Remove(job)
		return fmt.Errorf("start update helper: %w", err)
	}
	_ = cmd.Process.Release()
	return nil
}

// RunHelper is an internal entry point. Errors persist beside the installation
// so the next shell launch can report them even when no console was attached.
func RunHelper(job string) error {
	data, err := os.ReadFile(job)
	if err != nil {
		return err
	}
	var h handoff
	if err := json.Unmarshal(data, &h); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := waitParent(ctx, h.Parent); err != nil {
		return err
	}
	if h.MSI != nil {
		return runMSIHelper(h)
	}
	layout, layoutErr := LayoutForExe(h.Exe)
	if layoutErr != nil {
		return layoutErr
	}
	err = Apply(h.Prepared.Staged, h.Exe, h.Prepared.Tag, nil)
	if err != nil {
		recordUpdateError(layout.Dir, err)
	}
	// Apply restores the previous generation on failure; either way the user
	// gets a shell again. Keep the stage on failure for diagnosis/recovery.
	cmd := exec.Command(h.Exe, h.Args...)
	detach(cmd)
	if startErr := cmd.Start(); startErr != nil {
		if err == nil {
			p := pendingFiles{}
			if h.Prepared.Staged.HasChild {
				p.child = filepath.Join(layout.Dir, ".pending-webchild")
			}
			if rollbackErr := rollback(layout, p); rollbackErr != nil {
				return fmt.Errorf("restart failed (%v); rollback: %w", startErr, rollbackErr)
			}
			old := exec.Command(h.Exe, h.Args...)
			detach(old)
			if old.Start() == nil {
				_ = old.Process.Release()
			}
		}
		return fmt.Errorf("restart client: %w", startErr)
	}
	_ = cmd.Process.Release()
	if err == nil {
		h.Prepared.Close()
	}
	return err
}

// runMSIHelper executes the installer half of the handoff: the parent has
// exited, so msiexec owns the install directory uncontested. A failure keeps
// the stage (including msi-update.log) for diagnosis; success cleans it.
// The client relaunches only when args are present. The marker goes beside
// the running binary: that is the binary the user will launch again, and
// the one whose directory [TakeError] reads.
//
// A failed install still relaunches the client: msiexec is transactional,
// so the previous build is in place exactly as the user left it, and
// stranding them with nothing running is worse than any restart risk. The
// recorded error surfaces on the next launch through [TakeError].
func runMSIHelper(h handoff) error {
	pkg := &MSIPackage{Path: h.MSI.Path, Tag: h.MSI.Tag, Dir: h.MSI.Dir, MachineScope: h.MSI.Machine, Work: h.MSI.Work}
	if h.MSI.Machine {
		return runMachineMSIHelper(h, pkg)
	}
	err := applyMSI(pkg, nil)
	if err != nil {
		recordUpdateError(filepath.Dir(h.Exe), err)
		if len(h.Args) > 0 {
			if rerr := startDetached(h.Exe, h.Args); rerr != nil {
				return fmt.Errorf("%w; restart client: %v", err, rerr)
			}
		}
		return err
	}
	pkg.Close()
	if len(h.Args) == 0 {
		return nil
	}
	if err := startDetached(h.Exe, h.Args); err != nil {
		return fmt.Errorf("restart client: %w", err)
	}
	return nil
}

// runMachineMSIHelper is the unelevated waiter's half of a per-machine
// update: the elevated leg (RunElevatedMSI, consented up front in
// StartMSIHelper) runs msiexec and leaves its outcome in the done file.
// This leg cleans the stage on success and relaunches the client either
// way — a failed install still hands the user a shell, with the recorded
// error surfacing on the next launch.
func runMachineMSIHelper(h handoff, pkg *MSIPackage) error {
	code, err := waitElevatedDone(h.MSI.Work, elevatedWaitTimeout)
	if err != nil {
		recordUpdateError(filepath.Dir(h.Exe), err)
		if len(h.Args) > 0 {
			if rerr := startDetached(h.Exe, h.Args); rerr != nil {
				return fmt.Errorf("%w; restart client: %v", err, rerr)
			}
		}
		return err
	}
	if code != 0 {
		// The elevated leg recorded the real failure already; surface it
		// without consuming the marker the next launch reports.
		msg := peekUpdateError(filepath.Dir(h.Exe))
		if msg == "" {
			msg = "update: elevated installer failed"
		}
		if len(h.Args) > 0 {
			if rerr := startDetached(h.Exe, h.Args); rerr != nil {
				return fmt.Errorf("%s; restart client: %v", msg, rerr)
			}
		}
		return errors.New(msg)
	}
	pkg.Close()
	if len(h.Args) == 0 {
		return nil
	}
	if err := startDetached(h.Exe, h.Args); err != nil {
		return fmt.Errorf("restart client: %w", err)
	}
	return nil
}

// RunElevatedMSI is the elevated leg's entry point (the
// `update-helper-runmsi` subcommand): consent was already collected up
// front, so this runs msiexec, records the outcome for the unelevated
// waiter, and exits. It never relaunches the client itself — an elevated
// child would inherit administrator rights, so the unelevated waiter owns
// every restart.
func RunElevatedMSI(job string) error {
	data, err := os.ReadFile(job)
	if err != nil {
		return err
	}
	var h handoff
	if err := json.Unmarshal(data, &h); err != nil {
		return err
	}
	if h.MSI == nil {
		return errors.New("update: no installer in handoff")
	}
	ctx, cancel := context.WithTimeout(context.Background(), elevatedWaitTimeout)
	defer cancel()
	if err := waitParent(ctx, h.Parent); err != nil {
		return finishElevated(h, 1, err)
	}
	pkg := &MSIPackage{Path: h.MSI.Path, Tag: h.MSI.Tag, Dir: h.MSI.Dir, MachineScope: h.MSI.Machine, Work: h.MSI.Work}
	if err := applyMSI(pkg, nil); err != nil {
		return finishElevated(h, 1, err)
	}
	return finishElevated(h, 0, nil)
}

// finishElevated records a failure for the next launch and always signals
// the done file the waiter polls: a waiter left polling until timeout,
// then relaunching over a finished install, is how versions get skipped.
func finishElevated(h handoff, code int, err error) error {
	if err != nil {
		recordUpdateError(filepath.Dir(h.Exe), err)
	}
	if werr := writeElevatedDone(h.MSI.Work, code); werr != nil {
		if err == nil {
			return fmt.Errorf("update: signal completion: %w", werr)
		}
		return fmt.Errorf("%w; signal completion: %v", err, werr)
	}
	return err
}

// startDetached launches exe with args outside this process's lifetime,
// for the helper's client restart after the install (or its rollback).
func startDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

// errorMarker is where a failed update records its message for the next
// launch to report (see [TakeError]).
const errorMarker = ".update-error"

// recordUpdateError leaves err where the next shell launch will find it.
//
// installDir is the running binary's directory — the same place [TakeError]
// looks — and it is preferred because it sits beside the binaries whose swap
// failed. When it cannot be written, the message goes to the user cache
// instead. That fallback is not a nicety: an MSI update of a per-machine
// install runs from an ordinary user process (Windows Installer does the
// writing, not us), so writing the marker into Program Files fails and the
// only record of a failed update would be discarded. Best effort by nature: a
// helper that can write nowhere has nothing left to report with.
func recordUpdateError(installDir string, err error) {
	if err == nil {
		return
	}
	if werr := os.WriteFile(filepath.Join(installDir, errorMarker), []byte(err.Error()), 0o600); werr == nil {
		return
	}
	cache, cerr := CacheDir()
	if cerr != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(cache, errorMarker), []byte(err.Error()), 0o600)
}

// TakeError returns the previous helper's failure once.
func TakeError(exe string) string {
	l, err := LayoutForExe(exe)
	if err != nil {
		return ""
	}
	if msg := takeErrorMarker(l.Dir); msg != "" {
		return msg
	}
	// The cache fallback, for a per-machine install the helper could not
	// write into (see recordUpdateError).
	cache, cerr := CacheDir()
	if cerr != nil {
		return ""
	}
	return takeErrorMarker(cache)
}

// takeErrorMarker consumes the marker in dir, if any.
func takeErrorMarker(dir string) string {
	msg := peekUpdateError(dir)
	if msg != "" {
		_ = os.Remove(filepath.Join(dir, errorMarker))
	}
	return msg
}

// peekUpdateError reads the marker in dir without consuming it, for legs
// that report a failure recorded by another leg.
func peekUpdateError(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, errorMarker))
	if err != nil {
		return ""
	}
	return string(b)
}
