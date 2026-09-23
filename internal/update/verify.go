// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// SumsName is the checksum file every release publishes.
const SumsName = "SHA256SUMS"

// ErrChecksumMismatch means the downloaded archive does not match the
// release's SHA256SUMS entry: the download is corrupt or tampered with, and
// must never be installed.
var ErrChecksumMismatch = errors.New("update: archive checksum does not match SHA256SUMS")

// ErrChecksumMissing means SHA256SUMS has no entry for the archive: the
// release is malformed, and installing would mean trusting an unlisted file.
var ErrChecksumMissing = errors.New("update: SHA256SUMS has no entry for the archive")

// ErrSignatureNotConfigured is what the signature step returns until code
// signing is funded. It is a sentinel rather than a silent skip so the
// install flow cannot accidentally treat "not verified" as "verified": the
// caller decides that unsigned releases are acceptable today, in one place.
var ErrSignatureNotConfigured = errors.New("update: no signature verifier configured")

// Verifier checks a staged archive before it may be installed. The two steps
// are separate so signature verification plugs in beside the checksum when
// releases start being signed, without restructuring the install flow.
type Verifier interface {
	// VerifyChecksum compares the file at path against the SHA256SUMS data
	// for assetName.
	VerifyChecksum(path string, sums []byte, assetName string) error
	// VerifySignature verifies the release signature over path. Until a
	// verifier is configured it returns [ErrSignatureNotConfigured].
	VerifySignature(path string, rel *Release) error
}

// ChecksumVerifier is the v1 [Verifier]: SHA256SUMS over TLS, no signatures.
type ChecksumVerifier struct{}

// VerifyChecksum implements [Verifier].
func (ChecksumVerifier) VerifyChecksum(path string, sums []byte, assetName string) error {
	want, err := sumsEntry(sums, assetName)
	if err != nil {
		return err
	}
	fh, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("update: open download for checksum: %w", err)
	}
	defer func() { _ = fh.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return fmt.Errorf("update: checksum read: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%w: got %s, want %s", ErrChecksumMismatch, got, want)
	}
	return nil
}

// VerifySignature implements [Verifier]. Unsigned releases are the norm
// until code signing is funded, so this always reports not-configured and
// the install flow explicitly accepts that one sentinel.
func (ChecksumVerifier) VerifySignature(string, *Release) error {
	return ErrSignatureNotConfigured
}

// sumsEntry finds the expected hex digest for assetName in SHA256SUMS data.
// Lines are "<hex>[ *]<filename>"; BSD-style "SHA256 (file) = hex" is also
// accepted because coreutils is not the only checksum writer.
func sumsEntry(sums []byte, assetName string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var hash, name string
		if i := strings.Index(line, "="); i >= 0 && strings.Contains(line[:i], "(") {
			// BSD: SHA256 (name) = hex
			start, end := strings.Index(line, "("), strings.Index(line, ")")
			if end <= start || end >= i {
				return "", fmt.Errorf("update: malformed checksum line")
			}
			inner := line[start+1 : end]
			name = strings.TrimSpace(inner)
			hash = strings.TrimSpace(line[i+1:])
		} else {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			hash, name = fields[0], strings.TrimPrefix(fields[1], "*")
		}
		if name != assetName && name != filepathBase(assetName) {
			continue
		}
		if len(hash) != 64 || !isHex(hash) {
			return "", fmt.Errorf("update: SHA256SUMS entry for %q is malformed", assetName)
		}
		return strings.ToLower(hash), nil
	}
	return "", fmt.Errorf("%w: %q", ErrChecksumMissing, assetName)
}

func filepathBase(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
