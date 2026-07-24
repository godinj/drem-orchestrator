#!/usr/bin/env python3
"""Relay dremctl invocations through workspace files for network-sandboxed tasks."""

import argparse
import json
import os
import pathlib
import subprocess
import sys
import time
import uuid


def atomic_json(path, payload):
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(payload))
    os.chmod(tmp, 0o600)
    os.replace(tmp, path)


def relay_dirs(root):
    requests = root / "requests"
    responses = root / "responses"
    requests.mkdir(parents=True, exist_ok=True)
    responses.mkdir(parents=True, exist_ok=True)
    os.chmod(root, 0o700)
    os.chmod(requests, 0o700)
    os.chmod(responses, 0o700)
    return requests, responses


def client(root, args):
    requests, responses = relay_dirs(root)
    request_id = str(uuid.uuid4())
    actor = os.environ.get("DREM_ACTOR", "")
    if not actor and os.environ.get("CODEX_THREAD_ID"):
        actor = "codex:" + os.environ["CODEX_THREAD_ID"]
    atomic_json(
        requests / f"{request_id}.json",
        {
            "id": request_id,
            "args": args,
            "actor": actor,
            "project": os.environ.get("DREM_PROJECT", "canvas-local"),
        },
    )
    response = responses / f"{request_id}.json"
    deadline = time.monotonic() + float(os.environ.get("DREMCTL_FILE_RELAY_TIMEOUT", "7200"))
    while not response.exists():
        if time.monotonic() >= deadline:
            print("error: timed out waiting for dremctl file relay", file=sys.stderr)
            return 124
        time.sleep(0.05)
    payload = json.loads(response.read_text())
    response.unlink()
    sys.stdout.write(payload.get("stdout", ""))
    sys.stderr.write(payload.get("stderr", ""))
    return int(payload.get("exit_code", 1))


def serve(root, dremctl):
    requests, responses = relay_dirs(root)
    while True:
        for request_path in sorted(requests.glob("*.json")):
            processing = request_path.with_suffix(".processing")
            try:
                os.replace(request_path, processing)
            except FileNotFoundError:
                continue
            try:
                payload = json.loads(processing.read_text())
                request_id = payload["id"]
                args = payload["args"]
                if not isinstance(args, list) or not all(isinstance(arg, str) for arg in args):
                    raise ValueError("args must be a string array")
                env = os.environ.copy()
                env.pop("DREM_ORCH_UNIX_SOCKET", None)
                env["DREM_PROJECT"] = payload.get("project") or "canvas-local"
                if payload.get("actor"):
                    env["DREM_ACTOR"] = payload["actor"]
                completed = subprocess.run(
                    [str(dremctl), *args],
                    env=env,
                    text=True,
                    capture_output=True,
                    timeout=7200,
                    check=False,
                )
                result = {
                    "exit_code": completed.returncode,
                    "stdout": completed.stdout,
                    "stderr": completed.stderr,
                }
            except Exception as exc:
                request_id = payload.get("id", processing.stem) if "payload" in locals() else processing.stem
                result = {"exit_code": 1, "stdout": "", "stderr": f"file relay: {exc}\n"}
            atomic_json(responses / f"{request_id}.json", result)
            processing.unlink(missing_ok=True)
        time.sleep(0.05)


def main():
    if len(sys.argv) >= 2 and sys.argv[1] == "serve":
        parser = argparse.ArgumentParser()
        parser.add_argument("serve")
        parser.add_argument("--root", required=True, type=pathlib.Path)
        parser.add_argument("--dremctl", required=True, type=pathlib.Path)
        args = parser.parse_args()
        if not args.root.is_absolute() or not args.dremctl.is_absolute():
            parser.error("--root and --dremctl must be absolute")
        serve(args.root, args.dremctl)
        return 0
    root = os.environ.get("DREMCTL_FILE_RELAY_ROOT", "")
    if not root or not os.path.isabs(root):
        print("error: DREMCTL_FILE_RELAY_ROOT must be an absolute path", file=sys.stderr)
        return 2
    return client(pathlib.Path(root), sys.argv[1:])


if __name__ == "__main__":
    raise SystemExit(main())
