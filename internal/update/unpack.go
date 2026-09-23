// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ShellBinary and WebChildBinary are the file names the updater looks for
// inside a release archive's stage dir (with .exe on Windows archives).
const (
	ShellBinary    = "kube-workspaces"
	WebChildBinary = "kube-workspaces-web"
)

// Staged is an unpacked release archive, ready for [Apply].
type Staged struct {
	// Dir is the stage dir the archive held (kube-workspaces-<os>-<arch>/).
	Dir string
	// Shell is the staged shell binary.
	Shell string
	// WebChild is the staged webview child, or "" on shell-only targets
	// (windows/arm64 ships no child).
	WebChild string
	// HasChild reports whether the archive carried the webview child.
	HasChild bool
	// Windows reports whether the staged binaries are .exe (i.e. the archive
	// targets Windows). It decides filename conventions and exec-bit
	// handling, not the host's own GOOS: unpacking is generic.
	Windows bool
}

// Unpack extracts archivePath (a .tar.gz or .zip release archive) under
// destDir and locates the staged binaries. The archive must hold exactly one
// top-level kube-workspaces-<os>-<arch>/ stage dir; anything else is a
// malformed release and unpacking aborts before touching the install.
func Unpack(archivePath, destDir string) (Staged, error) {
	var err error
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz"):
		err = unpackTarGz(archivePath, destDir)
	case strings.HasSuffix(archivePath, ".zip"):
		err = unpackZip(archivePath, destDir)
	default:
		return Staged{}, fmt.Errorf("update: unsupported archive type %q (want .tar.gz or .zip)", archivePath)
	}
	if err != nil {
		return Staged{}, err
	}
	return findStaged(destDir)
}

// unpackTarGz extracts a .tar.gz with zip-slip protection: absolute names,
// ".." escapes and symlinks/hardlinks are refused rather than followed.
func unpackTarGz(archivePath, destDir string) error {
	fh, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("update: open archive: %w", err)
	}
	defer func() { _ = fh.Close() }()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return fmt.Errorf("update: gunzip archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	var total int64
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("update: read archive: %w", err)
		}
		count++
		total += hdr.Size
		if count > 1024 || hdr.Size > 256<<20 || total > 1<<30 {
			return fmt.Errorf("update: archive extraction limit exceeded")
		}
		if _, err := safeJoin(destDir, hdr.Name); err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			return fmt.Errorf("update: archive contains a link %q", hdr.Name)
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		dst, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("update: create stage dir: %w", err)
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("update: create stage file: %w", err)
		}
		_, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil {
			return fmt.Errorf("update: extract %q: %w", hdr.Name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("update: write %q: %w", hdr.Name, closeErr)
		}
	}
}

// unpackZip extracts a .zip with the same traversal protection.
func unpackZip(archivePath, destDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("update: open archive: %w", err)
	}
	defer func() { _ = zr.Close() }()
	var total uint64
	if len(zr.File) > 1024 {
		return fmt.Errorf("update: too many archive entries")
	}
	for _, f := range zr.File {
		if _, err := safeJoin(destDir, f.Name); err != nil {
			return err
		}
		if f.UncompressedSize64 > 256<<20 {
			return fmt.Errorf("update: archive entry too large")
		}
		total += f.UncompressedSize64
		if total > 1<<30 {
			return fmt.Errorf("update: archive extraction limit exceeded")
		}
		if f.Mode()&os.ModeType != 0 && !f.FileInfo().IsDir() {
			return fmt.Errorf("update: archive contains a special file %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		dst, err := safeJoin(destDir, f.Name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("update: create stage dir: %w", err)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("update: read %q: %w", f.Name, err)
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			_ = rc.Close()
			return fmt.Errorf("update: create stage file: %w", err)
		}
		_, copyErr := io.Copy(out, rc)
		_ = rc.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return fmt.Errorf("update: extract %q: %w", f.Name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("update: write %q: %w", f.Name, closeErr)
		}
	}
	return nil
}

// safeJoin resolves an archive member name under destDir, refusing absolute
// paths and escapes above destDir.
func safeJoin(destDir, name string) (string, error) {
	clean := path.Clean(name)
	if strings.ContainsAny(name, "\\:\x00") || path.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("update: invalid archive member %q", name)
	}
	dst := filepath.Join(destDir, clean)
	rel, err := filepath.Rel(destDir, dst)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("update: archive member %q escapes its directory", name)
	}
	return dst, nil
}

// findStaged locates the single stage dir under destDir and the binaries in
// it. Binaries are made executable (non-Windows archives): zip/tar modes
// survive unevenly across writers, and a staged binary that cannot run fails
// its version self-check confusingly later.
func findStaged(destDir string) (Staged, error) {
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return Staged{}, fmt.Errorf("update: list stage: %w", err)
	}
	var stage string
	if len(entries) != 1 {
		return Staged{}, fmt.Errorf("update: archive must have exactly one stage directory")
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "kube-workspaces-") {
			continue
		}
		if stage != "" {
			return Staged{}, fmt.Errorf("update: archive holds more than one stage dir")
		}
		stage = filepath.Join(destDir, e.Name())
	}
	if stage == "" {
		return Staged{}, fmt.Errorf("update: archive holds no kube-workspaces-<os>-<arch> stage dir")
	}
	var st Staged
	st.Dir = stage
	binDir := stage
	if strings.Contains(filepath.Base(stage), "-darwin-") {
		binDir = filepath.Join(stage, "Kube Workspaces.app", "Contents", "MacOS")
	}
	shell := filepath.Join(binDir, ShellBinary)
	if _, err := os.Stat(shell); err != nil {
		exe := shell + ".exe"
		if _, serr := os.Stat(exe); serr != nil {
			return Staged{}, fmt.Errorf("update: stage holds no shell binary")
		}
		st.Windows = true
		st.Shell = exe
	} else {
		st.Shell = shell
	}
	if fi, err := os.Lstat(st.Shell); err != nil || !fi.Mode().IsRegular() {
		return Staged{}, fmt.Errorf("update: shell is not a regular file")
	}
	child := filepath.Join(binDir, WebChildBinary)
	if st.Windows {
		child += ".exe"
	}
	if fi, err := os.Stat(child); err == nil && !fi.IsDir() {
		st.WebChild, st.HasChild = child, true
	}
	if !st.Windows {
		for _, p := range []string{st.Shell, st.WebChild} {
			if p == "" {
				continue
			}
			if err := os.Chmod(p, 0o755); err != nil {
				return Staged{}, fmt.Errorf("update: chmod staged binary: %w", err)
			}
		}
	}
	return st, nil
}
