package update

import (
	"context"
	"golang.org/x/sys/windows"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func waitParent(ctx context.Context, pid int) error {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err == windows.ERROR_INVALID_PARAMETER {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	for {
		status, err := windows.WaitForSingleObject(h, 100)
		if err != nil {
			return err
		}
		if status == windows.WAIT_OBJECT_0 {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

var (
	modShell32        = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW = modShell32.NewProc("ShellExecuteW")
)

// platformElevateHelper relaunches exe elevated through the UAC consent
// prompt (ShellExecute "runas") and returns once the prompt resolves: nil
// means approved and the elevated child started, an [*elevationError]
// otherwise. It is fire-and-forget by design — completion travels back
// through the handoff's done file, which the unelevated waiter polls —
// because ShellExecute hands back no process handle to wait on.
//
// Pure syscall, no cgo: the shell's cross-build contract is unaffected.
func platformElevateHelper(exe string, args []string) error {
	verb, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, windows.EscapeArg(a))
	}
	params, err := syscall.UTF16PtrFromString(strings.Join(quoted, " "))
	if err != nil {
		return err
	}
	// SW_HIDE: the elevated runner has no UI of its own; the consent
	// dialog is rendered by the system regardless.
	const swHide = 0
	r1, _, _ := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		0, swHide)
	if r1 <= 32 {
		return &elevationError{Code: int(r1)}
	}
	return nil
}
