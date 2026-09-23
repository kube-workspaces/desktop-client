#!/usr/bin/env python3
"""Pin the archive contract consumed by installed desktop updaters."""
import argparse
import hashlib
from pathlib import Path, PurePosixPath
import tarfile
import zipfile


def verify(directory, version, shell_only=False):
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
            print(f"{name}: update contract OK")
    assert set(sums) == expected, "checksum inventory differs from six-target contract"


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("version")
    parser.add_argument("--shell-only", action="store_true")
    args = parser.parse_args()
    verify(args.directory, args.version, args.shell_only)
