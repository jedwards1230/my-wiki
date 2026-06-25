#!/usr/bin/env python3
"""Debounced wiki-capture Stop hook.

Replaces the per-turn `prompt`-type Stop hook, which ran a Haiku eval on the
critical path of EVERY turn (~3.5s median) for near-zero yield. This command
hook self-gates on a per-session state file so the evaluation runs at most once
per debounce window, and surfaces at most ONE capture flag per session.

Contract (Stop hook):
  - stdin: JSON with session_id, transcript_path, stop_hook_active, ...
  - On a real, portable, durable fact: print {"decision":"block","reason":...}
    so the flag surfaces in-chat (same UX as the old prompt hook).
  - Otherwise: exit 0 silently (nothing reaches the conversation).

Design notes:
  - The evaluation is delegated to `claude -p` (Haiku). A recursion guard
    (WIKI_CAPTURE_EVAL) prevents that child session's own Stop hook from
    re-triggering this one (fork-bomb guard).
  - Everything fails SAFE: any error, missing binary, timeout, or unparseable
    output results in exit 0 (never break or block the parent session).

Tunables (env):
  - WIKI_CAPTURE_INTERVAL  debounce window in seconds (default 600)
  - WIKI_CAPTURE_MODEL     evaluator model id (default claude-haiku-4-5-20251001)
  - WIKI_CAPTURE_DISABLE   set to any value to disable the hook entirely
"""
import json
import os
import re
import subprocess
import sys
import tempfile
import time

MODEL = os.environ.get("WIKI_CAPTURE_MODEL", "claude-haiku-4-5-20251001")
INTERVAL = int(os.environ.get("WIKI_CAPTURE_INTERVAL", "600"))
MAX_WINDOW_CHARS = 8000
MIN_WINDOW_CHARS = 300
EVAL_TIMEOUT_S = 30

EVAL_PROMPT = """\
You are a capture filter for a SHARED, cross-machine, multi-reader wiki (a \
homelab/personal knowledge base — NOT a per-session notebook). Below is an \
excerpt of a Claude Code session. Decide whether it produced a fact worth a \
wiki page. Respond with JSON only, no prose.

PORTABILITY TEST (the discriminator): a fact qualifies only if it would still \
matter to a reader with NO access to this repo, working tree, machine, or the \
tool in use. Local lore — build/run commands, CLI or tool flags, \
library/framework/plugin/editor authoring idioms, linter quirks, local file \
paths, dependency versions — NEVER belongs in the wiki, however durable or \
non-obvious; it belongs in that repo's own docs or in machine-local memory.

A fact qualifies only if ALL hold: portable (passes the test above), durable \
(not ephemeral session state), and substantive (a decision, architecture fact, \
gotcha, or finding — not a restatement of code/config that is obvious from the \
source).

Return {"ok": true} for the common case: trivial, conversational, exploratory, \
or routine excerpts; work still in progress; or local lore.
Return {"ok": false, "reason": "Suggest saving this to the wiki and ask before \
writing: <one concise portable fact>"} ONLY for a clearly portable, durable, \
substantive fact. When in doubt, return {"ok": true}.

SESSION EXCERPT:
"""


def _silent_exit():
    sys.exit(0)


def _load_state(path):
    try:
        with open(path) as f:
            return json.load(f)
    except Exception:
        return {"last_eval": 0, "suggested": False}


def _save_state(path, state):
    try:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            json.dump(state, f)
    except Exception:
        pass


def _iso_to_epoch(ts):
    if not ts:
        return 0.0
    try:
        # transcript timestamps look like 2026-06-21T15:39:47.123Z
        return time.mktime(time.strptime(ts[:19], "%Y-%m-%dT%H:%M:%S"))
    except Exception:
        return 0.0


