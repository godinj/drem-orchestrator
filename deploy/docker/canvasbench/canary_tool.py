#!/usr/bin/env python3
"""Run the four CanvasBench harness images against a deterministic fake model.

This is a runtime compatibility canary, not an inference benchmark. Any image,
environment, CLI, proxy-ledger, or normalizer mismatch is reported as an
explicit unsupported harness instead of being guessed around.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


HARNESSES = ("opencode", "qwen-code", "mini-swe-agent", "pi")
PINNED_IMAGE = re.compile(r"^[A-Za-z0-9._/:@+-]+:[A-Za-z0-9._+-]+@sha256:[0-9a-f]{64}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
PROMPT = "Respond with exactly CANVASBENCH_CANARY_OK and do not use tools."


class UnsupportedCanary(RuntimeError):
    pass


def run(command: list[str], *, capture: bool = False, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        command,
        check=check,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )


def load_inputs(attestation_path: Path, lock_path: Path) -> tuple[dict, dict]:
    attestation = json.loads(attestation_path.read_text())
    lock = json.loads(lock_path.read_text())
    if attestation.get("schema") != "canvasbench.image-attestation.v1":
        raise UnsupportedCanary("image attestation schema is unsupported")
    if lock.get("schema") != "canvasbench.images.v1":
        raise UnsupportedCanary("image lock schema is unsupported")
    if attestation.get("platform") != "linux/amd64" or lock.get("platform") != "linux/amd64":
        raise UnsupportedCanary("canary requires linux/amd64 images")
    if attestation.get("lock_sha256") != hashlib.sha256(lock_path.read_bytes()).hexdigest():
        raise UnsupportedCanary("image attestation does not bind the supplied lock")
    images = attestation.get("images", {})
    canonical_images = {"usage-proxy", *HARNESSES}
    if set(images) != canonical_images or len(images) != len(canonical_images):
        raise UnsupportedCanary("image attestation does not contain the canonical image set")
    for name, record in images.items():
        if not PINNED_IMAGE.fullmatch(record.get("image", "")):
            raise UnsupportedCanary(f"{name} image is not content-addressed")
        expected = lock["images"].get(name, {})
        for key in ("executable", "env_contract", "normalizer"):
            if record.get(key) != expected.get(key):
                raise UnsupportedCanary(f"{name} {key} does not match the lock")
        if not SHA256.fullmatch(record.get("config_sha256", "")):
            raise UnsupportedCanary(f"{name} config hash is invalid")
    proxy_runtime = attestation.get("proxy_runtime", {})
    proxy = images["usage-proxy"]
    if proxy_runtime.get("image") != proxy["image"] or proxy_runtime.get("source_state") != proxy["source_state"]:
        raise UnsupportedCanary("proxy runtime identity is not bound to its image record")
    if proxy.get("usage_proxy_config_sha256") != hashlib.sha256(
        json.dumps(proxy_runtime, sort_keys=True, separators=(",", ":")).encode()
    ).hexdigest():
        raise UnsupportedCanary("proxy runtime config hash does not match")
    supported_proxy_runtime = {
        "listen": "0.0.0.0:8080",
        "public_base_url": "http://canvasbench-usage-proxy:8080/v1",
        "upstream_chat_completions": "http://canvasbench-fake-openai:8082/v1/chat/completions",
    }
    for key, value in supported_proxy_runtime.items():
        if proxy_runtime.get(key) != value:
            raise UnsupportedCanary(f"proxy runtime {key} is unsupported by this canary")
    return attestation, lock


def admin_json(url: str, token: str, method: str = "GET", *, attempts: int = 1) -> dict:
    last_error: Exception | None = None
    for _ in range(attempts):
        request = urllib.request.Request(url, method=method, headers={"Authorization": f"Bearer {token}"})
        try:
            with urllib.request.urlopen(request, timeout=5) as response:
                return json.load(response)
        except (OSError, urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError) as error:
            last_error = error
            time.sleep(0.25)
    raise UnsupportedCanary(f"trusted proxy admin endpoint failed: {last_error}")


def harness_command(name: str) -> list[str]:
    if name == "opencode":
        return ["opencode", "run", "--pure", "--auto", "--format", "json", "--agent", "build", "--dir", "/workspace", "--model", "canvasbench/canary", PROMPT]
    if name == "qwen-code":
        return ["qwen", "--prompt", PROMPT, "--output-format", "stream-json", "--system-prompt", PROMPT, "--model", "canvasbench-canary", "--safe-mode", "--yolo", "--max-tool-calls", "1", "--max-session-turns", "2", "--max-wall-time", "60s", "--exclude-tools", "agent"]
    if name == "mini-swe-agent":
        return ["mini", "-t", PROMPT, "-m", "openai/canvasbench-canary", "-y", "--exit-immediately", "-o", "/workspace/.canvasbench/mini-swe-agent-trajectory.json"]
    if name == "pi":
        return ["pi", "--mode", "json", "--no-session", "--no-context-files", "--model", "canvasbench/canary", "--system-prompt", PROMPT, PROMPT]
    raise UnsupportedCanary(f"unsupported canary harness {name!r}")


def validate_wire(root: Path, harness: str, capture_path: Path) -> None:
    command = ["go", "run", "./cmd/canvasbench-canary-validate", "-harness", harness, "-input", str(capture_path)]
    completed = run(command, capture=True, check=False)
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout).strip()
        raise UnsupportedCanary(f"{harness} normalizer wire rejected: {detail}")


def write_secret(path: Path, value: str) -> None:
    path.write_text(value.rstrip("\n") + "\n")
    path.chmod(0o600)


def run_canary(options: argparse.Namespace) -> None:
    root = Path(__file__).resolve().parents[3]
    attestation_path = Path(options.attestation).resolve()
    lock_path = (root / options.lock).resolve() if not Path(options.lock).is_absolute() else Path(options.lock)
    attestation, lock = load_inputs(attestation_path, lock_path)
    selected = tuple(options.harness or HARNESSES)
    if not selected or any(name not in HARNESSES for name in selected) or len(set(selected)) != len(selected):
        raise UnsupportedCanary("harness selection must be unique members of the canonical four")

    run_id = secrets.token_hex(6)
    network = f"canvasbench-canary-{run_id}"
    proxy_name = f"canvasbench-usage-proxy-{run_id}"
    fake_name = f"canvasbench-fake-openai-{run_id}"
    temporary = Path(tempfile.mkdtemp(prefix="canvasbench-canary-"))
    admin_token = secrets.token_urlsafe(32)
    admin_file = temporary / "admin.token"
    write_secret(admin_file, admin_token)
    records: list[dict] = []
    created_network = False
    containers: list[str] = []
    try:
        run(["docker", "network", "create", "--internal", network])
        created_network = True
        fake_script = root / "deploy/docker/canvasbench/fake_openai.py"
        fake_image = lock["base_images"]["python"]
        run([
            "docker", "run", "--detach", "--name", fake_name, "--network", network,
            "--network-alias", "canvasbench-fake-openai", "--user", "65532:65532",
            "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
            "--mount", f"type=bind,src={fake_script},dst=/opt/canvasbench/fake_openai.py,readonly",
            fake_image, "python", "/opt/canvasbench/fake_openai.py",
        ], capture=True)
        containers.append(fake_name)

        proxy = attestation["images"]["usage-proxy"]
        proxy_runtime = attestation["proxy_runtime"]
        run([
            "docker", "run", "--detach", "--name", proxy_name, "--network", network,
            "--network-alias", "canvasbench-usage-proxy", "--publish", "127.0.0.1::8080",
            "--user", "65532:65532", "--read-only", "--cap-drop", "ALL",
            "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m",
            "--mount", f"type=bind,src={admin_file},dst=/run/secrets/admin.token,readonly",
            proxy["image"], "--listen", "0.0.0.0:8080",
            "--public-base-url", proxy_runtime["public_base_url"],
            "--upstream", proxy_runtime["upstream_chat_completions"],
            "--admin-token-file", "/run/secrets/admin.token",
            "--source-state", proxy["source_state"], "--image", proxy["image"],
            "--config-sha256", proxy["usage_proxy_config_sha256"],
        ], capture=True)
        containers.append(proxy_name)
        published = run(["docker", "port", proxy_name, "8080/tcp"], capture=True).stdout.strip()
        match = re.fullmatch(r"127\.0\.0\.1:(\d+)", published)
        if not match:
            raise UnsupportedCanary(f"trusted proxy host port is unsupported: {published!r}")
        admin_url = f"http://127.0.0.1:{match.group(1)}"
        live = admin_json(admin_url + "/admin/v1/attestation", admin_token, attempts=40)
        expected_live = {"source_state": proxy["source_state"], "image": proxy["image"], "config_sha256": proxy["usage_proxy_config_sha256"]}
        if live != expected_live:
            raise UnsupportedCanary("live trusted proxy identity does not match the build attestation")

        for harness in selected:
            session = admin_json(admin_url + "/admin/v1/trials", admin_token, "POST")
            if not all(session.get(key) for key in ("correlation_id", "base_url", "api_key")):
                raise UnsupportedCanary(f"{harness} received an incomplete trial credential")
            workspace = temporary / f"workspace-{harness}"
            capture = temporary / f"{harness}.jsonl"
            (workspace / ".canvasbench").mkdir(parents=True)
            os.chmod(workspace, 0o777)
            os.chmod(workspace / ".canvasbench", 0o777)
            env_file = temporary / f"{harness}.env"
            base_key = "OPENAI_API_BASE" if harness == "mini-swe-agent" else "OPENAI_BASE_URL"
            write_secret(env_file, f"{base_key}={session['base_url']}\nOPENAI_API_KEY={session['api_key']}")
            image = attestation["images"][harness]["image"]
            command = [
                "docker", "run", "--rm", "--network", network, "--user", "65532:65532",
                "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
                "--env-file", str(env_file), "--workdir", "/workspace",
                "--mount", f"type=bind,src={workspace},dst=/workspace",
                "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m,uid=65532,gid=65532,mode=1777",
                "--tmpfs", "/home/bench:rw,nosuid,size=64m,uid=65532,gid=65532,mode=0700",
                image, *harness_command(harness),
            ]
            completed = run(command, capture=True, check=False)
            capture.write_text(completed.stdout)
            if completed.returncode != 0:
                detail = completed.stderr.replace(session["api_key"], "[REDACTED]").strip()[-1200:]
                raise UnsupportedCanary(f"{harness} executable/CLI contract failed: {detail}")
            if harness == "mini-swe-agent":
                capture = workspace / ".canvasbench" / "mini-swe-agent-trajectory.json"
                if not capture.is_file():
                    raise UnsupportedCanary("mini-swe-agent did not create its declared trajectory")
            validate_wire(root, harness, capture)
            usage = admin_json(
                admin_url + f"/admin/v1/trials/{session['correlation_id']}/consume",
                admin_token,
                "POST",
            )
            if usage.get("source") != "trusted_usage_proxy" or not usage.get("complete") or usage.get("requests_measured", 0) <= 0 or usage.get("requests_measured") != usage.get("requests_total"):
                raise UnsupportedCanary(f"{harness} trusted usage ledger is incomplete")
            records.append({
                "harness": harness,
                "image": image,
                "config_sha256": attestation["images"][harness]["config_sha256"],
                "normalizer": attestation["images"][harness]["normalizer"],
                "env_contract": attestation["images"][harness]["env_contract"],
                "usage": usage,
                "status": "supported",
            })
    finally:
        for container in reversed(containers):
            run(["docker", "rm", "--force", container], capture=True, check=False)
        if created_network:
            run(["docker", "network", "rm", network], capture=True, check=False)
        shutil.rmtree(temporary, ignore_errors=True)

    result = {
        "schema": "canvasbench.image-canary.v1",
        "image_attestation_sha256": hashlib.sha256(attestation_path.read_bytes()).hexdigest(),
        "proxy_image": attestation["images"]["usage-proxy"]["image"],
        "canaries": records,
    }
    destination = Path(options.output).resolve()
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary_output = destination.with_suffix(destination.suffix + ".tmp")
    temporary_output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
    os.chmod(temporary_output, 0o644)
    os.replace(temporary_output, destination)
    print(destination)


def validate(options: argparse.Namespace) -> None:
    root = Path(__file__).resolve().parents[3]
    lock_path = (root / options.lock).resolve() if not Path(options.lock).is_absolute() else Path(options.lock)
    attestation, _ = load_inputs(Path(options.attestation).resolve(), lock_path)
    print(json.dumps({"schema": attestation["schema"], "harnesses": list(HARNESSES)}, sort_keys=True))


def main() -> None:
    parser = argparse.ArgumentParser()
    subcommands = parser.add_subparsers(dest="command", required=True)
    for name in ("validate", "run"):
        command = subcommands.add_parser(name)
        command.add_argument("--attestation", required=True)
        command.add_argument("--lock", default="deploy/docker/canvasbench/locks.json")
        if name == "run":
            command.add_argument("--harness", action="append", choices=HARNESSES)
            command.add_argument("--output", required=True)
    options = parser.parse_args()
    try:
        if options.command == "validate":
            validate(options)
        else:
            run_canary(options)
    except UnsupportedCanary as error:
        print(f"unsupported canary: {error}", file=sys.stderr)
        raise SystemExit(1) from error


if __name__ == "__main__":
    main()
