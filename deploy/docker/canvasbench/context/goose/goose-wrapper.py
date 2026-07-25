#!/usr/bin/env python3
import hashlib
import json
import os
import subprocess
import sys
from urllib.parse import urlsplit


base_url = os.environ.get("OPENAI_BASE_URL", "").rstrip("/")
api_key = os.environ.get("OPENAI_API_KEY", "")
if not base_url or not api_key:
    raise SystemExit("canvasbench goose contract requires OPENAI_BASE_URL and OPENAI_API_KEY")
parsed = urlsplit(base_url)
if parsed.scheme not in {"http", "https"} or not parsed.netloc:
    raise SystemExit("canvasbench goose contract received invalid OPENAI_BASE_URL")
path = parsed.path.strip("/")
if path != "v1":
    raise SystemExit("canvasbench goose contract requires an OpenAI /v1 base URL")

environment = dict(os.environ)
environment.update({
    "OPENAI_HOST": f"{parsed.scheme}://{parsed.netloc}",
    "OPENAI_BASE_PATH": "v1/chat/completions",
    "GOOSE_MODE": "auto",
    "GOOSE_DISABLE_SESSION_NAMING": "true",
    "GOOSE_CLI_MIN_PRIORITY": "0.0",
})
args = sys.argv[1:]
completed = subprocess.run(
    ["/opt/harness/goose-real", *args],
    env=environment,
    text=True,
    stdout=subprocess.PIPE,
    stderr=subprocess.STDOUT,
)
output = completed.stdout.strip() or f"goose exited {completed.returncode} without output"
session = hashlib.sha256("\0".join(args).encode()).hexdigest()[:24]
print(json.dumps({
    "schema": "canvasbench.cli-wrapper.v1",
    "harness": "goose",
    "session_id": f"goose-{session}",
    "output": output,
    "exit_code": completed.returncode,
}, separators=(",", ":")))
raise SystemExit(completed.returncode)