def _extract_text(content):
    """Pull human-readable text out of a message's content field."""
    if isinstance(content, str):
        s = content.strip()
        # Skip hook/system/tool wrappers that aren't real conversation.
        if s.startswith("<") or s.startswith("[") or "stop_hook" in s:
            return ""
        return s
    if isinstance(content, list):
        parts = []
        for block in content:
            if isinstance(block, dict) and block.get("type") == "text":
                parts.append(block.get("text", ""))
        return "\n".join(p for p in parts if p).strip()
    return ""


def _build_window(transcript_path, since_epoch):
    """Collect user/assistant text newer than since_epoch, capped, tail-first."""
    msgs = []
    try:
        with open(transcript_path) as f:
            for line in f:
                try:
                    o = json.loads(line)
                except Exception:
                    continue
                if o.get("type") not in ("user", "assistant"):
                    continue
                if o.get("isMeta"):
                    continue
                if since_epoch and _iso_to_epoch(o.get("timestamp")) <= since_epoch:
                    continue
                msg = o.get("message") or {}
                text = _extract_text(msg.get("content"))
                if text:
                    role = o.get("type")
                    msgs.append(f"[{role}] {text}")
    except Exception:
        return ""
    if not msgs:
        return ""
    # Tail-first so the most recent content survives the cap, then restore order.
    kept, total = [], 0
    for m in reversed(msgs):
        total += len(m)
        kept.append(m)
        if total >= MAX_WINDOW_CHARS:
            break
    return "\n\n".join(reversed(kept))[:MAX_WINDOW_CHARS]


def _evaluate(window):
    """Run the Haiku eval via `claude -p`. Returns (ok, reason) or None on error."""
    env = dict(os.environ)
    env["WIKI_CAPTURE_EVAL"] = "1"  # recursion guard for the child's Stop hook
    try:
        proc = subprocess.run(
            ["claude", "-p", EVAL_PROMPT + window, "--model", MODEL,
             "--output-format", "text"],
            capture_output=True, text=True, timeout=EVAL_TIMEOUT_S, env=env,
        )
    except Exception:
        return None
    if proc.returncode != 0:
        return None
    m = re.search(r"\{.*\}", proc.stdout, re.DOTALL)
    if not m:
        return None
    try:
        result = json.loads(m.group(0))
    except Exception:
        return None
    return bool(result.get("ok", True)), result.get("reason", "")


def main():
    # Hard kill switches first (cheapest paths).
    if os.environ.get("WIKI_CAPTURE_DISABLE"):
        _silent_exit()
    if os.environ.get("WIKI_CAPTURE_EVAL"):  # we are the spawned evaluator child
        _silent_exit()

    try:
        payload = json.load(sys.stdin)
    except Exception:
        _silent_exit()

    # Don't re-block a stop that is already a continuation of our own block.
    if payload.get("stop_hook_active"):
        _silent_exit()

    session_id = payload.get("session_id") or "unknown"
    transcript_path = payload.get("transcript_path")
    if not transcript_path or not os.path.exists(transcript_path):
        _silent_exit()

    state_path = os.path.join(
        tempfile.gettempdir(), "my-wiki-capture-hook", f"{session_id}.json"
    )
    state = _load_state(state_path)

    # Debounce: one flag per session, and at most one eval per interval.
    if state.get("suggested"):
        _silent_exit()
    now = time.time()
    if now - state.get("last_eval", 0) < INTERVAL:
        _silent_exit()

    window = _build_window(transcript_path, state.get("last_eval", 0))
    state["last_eval"] = now  # mark the attempt regardless of outcome
    if len(window) < MIN_WINDOW_CHARS:
        _save_state(state_path, state)
        _silent_exit()

    verdict = _evaluate(window)
    if verdict is None:
        _save_state(state_path, state)  # fail-safe: silent, retry next interval
        _silent_exit()

    ok, reason = verdict
    if ok or not reason:
        _save_state(state_path, state)
        _silent_exit()

    # A genuine, portable, durable capture candidate — surface it once.
    state["suggested"] = True
    _save_state(state_path, state)
    print(json.dumps({"decision": "block", "reason": reason}))
    sys.exit(0)


if __name__ == "__main__":
    main()
