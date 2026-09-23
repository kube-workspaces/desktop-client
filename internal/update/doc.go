// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package update is the client's binary-level self-updater: it discovers new
// desktop-client releases on GitHub Releases, verifies the archive for this
// platform against the release's SHA256SUMS, and swaps the installed binaries
// in place.
//
// The updater depends on the release asset contract locked in by
// .github/workflows/build.yml — archive names
// kube-workspaces-<tag>-<os>-<arch>.tar.gz (.zip on Windows), each holding a
// single kube-workspaces-<os>-<arch>/ stage dir, plus a regenerated
// SHA256SUMS. [AssetName] is that contract in code, and
// TestAssetNameContract pins it; do not rename the workflow's artifacts
// without updating the client, or every installed updater breaks silently.
//
// Verification is a two-step interface ([Verifier]) on purpose:
// [ChecksumVerifier] checks SHA256SUMS today, and signature verification
// plugs in as the second step when code signing is funded (see the tracking
// plan's §7) without restructuring the install flow. Until then the trust
// anchor is TLS to github.com plus the checksum file.
//
// The package is pure Go, with no UI imports — the same
// testability rule as internal/rfb and internal/kwclient. Platform specifics
// live behind OS-specific files (using the existing x/sys dependency for
// macOS attributes and Windows process handles); everything else is shared. Tests run offline against
// httptest fixtures and synthetic archives, never against GitHub.
package update
