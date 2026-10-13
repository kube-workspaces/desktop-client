#!/usr/bin/env bash
# Build the minimal LGPL FFmpeg 5.1 shared libraries our client binds:
# libavcodec 59 (H.264 decoder only) + libavutil 57.
#
# Usage: third_party/media-libs/build-ffmpeg.sh <target> [dl] [out]
#   target: linux-amd64 | linux-arm64 | win-amd64 | win-arm64 | mac-amd64 | mac-arm64
#
# LICENSE SAFETY (read before touching the flags): this build must stay LGPL.
# --disable-gpl/--disable-nonfree/--disable-version3 are passed explicitly so a
# future flag edit cannot silently flip the build to GPL. Decoder-only means no
# x264/x265: our client only *decodes* H.264. The full configure line is
# recorded in out/<target>/BUILD-INFO.txt as the compliance record.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
target="${1:?target required (linux-amd64|linux-arm64|win-amd64|win-arm64|mac-amd64|mac-arm64)}"
dl="${2:-$here/dl}"
out="${3:-$here/out}"
src="$out/src-ffmpeg"
prefix="$out/$target/ffmpeg"

"$here/fetch.sh" "$dl"
rm -rf "$src" "$prefix"
mkdir -p "$src" "$prefix"
tar -xf "$dl"/ffmpeg-*.tar.xz -C "$src" --strip-components=1

# Decoder-only, LGPL, no programs/docs/network. Every --disable narrows the
# surface (and the binary); every --enable is something our client binds.
flags=(
  --disable-gpl --disable-nonfree --disable-version3
  --disable-everything
  --disable-programs --disable-doc
  --disable-avdevice --disable-avformat --disable-avfilter
  --disable-swscale --disable-swresample --disable-postproc
  --disable-network --disable-debug
  --disable-bzlib --disable-lzma --disable-zlib
  # Software decode only: no VA-API/VDPAU/D3D11VA/DXVA2/Vulkan linkage, so the
  # libraries load on machines without any GPU stack installed.
  --disable-hwaccels
  --disable-vaapi --disable-vdpau --disable-d3d11va --disable-dxva2 --disable-vulkan
  --enable-shared --disable-static --enable-pic
  --enable-avcodec --enable-avutil
  --enable-decoder=h264 --enable-parser=h264
)

case "$target" in
  linux-amd64) cc="gcc" ;;
  linux-arm64)
    command -v aarch64-linux-gnu-gcc >/dev/null || { echo "build-ffmpeg: missing aarch64-linux-gnu-gcc" >&2; exit 1; }
    flags+=(--arch=aarch64 --cross-prefix=aarch64-linux-gnu- --target-os=linux)
    cc="aarch64-linux-gnu-gcc" ;;
  win-amd64)
    command -v x86_64-w64-mingw32-gcc >/dev/null || { echo "build-ffmpeg: missing x86_64-w64-mingw32-gcc" >&2; exit 1; }
    flags+=(--arch=x86_64 --cross-prefix=x86_64-w64-mingw32- --target-os=mingw64)
    cc="x86_64-w64-mingw32-gcc" ;;
  win-arm64)
    command -v aarch64-w64-mingw32-gcc >/dev/null || { echo "build-ffmpeg: missing aarch64-w64-mingw32-gcc (woa64 needs an explicit toolchain)" >&2; exit 1; }
    flags+=(--arch=aarch64 --cross-prefix=aarch64-w64-mingw32- --target-os=mingw64)
    cc="aarch64-w64-mingw32-gcc" ;;
  mac-amd64|mac-arm64)
    arch="${target#mac-}"
    command -v clang >/dev/null || { echo "build-ffmpeg: missing clang" >&2; exit 1; }
    [ "$(uname)" = "Darwin" ] || { echo "build-ffmpeg: mac targets need a macOS host" >&2; exit 1; }
    flags+=(--arch="$arch" --target-os=darwin --cc="clang -arch $arch")
    # @loader_path lets libavcodec find its sibling libavutil in Frameworks.
    flags+=(--extra-ldflags="-Wl,-rpath,@loader_path")
    cc="clang" ;;
  *) echo "build-ffmpeg: unknown target $target" >&2; exit 1 ;;
esac

# x86 assembly needs nasm/yasm; without it fall back to C (slower decode) with
# a loud warning. Release/CI builds must install nasm instead of accepting this:
# set MEDIA_LIBS_REQUIRE_ASM=1 to turn the fallback into an error.
case "$target" in
  linux-amd64|win-amd64|mac-amd64)
    if ! command -v nasm >/dev/null && ! command -v yasm >/dev/null; then
      if [ "${MEDIA_LIBS_REQUIRE_ASM:-0}" = "1" ]; then
        echo "build-ffmpeg: nasm/yasm required (MEDIA_LIBS_REQUIRE_ASM=1)" >&2; exit 1
      fi
      echo "build-ffmpeg: WARNING: no nasm/yasm — building --disable-asm (install nasm for release builds)"
      flags+=(--disable-asm)
    fi ;;
esac

(
  cd "$src"
  # shellcheck disable=SC2086
  ./configure --cc="$cc" --prefix="$prefix" "${flags[@]}" | tail -3
  make -j"$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)"
  make install
)

{
  echo "source: ffmpeg 5.1.9 (https://ffmpeg.org/releases/ffmpeg-5.1.9.tar.xz)"
  echo "target: $target"
  echo "license: LGPL-2.1+ (configured --disable-gpl --disable-nonfree --disable-version3)"
  echo "configure: ./configure --cc=$cc --prefix=<prefix> ${flags[*]}"
} > "$prefix/BUILD-INFO.txt"
echo "build-ffmpeg: $target installed to $prefix"
