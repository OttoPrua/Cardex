#!/usr/bin/env python3
"""Build the current source for supported release targets; no upload or install."""
import hashlib
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import zipfile

root = Path(__file__).resolve().parent.parent
version = re.search(r'const version = "([^"]+)"', (root / "cmd/cardex/main.go").read_text()).group(1)
out = root / "bin" / "release" / version
out.mkdir(parents=True, exist_ok=True)
files = ["README.md", "README.en.md", "LICENSE", "skills/cardex-dispatch/SKILL.md"]
files += [str(p.relative_to(root)) for p in (root / "docs").glob("*.md") if not p.name[0].isdigit()]
files += [str(p.relative_to(root)) for p in (root / "docs/images").glob("*") if p.is_file()]
files += [str(p.relative_to(root)) for p in (root / "skills/perlica-low-token-manager").rglob("*") if p.is_file()]
artifacts = []
for system, arch in [("darwin", "arm64"), ("darwin", "amd64"), ("linux", "amd64"), ("linux", "arm64"), ("windows", "amd64"), ("windows", "arm64")]:
    name = f"cardex_{version}_{system}_{arch}"
    binary = "cardex.exe" if system == "windows" else "cardex"
    with tempfile.TemporaryDirectory(prefix="cardex-build-") as tmp:
        built = Path(tmp) / binary
        subprocess.run(["go", "build", "-trimpath", "-o", str(built), "./cmd/cardex"], cwd=root, env=dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED="0"), check=True)
        if system == "windows":
            artifact = out / (name + ".zip")
            with zipfile.ZipFile(artifact, "w", zipfile.ZIP_DEFLATED) as bundle:
                bundle.write(built, binary)
                for file in files: bundle.write(root / file, file)
        else:
            artifact = out / (name + ".tar.gz")
            with tarfile.open(artifact, "w:gz") as bundle:
                bundle.add(built, binary)
                for file in files: bundle.add(root / file, file)
        artifacts.append(artifact)
        print(artifact.name, flush=True)
for installer in ["install.sh", "install.ps1"]:
    dest = out / installer
    shutil.copyfile(root / "scripts" / installer, dest)
    artifacts.append(dest)
(out / "SHA256SUMS").write_text("".join(f"{hashlib.sha256(file.read_bytes()).hexdigest()}  {file.name}\n" for file in artifacts))
print(out)
