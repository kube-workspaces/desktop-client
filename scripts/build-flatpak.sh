#!/usr/bin/env bash
# Build a Flatpak bundle (.flatpak) from a kube-workspaces release archive.
#
# Usage:
#   scripts/build-flatpak.sh <archive> <version> <outdir>
#
#   <archive> is the assembled linux release archive (from `make build-all`
#     or the assemble CI job): kube-workspaces-<version>-linux-<goarch>.tar.gz
#     (goarch amd64 or arm64), containing one kube-workspaces-linux-<arch>/
#     stage dir. If the web child (kube-workspaces-web) was injected by
#     scripts/insert-web-child.sh it is carried into the bundle, so
#     container workspaces keep working.
#   <version> is the release tag (v*), used in the bundle file name.
#   <outdir>  receives io.github.kube-workspaces.KubeWorkspaces-<version>.flatpak
#     plus a sidecar <name>.sha256.
#
# flatpak-builder does the build, so it and the org.gnome Platform/Sdk 48
# runtimes must exist (Flathub remote; the CI job installs them). The
# modules only copy host-built binaries in — no toolchain runs in the
# sandbox — the same "build outside, pack inside" split as the web-child
# matrix. The bundle is unsigned, like the MSI and AppImage: Flathub
# submission and signing live in tracking/future/linux-package-repos.md.
#
# Like the MSI and AppImage, the bundle sits OUTSIDE the updater contract:
# the six-archive contract and its SHA256SUMS (verified by
# scripts/verify-update-archives.py) are untouched. The bundle gets a
# sidecar checksum instead, and the manifest disables the in-app updater
# (KUBE_WORKSPACES_NO_UPDATE) since flatpak owns these files now.
#
# Quirk the manifest relies on: flatpak-builder extracts `dir` sources by
# content, so the `icons` source lands as hicolor/... in the build root
# and the icon loop reads that path, not icons/hicolor/...
set -euo pipefail

archive="$(realpath "$1")"
version="$2"
outdir="$(realpath -m "$3")"

test -f "$archive" || { echo "build-flatpak: archive not found: $archive" >&2; exit 1; }
case "$version" in
  v*) ;;
  *) echo "build-flatpak: version must be a tag (v*), got $version" >&2; exit 1 ;;
esac
case "$archive" in
  *-linux-amd64.tar.gz) flatarch=x86_64 ;;
  *-linux-arm64.tar.gz) flatarch=aarch64 ;;
  *) echo "build-flatpak: archive names a linux goarch (amd64/arm64): $archive" >&2; exit 1 ;;
esac
command -v flatpak-builder >/dev/null 2>&1 || { echo "build-flatpak: flatpak-builder not found" >&2; exit 1; }
command -v flatpak >/dev/null 2>&1 || { echo "build-flatpak: flatpak not found" >&2; exit 1; }
mkdir -p "$outdir"

appid="io.github.kubeworkspaces.KubeWorkspaces"

work="$(mktemp -d)"
# The builder mounts scratch filesystems under state/ that can still be
# busy when it exits; a failing cleanup must not fail the build.
trap 'rm -rf "$work" 2>/dev/null || true' EXIT

tar -xzf "$archive" -C "$work"
stage="$( (cd "$work" && ls -d kube-workspaces-*/) 2>/dev/null | head -1 | tr -d '/')"
test -n "$stage" || { echo "build-flatpak: no stage dir in $archive" >&2; exit 1; }

root="$(cd "$(dirname "$0")/.." && pwd)"
src="$work/src"
mkdir -p "$src"
cp "$work/$stage/kube-workspaces" "$src/kube-workspaces"
if test -f "$work/$stage/kube-workspaces-web"; then
  cp "$work/$stage/kube-workspaces-web" "$src/kube-workspaces-web"
  printf '%s\n' '        "install -Dm755 kube-workspaces-web /app/bin/kube-workspaces-web",' > "$work/webinstall"
  printf '%s\n' '        {' '          "type": "file",' '          "path": "kube-workspaces-web"' '        },' > "$work/websource"
  echo "build-flatpak: bundled web child kube-workspaces-web"
else
  : > "$work/webinstall"
  : > "$work/websource"
  echo "build-flatpak: archive carries no web child; packaging shell only" >&2
fi
# The desktop entry and icon carry the app ID inside the sandbox, per the
# Flatpak convention; the Exec stays the on-PATH binary name.
sed 's/^Icon=kube-workspaces$/Icon=io.github.kubeworkspaces.KubeWorkspaces/' \
  "$root/packaging/linux/kube-workspaces.desktop" > "$src/$appid.desktop"
cp "$root/packaging/linux/flatpak/$appid.metainfo.xml" "$src/"
cp -r "$root/packaging/linux/icons" "$src/"
sed -e "/@@WEBCHILD_INSTALL@@/r $work/webinstall" \
    -e "/@@WEBCHILD_INSTALL@@/d" \
    -e "/@@WEBCHILD_SOURCE@@/r $work/websource" \
    -e "/@@WEBCHILD_SOURCE@@/d" \
  "$root/packaging/linux/flatpak/$appid.json.in" > "$src/$appid.json"
if command -v python3 >/dev/null 2>&1; then
  python3 -c "import json,sys; json.load(open(sys.argv[1]))" "$src/$appid.json" || { echo "build-flatpak: generated manifest is not valid JSON" >&2; exit 1; }
fi

flatpak-builder --force-clean --state-dir="$work/state" --repo="$work/repo" --arch="$flatarch" "$work/build" "$src/$appid.json" >&2
bundle="$outdir/$appid-$version.flatpak"
flatpak build-bundle "$work/repo" "$bundle" "$appid" --arch="$flatarch" >&2
test -f "$bundle" || { echo "build-flatpak: no bundle produced at $bundle" >&2; exit 1; }
sha256sum "$bundle" | awk '{print $1}' > "$bundle.sha256"
echo "build-flatpak: wrote $bundle"
echo "build-flatpak: sha256 $(cat "$bundle.sha256") (sidecar; not part of SHA256SUMS)"
