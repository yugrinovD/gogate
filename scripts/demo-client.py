#!/usr/bin/env python3
"""Small terminal demo client used by docs/demo.tape.

It talks to a running gogate instance over plain HTTP and prints a concise,
human-readable transcript. No third-party dependencies.
"""
from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

BASE = os.environ.get("GOGATE_DEMO_URL", "http://127.0.0.1:8080").rstrip("/")
KEY = os.environ.get("GOGATE_DEMO_KEY", "")
RL_KEY = os.environ.get("GOGATE_DEMO_RL_KEY", "")


def load_key(path: str, env: str) -> str:
    if env:
        return env
    p = Path(path)
    if p.exists():
        return p.read_text().strip()
    return ""


KEY = load_key("build/demo-key.txt", KEY)
RL_KEY = load_key("build/demo-rate-limit-key.txt", RL_KEY)

BOLD = "\033[1m"
DIM = "\033[2m"
GREEN = "\033[32m"
CYAN = "\033[36m"
YELLOW = "\033[33m"
RESET = "\033[0m"


def title(text: str) -> None:
    print(f"\n{BOLD}{CYAN}== {text} =={RESET}")


def request(path: str, key: str, body: dict | None = None) -> tuple[int, dict[str, str], bytes]:
    data = None
    headers = {"Authorization": f"Bearer {key}"}
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method="POST" if body else "GET")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, {k.lower(): v for k, v in resp.headers.items()}, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, {k.lower(): v for k, v in e.headers.items()}, e.read()


def main() -> int:
    if not KEY:
        print("missing build/demo-key.txt; run make demo first", file=sys.stderr)
        return 1
    print(f"{DIM}gogate demo · {BASE}{RESET}")

    title("GET /v1/models")
    status, _, raw = request("/v1/models", KEY)
    if status != 200:
        print(f"status={status} {raw[:200]!r}")
        return 1
    models = [m["id"] for m in json.loads(raw)["data"]]
    print("  " + ", ".join(models))

    prompt = f"Hello from the gogate demo {int(time.time())}"
    payload = {
        "model": "mock",
        "messages": [{"role": "user", "content": prompt}],
        "temperature": 0.2,
        "max_tokens": 48,
    }

    title("POST /v1/chat/completions")
    status, headers, raw = request("/v1/chat/completions", KEY, payload)
    body = json.loads(raw)
    print(f"  status={status} provider={headers.get('x-gogate-provider', '-')} cache={headers.get('x-gogate-cache', '-')}")
    print(f"  {GREEN}{body['choices'][0]['message']['content']}{RESET}")
    print(f"  usage: prompt={body['usage']['prompt_tokens']} completion={body['usage']['completion_tokens']}")

    title("same request again (exact cache)")
    status, headers, _ = request("/v1/chat/completions", KEY, payload)
    print(f"  status={status} provider={headers.get('x-gogate-provider', '-')} cache={headers.get('x-gogate-cache', '-')}")

    title("streaming SSE")
    stream_payload = {
        "model": "mock",
        "messages": [{"role": "user", "content": "Stream this response"}],
        "stream": True,
        "stream_options": {"include_usage": True},
        "max_tokens": 32,
    }
    req = urllib.request.Request(
        BASE + "/v1/chat/completions",
        data=json.dumps(stream_payload).encode(),
        headers={"Authorization": f"Bearer {KEY}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        lines = 0
        for raw_line in resp:
            line = raw_line.decode().strip()
            if not line:
                continue
            print("  " + DIM + line[:120] + RESET)
            lines += 1
            if line == "data: [DONE]" or lines >= 8:
                break

    if RL_KEY:
        title("rate limit · key quota 2 RPM")
        rl_payload = {
            "model": "mock",
            "messages": [{"role": "user", "content": "rate limit demo"}],
            "max_tokens": 8,
        }
        for i in range(1, 4):
            status, headers, _ = request("/v1/chat/completions", RL_KEY, rl_payload)
            retry = headers.get("retry-after", "-")
            color = GREEN if status < 300 else YELLOW
            print(f"  {color}request {i}: HTTP {status} Retry-After={retry}{RESET}")

    print(f"\n{DIM}Grafana: http://localhost:3000 · Jaeger: http://localhost:16686{RESET}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
