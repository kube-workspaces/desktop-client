#!/usr/bin/env bash
# Build shared Opus (BSD-3-Clause, bundling-safe with attribution) for one target.
# Usage: third_party/media-libs/build-opus.sh <target> [dl] [out]
#   (same target set as build-ffmpeg.sh)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
target="${1:?target required}"
dl="${2:-$here/dl}"
out="${3:-$here/out}"
src="$out/src-opus"
prefix="$out/$target/opus"

"$here/fetch.sh" "$dl"
rm -rf "$src" "$prefix"
mkdir -p "$src" "$prefix"
tar -xf "$dl"/opus-*.tar.gz -C "$src" --strip-components=1

flags=(--disable-static --enable-shared --disable-doc --disable-extra-programs)
case "$target" in
  linux-amd64) ;;
  linux-arm64)
    command -v aarch64-linux-gnu-gcc >/dev/null || { echo "build-opus: missing aarch64-linux-gnu-gcc" >&2; exit 1; }
    flags+=(--host=aarch64-linux-gnu) ;;
  win-amd64)
    command -v x86_64-w64-mingw32-gcc >/dev/null || { echo "build-opus: missing x86_64-w64-mingw32-gcc" >&2; exit 1; }
    flags+=(--host=x86_64-w64-mingw32)
    # Debian's mingw headers redirect to __memset_chk under _FORTIFY_SOURCE,
    # which mingw cannot link: build this library unhardened (it decodes
    # bounded audio frames behind our own size caps; the exe keeps its guards).
    export CFLAGS="${CFLAGS:-} -U_FORTIFY_SOURCE -D_FORTIFY_SOURCE=0" ;;
  win-arm64)
    command -v aarch64-w64-mingw32-gcc >/dev/null || { echo "build-opus: missing aarch64-w64-mingw32-gcc" >&2; exit 1; }
    flags+=(--host=aarch64-w64-mingw32)
    export CFLAGS="${CFLAGS:-} -U_FORTIFY_SOURCE -D_FORTIFY_SOURCE=0" ;;
  mac-amd64|mac-arm64)
    [ "$(uname)" = "Darwin" ] || { echo "build-opus: mac targets need a macOS host" >&2; exit 1; }
    arch="${target#mac-}"
    export CC="clang -arch $arch" ;;
  *) echo "build-opus: unknown target $target" >&2; exit 1 ;;
esac

(
  cd "$src"
  ./configure --prefix="$prefix" "${flags[@]}" | tail -2
  make -j"$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)"
  make install
)

{
  echo "source: opus 1.6.1 (https://downloads.xiph.org/releases/opus/opus-1.6.1.tar.gz)"
  echo "target: $target"
  echo "license: BSD-3-Clause (see COPYING in source; attribution shipped in NOTICE)"
  echo "configure: ./configure --prefix=<prefix> ${flags[*]}"
} > "$prefix/BUILD-INFO.txt"
echo "build-opus: $target installed to $prefix"
