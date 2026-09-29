#!/usr/bin/env bash
# Build an RPM package (.rpm) from a kube-workspaces release archive.
#
# Usage:
#   scripts/build-rpm.sh <archive> <version> <outdir>
#
#   <archive> is the assembled linux release archive (from `make build-all`
#     or the assemble CI job): kube-workspaces-<version>-linux-<goarch>.tar.gz
#     (goarch amd64 or arm64), containing one kube-workspaces-linux-<arch>/
#     stage dir. If the web child (kube-workspaces-web) was injected by
#     scripts/insert-web-child.sh it is carried into the package, so
#     container workspaces keep working.
#   <version> is the release tag (v*); the leading v is stripped for the
#     RPM version.
#   <outdir>  receives kube-workspaces-<version>-1.<rpmarch>.rpm plus a
#     sidecar <name>.sha256.
#
# rpmbuild does the build, so it must exist (Fedora: rpm-build; the CI job
# runs in a Fedora container). The package is unsigned, like the MSI and
# AppImage: repository/RPM signing lives in
# tracking/future/linux-package-repos.md.
#
# Like the MSI and AppImage, the .rpm sits OUTSIDE the updater contract:
# the six-archive contract and its SHA256SUMS (verified by
# scripts/verify-update-archives.py) are untouched. The .rpm gets a sidecar
# checksum instead, and installs a packaged-install marker that tells the
# client to stand its in-app updater down (rpm owns these files now).
set -euo pipefail

archive="$(realpath "$1")"
version="$2"
outdir="$(realpath -m "$3")"

test -f "$archive" || { echo "build-rpm: archive not found: $archive" >&2; exit 1; }
case "$version" in
  v*) ;;
  *) echo "build-rpm: version must be a tag (v*), got $version" >&2; exit 1 ;;
esac
rpmver="${version#v}"
# RPM versions cannot contain '-'; dev versions (v0.8.1-1-gXXX) fold them
# to dots. Tags are unaffected.
rpmver="${rpmver//-/.}"
case "$rpmver" in
  [0-9]*)
    case "$rpmver" in
      *[!A-Za-z0-9.+~^]*)
        echo "build-rpm: version not RPM-safe: $rpmver" >&2; exit 1 ;;
    esac ;;
  *) echo "build-rpm: version must start with a digit: $rpmver" >&2; exit 1 ;;
esac
case "$archive" in
  *-linux-amd64.tar.gz) rpmarch=x86_64 ;;
  *-linux-arm64.tar.gz) rpmarch=aarch64 ;;
  *) echo "build-rpm: archive names a linux goarch (amd64/arm64): $archive" >&2; exit 1 ;;
esac
command -v rpmbuild >/dev/null 2>&1 || { echo "build-rpm: rpmbuild not found (Fedora: rpm-build)" >&2; exit 1; }
mkdir -p "$outdir"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

tar -xzf "$archive" -C "$work"
stage="$( (cd "$work" && ls -d kube-workspaces-*/) 2>/dev/null | head -1 | tr -d '/')"
test -n "$stage" || { echo "build-rpm: no stage dir in $archive" >&2; exit 1; }

root="$(cd "$(dirname "$0")/.." && pwd)"
topdir="$work/rpm"
mkdir -p "$topdir"/{BUILD,RPMS,SOURCES,SPECS,SRPMS}
cp -r "$work/$stage" "$topdir/SOURCES/stage"
cp "$root/packaging/linux/kube-workspaces.desktop" "$topdir/SOURCES/"
cp "$root/packaging/linux/copyright" "$topdir/SOURCES/"
cp -r "$root/packaging/linux/icons" "$topdir/SOURCES/"
# Archives without the web child (developer `make build-all` runs, which
# are shell-only) package the shell alone. Both the install lines and the
# %files entry come from placeholders expanded below, never from pattern
# deletion (which once ate a surrounding `if`, leaving a dangling `fi`
# that failed every such build). CI archives always carry the child.
if test -f "$work/$stage/kube-workspaces-web"; then
  printf '%s\n' 'install -m755 %{stagedir}/kube-workspaces-web %{buildroot}/usr/bin/kube-workspaces-web' > "$work/webinstall"
  printf '%s\n' '/usr/bin/kube-workspaces-web' > "$work/webfiles"
else
  : > "$work/webinstall"
  : > "$work/webfiles"
  echo "build-rpm: archive carries no web child; packaging shell only" >&2
fi
sed -e "s/@@VERSION@@/$rpmver/g" \
    -e "s/@@ARCH@@/$rpmarch/g" \
    -e "s/@@TAG@@/$version/g" \
    -e "s/@@DATE@@/$(date -u '+%a %b %d %Y')/g" \
    -e "/@@WEBCHILD_INSTALL@@/r $work/webinstall" \
    -e "/@@WEBCHILD_INSTALL@@/d" \
    -e "/@@WEBCHILD_FILES@@/r $work/webfiles" \
    -e "/@@WEBCHILD_FILES@@/d" \
  "$root/packaging/linux/kube-workspaces.spec.in" > "$topdir/SPECS/kube-workspaces.spec"

rpmbuild --define "_topdir $topdir" \
  --define "stagedir $topdir/SOURCES/stage" \
  --define "sourcedir $topdir/SOURCES" \
  -bb "$topdir/SPECS/kube-workspaces.spec" >&2

rpm="$(find "$topdir/RPMS" -name '*.rpm' | head -1)"
test -n "$rpm" || { echo "build-rpm: no package produced" >&2; exit 1; }
out="$outdir/$(basename "$rpm")"
cp "$rpm" "$out"
sha256sum "$out" | awk '{print $1}' > "$out.sha256"
echo "build-rpm: wrote $out"
echo "build-rpm: sha256 $(cat "$out.sha256") (sidecar; not part of SHA256SUMS)"
