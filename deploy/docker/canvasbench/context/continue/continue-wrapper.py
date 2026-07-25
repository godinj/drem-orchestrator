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
    raise SystemExit("canvasbench Continue contract requires OPENAI_BASE_URL and OPENAI_API_KEY")
parsed = urlsplit(base_url)
if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.path.rstrip("/") != "/v1":
    raise SystemExit("canvasbench Continue contract requires a valid OpenAI /v1 base URL")

state = Path(tempfile.mkdtemp(prefix="canvasbench-continue-", dir="/tmp"))
try:
    config_path = state / "config.yaml"
    config = {
        "name": "CanvasBench", "version": "1.0.0", "schema": "v1",
        "models": [{
            "name": "canvasbench", "provider": "openai",
            "model": os.environ.get("CANVASBENCH_ADAPTER_MODEL", ""),
            "apiBase": base_url, "apiKey": "${{ secrets.OPENAI_API_KEY }}",
            "contextLength": int(os.environ.get("CANVASBENCH_CONTEXT_WINDOW", "32768")),
            "defaultCompletionOptions": {
                "maxTokens": int(os.environ.get("CANVASBENCH_MAX_OUTPUT_TOKENS", "4096")),
                "temperature": float(os.environ.get("CANVASBENCH_TEMPERATURE", "0")),
                "topP": float(os.environ.get("CANVASBENCH_TOP_P", "1")),
            },
            "capabilities": ["tool_use"], "roles": ["chat", "edit", "apply"],
        }],
    }
    config_path.write_text(json.dumps(config, separators=(",", ":")) + "\n")
    config_path.chmod(0o600)
    args = [str(config_path) if value == "__CANVASBENCH_CONFIG__" else value for value in sys.argv[1:]]
    completed = subprocess.run(
        ["node", "/opt/harness/cn-real.js", *args], env=dict(os.environ), text=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    output = completed.stdout.strip() or f"Continue exited {completed.returncode} without output"
    session = hashlib.sha256("\0".join(sys.argv[1:]).encode()).hexdigest()[:24]
    print(json.dumps({
        "schema": "canvasbench.cli-wrapper.v1", "harness": "continue",
        "session_id": f"continue-{session}", "output": output, "exit_code": completed.returncode,
    }, separators=(",", ":")))
    raise SystemExit(completed.returncode)
finally:
    shutil.rmtree(state, ignore_errors=True)
