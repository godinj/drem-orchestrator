#!/usr/bin/env python3
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys


def required_int(name: str) -> int:
    try:
        value = int(os.environ[name])
    except (KeyError, ValueError):
        raise SystemExit(f"canvasbench aider contract requires valid {name}")
    if value <= 0:
        raise SystemExit(f"canvasbench aider contract requires positive {name}")
    return value


base_url = os.environ.get("OPENAI_BASE_URL", "").rstrip("/")
api_key = os.environ.get("OPENAI_API_KEY", "")
if not base_url or not api_key:
    raise SystemExit("canvasbench aider contract requires OPENAI_BASE_URL and OPENAI_API_KEY")
context_window = required_int("CANVASBENCH_CONTEXT_WINDOW")
max_output = required_int("CANVASBENCH_MAX_OUTPUT_TOKENS")
args = sys.argv[1:]
try:
    model = args[args.index("--model") + 1]
except (ValueError, IndexError):
    raise SystemExit("canvasbench aider contract requires --model")

metadata_path = Path(os.environ.get("HOME", "/tmp")) / "aider-model-metadata.json"
metadata_path.write_text(json.dumps({model: {
    "litellm_provider": "openai",
    "mode": "chat",
    "max_input_tokens": context_window,
    "max_output_tokens": max_output,
}}))
environment = dict(os.environ)
environment["OPENAI_API_BASE"] = base_url
command = ["/opt/venv/bin/aider", *args,
           "--model-metadata-file", str(metadata_path),
           "--no-show-model-warnings", "--no-fancy-input",
           "--no-suggest-shell-commands", "--no-detect-urls"]
completed = subprocess.run(command, env=environment, text=True,
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
output = completed.stdout.strip() or f"aider exited {completed.returncode} without output"
session = hashlib.sha256((model + "\0" + " ".join(args)).encode()).hexdigest()[:24]
print(json.dumps({
    "schema": "canvasbench.cli-wrapper.v1",
    "harness": "aider",
    "session_id": f"aider-{session}",
    "output": output,
    "exit_code": completed.returncode,
}, separators=(",", ":")))
raise SystemExit(completed.returncode)
