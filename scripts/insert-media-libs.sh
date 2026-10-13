#!/usr/bin/env bash
# Inject staged Tier 1 media libraries into a shell release archive, mirroring
# scripts/insert-web-child.sh. Placement matches the loader's bundle lookup
# (internal/media bundleDirs): beside the exe (Windows/Linux) or
# Contents/Frameworks (macOS app bundle).
#
# Usage: scripts/insert-media-libs.sh <archive> <stage-target-dir>
#   <stage-target-dir> is third_party/media-libs/stage/<target> for the
#   archive's platform (windows/ or linux/lib/ or macos/Frameworks/).
#
# Idempotent; refreshes dist/SHA256SUMS like the web-child injector.
set -euo pipefail

archive="$(realpath "$1")"
staged="$(realpath "$2")"

test -f "$archive" || { echo "insert-media-libs: archive not found: $archive" >&2; exit 1; }
test -d "$staged" || { echo "insert-media-libs: stage dir not found: $staged" >&2; exit 1; }

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
    echo "insert-media-libs: unsupported archive type (want .tar.gz or .zip): $archive" >&2; exit 1
    ;;
esac

stage="$( (cd "$work" && ls -d kube-workspaces-*/) 2>/dev/null | head -1 )"
test -n "$stage" || { echo "insert-media-libs: no kube-workspaces-{os}-{arch} stage dir found in $archive" >&2; exit 1; }
os="${stage#kube-workspaces-}"
os="${os%-*}"

# License texts (NOTICE + COPYING files) are injected at the stage root beside
# the binaries in every package: attribution ships with the binaries.
inject_licenses() {
  local dest="$1" base="$2" staged="$3"
  for f in NOTICE COPYING.LGPLv2.1 COPYING.opus-BSD; do
    if [ -f "$base/$f" ]; then cp "$base/$f" "$dest/NOTICE-media-$f"; else echo "insert-media-libs: WARNING: $f not staged" >&2; fi
  done
  for f in BUILD-INFO-ffmpeg.txt BUILD-INFO-opus.txt; do
    if [ -f "$staged/$f" ]; then
      cp "$staged/$f" "$dest/"
    else
      echo "insert-media-libs: WARNING: $f not staged" >&2
    fi
  done
}

# $staged is third_party/media-libs/stage/<target>; license texts live two up.
licensed="$(dirname "$(dirname "$staged")")"
case "$os" in
  windows)
    test -d "$staged/windows" || { echo "insert-media-libs: no windows/ payload in $staged" >&2; exit 1; }
    cp "$staged"/windows/*.dll "$work/$stage"
    inject_licenses "$work/$stage" "$licensed" "$staged"
    ;;
  linux)
    test -d "$staged/linux/lib" || { echo "insert-media-libs: no linux/lib/ payload in $staged" >&2; exit 1; }
    mkdir -p "$work/$stage/lib"
    cp -d "$staged"/linux/lib/*.so* "$work/$stage/lib/"
    inject_licenses "$work/$stage" "$licensed" "$staged"
    ;;
  darwin|macos)
    test -d "$staged/macos/Frameworks" || { echo "insert-media-libs: no macos/Frameworks/ payload in $staged" >&2; exit 1; }
    app="$(find "$work/$stage" -maxdepth 1 -name "*.app" | head -1)"
    if [ -n "$app" ]; then
      mkdir -p "$app/Contents/Frameworks"
      cp "$staged"/macos/Frameworks/*.dylib "$app/Contents/Frameworks/"
      inject_licenses "$work/$stage" "$licensed" "$staged"
    else
      mkdir -p "$work/$stage/Frameworks"
      cp "$staged"/macos/Frameworks/*.dylib "$work/$stage/Frameworks/"
      inject_licenses "$work/$stage" "$licensed" "$staged"
    fi
    ;;
  *)
    echo "insert-media-libs: unknown os $os" >&2; exit 1
    ;;
esac

case "$archive" in
  *.zip)
    (cd "$work" && zip -qr "$archive" "$stage")
    ;;
  *.tar.gz)
    (cd "$work" && tar -czf "$archive" "$stage")
    ;;
esac

sums="$(dirname "$archive")/SHA256SUMS"
if [ -f "$sums" ]; then
  (cd "$(dirname "$archive")" && sha256sum "${archive##*/}" > "$sums.tmp" 2>/dev/null || true)
  if [ -f "$sums.tmp" ]; then
    grep -v " ${archive##*/}\$" "$sums" > "$sums.new" || true
    cat "$sums.tmp" >> "$sums.new"
    mv "$sums.new" "$sums"
    rm -f "$sums.tmp"
  fi
fi
echo "insert-media-libs: $archive <- $staged"
