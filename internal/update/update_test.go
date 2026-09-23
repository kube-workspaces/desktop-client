package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
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

func put(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func archiveFixture(t *testing.T, goos, arch string, extra string) []byte {
	t.Helper()
	root := "kube-workspaces-" + goos + "-" + arch + "/"
	if goos == "darwin" {
		root += "Kube Workspaces.app/Contents/MacOS/"
	}
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	files := map[string]string{root + ShellBinary + ext: "new-shell"}
	if goos != "windows" || arch != "arm64" {
		files[root+WebChildBinary+ext] = "new-child"
	}
	if extra != "" {
		files[extra] = "bad"
	}
	var b bytes.Buffer
	if goos == "windows" {
		w := zip.NewWriter(&b)
		for name, data := range files {
			f, err := w.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte(data)); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&b)
		w := tar.NewWriter(gz)
		for name, data := range files {
			if err := w.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(data)); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return b.Bytes()
}

func TestAssetNameContract(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(goos+"-"+arch, func(t *testing.T) {
				name, err := AssetName("v1.2.3", goos, arch)
				if err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				archive := filepath.Join(dir, name)
				put(t, archive, string(archiveFixture(t, goos, arch, "")))
				stage := filepath.Join(dir, "stage")
				if err := os.Mkdir(stage, 0o700); err != nil {
					t.Fatal(err)
				}
				s, err := Unpack(archive, stage)
				if err != nil {
					t.Fatal(err)
				}
				if s.HasChild != (goos != "windows" || arch != "arm64") {
					t.Fatal("wrong child capability")
				}
				if s.Windows != (goos == "windows") {
					t.Fatal("wrong executable layout")
				}
			})
		}
	}
}

func TestArchiveTraversalRejected(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		for _, evil := range []string{"../escape", "/absolute", "a/../../escape", "C:/escape", "a\\..\\escape"} {
			name, _ := AssetName("v1.2.3", goos, "amd64")
			file := filepath.Join(t.TempDir(), name)
			put(t, file, string(archiveFixture(t, goos, "amd64", evil)))
			if _, err := Unpack(file, t.TempDir()); err == nil {
				t.Fatalf("accepted %q in %s", evil, goos)
			}
		}
	}
}

func TestChecksumRejectsCorruptionAndMalformedSums(t *testing.T) {
	file := filepath.Join(t.TempDir(), "archive")
	put(t, file, "archive")
	good := fmt.Sprintf("%x  archive\n", sha256.Sum256([]byte("archive")))
	v := ChecksumVerifier{}
	if err := v.VerifyChecksum(file, []byte(good), "archive"); err != nil {
		t.Fatal(err)
	}
	for _, sums := range []string{"", "SHA256 (archive = bad", "bad archive", strings.Repeat("0", 64) + " archive"} {
		if err := v.VerifyChecksum(file, []byte(sums), "archive"); err == nil {
			t.Fatalf("accepted %q", sums)
		}
	}
}

func TestPrepareVerifiedReleaseAndCorruption(t *testing.T) {
	name, _ := AssetName("v1.2.3", "linux", "amd64")
	data := archiveFixture(t, "linux", "amd64", "")
	bad := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + name:
			_, _ = w.Write(data)
		case "/SHA256SUMS":
			if bad {
				_, _ = fmt.Fprintf(w, "%064d  %s\n", 0, name)
			} else {
				_, _ = fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(data), name)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	rel := &Release{Tag: "v1.2.3", Assets: []Asset{{Name: name, URL: srv.URL + "/" + name}, {Name: SumsName, URL: srv.URL + "/SHA256SUMS"}}}
	in := Installer{Client: srv.Client(), GOOS: "linux", GOARCH: "amd64", VersionVerify: func(path, tag string) error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(b) != "new-shell" || tag != "v1.2.3" {
			return errors.New("wrong payload")
		}
		return nil
	}}
	p, err := in.Prepare(context.Background(), rel, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	bad = true
	if _, err := in.Prepare(context.Background(), rel, nil); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("got %v", err)
	}
}

func TestApplyRollbackRestoresBothFiles(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			install, stage := t.TempDir(), t.TempDir()
			exe, child := filepath.Join(install, ShellBinary), filepath.Join(install, WebChildBinary)
			put(t, exe, "old-shell")
			put(t, child, "old-child")
			s := Staged{Shell: filepath.Join(stage, ShellBinary), WebChild: filepath.Join(stage, WebChildBinary), HasChild: true}
			put(t, s.Shell, "new-shell")
			put(t, s.WebChild, "new-child")
			verify := func(binary, _ string) error {
				if fail && binary == exe {
					return errors.New("installed check failed")
				}
				return nil
			}
			err := Apply(s, exe, "v1.2.3", verify)
			if (err != nil) != fail {
				t.Fatalf("Apply: %v", err)
			}
			prefix := "new-"
			if fail {
				prefix = "old-"
			}
			for _, item := range []struct{ p, content string }{{exe, prefix + "shell"}, {child, prefix + "child"}} {
				b, err := os.ReadFile(item.p)
				if err != nil || string(b) != item.content {
					t.Fatalf("%s = %s (%v)", item.p, b, err)
				}
			}
			if !fail {
				if _, err := os.Stat(exe + BackupSuffix); err != nil {
					t.Fatal("backup lost before confirmation")
				}
				if _, err := ConfirmPending(exe, "v1.2.3"); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(exe + BackupSuffix); !os.IsNotExist(err) {
					t.Fatal("backup retained after confirmation")
				}
			}
		})
	}
}

func TestRecoverInterruptedSwap(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, ShellBinary)
	put(t, exe, "new")
	put(t, exe+BackupSuffix, "old")
	put(t, filepath.Join(dir, journalName), `{"HadChild":false,"NewChild":true}`)
	put(t, filepath.Join(dir, WebChildBinary), "new-child")
	if err := Recover(exe); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil || string(b) != "old" {
		t.Fatalf("recovery: %s %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, WebChildBinary)); !os.IsNotExist(err) {
		t.Fatal("new-only child survived rollback")
	}
}

func TestDiscoveryFailuresAndStableSelection(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		ok     bool
	}{
		{200, `{"tag_name":"v1.2.3"}`, true},
		{200, `{"tag_name":"v1.2.3","draft":true}`, false},
		{200, `{"tag_name":"v1.2.3-rc1","prerelease":true}`, false},
		{429, `{}`, false}, {200, `not json`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := (Fetcher{APIBase: srv.URL}).GetLatest(context.Background())
		srv.Close()
		if (err == nil) != tc.ok {
			t.Fatalf("%s: %v", tc.body, err)
		}
	}
}
