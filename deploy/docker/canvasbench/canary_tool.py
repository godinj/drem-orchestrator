#!/usr/bin/env python3
"""Run the canonical CanvasBench harness images against a deterministic fake model.

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


HARNESSES = ("opencode", "qwen-code", "mini-swe-agent", "pi", "aider", "openhands", "goose", "cline", "continue", "swe-agent")
PINNED_IMAGE = re.compile(r"^[A-Za-z0-9._/:@+-]+:[A-Za-z0-9._+-]+@sha256:[0-9a-f]{64}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
PROMPT = "Respond with exactly CANVASBENCH_CANARY_OK and do not use tools."


class UnsupportedCanary(RuntimeError):
    pass


def run(
    command: list[str],
    *,
    capture: bool = False,
    check: bool = True,
    input_text: str | None = None,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        command,
        check=check,
        text=True,
        input=input_text,
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


def docker_admin_json(
    image: str,
    network: str,
    secret_volume: str,
    url: str,
    method: str = "GET",
    *,
    attempts: int = 1,
    body: dict | None = None,
) -> dict:
    script = (
        "import json,pathlib,sys,time,urllib.request; "
        "token=pathlib.Path('/run/secrets/admin.token').read_text().strip(); "
        "url,method,attempts,raw=sys.argv[1],sys.argv[2],int(sys.argv[3]),sys.argv[4]; last=None; "
        "\nfor _ in range(attempts):\n"
        " try:\n"
        "  data=raw.encode() if raw else None; headers={'Authorization':'Bearer '+token}; "
        "headers.update({'Content-Type':'application/json'} if data else {}); "
        "request=urllib.request.Request(url,data=data,method=method,headers=headers); "
        "response=urllib.request.urlopen(request,timeout=5); print(json.dumps(json.load(response))); sys.exit(0)\n"
        " except Exception as error:\n  last=error; time.sleep(0.25)\n"
        "raise SystemExit(str(last))"
    )
    completed = run([
        "docker", "run", "--rm", "--network", network, "--user", "65532:65532",
        "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
        "--mount", f"type=volume,src={secret_volume},dst=/run/secrets,readonly",
        image, "python", "-c", script, url, method, str(attempts),
        json.dumps(body, sort_keys=True, separators=(",", ":")) if body is not None else "",
    ], capture=True, check=False)
    if completed.returncode != 0:
        raise UnsupportedCanary(f"trusted proxy admin endpoint failed: {completed.stderr.strip()[-1200:]}")
    try:
        return json.loads(completed.stdout)
    except json.JSONDecodeError as error:
        raise UnsupportedCanary(f"trusted proxy admin response is invalid: {error}") from error


def harness_command(name: str) -> list[str]:
    if name == "opencode":
        return ["opencode", "run", "--pure", "--auto", "--format", "json", "--agent", "build", "--dir", "/workspace", "--model", "canvasbench/canary", PROMPT]
    if name == "qwen-code":
        return ["qwen", "--prompt", PROMPT, "--output-format", "stream-json", "--system-prompt", PROMPT, "--model", "canvasbench-canary", "--auth-type", "openai", "--safe-mode", "--yolo", "--max-tool-calls", "1", "--max-session-turns", "2", "--max-wall-time", "60s", "--exclude-tools", "agent"]
    if name == "mini-swe-agent":
        return ["mini", "-t", PROMPT, "-m", "openai/canvasbench-canary", "-y", "--exit-immediately", "-o", "/workspace/.canvasbench/mini-swe-agent-trajectory.json"]
    if name == "pi":
        return ["pi", "--mode", "json", "--no-session", "--no-context-files", "--model", "canvasbench/canary", "--system-prompt", PROMPT, PROMPT]
    if name == "aider":
        return ["aider", "--model", "openai/canvasbench-canary", "--message", PROMPT, "--edit-format", "diff", "--yes-always", "--no-git", "--no-auto-commits", "--no-dirty-commits", "--no-stream", "--no-pretty", "--no-check-update", "--no-analytics", "--no-cache-prompts", "--map-tokens", "0"]
    if name == "openhands":
        return ["openhands", "--model", "openai/canvasbench-canary", "--headless", "--json", "--yolo", "--override-with-envs", "-t", PROMPT]
    if name == "goose":
        return ["goose", "run", "--no-session", "--no-profile", "--with-builtin", "developer", "--provider", "openai", "--model", "canvasbench-canary", "--max-turns", "2", "--quiet", "--output-format", "json", "--text", PROMPT]
    if name == "cline":
        return ["cline", "--json", "--auto-approve", "--cwd", "/workspace", "--provider", "openai-compatible", "--model", "canvasbench-canary", "--system", PROMPT, "--retries", "1", "--timeout", "60", PROMPT]
    if name == "continue":
        return ["cn", "--config", "__CANVASBENCH_CONFIG__", "--auto", "--print", "--format", "json", PROMPT]
    if name == "swe-agent":
        return ["sweagent", "--canvasbench-task", PROMPT]
    raise UnsupportedCanary(f"unsupported canary harness {name!r}")


def validate_wire(root: Path, harness: str, capture_path: Path) -> None:
    command = ["go", "run", "./cmd/canvasbench-canary-validate", "-harness", harness, "-input", str(capture_path)]
    completed = run(command, capture=True, check=False)
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout).strip()
        wire = capture_path.read_text(errors="replace").strip()[-2400:]
        raise UnsupportedCanary(f"{harness} normalizer wire rejected: {detail}; wire tail: {wire}")


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
        raise UnsupportedCanary("harness selection must be unique members of the canonical set")

    run_id = secrets.token_hex(6)
    network = f"canvasbench-canary-{run_id}"
    secret_volume = f"canvasbench-canary-secrets-{run_id}"
    proxy_name = f"canvasbench-usage-proxy-{run_id}"
    fake_name = f"canvasbench-fake-openai-{run_id}"
    temporary = Path(tempfile.mkdtemp(prefix="canvasbench-canary-"))
    admin_token = secrets.token_urlsafe(32)
    records: list[dict] = []
    created_network = False
    created_secret_volume = False
    containers: list[str] = []
    try:
        fake_image = lock["base_images"]["python"]
        run(["docker", "volume", "create", secret_volume], capture=True)
        created_secret_volume = True
        run([
            "docker", "run", "--rm", "--interactive", "--network", "none", "--user", "0:0",
            "--read-only", "--cap-drop", "ALL", "--cap-add", "CHOWN",
            "--security-opt", "no-new-privileges",
            "--mount", f"type=volume,src={secret_volume},dst=/run/secrets",
            fake_image, "python", "-c",
            "import os,pathlib,sys; p=pathlib.Path('/run/secrets/admin.token'); "
            "p.write_text(sys.stdin.read()); p.chmod(0o400); os.chown(p,65532,65532)",
        ], input_text=admin_token + "\n")
        run(["docker", "network", "create", "--internal", network])
        created_network = True
        fake_script = root / "deploy/docker/canvasbench/fake_openai.py"
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
            "--network-alias", "canvasbench-usage-proxy",
            "--user", "65532:65532", "--read-only", "--cap-drop", "ALL",
            "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m",
            "--mount", f"type=volume,src={secret_volume},dst=/run/secrets,readonly",
            proxy["image"], "--listen", "0.0.0.0:8080",
            "--public-base-url", proxy_runtime["public_base_url"],
            "--upstream", proxy_runtime["upstream_chat_completions"],
            "--admin-token-file", "/run/secrets/admin.token",
            "--source-state", proxy["source_state"], "--image", proxy["image"],
            "--config-sha256", proxy["usage_proxy_config_sha256"],
        ], capture=True)
        containers.append(proxy_name)
        admin_url = "http://canvasbench-usage-proxy:8080"
        live = docker_admin_json(
            fake_image, network, secret_volume, admin_url + "/admin/v1/attestation", attempts=40,
        )
        expected_live = {"source_state": proxy["source_state"], "image": proxy["image"], "config_sha256": proxy["usage_proxy_config_sha256"]}
        if live != expected_live:
            raise UnsupportedCanary("live trusted proxy identity does not match the build attestation")

        for harness in selected:
            trial_policy = {
                "model_id": "canvasbench-canary-runtime", "seed": 42,
                "temperature": 0.2, "top_p": 0.9, "top_k": 20,
                "context_window": 32768, "max_output_tokens": 1024,
                "preserve_thinking": True,
            }
            session = docker_admin_json(
                fake_image, network, secret_volume, admin_url + "/admin/v1/trials", "POST", body=trial_policy,
            )
            if not all(session.get(key) for key in ("correlation_id", "base_url", "api_key")):
                raise UnsupportedCanary(f"{harness} received an incomplete trial credential")
            workspace = temporary / f"workspace-{harness}"
            capture = temporary / f"{harness}.jsonl"
            (workspace / ".canvasbench").mkdir(parents=True)
            os.chmod(workspace, 0o777)
            os.chmod(workspace / ".canvasbench", 0o777)
            env_file = temporary / f"{harness}.env"
            base_key = "OPENAI_API_BASE" if harness == "mini-swe-agent" else "OPENAI_BASE_URL"
            environment = (
                f"{base_key}={session['base_url']}\nOPENAI_API_KEY={session['api_key']}\n"
                "CANVASBENCH_SEED=42\nCANVASBENCH_TEMPERATURE=0.2\n"
                "CANVASBENCH_TOP_P=0.9\nCANVASBENCH_TOP_K=20\n"
                "CANVASBENCH_CONTEXT_WINDOW=32768\nCANVASBENCH_MAX_OUTPUT_TOKENS=1024\n"
                "CANVASBENCH_MAX_ITERATIONS=2\n"
                "CANVASBENCH_PRESERVE_THINKING=true\nCANVASBENCH_ADAPTER_MODEL=canvasbench-canary"
            )
            if harness == "swe-agent":
                environment = environment.replace(
                    "CANVASBENCH_ADAPTER_MODEL=canvasbench-canary",
                    "CANVASBENCH_ADAPTER_MODEL=openai/canvasbench-canary",
                )
            if harness == "mini-swe-agent":
                environment += "\nMSWEA_CONFIGURED=true\nMSWEA_COST_TRACKING=ignore_errors\nMSWEA_GLOBAL_CONFIG_DIR=/tmp/mini-swe-agent\nMSWEA_SILENT_STARTUP=1"
            write_secret(env_file, environment)
            image = attestation["images"][harness]["image"]
            command = [
                "docker", "run", "--rm", "--network", network, "--user", "65532:65532",
                "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
                "--env-file", str(env_file), "--workdir", "/workspace",
                "--mount", f"type=bind,src={workspace},dst=/workspace",
                "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m,uid=65532,gid=65532,mode=1777",
                "--tmpfs", "/home/bench:rw,nosuid,size=64m,uid=65532,gid=65532,mode=0700",
                "--tmpfs", "/root:rw,nosuid,size=64m,uid=65532,gid=65532,mode=0700",
                image, *harness_command(harness),
            ]
            completed = run(command, capture=True, check=False)
            capture.write_text(completed.stdout)
            if completed.returncode != 0:
                detail = (completed.stdout + "\n" + completed.stderr).replace(
                    session["api_key"], "[REDACTED]",
                ).strip()[-2400:]
                raise UnsupportedCanary(f"{harness} executable/CLI contract failed: {detail}")
            if harness == "mini-swe-agent":
                capture = workspace / ".canvasbench" / "mini-swe-agent-trajectory.json"
                if not capture.is_file():
                    raise UnsupportedCanary("mini-swe-agent did not create its declared trajectory")
            validate_wire(root, harness, capture)
            usage = docker_admin_json(
                fake_image,
                network,
                secret_volume,
                admin_url + f"/admin/v1/trials/{session['correlation_id']}/consume",
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
        if created_secret_volume:
            run(["docker", "volume", "rm", "--force", secret_volume], capture=True, check=False)
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
