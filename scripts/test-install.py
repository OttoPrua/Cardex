#!/usr/bin/env python3
"""Isolated installer checks. Optional argument: the freshly built Cardex binary."""
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tarfile
import tempfile
import zipfile

root = Path(__file__).resolve().parent.parent
windows = platform.system() == 'Windows'
system = {'Darwin': 'darwin', 'Linux': 'linux', 'Windows': 'windows'}[platform.system()]
arch = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'amd64', 'amd64': 'amd64'}[platform.machine().lower()]
source_binary = Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else None
if windows and not source_binary:
    raise SystemExit('Windows check requires a built executable: python scripts/test-install.py bin/cardex.exe')
binary_name = 'cardex.exe' if windows else 'cardex'
with tempfile.TemporaryDirectory(prefix='cardex-installer-check-') as scratch:
    scratch = Path(scratch)
    home = scratch / 'home with spaces'
    home.mkdir()
    assets = scratch / 'assets'
    assets.mkdir()
    extension = '.zip' if windows else '.tar.gz'
    archive = assets / f'cardex_0.0.1_{system}_{arch}{extension}'
    binary_data = source_binary.read_bytes() if source_binary else b'#!/bin/sh\n[ "$1 $2" = "setup -inventory" ] || exit 7\nprintf \'{"fixture":true}\\n\'\n'
    skill = root / 'skills/cardex-dispatch/SKILL.md'
    skill_data = skill.read_bytes() if source_binary else b'---\nname: cardex-dispatch\n---\nfixture skill\n'
    files = {binary_name: binary_data, 'skills/cardex-dispatch/SKILL.md': skill_data}
    if windows:
        with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as bundle:
            for name, data in files.items():
                bundle.writestr(name, data)
    else:
        with tarfile.open(archive, 'w:gz') as bundle:
            for name, data in files.items():
                info = tarfile.TarInfo(name)
                info.size = len(data)
                bundle.addfile(info, io.BytesIO(data))
    (assets / 'SHA256SUMS').write_text(f'{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}\n')
    env = dict(os.environ, HOME=str(home), USERPROFILE=str(home), LOCALAPPDATA=str(home / 'AppData/Local'),
               CODEX_HOME=str(home / 'codex'), HERMES_HOME=str(home / 'hermes'), CARDEX_ROOT=str(home / '.cardex'))
    if windows:
        # Force the native-OS architecture when the Python interpreter is emulated.
        native_arch = os.environ.get('PROCESSOR_ARCHITEW6432', os.environ.get('PROCESSOR_ARCHITECTURE', '')).lower()
        assert native_arch == arch, f'Python arch {arch} differs from Windows arch {native_arch}'
    config = home / '.cardex/config.json'
    config.parent.mkdir(parents=True)
    config.write_bytes(b'{"existing_private_queue_configuration":true}\n')
    before = config.read_bytes()
    shell = (shutil.which('powershell') or shutil.which('pwsh')) if windows else 'sh'
    assert shell, 'PowerShell is required on Windows'
    def install(*args, success=True):
        if windows:
            converted = [{'--manager': '-Manager', '--version': '-Version', '--skill-dir': '-SkillDir'}.get(arg, arg) for arg in args]
            command = [shell, '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', str(root / 'scripts/install.ps1'), '-ArchiveDir', str(assets), *converted]
        else:
            command = [shell, str(root / 'scripts/install.sh'), '--archive-dir', str(assets), *args]
        result = subprocess.run(command, env=env, text=True, encoding='utf-8', errors='replace', capture_output=True)
        assert (result.returncode == 0) == success, result.stdout + result.stderr
        return result
    first = install()
    assert 'CARDEX_AGENT_ONBOARDING' in first.stdout, first.stdout
    assert 'NEXT_OWNER_QUESTION:' in first.stdout, first.stdout
    assert 'SAME conversation' in first.stdout, first.stdout
    assert str(home / '.agents/skills/cardex-dispatch') in first.stdout, first.stdout
    binary = home / ('AppData/Local/Cardex/bin/cardex.exe' if windows else '.local/bin/cardex')
    assert binary.is_file()
    assert (home / '.agents/skills/cardex-dispatch/SKILL.md').read_bytes() == skill_data
    if source_binary:
        version = subprocess.run([str(binary), 'version'], env=env, text=True, encoding="utf-8", capture_output=True, check=True)
        assert version.stdout.startswith('cardex '), version.stdout
        inventory = subprocess.run([str(binary), 'setup', '-inventory'], env=env, text=True, encoding="utf-8", capture_output=True, check=True)
        assert 'executors' in json.loads(inventory.stdout), inventory.stdout
        assert '"executors"' in first.stdout, first.stdout
    else:
        assert '{"fixture":true}' in first.stdout
    original = binary.read_bytes()
    old_skill = home / '.agents/skills/cardex-dispatch/SKILL.md'
    old_skill.write_text('customized skill')
    install('--version', 'v0.0.1')
    assert list(binary.parent.glob(binary_name + '.backup-*'))
    backup_dir = home / ('AppData/Local/Cardex/backups' if windows else '.local/state/cardex/backups')
    backups = list(backup_dir.glob('cardex-dispatch-codex.backup-*/SKILL.md'))
    assert not list(old_skill.parent.parent.glob('*backup*'))
    assert any(path.read_text() == 'customized skill' for path in backups)
    install('--manager', 'hermes')
    assert (home / 'hermes/skills/cardex-dispatch/SKILL.md').read_bytes() == skill_data
    install('--skill-dir', str(home / 'custom skills'))
    assert (home / 'custom skills/cardex-dispatch/SKILL.md').read_bytes() == skill_data
    installed_files = {str(path.relative_to(home)): hashlib.sha256(path.read_bytes()).hexdigest() for path in home.rglob('*') if path.is_file()}
    with archive.open('ab') as stream:
        stream.write(b'tampered')
    failed = install(success=False)
    assert 'SHA256 mismatch' in failed.stdout + failed.stderr
    assert binary.read_bytes() == original
    assert config.read_bytes() == before
    assert installed_files == {str(path.relative_to(home)): hashlib.sha256(path.read_bytes()).hexdigest() for path in home.rglob('*') if path.is_file()}
print('PASS: local install, inventory, version, manager selection, backups, config preservation, SHA256 rejection')
