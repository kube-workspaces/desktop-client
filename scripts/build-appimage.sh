#!/usr/bin/env bash
# Build a Linux x86_64 AppImage from a kube-workspaces release archive.
#
# Usage:
#   scripts/build-appimage.sh <archive> <version> <outdir>
#
#   <archive> is the assembled linux/amd64 release archive (from `make build-all`
#     or the assemble CI job): kube-workspaces-<version>-linux-amd64.tar.gz,
#     containing one kube-workspaces-linux-amd64/ stage dir. If the web child
#     (kube-workspaces-web) was injected by scripts/insert-web-child.sh it is
#     carried into the AppImage, so container workspaces keep working.
#   <version> is the release tag (v*), used in the AppImage file name.
#   <outdir>  receives kube-workspaces-<version>-linux-amd64.AppImage plus the
#     sidecar <name>.sha256.
#
# The AppImage is assembled here rather than in the Makefile for one reason:
# it needs appimagetool, a binary, and the whole tree is plain go. The pinned
# tool (AppImage/appimagetool v1.9.1) is fetched on first use into
# $XDG_CACHE_HOME/kube-workspaces, and mksquashfs + desktop-file-validate
# (squashfs-tools, desktop-file-utils) must exist on PATH. The CI assemble job
# installs those two packages before running this.
#
# The AppImage sits OUTSIDE the updater contract: the six-archive contract and
# its SHA256SUMS (verified by scripts/verify-update-archives.py) are untouched.
# The AppImage gets a sidecar checksum instead, listed in the release notes.
set -euo pipefail

archive="$(realpath "$1")"
version="$2"
outdir="$(realpath -m "$3")"

test -f "$archive" || { echo "build-appimage: archive not found: $archive" >&2; exit 1; }
case "$version" in
  v*) ;;
  *) echo "build-appimage: version must be a tag (v*), got $version" >&2; exit 1 ;;
esac
mkdir -p "$outdir"

command -v mksquashfs >/dev/null 2>&1 || { echo "build-appimage: mksquashfs not found (apt: squashfs-tools)" >&2; exit 1; }
command -v desktop-file-validate >/dev/null 2>&1 || { echo "build-appimage: desktop-file-validate not found (apt: desktop-file-utils)" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

tar -xzf "$archive" -C "$work"
stage="$( (cd "$work" && ls -d kube-workspaces-*/) 2>/dev/null | head -1 | tr -d '/')"
test -n "$stage" || { echo "build-appimage: no stage dir in $archive" >&2; exit 1; }

appdir="$work/AppDir"
mkdir -p "$appdir/usr/bin" \
  "$appdir/usr/share/icons/hicolor/512x512/apps"

cp "$work/$stage/kube-workspaces" "$appdir/usr/bin/kube-workspaces"
chmod +x "$appdir/usr/bin/kube-workspaces"
if test -f "$work/$stage/kube-workspaces-web"; then
  cp "$work/$stage/kube-workspaces-web" "$appdir/usr/bin/kube-workspaces-web"
  chmod +x "$appdir/usr/bin/kube-workspaces-web"
  echo "build-appimage: bundled web child kube-workspaces-web"
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
cp "$root/README.md" "$root/LICENSE" "$appdir/usr/share/"

# One master icon everywhere the AppImage spec looks: the desktop entry's Icon,
# the hicolor tree, and the root copy appimagetool embeds in the header.
cp "$root/assets/icon.png" "$appdir/usr/share/icons/hicolor/512x512/apps/kube-workspaces.png"
cp "$root/assets/icon.png" "$appdir/kube-workspaces.png"
cp "$root/assets/icon.png" "$appdir/.DirIcon"

# The desktop file goes at the AppDir root: that is where current appimagetool
# looks for it (a deeper usr/share/applications copy is not found), and the
# tool relocates a copy into the packaged filesystem.
cat > "$appdir/kube-workspaces.desktop" <<'EOF'
[Desktop Entry]
Type=Application
Name=Kube Workspaces
GenericName=Remote desktop client
Comment=Access your kube-workspaces workspaces
Exec=kube-workspaces
Icon=kube-workspaces
Terminal=false
Categories=Network;RemoteAccess;
StartupWMClass=kube-workspaces
EOF

cat > "$appdir/AppRun" <<'EOF'
#!/bin/sh
# AppImage entry point. The shell's spawnWeb looks for the web child beside
# itself, and for a mounted AppImage that path is usr/bin; putting usr/bin on
# PATH keeps kube-workspaces-web discoverable exactly as the unpacked archives
# behave.
HERE="$(dirname "$(readlink -f "$0")")"
export PATH="$HERE/usr/bin:$PATH"
exec "$HERE/usr/bin/kube-workspaces" "$@"
EOF
chmod +x "$appdir/AppRun"

# Fetch appimagetool (pinned) on first use; a caller-supplied binary wins.
if test -n "${APPIMAGETOOL:-}" && test -x "$APPIMAGETOOL"; then
  tool="$APPIMAGETOOL"
else
  cache="${XDG_CACHE_HOME:-$HOME/.cache}/kube-workspaces"
  tool="$cache/appimagetool-1.9.1-x86_64.AppImage"
  if ! test -f "$tool"; then
    mkdir -p "$(dirname "$tool")"
    echo "build-appimage: fetching pinned appimagetool 1.9.1" >&2
    curl -fL --retry 3 -o "$tool" \
      https://github.com/AppImage/appimagetool/releases/download/1.9.1/appimagetool-x86_64.AppImage
    chmod +x "$tool"
    echo "$tool" >&2
  fi
fi

# The tool itself is an AppImage; on runners/containers without FUSE use the
# runtime's extract-and-run path (both the env var and the flag cover the
# runtime flavours that look for one or the other).
export APPIMAGE_EXTRACT_AND_RUN=1
option=--appimage-extract-and-run
if ! "$tool" "$option" --version >/dev/null 2>&1; then
  option=
fi

appimage="$outdir/kube-workspaces-$version-linux-amd64.AppImage"
if ! (cd "$appdir" && ARCH=x86_64 "$tool" $option --no-appstream \
     "$appdir" "$appimage" 2>&1); then
  echo "build-appimage: appimagetool failed; see above" >&2
  exit 1
fi

test -f "$appimage" || { echo "build-appimage: no AppImage produced at $appimage" >&2; exit 1; }
sha256sum "$appimage" | awk '{print $1}' > "$appimage.sha256"
echo "build-appimage: wrote $appimage"
echo "build-appimage: sha256 $(cat "$appimage.sha256") (sidecar; not part of SHA256SUMS)"