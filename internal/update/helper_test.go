// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var (
	errSwapFailed   = errors.New("update: install failed, previous version restored: rename: access denied")
	errCacheFailure = errors.New("update: earlier failure kept in the cache")
)

// isolateCache points [CacheDir] at a private directory so the update-error
// marker cannot leak between tests. The three names cover the three platform
// spellings of the user cache root.
func isolateCache(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, k := range []string{"XDG_CACHE_HOME", "LocalAppData", "HOME"} {
		t.Setenv(k, root)
	}
	cache, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

// fakeExe writes an executable stand-in for the running binary, so
// [LayoutForExe] resolves a directory the way it resolves a real install.
func fakeExe(t *testing.T, dir string) string {
	t.Helper()
	exe := filepath.Join(dir, ShellBinary+".exe")
	if err := os.WriteFile(exe, []byte("not really a shell"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestTakeErrorReportsAndConsumesTheMarker(t *testing.T) {
	isolateCache(t)
	dir := t.TempDir()
	exe := fakeExe(t, dir)

	recordUpdateError(dir, errSwapFailed)
	if got := TakeError(exe); got != errSwapFailed.Error() {
		t.Fatalf("TakeError = %q, want %q", got, errSwapFailed.Error())
	}
	// Once only: the notice is not replayed on every launch.
	if got := TakeError(exe); got != "" {
		t.Fatalf("TakeError replayed the marker: %q", got)
	}
}

// TestRecordUpdateErrorFallsBackToTheCache is the per-machine MSI case. The
// helper is an ordinary user process, so a Program Files install directory is
// not writable to it and the marker must not be lost with it: a failed update
// that cannot report itself on the next launch is indistinguishable from one
// that never ran.
//
// "Cannot write there" is simulated with a path that does not exist rather
// than with a directory mode, because a mode is not enforced on Windows —
// chmod only toggles the read-only attribute, so a 0500 directory stays
// writable there and the fallback would never be exercised. A missing
// directory fails the same write on every platform, and the fallback only
// cares that the first write returned an error. It also means the test is
// still meaningful as root, which ignores directory permissions entirely.
func TestRecordUpdateErrorFallsBackToTheCache(t *testing.T) {
	cache := isolateCache(t)
	dir := filepath.Join(t.TempDir(), "Program Files", "Kube Workspaces")

	recordUpdateError(dir, errSwapFailed)

	// Guard against a vacuous pass: the install dir must really have refused
	// the marker, or the cache copy would prove nothing.
	if _, err := os.Stat(filepath.Join(dir, errorMarker)); err == nil {
		t.Fatal("the marker reached the install dir; the fallback was not exercised")
	}
	if b, err := os.ReadFile(filepath.Join(cache, errorMarker)); err != nil {
		t.Fatalf("no marker in the cache fallback: %v", err)
	} else if string(b) != errSwapFailed.Error() {
		t.Fatalf("cached marker = %q, want %q", b, errSwapFailed.Error())
	}
}

// TestTakeErrorPrefersTheInstallDir pins the order: a marker beside the
// binaries is the one that describes this install, and a stale cached failure
// must not shadow it.
func TestTakeErrorPrefersTheInstallDir(t *testing.T) {
	cache := isolateCache(t)
	dir := t.TempDir()
	exe := fakeExe(t, dir)

	// Seed the fallback first; the install dir must still win.
	if err := os.WriteFile(filepath.Join(cache, errorMarker), []byte(errCacheFailure.Error()), 0o600); err != nil {
		t.Fatal(err)
	}
	recordUpdateError(dir, errSwapFailed)

	if got := TakeError(exe); got != errSwapFailed.Error() {
		t.Fatalf("TakeError = %q, want the install-dir marker %q", got, errSwapFailed)
	}
	// With the install-dir marker consumed, the cached one is next.
	if got := TakeError(exe); got != errCacheFailure.Error() {
		t.Fatalf("TakeError = %q, want the cached marker %q", got, errCacheFailure)
	}
	if got := TakeError(exe); got != "" {
		t.Fatalf("TakeError = %q, want both markers consumed", got)
	}
}

func TestTakeErrorIgnoresANonExe(t *testing.T) {
	isolateCache(t)
	if got := TakeError(filepath.Join(t.TempDir(), "nowhere", ShellBinary+".exe")); got != "" {
		t.Fatalf("TakeError = %q for an unresolvable path", got)
	}
}

func TestRecordUpdateErrorIgnoresNil(t *testing.T) {
	cache := isolateCache(t)
	recordUpdateError(t.TempDir(), nil)
	if _, err := os.Stat(filepath.Join(cache, errorMarker)); !os.IsNotExist(err) {
		t.Fatalf("a nil error left a marker behind: %v", err)
	}
}

// TestErrorMarkerNamesTheFailure is a cheap guard on the message the user
// actually reads: it must carry the installer's own words, not a placeholder.
func TestErrorMarkerNamesTheFailure(t *testing.T) {
	isolateCache(t)
	dir := t.TempDir()
	recordUpdateError(dir, errSwapFailed)
	b, err := os.ReadFile(filepath.Join(dir, errorMarker))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "access denied") {
		t.Fatalf("marker %q does not carry the failure", b)
	}
}

// TestHelperProbe is the relaunch target for
// TestRunMSIHelperRelaunchesAfterFailure, in the standard helper-process
// pattern: with KW_UPDATE_PROBE set it records the launch and exits,
// otherwise it returns immediately so the suite is unaffected.
func TestHelperProbe(t *testing.T) {
	marker := os.Getenv("KW_UPDATE_PROBE")
	if marker == "" {
		return
	}
	if err := os.WriteFile(marker, []byte("ran"), 0o600); err != nil {
		t.Fatalf("probe: %v", err)
	}
}

// deadPid returns a process ID that is already gone, so waitParent returns
// at once instead of watching a live process: the test binary itself, run
// once with the probe unset and reaped.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProbe")
	env := []string{"PATH=" + os.Getenv("PATH")}
	if runtime.GOOS == "windows" {
		env = append(env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"))
	}
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		t.Fatalf("probe run: %v", err)
	}
	return cmd.Process.Pid
}

// TestRunMSIHelperRelaunchesAfterFailure checks that a failed MSI install
// still hands the user a shell: msiexec is transactional, so the previous
// build is in place exactly as left. Seen live: the helper recorded the
// error and exited, and nothing was running after "restart to update".
func TestRunMSIHelperRelaunchesAfterFailure(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	ran := filepath.Join(dir, "relaunched")
	t.Setenv("KW_UPDATE_PROBE", ran)
	old := execMsiexec
	defer func() { execMsiexec = old }()
	execMsiexec = func([]string) error { return &msiExitError{Code: 1603} }
	h := handoff{
		MSI:    &msiHandoff{Path: filepath.Join(work, "pkg.msi"), Tag: "v0.8.0", Dir: dir, Work: work},
		Exe:    os.Args[0],
		Parent: deadPid(t),
		Args:   []string{"-test.run=TestHelperProbe"},
	}
	err := runMSIHelper(h)
	if err == nil || !strings.Contains(err.Error(), "exit code 1603") {
		t.Fatalf("got %v, want the install failure", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, statErr := os.Stat(ran); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed install did not relaunch the client")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if msg := TakeError(os.Args[0]); !strings.Contains(msg, "1603") {
		t.Fatalf("failure marker = %q, want the install failure", msg)
	}
}

// writeRecorderScript stages an executable shell script that records its
// argv to marker and exits, so spawn paths are asserted without running
// real binaries.
func writeRecorderScript(t *testing.T, path, marker string) {
	t.Helper()
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + marker + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestElevationErrorMessage(t *testing.T) {
	if err := (&elevationError{Code: 1223}).Error(); !strings.Contains(err, "declined") {
		t.Fatalf("1223 reported %q, want the declined message", err)
	}
	if err := (&elevationError{Code: 5}).Error(); !strings.Contains(err, "5") {
		t.Fatalf("code 5 reported %q, want the code", err)
	}
}

func TestElevatedDoneRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := writeElevatedDone(dir, 0); err != nil {
		t.Fatal(err)
	}
	if code, err := waitElevatedDone(dir, 5*time.Second); err != nil || code != 0 {
		t.Fatalf("got %d, %v, want 0, nil", code, err)
	}
	if err := writeElevatedDone(dir, 7); err != nil {
		t.Fatal(err)
	}
	if code, err := waitElevatedDone(dir, 5*time.Second); err != nil || code != 7 {
		t.Fatalf("got %d, %v, want 7, nil", code, err)
	}
	if _, err := waitElevatedDone(t.TempDir(), 100*time.Millisecond); err == nil {
		t.Fatal("missing done file did not time out")
	}
}

func TestPeekUpdateErrorKeepsTheMarker(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, errorMarker)
	if err := os.WriteFile(p, []byte("boom"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := peekUpdateError(dir); got != "boom" {
		t.Fatalf("peek = %q, want boom", got)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("peek consumed the marker")
	}
}

// elevatedHandoff writes a machine-scope handoff job file and returns the
// job path plus the handoff.
func elevatedHandoff(t *testing.T, dir, exe string, args []string) (string, handoff) {
	t.Helper()
	h := handoff{
		MSI:    &msiHandoff{Path: filepath.Join(dir, "pkg.msi"), Tag: "v9.9.9", Dir: dir, Machine: true, Work: dir},
		Exe:    exe,
		Parent: deadPid(t),
		Args:   args,
	}
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	job := filepath.Join(dir, "handoff.json")
	if err := os.WriteFile(job, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return job, h
}

func TestRunElevatedMSIFailure(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "probe")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := execMsiexec
	defer func() { execMsiexec = old }()
	execMsiexec = func([]string) error { return &msiExitError{Code: 1603} }
	job, _ := elevatedHandoff(t, dir, exe, []string{"shell"})
	if err := RunElevatedMSI(job); err == nil || !strings.Contains(err.Error(), "1603") {
		t.Fatalf("got %v, want the install failure", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, elevatedDoneFile)); err != nil || strings.TrimSpace(string(b)) != "1" {
		t.Fatalf("done = %q, %v, want 1", b, err)
	}
	if msg := takeErrorMarker(dir); !strings.Contains(msg, "1603") {
		t.Fatalf("marker = %q, want the install failure", msg)
	}
}

func TestRunElevatedMSISuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell binary is a shell script; verify on linux/darwin")
	}
	dir := t.TempDir()
	shell := filepath.Join(dir, ShellBinary+".exe")
	writeRecorderScript(t, shell, filepath.Join(dir, "version-args"))
	// The recorder records argv; make it report the tag like `version` would.
	if err := os.WriteFile(shell, []byte("#!/bin/sh\necho v9.9.9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := execMsiexec
	defer func() { execMsiexec = old }()
	execMsiexec = func([]string) error { return nil }
	job, _ := elevatedHandoff(t, dir, filepath.Join(dir, "probe"), nil)
	if err := RunElevatedMSI(job); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, elevatedDoneFile)); err != nil || strings.TrimSpace(string(b)) != "0" {
		t.Fatalf("done = %q, %v, want 0", b, err)
	}
	if msg := takeErrorMarker(dir); msg != "" {
		t.Fatalf("success recorded a marker: %q", msg)
	}
}

func TestRunElevatedMSINoMSI(t *testing.T) {
	dir := t.TempDir()
	job := filepath.Join(dir, "handoff.json")
	data, err := json.Marshal(handoff{Exe: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(job, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunElevatedMSI(job); err == nil {
		t.Fatal("accepted a handoff without an installer")
	}
}

func TestRunMachineMSIHelperSuccess(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	ran := filepath.Join(dir, "relaunched")
	t.Setenv("KW_UPDATE_PROBE", ran)
	if err := writeElevatedDone(work, 0); err != nil {
		t.Fatal(err)
	}
	h := handoff{
		MSI:    &msiHandoff{Path: "pkg.msi", Tag: "v9.9.9", Dir: dir, Machine: true, Work: work},
		Exe:    os.Args[0],
		Parent: deadPid(t),
		Args:   []string{"-test.run=TestHelperProbe"},
	}
	if err := runMachineMSIHelper(h, &MSIPackage{Path: "pkg.msi", Tag: "v9.9.9", Dir: dir, Work: work}); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	waitForRelaunch(t, ran)
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatal("successful machine update kept the stage")
	}
}

func TestRunMachineMSIHelperFailure(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	ran := filepath.Join(dir, "relaunched")
	t.Setenv("KW_UPDATE_PROBE", ran)
	if err := writeElevatedDone(work, 3); err != nil {
		t.Fatal(err)
	}
	h := handoff{
		MSI:    &msiHandoff{Path: "pkg.msi", Tag: "v9.9.9", Dir: dir, Machine: true, Work: work},
		Exe:    os.Args[0],
		Parent: deadPid(t),
		Args:   []string{"-test.run=TestHelperProbe"},
	}
	err := runMachineMSIHelper(h, &MSIPackage{Path: "pkg.msi", Tag: "v9.9.9", Dir: dir, Work: work})
	if err == nil {
		t.Fatal("accepted a failed elevated install")
	}
	waitForRelaunch(t, ran)
}

// waitForRelaunch waits for a probe child to record its launch: process
// startup takes longer than any assertion may assume.
func waitForRelaunch(t *testing.T, ran string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ran); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("relaunched client never recorded its launch")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunMachineMSIHelperTimeout(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	old := elevatedWaitTimeout
	elevatedWaitTimeout = 100 * time.Millisecond
	defer func() { elevatedWaitTimeout = old }()
	probe := filepath.Join(dir, "probe-missing")
	h := handoff{
		MSI:    &msiHandoff{Path: "pkg.msi", Tag: "v9.9.9", Dir: dir, Machine: true, Work: work},
		Exe:    probe,
		Parent: deadPid(t),
		Args:   []string{"shell"},
	}
	err := runMachineMSIHelper(h, &MSIPackage{Path: "pkg.msi", Tag: "v9.9.9", Dir: dir, Work: work})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v, want the timeout", err)
	}
	if msg := takeErrorMarker(dir); !strings.Contains(msg, "timed out") {
		t.Fatalf("timeout marker = %q", msg)
	}
}

func TestStartMSIHelperMachineScope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recorder exe is a shell script; verify on linux/darwin")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "shell-stub")
	marker := filepath.Join(dir, "spawned")
	writeRecorderScript(t, exe, marker)
	var elevated []string
	old := elevateHelper
	defer func() { elevateHelper = old }()
	elevateHelper = func(_ string, args []string) error {
		elevated = append([]string(nil), args...)
		return nil
	}
	pkg := &MSIPackage{Path: "pkg.msi", Tag: "v9.9.9", Dir: dir, MachineScope: true, Work: dir}
	if err := StartMSIHelper(pkg, exe, []string{"shell"}); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	if len(elevated) != 2 || elevated[0] != "update-helper-runmsi" {
		t.Fatalf("elevated leg got %q, want [update-helper-runmsi job]", elevated)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(marker); err == nil && len(b) > 0 {
			if got := strings.Fields(string(b)); len(got) != 2 || got[0] != "update-helper" {
				t.Fatalf("waiter got %q, want [update-helper job]", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unelevated waiter never launched")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartMSIHelperMachineScopeDeclined(t *testing.T) {
	dir := t.TempDir()
	old := elevateHelper
	defer func() { elevateHelper = old }()
	elevateHelper = func(string, []string) error { return &elevationError{Code: 1223} }
	pkg := &MSIPackage{Path: "pkg.msi", Tag: "v9.9.9", Dir: dir, MachineScope: true, Work: dir}
	err := StartMSIHelper(pkg, os.Args[0], []string{"shell"})
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("got %v, want the declined message", err)
	}
	// Nothing may be left behind for a start that never happened.
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("declined elevation left %v behind", names)
	}
}
