#!/usr/bin/env bash
# Fetch pinned media sources and verify hashes (see MANIFEST).
# Usage: third_party/media-libs/fetch.sh [destdir]
# Idempotent: skips archives that already verify.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
dest="${1:-$here/dl}"
mkdir -p "$dest"

while read -r name version url sha; do
  case "$name" in \#*|"") continue;; esac
  file="$dest/$(basename "$url")"
  if [ -f "$file" ] && echo "$sha  $file" | sha256sum -c --status -; then
    echo "fetch: $file already pinned-verified"
    continue
  fi
  echo "fetch: downloading $url"
  curl --proto '=https' --tlsv1.2 -sSfL -o "$file" "$url"
  echo "$sha  $file" | sha256sum -c -
done < "$here/MANIFEST"
echo "fetch: all pinned sources verified in $dest"
