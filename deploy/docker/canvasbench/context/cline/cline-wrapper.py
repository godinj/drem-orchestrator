#!/usr/bin/env python3
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit


base_url = os.environ.get("OPENAI_BASE_URL", "").rstrip("/")
api_key = os.environ.get("OPENAI_API_KEY", "")
if not base_url or not api_key:
    raise SystemExit("canvasbench cline contract requires OPENAI_BASE_URL and OPENAI_API_KEY")
parsed = urlsplit(base_url)
if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.path.rstrip("/") != "/v1":
    raise SystemExit("canvasbench cline contract requires a valid OpenAI /v1 base URL")

state = Path(tempfile.mkdtemp(prefix="canvasbench-cline-", dir="/tmp"))
try:
    settings = state / "settings"
    settings.mkdir(mode=0o700)
    provider = {
        "provider": "openai-compatible",
        "model": os.environ.get("CANVASBENCH_ADAPTER_MODEL", ""),
        "apiKey": api_key,
        "baseUrl": base_url,
        "protocol": "openai-chat",
        "client": "openai-compatible",
        "contextWindow": int(os.environ.get("CANVASBENCH_CONTEXT_WINDOW", "32768")),
        "maxTokens": int(os.environ.get("CANVASBENCH_MAX_OUTPUT_TOKENS", "4096")),
        "capabilities": ["streaming", "tools"],
    }
    providers = {
        "version": 1,
        "lastUsedProvider": "openai-compatible",
        "providers": {
            "openai-compatible": {
                "settings": provider,
                "updatedAt": "2026-01-01T00:00:00.000Z",
                "tokenSource": "manual",
            }
        },
    }
    provider_path = settings / "providers.json"
    provider_path.write_text(json.dumps(providers, separators=(",", ":")) + "\n")
    provider_path.chmod(0o600)
    environment = dict(os.environ)
    environment["CLINE_DATA_DIR"] = str(state)
    environment["CLINE_PROVIDER_SETTINGS_PATH"] = str(provider_path)
    completed = subprocess.run(
        ["/opt/harness/cline-real", *sys.argv[1:]], env=environment, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    output = completed.stdout.strip() or f"cline exited {completed.returncode} without output"
    session = hashlib.sha256("\0".join(sys.argv[1:]).encode()).hexdigest()[:24]
    print(json.dumps({
        "schema": "canvasbench.cli-wrapper.v1", "harness": "cline",
        "session_id": f"cline-{session}", "output": output, "exit_code": completed.returncode,
    }, separators=(",", ":")))
    raise SystemExit(completed.returncode)
finally:
    shutil.rmtree(state, ignore_errors=True)
