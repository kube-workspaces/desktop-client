#!/usr/bin/env bash
# Normalize built media libraries into a staging tree mirroring package layout:
#   stage/<target>/windows/*.dll        (beside the exe)
#   stage/<target>/linux/lib/*.so*     (tarball lib/ dir)
#   stage/<target>/macos/Frameworks/*.dylib
# plus BUILD-INFO.txt files and license texts (see ../NOTICE).
# Usage: third_party/media-libs/stage.sh [out] [stage]
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
out="${1:-$here/out}"
stage="${2:-$here/stage}"

have() { test -e "$1" || { echo "stage: missing $1 (build it first)" >&2; exit 1; }; }

for target in linux-amd64 linux-arm64 win-amd64 win-arm64 mac-amd64 mac-arm64; do
  [ -d "$out/$target" ] || continue
  case "$target" in
    win-*)
      d="$stage/$target/windows"; mkdir -p "$d"
      for f in "$out/$target"/ffmpeg/bin/avcodec-59.dll "$out/$target"/ffmpeg/bin/avutil-57.dll; do
        have "$f"; cp "$f" "$d/"
      done
      if [ -f "$out/$target/opus/bin/opus.dll" ]; then
        cp "$out/$target/opus/bin/opus.dll" "$d/"
      elif [ -f "$out/$target/opus/bin/libopus-0.dll" ]; then
        cp "$out/$target/opus/bin/libopus-0.dll" "$d/"
      else
        echo "stage: missing opus dll for $target" >&2; exit 1
      fi ;;
    linux-*)
      d="$stage/$target/linux/lib"; mkdir -p "$d"
      for f in "$out/$target"/ffmpeg/lib/libavcodec.so.59* "$out/$target"/ffmpeg/lib/libavutil.so.57* "$out/$target"/opus/lib/libopus.so.0*; do
        have "$f"; cp -d "$f" "$d/"
      done ;;
    mac-*)
      d="$stage/$target/macos/Frameworks"; mkdir -p "$d"
      for f in "$out/$target"/ffmpeg/lib/libavcodec.59.dylib "$out/$target"/ffmpeg/lib/libavutil.57.dylib "$out/$target"/opus/lib/libopus.0.dylib; do
        have "$f"; cp "$f" "$d/"
      done ;;
  esac
  mkdir -p "$stage/$target"
  cp "$out/$target"/ffmpeg/BUILD-INFO.txt "$stage/$target/BUILD-INFO-ffmpeg.txt"
  cp "$out/$target"/opus/BUILD-INFO.txt "$stage/$target/BUILD-INFO-opus.txt"
  echo "stage: $target ready"
done
# License texts ride with every package (LGPL/BSD attribution is a
# distribution obligation, not documentation).
for f in NOTICE COPYING.LGPLv2.1 COPYING.opus-BSD; do
  have "$here/$f"
done
echo "stage: done in $stage"
