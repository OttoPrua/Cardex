#!/usr/bin/env python3
"""Verify release checksums, archive layout, binary version and source commit."""
import argparse
import hashlib
from pathlib import Path
import platform
import subprocess
import tarfile
import tempfile
import zipfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("version")
parser.add_argument("commit")
args = parser.parse_args()
root = Path(__file__).resolve().parent.parent
out = root / "bin" / "release" / args.version
targets = [("darwin", "arm64"), ("darwin", "amd64"), ("linux", "amd64"),
           ("linux", "arm64"), ("windows", "amd64"), ("windows", "arm64")]
expected = {f"cardex_{args.version}_{system}_{arch}." + ("zip" if system == "windows" else "tar.gz")
            for system, arch in targets} | {"install.sh", "install.ps1"}
checksums = {}
for line in (out / "SHA256SUMS").read_text().splitlines():
    digest, name = line.split("  ", 1)
    assert name not in checksums, f"duplicate checksum: {name}"
    assert hashlib.sha256((out / name).read_bytes()).hexdigest() == digest, name
    checksums[name] = digest
assert set(checksums) == expected, "missing or unexpected release payload"
assert {p.name for p in out.iterdir()} == expected | {"SHA256SUMS"}, "unexpected release file"
host = (platform.system().lower(), {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine()))
for system, arch in targets:
    name = f"cardex_{args.version}_{system}_{arch}"
    binary = "cardex.exe" if system == "windows" else "cardex"
    if system == "windows":
        with zipfile.ZipFile(out / (name + ".zip")) as bundle:
            entries = bundle.namelist()
            data = bundle.read(binary)
    else:
        with tarfile.open(out / (name + ".tar.gz")) as bundle:
            entries = bundle.getnames()
            data = bundle.extractfile(binary).read()
    assert len(entries) == len(set(entries)), f"duplicate archive paths: {name}"
    assert {binary, "README.md", "README.en.md", "LICENSE", "docs/mixed-routing.md",
            "skills/cardex-dispatch/SKILL.md"} <= set(entries), name
    assert all(p == binary or p in {"README.md", "README.en.md", "LICENSE"} or
               p.startswith(("docs/", "skills/")) for p in entries), f"unexpected archive path: {name}"
    with tempfile.TemporaryDirectory(prefix="cardex-release-verify-") as tmp:
        built = Path(tmp) / binary
        built.write_bytes(data)
        built.chmod(0o755)
        metadata = subprocess.check_output(["go", "version", "-m", str(built)], text=True)
        assert "vcs.revision=" + args.commit in metadata, f"wrong source commit: {name}"
        assert "vcs.modified=false" in metadata, f"dirty source build: {name}"
        if (system, arch) == host:
            version = subprocess.check_output([str(built), "version"], text=True).strip()
            assert version == "cardex " + args.version, version
print(f"PASS: six archives, eight payload checksums, clean source {args.commit}, version {args.version}")
