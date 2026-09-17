//go:build !windows

package console

// AttachParent does nothing off Windows.
//
// Every other platform this client ships on hands a process working standard
// streams whether it was started from a terminal, a .desktop launcher, Finder
// or launchd — there is no subsystem flag, no loader-allocated console, and so
// nothing to repair. The Windows implementation and the reason it exists are in
// attach_windows.go.
//
// This stub exists so that main can call it without a build tag of its own.
func AttachParent() {}
