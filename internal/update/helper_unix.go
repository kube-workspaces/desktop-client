//go:build linux || darwin

package update

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// platformElevateHelper is unreachable off Windows: per-machine MSI
// updates only exist there.
func platformElevateHelper(exe string, args []string) error {
	return errors.New("update: elevated installer updates are Windows-only")
}

func waitParent(ctx context.Context, pid int) error {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
