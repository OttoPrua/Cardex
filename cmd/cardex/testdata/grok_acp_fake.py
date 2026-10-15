#!/usr/bin/env python3
"""Fake Grok 1.0.40 agent stdio ACP speaker for Cardex tests. No provider calls."""
import json
import os
import sys

DIR = sys.argv[1] if len(sys.argv) > 1 else os.getcwd()
PAYLOAD = os.path.join(DIR, "result.jsonl")
PROMPT_DUMP = os.path.join(DIR, "prompt.dump")
PERM_PATH = os.path.join(DIR, "permission.json")
EFFECT_PATH_FILE = os.path.join(DIR, "effect.path")
STDERR_FILE = os.path.join(DIR, "stderr.txt")
EXIT_FILE = os.path.join(DIR, "exit.code")
METHODS_DUMP = os.path.join(DIR, "acp.methods")


def load_json(path, default):
    if not os.path.exists(path):
        return default
    with open(path, "r", encoding="utf-8") as f:
        raw = f.read().strip()
    if not raw:
        return default
    return json.loads(raw)


def send(obj):
    sys.stdout.write(json.dumps(obj, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def payload_text():
    if not os.path.exists(PAYLOAD):
        return "GROK_OK", "session-grok"
    text = []
    session = "session-grok"
    with open(PAYLOAD, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                ev = json.loads(line)
            except json.JSONDecodeError:
                continue
            if ev.get("type") == "text" and ev.get("data"):
                text.append(ev["data"])
            if ev.get("sessionId"):
                session = ev["sessionId"]
    return "".join(text) or "GROK_OK", session


def main():
    cfg = load_json(PERM_PATH, {})
    requests = cfg.get("requests") or []
    effect_file = cfg.get("effectFile") or ""
    if not effect_file and os.path.exists(EFFECT_PATH_FILE):
        with open(EFFECT_PATH_FILE, "r", encoding="utf-8") as f:
            effect_file = f.read().strip()
    crash = bool(cfg.get("crashAfterRequest"))
    hang = bool(cfg.get("hangAfterRequest"))
    text, session_id = payload_text()
    pending = {}
    prompt_id = None
    executed = []
    methods = []

    def finish_prompt():
        send(
            {
                "jsonrpc": "2.0",
                "method": "session/update",
                "params": {
                    "sessionId": session_id,
                    "update": {
                        "sessionUpdate": "agent_message_chunk",
                        "content": {"type": "text", "text": text},
                    },
                },
            }
        )
        if prompt_id is not None:
            send({"jsonrpc": "2.0", "id": prompt_id, "result": {"stopReason": "end_turn", "sessionId": session_id}})
        if executed and effect_file:
            parent = os.path.dirname(effect_file)
            if parent:
                os.makedirs(parent, exist_ok=True)
            with open(effect_file, "w", encoding="utf-8") as f:
                f.write("executed:" + ",".join(executed) + "\n")
        if os.path.exists(STDERR_FILE):
            with open(STDERR_FILE, "r", encoding="utf-8") as f:
                err = f.read()
            if err:
                sys.stderr.write(err if err.endswith("\n") else err + "\n")
        code = 0
        if os.path.exists(EXIT_FILE):
            with open(EXIT_FILE, "r", encoding="utf-8") as f:
                raw = f.read().strip()
            if raw:
                code = int(raw)
        with open(METHODS_DUMP, "w", encoding="utf-8") as f:
            f.write("\n".join(methods) + "\n")
        sys.exit(code)

    def emit_permissions():
        for i, spec in enumerate(requests):
            rid = 1000 + i
            tool_name = spec.get("toolName") or spec.get("title") or "run_terminal_command"
            raw_input = spec.get("rawInput") or {}
            tool_call_id = spec.get("toolCallId") or ("call-%d" % (i + 1))
            pending[rid] = spec
            send(
                {
                    "jsonrpc": "2.0",
                    "id": rid,
                    "method": "session/request_permission",
                    "params": {
                        "sessionId": session_id,
                        "toolCall": {
                            "toolCallId": tool_call_id,
                            "title": spec.get("title") or tool_name,
                            "kind": spec.get("kind") or "execute",
                            "status": "pending",
                            "toolName": tool_name,
                            "rawInput": raw_input,
                        },
                        "options": [
                            {"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"},
                            {"optionId": "reject-once", "name": "Reject once", "kind": "reject_once"},
                        ],
                    },
                }
            )
        if crash:
            sys.exit(1)

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            continue
        method = msg.get("method")
        mid = msg.get("id")
        if method:
            methods.append(method)
        if method == "initialize":
            send(
                {
                    "jsonrpc": "2.0",
                    "id": mid,
                    "result": {
                        "protocolVersion": 1,
                        "agentCapabilities": {
                            "loadSession": True,
                            "promptCapabilities": {"image": False, "audio": False, "embeddedContext": True},
                        },
                    },
                }
            )
            continue
        if method in ("session/new", "session/load"):
            params = msg.get("params") or {}
            sid = params.get("sessionId") or session_id
            session_id = sid
            send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": session_id}})
            continue
        if method == "session/prompt":
            prompt_id = mid
            params = msg.get("params") or {}
            chunks = []
            for block in params.get("prompt") or []:
                if isinstance(block, dict) and block.get("text"):
                    chunks.append(block["text"])
            with open(PROMPT_DUMP, "w", encoding="utf-8") as f:
                f.write("".join(chunks))
            if requests:
                emit_permissions()
                if hang:
                    continue
                if not pending:
                    finish_prompt()
                continue
            finish_prompt()
            return
        if "result" in msg or "error" in msg:
            rid = mid
            spec = pending.pop(rid, None)
            if spec is None:
                continue
            outcome = ((msg.get("result") or {}).get("outcome") or {})
            selected = outcome.get("outcome")
            option_id = outcome.get("optionId")
            if selected == "selected" and option_id == "allow-once":
                executed.append(spec.get("toolCallId") or "")
            if not pending and not hang:
                finish_prompt()
            continue
    finish_prompt()


if __name__ == "__main__":
    main()
