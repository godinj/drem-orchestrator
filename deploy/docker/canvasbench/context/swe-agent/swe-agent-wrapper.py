#!/usr/bin/env python3
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


def required_int(name: str) -> int:
    try:
        value = int(os.environ[name])
    except (KeyError, ValueError):
        raise SystemExit(f"canvasbench swe-agent contract requires valid {name}")
    if value <= 0:
        raise SystemExit(f"canvasbench swe-agent contract requires positive {name}")
    return value


base_url = os.environ.get("OPENAI_BASE_URL", "").rstrip("/")
api_key = os.environ.get("OPENAI_API_KEY", "")
if not base_url or not api_key:
    raise SystemExit("canvasbench swe-agent contract requires OPENAI_BASE_URL and OPENAI_API_KEY")

if len(sys.argv) != 3 or sys.argv[1] != "--canvasbench-task":
    raise SystemExit("usage: sweagent --canvasbench-task PROMPT")
prompt = sys.argv[2]
model = os.environ.get("CANVASBENCH_ADAPTER_MODEL", "")
if not model.startswith("openai/"):
    raise SystemExit("canvasbench swe-agent contract requires an openai/<served-model> adapter model")

context_window = required_int("CANVASBENCH_CONTEXT_WINDOW")
max_output = required_int("CANVASBENCH_MAX_OUTPUT_TOKENS")
max_iterations = required_int("CANVASBENCH_MAX_ITERATIONS")
seed = required_int("CANVASBENCH_SEED")
temperature = float(os.environ["CANVASBENCH_TEMPERATURE"])
top_p = float(os.environ["CANVASBENCH_TOP_P"])
top_k = required_int("CANVASBENCH_TOP_K")

run_root = Path(tempfile.mkdtemp(prefix="canvasbench-swe-agent-", dir="/tmp"))
config_path = run_root / "canvasbench.yaml"
output_dir = run_root / "output"
config = {
    "env": {
        "deployment": {"type": "local"},
        "repo": {"type": "preexisting", "repo_name": "workspace", "base_commit": "HEAD", "reset": False},
    },
    "problem_statement": {"type": "text", "id": "canvasbench", "text": prompt},
    "output_dir": str(output_dir),
    "agent": {
        "model": {
            "name": model,
            "api_base": base_url,
            "api_key": "$OPENAI_API_KEY",
            "temperature": temperature,
            "top_p": top_p,
            "max_input_tokens": context_window,
            "per_instance_cost_limit": 0,
            "total_cost_limit": 0,
            "per_instance_call_limit": max_iterations,
            "completion_kwargs": {
                "top_k": top_k,
                "seed": seed,
                "max_tokens": max_output,
                "chat_template_kwargs": {"preserve_thinking": True},
            },
        },
        "templates": {
            "system_template": "You are a coding agent operating in a strictly scoped repository sandbox.",
            "instance_template": "{{problem_statement}}\n\nWork only in {{working_dir}}. Inspect before editing, make the smallest complete change, verify it when possible, then submit.",
        },
        "tools": {
            "registry_variables": {
                "USE_FILEMAP": "true",
                "SUBMIT_REVIEW_MESSAGES": [
                    "Review the current diff against the task, remove accidental changes, run the smallest useful verification, then submit again."
                ],
            }
        },
    },
}
config_path.write_text(json.dumps(config))

command = [
    "/opt/venv/bin/python", "-m", "sweagent", "run",
    "--config", "/opt/swe-agent/config/default.yaml",
    "--config", str(config_path),
]
completed = subprocess.run(
    command,
    cwd="/workspace",
    env=dict(os.environ),
    text=True,
    stdout=subprocess.PIPE,
    stderr=subprocess.STDOUT,
)

trajectory_path = output_dir / "canvasbench" / "canvasbench.traj"
submission = ""
exit_status = ""
if trajectory_path.is_file():
    try:
        trajectory = json.loads(trajectory_path.read_text())
        info = trajectory.get("info") or {}
        submission = info.get("submission") or ""
        exit_status = info.get("exit_status") or ""
    except (json.JSONDecodeError, OSError, TypeError):
        pass
output = submission or completed.stdout.strip() or f"swe-agent exited {completed.returncode} without output"
session = hashlib.sha256((model + "\0" + prompt).encode()).hexdigest()[:24]
print(json.dumps({
    "schema": "canvasbench.cli-wrapper.v1",
    "harness": "swe-agent",
    "session_id": f"swer-agent-{session}",
    "output": output,
    "exit_code": completed.returncode,
    "stop_reason": exit_status,
}, separators=(",", ":")))
shutil.rmtree(run_root, ignore_errors=True)
raise SystemExit(completed.returncode)
