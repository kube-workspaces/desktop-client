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
