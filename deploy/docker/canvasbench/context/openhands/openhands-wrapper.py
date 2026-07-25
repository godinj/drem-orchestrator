#!/usr/bin/env python3
import hashlib
import json
import os
import subprocess
import sys


base_url = os.environ.get("OPENAI_BASE_URL", "").rstrip("/")
api_key = os.environ.get("OPENAI_API_KEY", "")
if not base_url or not api_key:
    raise SystemExit("canvasbench OpenHands contract requires OPENAI_BASE_URL and OPENAI_API_KEY")
args = sys.argv[1:]
try:
    model_index = args.index("--model")
    model = args[model_index + 1]
except (ValueError, IndexError):
    raise SystemExit("canvasbench OpenHands contract requires --model")
del args[model_index:model_index + 2]

environment = dict(os.environ)
environment.update({
    "LLM_API_KEY": api_key,
    "LLM_BASE_URL": base_url,
    "LLM_MODEL": model,
    "LLM_MAX_INPUT_TOKENS": os.environ.get("CANVASBENCH_CONTEXT_WINDOW", ""),
    "LLM_MAX_OUTPUT_TOKENS": os.environ.get("CANVASBENCH_MAX_OUTPUT_TOKENS", ""),
})
completed = subprocess.run(["/opt/venv/bin/openhands", *args], env=environment,
                           text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
output = completed.stdout.strip() or f"openhands exited {completed.returncode} without output"
session = hashlib.sha256((model + "\0" + " ".join(args)).encode()).hexdigest()[:24]
print(json.dumps({
    "schema": "canvasbench.cli-wrapper.v1",
    "harness": "openhands",
    "session_id": f"openhands-{session}",
    "output": output,
    "exit_code": completed.returncode,
}, separators=(",", ":")))
raise SystemExit(completed.returncode)
