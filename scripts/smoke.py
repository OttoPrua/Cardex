#!/usr/bin/env python3
"""Exercise the shipped CLI and board in isolated state, without model calls."""
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

binary = str(Path(sys.argv[1]).resolve())
with tempfile.TemporaryDirectory(prefix="cardex-smoke-") as tmp:
    root = Path(tmp) / "state with spaces"
    project = Path(tmp) / "project"
    project.mkdir()
    env = dict(os.environ, CARDEX_ROOT=str(root), CARDEX_REQUIRE_OWNER_ROUTING="0")
    def run(*args, ok=True):
        result = subprocess.run([binary, *args], env=env, text=True, encoding="utf-8", capture_output=True, timeout=30)
        if ok and result.returncode:
            raise RuntimeError(f"{args}: {result.stdout}\n{result.stderr}")
        return result
    print(run("version").stdout.strip())
    # The real binary stands in only for executable discovery; no provider is run.
    run("setup", "-runner", "codex", "-bin", binary)
    config_before = (root / "config.json").read_bytes()
    assert run("setup", "-runner", "claude", ok=False).returncode != 0
    assert (root / "config.json").read_bytes() == config_before
    run("doctor")
    run("add", "-dir", str(project), "-dry-run", "inspect this project")
    assert not list((root / "tasks").glob("*.json"))
    run("add", "-dir", str(project), "-hold", "-title", "smoke held", "inspect this project")
    cards = json.loads(run("list", "-json").stdout)
    assert len(cards) == 1 and cards[0]["status"] == "held", cards
    task_id = cards[0]["id"]
    run("release", task_id)
    assert json.loads(run("list", "-json").stdout)[0]["status"] == "queued"
    run("hold", task_id)
    run("cancel", task_id)
    assert json.loads((root / "archive" / (task_id + ".json")).read_text())["status"] == "canceled"
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    board = subprocess.Popen([binary, "board", "-port", str(port)], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        # Do not route the loopback health check through the host's proxy.
        client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        for _ in range(100):
            try:
                with client.open(f"http://127.0.0.1:{port}/api/health", timeout=1) as response:
                    assert response.status == 200
                    json.load(response)
                with client.open(f"http://127.0.0.1:{port}/", timeout=1) as response:
                    assert b"<html" in response.read().lower()
                break
            except OSError:
                if board.poll() is not None:
                    raise RuntimeError(board.stderr.read().decode("utf-8", errors="replace"))
                time.sleep(0.1)
        else:
            raise RuntimeError("board did not become ready")
    finally:
        board.terminate()
        board.communicate(timeout=10)
print("PASS: setup, preservation, doctor, dry-run, queue lifecycle, board HTTP (no model calls)")
