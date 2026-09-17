//go:build windows

package console

import (
	"os"
	"syscall"
)

// This file is the Windows half of a two-part fix for a packaging bug in
// v0.1.0: running kube-workspaces.exe opened the graphical shell *and* a
// separate console window, and closing that console killed the application.
//
// The cause was the subsystem the binary is linked as. A console subsystem
// (IMAGE_SUBSYSTEM_WINDOWS_CUI) executable launched from Explorer gets a
// console allocated for it by the loader, and that console owns the process:
// closing it sends CTRL_CLOSE_EVENT and the process dies. The Makefile now
// links the Windows targets with `-H=windowsgui`, which marks them as
// IMAGE_SUBSYSTEM_WINDOWS_GUI, and no console is created.
//
// That alone would trade one bug for a worse one. The shell binary is both a
// desktop application and a CLI — `kube-workspaces list`, `probe`,
// `screenshot`, `login`, `version` and `--help` all print — and a GUI
// subsystem process started from cmd.exe or PowerShell inherits no console, so
// every one of those would become a silent no-op. AttachParent puts the
// output back by attaching to the console the process was launched from.
//
// Known and accepted trade-off, so that the next person does not file it as a
// new bug: cmd.exe does not *wait* for a GUI subsystem process. Running
// `kube-workspaces list` from cmd.exe returns the prompt immediately and then
// paints the output over it; pressing Enter gets a clean prompt back. PowerShell
// behaves the same way unless the call is piped (`kube-workspaces list | Out-String`)
// or wrapped (`Start-Process -Wait`, `cmd /c start /wait ...`), which do wait.
// This is inherent to the subsystem flag and cannot be fixed from inside the
// process; the alternatives are shipping two executables (kube-workspaces.exe
// plus a console kube-workspaces-cli.exe) or a launcher stub, neither of which
// is worth it for a client whose primary mode is the GUI.

// attachParentProcess is ATTACH_PARENT_PROCESS, the (DWORD)-1 that tells
// AttachConsole to use the console of the parent process. Written as an
// explicit 32-bit mask rather than ^uintptr(0) because the parameter is a
// DWORD, and on a 64-bit build those differ in width even though the callee
// only reads the low half.
const attachParentProcess = uintptr(0xFFFFFFFF)

// kernel32 is always mapped into every process, so a lazy load by bare name
// carries none of the DLL search-path risk that would rule it out for a
// third-party library. AttachConsole and SetStdHandle are not exposed by the
// syscall package (GetStdHandle and CreateFile are), hence the direct binding.
var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procAttachConsole = kernel32.NewProc("AttachConsole")
	procSetStdHandle  = kernel32.NewProc("SetStdHandle")
)

// AttachParent reconnects os.Stdout, os.Stderr and os.Stdin to the console the
// process was launched from, if there was one.
//
// It is called unconditionally from main, before any output, rather than only
// when a subcommand was given (`len(os.Args) > 1`). The reasoning:
//
//   - It costs nothing in the case the flag was added for. Launched from
//     Explorer, the Start menu or a shortcut there is no parent console,
//     AttachConsole fails, and this returns having done nothing. No console
//     window appears, because nothing here ever calls AllocConsole — that, not
//     attaching, is what would put the stray window back.
//   - Gating on argv would silence the one case where a terminal user most
//     needs output. `kube-workspaces` with no arguments opens the GUI, and a
//     developer or an admin who starts it from a terminal to find out why it
//     will not start wants the startup error, the panic trace and anything the
//     runtime writes to stderr to land in that terminal. Under a GUI subsystem
//     binary with no console attached, all of it goes nowhere.
//   - Behaviour that changes with the number of arguments is a thing to
//     explain to every future reader. "Attach to the console you were started
//     from, if any" needs no explaining.
//
// It is best-effort throughout: every failure means "carry on with the streams
// we already have". Nothing here can make output worse than it already is, and
// the GUI must start even on a machine where the console API misbehaves.
func AttachParent() {
	// Snapshot the standard handles *before* attaching. AttachConsole may
	// install console handles of its own, which would erase the evidence of a
	// shell-supplied redirection that must be preserved. See HandleUsable.
	preIn := stdHandle(syscall.STD_INPUT_HANDLE)
	preOut := stdHandle(syscall.STD_OUTPUT_HANDLE)
	preErr := stdHandle(syscall.STD_ERROR_HANDLE)

	ok, _, _ := procAttachConsole.Call(attachParentProcess)
	if ok == 0 {
		// Either there is no parent console (launched from Explorer:
		// ERROR_INVALID_HANDLE) or this process already has one
		// (ERROR_ACCESS_DENIED, which is what a console subsystem build such as
		// `go test` or `go run` sees). Both mean there is nothing to do: in the
		// first case there is nowhere to write, and in the second the streams
		// already work.
		return
	}

	// Attached. Adopt the console for each stream the shell did not already
	// give us a handle for. CONOUT$ and CONIN$ name the attached console's
	// screen buffer and input buffer regardless of how the standard handles
	// are set, which is why they are opened by name rather than reusing
	// whatever AttachConsole left behind.
	if !HandleUsable(preOut) {
		if f := openConsoleStream(`CONOUT$`, syscall.STD_OUTPUT_HANDLE); f != nil {
			os.Stdout = f
		}
	}
	if !HandleUsable(preErr) {
		// A second, independent handle rather than a copy of the stdout one, so
		// that `kube-workspaces list > out.txt` still shows errors on the
		// console while stdout goes to the file.
		if f := openConsoleStream(`CONOUT$`, syscall.STD_ERROR_HANDLE); f != nil {
			os.Stderr = f
		}
	}
	if !HandleUsable(preIn) {
		if f := openConsoleStream(`CONIN$`, syscall.STD_INPUT_HANDLE); f != nil {
			os.Stdin = f
		}
	}
}

// stdHandle returns the current value of one of the process's standard
// handles, or 0 if it cannot be read. A failure and an unset handle are the
// same thing to the caller, so both collapse to a value HandleUsable rejects.
func stdHandle(id int) uintptr {
	h, err := syscall.GetStdHandle(id)
	if err != nil {
		return 0
	}
	return uintptr(h)
}

// openConsoleStream opens one of the attached console's pseudo-files (CONOUT$
// or CONIN$), installs it as the given standard handle, and wraps it in an
// *os.File ready to be assigned over os.Stdout and friends. It returns nil if
// the console cannot be opened, leaving the caller's stream untouched.
func openConsoleStream(name string, stdHandleID int) *os.File {
	path, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil
	}
	// GENERIC_READ|GENERIC_WRITE for both: a console screen buffer wants read
	// access as well as write (the console API reads back attributes and the
	// cursor position), and opening CONOUT$ write-only fails on some
	// configurations. The share flags must allow both too, since the parent
	// shell still holds the same console open.
	h, err := syscall.CreateFile(
		path,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil,
		syscall.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return nil
	}
	// Publish it process-wide as well as on the os package's variables. Code
	// that reads syscall.Stdout directly, and any child process that inherits
	// standard handles, then sees the console too. The id is a negative
	// constant (STD_OUTPUT_HANDLE is -11) passed as a DWORD, hence the trip
	// through uint32.
	_, _, _ = procSetStdHandle.Call(uintptr(uint32(stdHandleID)), uintptr(h))
	return os.NewFile(uintptr(h), name)
}
