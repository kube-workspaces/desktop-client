#!/usr/bin/env bash
# Drop the embedded-webview child binary (cmd/kube-workspaces-web, built per OS
# with CGO_ENABLED=1) into a shell release archive so spawnWeb's sibling lookup
# (webChildPath in internal/shell/actions.go) finds it next to the executable.
#
# Usage:
#   scripts/insert-web-child.sh <archive> <child>
#
# <archive> is a dist/ archive from `make build-all` (a kube-workspaces-{os}-{arch}
# .tar.gz or .zip containing a single kube-workspaces-{os}-{arch}/ stage dir).
# <child>   is the CGO_ENABLED=1 build of ./cmd/kube-workspaces-web for the same
#           platform (kube-workspaces-web, or kube-workspaces-web.exe on Windows).
#
# The child is placed where spawnWeb looks for it: beside the shell for Linux and
# Windows, inside the mac app bundle for macOS. The archive is repacked in place
# (same name, same top-level stage dir) and, if dist/SHA256SUMS sits next to it,
# the checksum file is refreshed so release uploads stay coherent. Idempotent:
# re-running with the same child is a no-op relative to run order.
set -euo pipefail

archive="$(realpath "$1")"
child="$(realpath "$2")"

test -f "$archive" || { echo "insert-web-child: archive not found: $archive" >&2; exit 1; }
test -f "$child" || { echo "insert-web-child: child not found: $child" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

case "$archive" in
  *.zip)
    unzip -q "$archive" -d "$work"
    ;;
  *.tar.gz)
    tar -xzf "$archive" -C "$work"
    ;;
  *)
    echo "insert-web-child: unsupported archive type (want .tar.gz or .zip): $archive" >&2
    exit 1
    ;;
esac

# The archive's one top-level entry is the stage dir (kube-workspaces-{os}-{arch});
# its name tells us which platform the child targets.
stage="$( (cd "$work" && ls -d kube-workspaces-*/) 2>/dev/null | head -1 )"
test -n "$stage" || { echo "insert-web-child: no kube-workspaces-{os}-{arch} stage dir found in $archive" >&2; exit 1; }
os="${stage#kube-workspaces-}"
os="${os%-*}"

case "$os" in
  linux|windows)
    dest="$work/$stage/$(basename "$child")"
    ;;
  darwin)
    dest="$work/$stage/Kube Workspaces.app/Contents/MacOS/$(basename "$child")"
    ;;
  *)
    echo "insert-web-child: cannot place child for os \"$os\" in $archive" >&2
    exit 1
    ;;
esac

cp "$child" "$dest"
chmod +x "$dest"
echo "insert-web-child: $os :: $(basename "$dest") -> $archive"

# Repack in place. Rebuilding with the same members keeps permissions/mtime of
# everything else intact; zip stores absolute-free paths via the stage-dir --cd.
tmp="$archive.tmp-$$"
case "$archive" in
  *.zip)
    (cd "$work" && zip -qr "$tmp" "$stage")
    ;;
  *.tar.gz)
    (cd "$work" && tar -czf "$tmp" "$stage")
    ;;
esac
mv -f "$tmp" "$archive"

# Refresh the checksum file if one lives beside the archive, so uploads that
# include SHA256SUMS (the build/release jobs) never ship stale hashes.
sums="$(dirname "$archive")/SHA256SUMS"
if test -f "$sums"; then
  (cd "$(dirname "$archive")" && sha256sum ./*.tar.gz ./*.zip > "$sums")
  echo "insert-web-child: refreshed $(dirname "$archive")/SHA256SUMS"
fi