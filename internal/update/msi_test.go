// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMSIAssetNameContract(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		name, err := MSIAssetName("v1.2.3", arch)
		if err != nil {
			t.Fatal(err)
		}
		if want := "kube-workspaces-v1.2.3-windows-" + arch + ".msi"; name != want {
			t.Fatalf("got %q, want %q", name, want)
		}
	}
	for _, arch := range []string{"386", "", "x64"} {
		if _, err := MSIAssetName("v1.2.3", arch); err == nil {
			t.Fatalf("accepted arch %q", arch)
		}
	}
	if _, err := MSIAssetName("v1.2.3-4-gabc", "amd64"); err == nil {
		t.Fatal("named an MSI for a dev tag")
	}
}

func TestMSIAssetFor(t *testing.T) {
	rel := &Release{Tag: "v1.2.3", Assets: []Asset{
		{Name: "kube-workspaces-v1.2.3-windows-amd64.msi", URL: "https://example.com/amd64.msi"},
		{Name: "kube-workspaces-v1.2.3-windows-amd64.zip", URL: "https://example.com/amd64.zip"},
	}}
	a, err := rel.MSIAssetFor("windows", "amd64")
	if err != nil || a.Name != "kube-workspaces-v1.2.3-windows-amd64.msi" {
		t.Fatalf("got %+v, %v", a, err)
	}
	if _, err := rel.MSIAssetFor("windows", "arm64"); err == nil {
		t.Fatal("resolved a missing arm64 MSI")
	}
	if _, err := rel.MSIAssetFor("linux", "amd64"); err == nil {
		t.Fatal("resolved an MSI for linux")
	}
	if _, err := (*Release)(nil).MSIAssetFor("windows", "amd64"); err == nil {
		t.Fatal("resolved an MSI for a nil release")
	}
}

