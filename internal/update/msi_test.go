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
	// The installed shell proves itself by reporting the tag.
	put(t, filepath.Join(dir, ShellBinary+".exe"), "#!/bin/sh\necho v1.2.3\n")
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
		err := applyMSI(&MSIPackage{Path: "pkg.msi", Tag: "v1.2.3", Dir: dir, Work: t.TempDir()})
		if tc.wantErr == "" && err != nil {
			t.Fatalf("code %d: %v", tc.code, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("code %d: got %v, want %q", tc.code, err, tc.wantErr)
		}
	}
	execMsiexec = func([]string) error { return errors.New("no msiexec here") }
	if err := applyMSI(&MSIPackage{Path: "pkg.msi", Tag: "v1.2.3", Dir: dir, Work: t.TempDir()}); err == nil {
		t.Fatal("accepted a launch failure")
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
