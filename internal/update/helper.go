package update

import (
	"context"
	"encoding/json"
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
		_ = os.WriteFile(filepath.Join(filepath.Dir(h.Exe), ".update-error"), []byte(err.Error()), 0o600)
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
// The client relaunches only when args are present.
func runMSIHelper(h handoff) error {
	pkg := &MSIPackage{Path: h.MSI.Path, Tag: h.MSI.Tag, Dir: h.MSI.Dir, MachineScope: h.MSI.Machine, Work: h.MSI.Work}
	err := applyMSI(pkg, nil)
	if err != nil {
		_ = os.WriteFile(filepath.Join(h.MSI.Dir, ".update-error"), []byte(err.Error()), 0o600)
		return err
	}
	pkg.Close()
	if len(h.Args) == 0 {
		return nil
	}
	cmd := exec.Command(h.Exe, h.Args...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart client: %w", err)
	}
	_ = cmd.Process.Release()
	return nil
}

// TakeError returns the previous helper's failure once.
func TakeError(exe string) string {
	l, err := LayoutForExe(exe)
	if err != nil {
		return ""
	}
	p := filepath.Join(l.Dir, ".update-error")
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	_ = os.Remove(p)
	return string(b)
}
