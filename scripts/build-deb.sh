#!/usr/bin/env bash
# Build a Debian package (.deb) from a kube-workspaces release archive.
#
# Usage:
#   scripts/build-deb.sh <archive> <version> <outdir>
#
#   <archive> is the assembled linux release archive (from `make build-all`
#     or the assemble CI job): kube-workspaces-<version>-linux-<goarch>.tar.gz
#     (goarch amd64 or arm64), containing one kube-workspaces-linux-<arch>/
#     stage dir. If the web child (kube-workspaces-web) was injected by
#     scripts/insert-web-child.sh it is carried into the package, so
#     container workspaces keep working.
#   <version> is the release tag (v*); the leading v is stripped for the
#     Debian version.
#   <outdir>  receives kube-workspaces-<version>_<debarch>.deb plus a
#     sidecar <name>.sha256.
#
# The package is assembled with dpkg-deb from a staged directory — no
# debhelper, so it builds anywhere dpkg-deb exists. It is unsigned, like
# the MSI and AppImage: repository metadata signing lives in
# tracking/future/linux-package-repos.md.
#
# Like the MSI and AppImage, the .deb sits OUTSIDE the updater contract:
# the six-archive contract and its SHA256SUMS (verified by
# scripts/verify-update-archives.py) are untouched. The .deb gets a sidecar
# checksum instead, and installs a packaged-install marker that tells the
# client to stand its in-app updater down (dpkg owns these files now).
set -euo pipefail

archive="$(realpath "$1")"
version="$2"
outdir="$(realpath -m "$3")"

test -f "$archive" || { echo "build-deb: archive not found: $archive" >&2; exit 1; }
case "$version" in
  v*) ;;
  *) echo "build-deb: version must be a tag (v*), got $version" >&2; exit 1 ;;
esac
debver="${version#v}"
case "$debver" in
  [0-9]*)
    case "$debver" in
      *[!A-Za-z0-9.+~:-]*)
        echo "build-deb: version not Debian-safe: $debver" >&2; exit 1 ;;
    esac ;;
  *) echo "build-deb: version must start with a digit: $debver" >&2; exit 1 ;;
esac
case "$archive" in
  *-linux-amd64.tar.gz) debarch=amd64 ;;
  *-linux-arm64.tar.gz) debarch=arm64 ;;
  *) echo "build-deb: archive names a linux goarch (amd64/arm64): $archive" >&2; exit 1 ;;
esac
command -v dpkg-deb >/dev/null 2>&1 || { echo "build-deb: dpkg-deb not found (apt: dpkg)" >&2; exit 1; }
command -v desktop-file-validate >/dev/null 2>&1 || echo "build-deb: warning: desktop-file-validate not found; skipping entry validation" >&2
mkdir -p "$outdir"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

tar -xzf "$archive" -C "$work"
stage="$( (cd "$work" && ls -d kube-workspaces-*/) 2>/dev/null | head -1 | tr -d '/')"
test -n "$stage" || { echo "build-deb: no stage dir in $archive" >&2; exit 1; }

root="$(cd "$(dirname "$0")/.." && pwd)"
pkg="$work/pkg"
mkdir -p "$pkg/usr/bin" \
  "$pkg/usr/share/applications" \
  "$pkg/usr/share/icons" \
  "$pkg/usr/share/doc/kube-workspaces" \
  "$pkg/DEBIAN"

cp "$work/$stage/kube-workspaces" "$pkg/usr/bin/kube-workspaces"
chmod 755 "$pkg/usr/bin/kube-workspaces"
if test -f "$work/$stage/kube-workspaces-web"; then
  cp "$work/$stage/kube-workspaces-web" "$pkg/usr/bin/kube-workspaces-web"
  chmod 755 "$pkg/usr/bin/kube-workspaces-web"
  echo "build-deb: bundled web child kube-workspaces-web"
fi

cp "$root/packaging/linux/kube-workspaces.desktop" "$pkg/usr/share/applications/kube-workspaces.desktop"
chmod 644 "$pkg/usr/share/applications/kube-workspaces.desktop"
if command -v desktop-file-validate >/dev/null 2>&1; then
  desktop-file-validate "$pkg/usr/share/applications/kube-workspaces.desktop"
fi

cp -r "$root/packaging/linux/icons/hicolor" "$pkg/usr/share/icons/"
find "$pkg/usr/share/icons" -type d -exec chmod 755 {} +
find "$pkg/usr/share/icons" -type f -exec chmod 644 {} +

cp "$root/packaging/linux/copyright" "$pkg/usr/share/doc/kube-workspaces/copyright"
chmod 644 "$pkg/usr/share/doc/kube-workspaces/copyright"
date_rfc="$(date -u '+%a, %d %b %Y %T %z')"
{
  echo "kube-workspaces ($debver) stable; urgency=medium"
  echo
  echo "  * Release $version (see https://github.com/kube-workspaces/desktop-client/releases/tag/$version)."
  echo
  echo " -- Kube Workspaces <https://github.com/kube-workspaces/desktop-client>  $date_rfc"
} | gzip -9 -n > "$pkg/usr/share/doc/kube-workspaces/changelog.Debian.gz"
chmod 644 "$pkg/usr/share/doc/kube-workspaces/changelog.Debian.gz"

# The packaged-install marker: its presence tells the client the binary
# belongs to dpkg, so the in-app updater stands down (see
# internal/update PackagedMarkerPath).
cat > "$pkg/usr/share/doc/kube-workspaces/packaged-install" <<'EOF'
Installed by the kube-workspaces .deb package; the in-app updater stands down for system packages.
EOF
chmod 644 "$pkg/usr/share/doc/kube-workspaces/packaged-install"

installed_size="$(du -sk "$pkg/usr" | cut -f1)"
cat > "$pkg/DEBIAN/control" <<EOF
Package: kube-workspaces
Version: $debver
Section: net
Priority: optional
Architecture: $debarch
Depends: libwebkit2gtk-4.1-0
Maintainer: Kube Workspaces <https://github.com/kube-workspaces/desktop-client>
Installed-Size: $installed_size
Description: Native desktop client for kube-workspaces
 Access your kube-workspaces workspaces (VM desktops, container web apps)
 from a real VDI client: fluid resize, clipboard, audio playback.
 .
 The web view needs WebKitGTK 4.1 (Debian 12+, Ubuntu 24.04+); the shell
 itself runs anywhere the archive runs.
EOF
chmod 644 "$pkg/DEBIAN/control"

# Refresh the desktop database and icon caches; every tool call is guarded
# so a minimal container without them still installs cleanly.
cat > "$pkg/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database /usr/share/applications >/dev/null 2>&1 || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor >/dev/null 2>&1 || true
fi
EOF
chmod 755 "$pkg/DEBIAN/postinst"
cat > "$pkg/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database /usr/share/applications >/dev/null 2>&1 || true
fi
EOF
chmod 755 "$pkg/DEBIAN/prerm"

find "$pkg" -type d -exec chmod 755 {} +

deb="$outdir/kube-workspaces-${debver}_${debarch}.deb"
dpkg-deb --build "$pkg" "$deb" >&2
test -f "$deb" || { echo "build-deb: no package produced at $deb" >&2; exit 1; }
sha256sum "$deb" | awk '{print $1}' > "$deb.sha256"
echo "build-deb: wrote $deb"
echo "build-deb: sha256 $(cat "$deb.sha256") (sidecar; not part of SHA256SUMS)"
