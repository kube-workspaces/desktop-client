#!/usr/bin/env python3
"""Pin the archive contract consumed by installed desktop updaters."""
import argparse
import hashlib
from pathlib import Path, PurePosixPath
import tarfile
import zipfile


def verify(directory, version, shell_only=False, require_msi=False):
    sums = {}
    for line in (directory / "SHA256SUMS").read_text().splitlines():
        digest, name = line.split(maxsplit=1)
        name = name.removeprefix("*").removeprefix("./")
        assert name not in sums, f"duplicate checksum: {name}"
        sums[name] = digest
    expected = set()
    for os_name in ("linux", "darwin", "windows"):
        for arch in ("amd64", "arm64"):
            ext = "zip" if os_name == "windows" else "tar.gz"
            name = f"kube-workspaces-{version}-{os_name}-{arch}.{ext}"
            expected.add(name)
            archive = directory / name
            with archive.open("rb") as f:
                assert hashlib.file_digest(f, "sha256").hexdigest() == sums[name], name
            if ext == "zip":
                with zipfile.ZipFile(archive) as z:
                    members = [f.filename for f in z.infolist() if not f.is_dir()]
            else:
                with tarfile.open(archive, "r:gz") as t:
                    entries = t.getmembers()
                    assert all(e.isfile() or e.isdir() for e in entries), name
                    members = [e.name for e in entries if e.isfile()]
            root = f"kube-workspaces-{os_name}-{arch}"
            assert len(members) == len(set(members)), f"duplicate member: {name}"
            for member in members:
                p = PurePosixPath(member)
                assert not p.is_absolute() and ".." not in p.parts, member
                assert p.parts[0] == root and "\\" not in member, member
            binary_dir = root
            if os_name == "darwin":
                binary_dir += "/Kube Workspaces.app/Contents/MacOS"
                assert root + "/Kube Workspaces.app/Contents/Info.plist" in members
            exe = ".exe" if os_name == "windows" else ""
            assert f"{binary_dir}/kube-workspaces{exe}" in members, name
            if not shell_only and (os_name, arch) != ("windows", "arm64"):
                assert f"{binary_dir}/kube-workspaces-web{exe}" in members, name
            # Tier 1 media bundle (LGPL 5.1 + BSD Opus): bundled everywhere a
            # build exists, so Tier 1 works out of the box. windows/arm64 has
            # no 5.1 shared source yet and keeps the Tier 0 fallback.
            if not shell_only and (os_name, arch) != ("windows", "arm64"):
                if os_name == "windows":
                    for dll in ("avcodec-59.dll", "avutil-57.dll"):
                        assert f"{root}/{dll}" in members, (name, dll)
                    assert f"{root}/opus.dll" in members or f"{root}/libopus-0.dll" in members, (name, "opus")
                elif os_name == "linux":
                    for soname in ("libavcodec.so.59", "libavutil.so.57", "libopus.so.0"):
                        assert any(m.startswith(f"{root}/lib/{soname}") for m in members), (name, soname)
                elif os_name == "darwin":
                    for dylib in ("libavcodec.59.dylib", "libavutil.57.dylib", "libopus.0.dylib"):
                        assert any(m.endswith("/Frameworks/" + dylib) or m == f"{root}/Frameworks/{dylib}" for m in members), (name, dylib)
                for doc in ("NOTICE-media-NOTICE", "NOTICE-media-COPYING.LGPLv2.1",
                            "NOTICE-media-COPYING.opus-BSD", "BUILD-INFO-ffmpeg.txt",
                            "BUILD-INFO-opus.txt"):
                    assert any(m == f"{root}/{doc}" or m.endswith("/" + doc) for m in members), (name, doc)
            print(f"{name}: update contract OK")
    # The unsigned Windows MSIs ship alongside the zips (same release, SHA
    # covered) but are NOT part of the updater contract: the in-app updater
    # consumes only the six archives above. Any extra checksum entry must be
    # an expected Windows MSI whose hash verifies.
    extra = set(sums) - expected
    installers = {f"kube-workspaces-{version}-windows-{arch}.msi" for arch in ("amd64", "arm64")}
    assert extra <= installers, f"unexpected checksum entries: {extra - installers}"
    if require_msi:
        assert extra == installers, "both Windows MSIs are required"
    for name in sorted(extra):
        assert name.endswith(".msi"), f"unexpected checksum entry: {name}"
        with (directory / name).open("rb") as f:
            assert hashlib.file_digest(f, "sha256").hexdigest() == sums[name], name
        print(f"{name}: checksum OK (msi, outside update contract)")
    actual = {p.name for p in directory.iterdir() if p.name.endswith((".tar.gz", ".zip", ".msi"))}
    assert set(sums) == actual, "checksum inventory differs from artifacts"


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("version")
    parser.add_argument("--shell-only", action="store_true")
    parser.add_argument("--require-msi", action="store_true")
    args = parser.parse_args()
    verify(args.directory, args.version, args.shell_only, args.require_msi)