func TestMatchMsiCandidate(t *testing.T) {
	cands := []msiCandidate{
		{ProductCode: "{AAA}", InstallLocation: `C:\Users\you\AppData\Local\Programs\Kube Workspaces`, DisplayVersion: "0.4.1"},
		{ProductCode: "{BBB}", InstallLocation: "", DisplayVersion: "0.0.0"},
	}
	m := matchMsiCandidate(cands, `c:\users\YOU\appdata\local\programs\kube workspaces\`)
	if m == nil || m.ProductCode != "{AAA}" {
		t.Fatalf("no match: %+v", m)
	}
	if matchMsiCandidate(cands, `C:\unrelated\place`) != nil {
		t.Fatal("matched an unrelated directory")
	}
	if matchMsiCandidate(nil, `C:\x`) != nil {
		t.Fatal("matched with no candidates")
	}
}

func TestLegacyMsiDir(t *testing.T) {
	lad := `C:\Users\you\AppData\Local`
	pf := `C:\Program Files`
	for _, dir := range []string{
		`C:\Users\you\AppData\Local\Programs\Kube Workspaces`,
		`c:\program files\kube workspaces\`,
	} {
		if !legacyMsiDir(lad, pf, dir) {
			t.Fatalf("rejected canonical dir %q", dir)
		}
	}
	for _, dir := range []string{`C:\Users\you\Downloads`, `D:\tools\kw`, ""} {
		if legacyMsiDir(lad, pf, dir) {
			t.Fatalf("accepted non-canonical dir %q", dir)
		}
	}
}

func TestInferMachineScope(t *testing.T) {
	t.Setenv("ProgramFiles", `C:\Program Files`)
	t.Setenv("ProgramFiles(x86)", `C:\Program Files (x86)`)
	if !inferMachineScope(`C:\Program Files\Kube Workspaces`) {
		t.Fatal("Program Files not machine-scoped")
	}
	if !inferMachineScope(`c:\program files (x86)\kube workspaces\`) {
		t.Fatal("x86 Program Files not machine-scoped")
	}
	if inferMachineScope(`C:\Users\you\AppData\Local\Programs\Kube Workspaces`) {
		t.Fatal("user profile counted as machine-scoped")
	}
	// A lookalike prefix without a separator boundary must not match.
	if inferMachineScope(`C:\Program Files22\Kube Workspaces`) {
		t.Fatal("prefix lookalike counted as machine-scoped")
	}
}

func TestMsiArgs(t *testing.T) {
	user := msiArgs(`C:\pkg\new.msi`, `C:\Users\you\AppData\Local\Programs\Kube Workspaces`, false, `C:\tmp\msi-update.log`)
	wantUser := []string{"/i", `C:\pkg\new.msi`, "/qn", "/norestart", "/l*v", `C:\tmp\msi-update.log`,
		"MSIINSTALLPERUSER=1", `INSTALLDIR=C:\Users\you\AppData\Local\Programs\Kube Workspaces`}
	if strings.Join(user, "\x00") != strings.Join(wantUser, "\x00") {
		t.Fatalf("user args = %q", user)
	}
	machine := msiArgs(`C:\pkg\new.msi`, `C:\Program Files\Kube Workspaces`, true, `C:\tmp\msi-update.log`)
	if machine[6] != "ALLUSERS=1" || machine[7] != `INSTALLDIR=C:\Program Files\Kube Workspaces` {
		t.Fatalf("machine args = %q", machine)
	}
}

func TestApplyMSIExitCodes(t *testing.T) {
	dir := t.TempDir()
	// Version verification is injected: executing a fixture binary is not
	// portable (a shell script is not runnable as .exe on Windows).
	verify := func(binary, tag string) error {
		if binary != filepath.Join(dir, ShellBinary+".exe") || tag != "v1.2.3" {
			return errors.New("wrong proof target")
		}
		return nil
	}
	old := execMsiexec
	defer func() { execMsiexec = old }()
	for _, tc := range []struct {
		code    int
		wantErr string
	}{
		{0, ""},
		{3010, ""},
		{1641, ""},
		{1602, "cancelled"},
		{1603, "exit code 1603"},
		{1925, "elevated permissions"},
	} {
		execMsiexec = func(args []string) error {
			if args[0] != "/i" || args[2] != "/qn" {
				t.Fatalf("unexpected msiexec args %q", args)
			}
			if tc.code == 0 {
				return nil
			}
			return &msiExitError{Code: tc.code}
		}
		err := applyMSI(&MSIPackage{Path: "pkg.msi", Tag: "v1.2.3", Dir: dir, Work: t.TempDir()}, verify)
		if tc.wantErr == "" && err != nil {
			t.Fatalf("code %d: %v", tc.code, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("code %d: got %v, want %q", tc.code, err, tc.wantErr)
		}
	}
	// 1925 is Windows Installer saying the administrative rights really were
	// not available, which is the one elevation story this flow has: it must
	// be the actionable error, not a bare exit code.
	execMsiexec = func([]string) error { return &msiExitError{Code: 1925} }
	if err := applyMSI(&MSIPackage{Path: "pkg.msi", Tag: "v1.2.3", Dir: dir, Work: t.TempDir()}, verify); !errors.Is(err, ErrNeedsElevation) {
		t.Fatalf("1925 reported %v, want ErrNeedsElevation", err)
	}
	execMsiexec = func([]string) error { return errors.New("no msiexec here") }
	if err := applyMSI(&MSIPackage{Path: "pkg.msi", Tag: "v1.2.3", Dir: dir, Work: t.TempDir()}, verify); err == nil {
		t.Fatal("accepted a launch failure")
	}
	verifyFail := func(string, string) error { return errors.New("staged binary reports \"v0.0.0\"") }
	execMsiexec = func([]string) error { return nil }
	if err := applyMSI(&MSIPackage{Path: "pkg.msi", Tag: "v1.2.3", Dir: dir, Work: t.TempDir()}, verifyFail); err == nil {
		t.Fatal("accepted a failed version proof")
	}
}

// msiReleaseFixture serves an .msi plus SHA256SUMS for PrepareMSI tests.
func msiReleaseFixture(t *testing.T, tag, arch string, msi []byte) (*httptest.Server, *Release) {
	t.Helper()
	name, err := MSIAssetName(tag, arch)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + name:
			_, _ = w.Write(msi)
		case "/SHA256SUMS":
			_, _ = fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(msi), name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	rel := &Release{Tag: tag, Assets: []Asset{
		{Name: name, URL: srv.URL + "/" + name},
		{Name: SumsName, URL: srv.URL + "/SHA256SUMS"},
	}}
	return srv, rel
}

func TestPrepareMSIDownloadsAndVerifies(t *testing.T) {
	srv, rel := msiReleaseFixture(t, "v1.2.3", "amd64", []byte("fake-msi-bytes"))
	dir := t.TempDir()
	in := Installer{
		Client: srv.Client(), GOOS: "windows", GOARCH: "amd64",
		MSIDetect: func(string) (*MsiInstall, error) {
			return &MsiInstall{ProductCode: "{AAA}", Dir: dir, Version: "v1.2.2"}, nil
		},
	}
	pkg, err := in.PrepareMSI(context.Background(), rel, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pkg.Close()
	b, err := os.ReadFile(pkg.Path)
	if err != nil || string(b) != "fake-msi-bytes" {
		t.Fatalf("staged MSI = %q, %v", b, err)
	}
	if pkg.Tag != "v1.2.3" || pkg.Dir != dir || pkg.MachineScope {
		t.Fatalf("package = %+v", pkg)
	}
}

func TestPrepareMSINotMSI(t *testing.T) {
	_, rel := msiReleaseFixture(t, "v1.2.3", "amd64", []byte("fake-msi-bytes"))
	in := Installer{
		GOOS: "windows", GOARCH: "amd64",
		MSIDetect: func(string) (*MsiInstall, error) { return nil, nil },
	}
	if _, err := in.PrepareMSI(context.Background(), rel, nil); !errors.Is(err, ErrNotMSI) {
		t.Fatalf("got %v", err)
	}
	// Non-Windows platforms never take the MSI path, even when a detector
	// claims an install.
	in.GOOS = "linux"
	in.MSIDetect = func(string) (*MsiInstall, error) {
		return &MsiInstall{Dir: t.TempDir()}, nil
	}
	if _, err := in.PrepareMSI(context.Background(), rel, nil); !errors.Is(err, ErrNotMSI) {
		t.Fatalf("got %v", err)
	}
}

// TestPrepareMSINotADirectoryIsAnErrorNotAPanic is the v0.5.2 Windows
// crash's shape: a bad recorded install location must be reported, never
// dereferenced. A file stands in for the bad location so the check fails on
// every platform and user (ENOTDIR needs no permission setup and root cannot
// dodge it).
func TestPrepareMSINotADirectoryIsAnErrorNotAPanic(t *testing.T) {
	_, rel := msiReleaseFixture(t, "v1.2.3", "amd64", []byte("fake-msi-bytes"))
	notDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := Installer{
		GOOS: "windows", GOARCH: "amd64",
		MSIDetect: func(string) (*MsiInstall, error) {
			return &MsiInstall{Dir: notDir}, nil
		},
	}
	_, err := in.PrepareMSI(context.Background(), rel, nil)
	if err == nil {
		t.Fatal("prepared into a location that is not a directory")
	}
	if errors.Is(err, ErrNotMSI) {
		t.Fatalf("got %v, want the bad-location failure, not ErrNotMSI", err)
	}
	if !strings.Contains(err.Error(), notDir) {
		t.Fatalf("error %q does not name the install dir", err)
	}
}

// TestPrepareMSIDoesNotWriteInstallDir pins the contract that replaced the
// writability probe: the MSI flow stages in the user cache and writes nothing
// at all into the install location, so a per-machine install in Program Files
// is updatable from an ordinary user process. Windows Installer does the
// writing (as LocalSystem, raising its own UAC prompt), so probing this
// process's own access to the directory rejects exactly the installs that work
// — the v0.6.2 bug, which failed every "All users" install at download time
// with an error the user could not act on.
func TestPrepareMSIDoesNotWriteInstallDir(t *testing.T) {
	srv, rel := msiReleaseFixture(t, "v1.2.3", "amd64", []byte("fake-msi-bytes"))
	dir := t.TempDir()
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	in := Installer{
		Client: srv.Client(), GOOS: "windows", GOARCH: "amd64",
		MSIDetect: func(string) (*MsiInstall, error) {
			return &MsiInstall{Dir: dir, MachineScope: true}, nil
		},
	}
	pkg, err := in.PrepareMSI(context.Background(), rel, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pkg.Close()
	if pkg.MachineScope != true {
		t.Fatalf("per-machine scope not carried through: %+v", pkg)
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("PrepareMSI wrote into the install dir: %d entries, want %d", len(after), len(before))
	}
	if fi, err := os.Stat(pkg.Path); err != nil || fi.IsDir() {
		t.Fatalf("staged package %q is not a file: %v", pkg.Path, err)
	}
	if filepath.Dir(pkg.Path) == dir {
		t.Fatal("staged the package inside the install dir instead of the cache")
	}
}

// TestPrepareMSIPerMachineInstallIsNotBlocked is the regression proper: a
// per-machine install directory this process cannot write must still prepare.
//
// "Unwritable" is the load-bearing half, and it needs a directory mode the
// platform enforces. Windows does not: chmod there only toggles the read-only
// attribute, so a 0500 directory stays writable and the assertion would pass
// vacuously — a green test that proved nothing. So that half is skipped on
// Windows and runs on Linux and macOS, which the native CI matrix covers. The
// portable half runs everywhere and pins the rest of the contract: machine
// scope survives preparation and nothing is left behind in the install
// directory.
func TestPrepareMSIPerMachineInstallIsNotBlocked(t *testing.T) {
	prepare := func(t *testing.T, dir string) {
		t.Helper()
		srv, rel := msiReleaseFixture(t, "v1.2.3", "amd64", []byte("fake-msi-bytes"))
		in := Installer{
			Client: srv.Client(), GOOS: "windows", GOARCH: "amd64",
			MSIDetect: func(string) (*MsiInstall, error) {
				return &MsiInstall{Dir: dir, MachineScope: true}, nil
			},
		}
		pkg, err := in.PrepareMSI(context.Background(), rel, nil)
		if err != nil {
			t.Fatalf("a per-machine install must still prepare, got %v", err)
		}
		defer pkg.Close()
		if !pkg.MachineScope {
			t.Fatalf("per-machine scope not carried through: %+v", pkg)
		}
	}

	t.Run("leaves the install dir untouched", func(t *testing.T) {
		dir := t.TempDir()
		before, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		prepare(t, dir)
		after, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Fatalf("PrepareMSI wrote into the install dir: %d entries, want %d", len(after), len(before))
		}
	})

	t.Run("install dir is not writable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		if runtime.GOOS == "windows" {
			t.Skip("windows does not enforce directory modes")
		}
		dir := filepath.Join(t.TempDir(), "Program Files", "Kube Workspaces")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		// Restrict only the leaf: a read-only parent would block creating the
		// child at all, and the point is the install directory itself.
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		// Confirm the restriction really bites, and leave nothing behind: a
		// probe file still open here would fail the TempDir cleanup later.
		if f, err := os.CreateTemp(dir, "probe"); err == nil {
			name := f.Name()
			_ = f.Close()
			_ = os.Remove(name)
			t.Skip("directory is writable after all")
		}
		prepare(t, dir)
	})
}

func TestPrepareMSIMissingAsset(t *testing.T) {
	in := Installer{
		GOOS: "windows", GOARCH: "amd64",
		MSIDetect: func(string) (*MsiInstall, error) {
			return &MsiInstall{Dir: t.TempDir()}, nil
		},
	}
	rel := &Release{Tag: "v1.2.3"}
	if _, err := in.PrepareMSI(context.Background(), rel, nil); err == nil {
		t.Fatal("prepared without an MSI asset")
	}
}

func TestDetectMSIViaInstaller(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "kube-workspaces.exe")
	in := Installer{
		ExePath: exe,
		MSIDetect: func(got string) (*MsiInstall, error) {
			if got != exe {
				t.Fatalf("detector saw %q", got)
			}
			return &MsiInstall{Dir: t.TempDir(), MachineScope: true}, nil
		},
	}
	m, err := in.DetectMSI()
	if err != nil || m == nil || !m.MachineScope {
		t.Fatalf("got %+v, %v", m, err)
	}
}

// TestApplyMSIAdminBlock checks that a 1603 caused by MSI error 1730 (a
// per-machine change attempted without administrator rights, as a silent
// unattended upgrade of older per-machine products produces) surfaces as
// actionable elevation guidance naming the staged package — not a bare
// exit code. Seen live: five stale per-machine registrations, every
// nested removal failing 1730, outer install rolling back to 1603.
func TestApplyMSIAdminBlock(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	log := filepath.Join(work, "msi-update.log")
	fail1730 := "Product: Kube Workspaces -- Error 1730. You must be an Administrator to remove this application.\nAction ended 13:31:17: INSTALL. Return value 3.\n"
	old := execMsiexec
	defer func() { execMsiexec = old }()
	execMsiexec = func([]string) error { return &msiExitError{Code: 1603} }

	// Plain-text log.
	if err := os.WriteFile(log, []byte(fail1730), 0o600); err != nil {
		t.Fatal(err)
	}
	err := applyMSI(&MSIPackage{Path: "pkg.msi", Tag: "v1.2.3", Dir: dir, Work: work}, func(string, string) error { return nil })
	if !errors.Is(err, ErrNeedsElevation) {
		t.Fatalf("1730 reported %v, want ErrNeedsElevation", err)
	}
	if !strings.Contains(err.Error(), "pkg.msi") || !strings.Contains(err.Error(), "manually") {
		t.Fatalf("1730 error %q does not name the staged package to run manually", err)
	}

	// UTF-16 log, as msiexec writes it: NUL-interleaved ASCII must still match.
	var wide []byte
	for _, c := range []byte(fail1730) {
		wide = append(wide, c, 0)
	}
	if err := os.WriteFile(log, wide, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyMSI(&MSIPackage{Path: "pkg.msi", Tag: "v1.2.3", Dir: dir, Work: work}, func(string, string) error { return nil }); !errors.Is(err, ErrNeedsElevation) {
		t.Fatalf("UTF-16 1730 reported %v, want ErrNeedsElevation", err)
	}

	// A 1603 without 1730 keeps the old bare message.
	if err := os.WriteFile(log, []byte("Action ended 13:31:17: INSTALL. Return value 3.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = applyMSI(&MSIPackage{Path: "pkg.msi", Tag: "v1.2.3", Dir: dir, Work: work}, func(string, string) error { return nil })
	if errors.Is(err, ErrNeedsElevation) || !strings.Contains(err.Error(), "exit code 1603") {
		t.Fatalf("plain 1603 reported %v, want the bare exit-code message", err)
	}
}

func TestLogShowsAdminBlock(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.log")
	if logShowsAdminBlock(missing) {
		t.Fatal("missing log reads as an admin block")
	}
	empty := filepath.Join(dir, "empty.log")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if logShowsAdminBlock(empty) {
		t.Fatal("empty log reads as an admin block")
	}
	plain := filepath.Join(dir, "plain.log")
	if err := os.WriteFile(plain, []byte("Error 1730. You must be an Administrator"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !logShowsAdminBlock(plain) {
		t.Fatal("plain 1730 not detected")
	}
	other := filepath.Join(dir, "other.log")
	if err := os.WriteFile(other, []byte("Return value 3"), 0o600); err != nil {
		t.Fatal(err)
	}
	if logShowsAdminBlock(other) {
		t.Fatal("unrelated failure reads as an admin block")
	}
}
