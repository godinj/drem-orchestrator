#!/usr/bin/env python3
"""Build and attest the immutable CanvasBench proxy/harness image set."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
PINNED_IMAGE = re.compile(r"^[A-Za-z0-9._/:@-]+@sha256:[0-9a-f]{64}$")
REPOSITORY = re.compile(r"^[A-Za-z0-9._-]+(?::[0-9]+)?(?:/[A-Za-z0-9._-]+)+$")
INTEGRITY = re.compile(r"^(?:sha256:[0-9a-f]{64}|sha512-[A-Za-z0-9+/]+={0,2})$")
ORDER = ("usage-proxy", "opencode", "qwen-code", "mini-swe-agent", "pi", "aider", "openhands", "goose", "cline", "continue", "swe-agent")


def run(command: list[str], *, capture: bool = False) -> str:
    completed = subprocess.run(
        command,
        check=True,
        text=True,
        stdout=subprocess.PIPE if capture else None,
    )
    return completed.stdout.strip() if capture else ""


def canonical_hash(value: object) -> str:
    raw = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(raw).hexdigest()


def load_lock(path: Path, root: Path | None = None) -> dict:
    lock = json.loads(path.read_text())
    if lock.get("schema") != "canvasbench.images.v1" or lock.get("platform") != "linux/amd64":
        raise SystemExit("invalid CanvasBench image lock schema/platform")
    if tuple(lock.get("images", {}).keys()) != ORDER:
        raise SystemExit("image lock must contain the canonical image order")
    for base in lock.get("base_images", {}).values():
        if not PINNED_IMAGE.fullmatch(base):
            raise SystemExit(f"base image is not digest-pinned: {base}")
    for name in ORDER:
        image = lock["images"][name]
        required = ("version", "upstream", "dockerfile", "executable", "env_contract", "normalizer")
        if any(not isinstance(image.get(field), str) or not image[field] for field in required if field != "normalizer"):
            raise SystemExit(f"incomplete image lock entry: {name}")
        if name != "usage-proxy" and not INTEGRITY.fullmatch(image.get("integrity", "")):
            raise SystemExit(f"missing or invalid immutable upstream integrity: {name}")
        dockerfile = Path(image["dockerfile"])
        if dockerfile.is_absolute() or ".." in dockerfile.parts:
            raise SystemExit(f"Dockerfile must be repository-relative: {name}")
        if root is not None and not (root / dockerfile).is_file():
            raise SystemExit(f"Dockerfile is missing: {name}")
    if root is not None:
        npm_packages = {
            "opencode": "opencode-ai",
            "qwen-code": "@qwen-code/qwen-code",
            "pi": "@earendil-works/pi-coding-agent",
            "cline": "@cline/cli-linux-x64",
            "continue": "@continuedev/cli",
        }
        for name, package in npm_packages.items():
            package_lock = root / "deploy/docker/canvasbench/context" / name / "package-lock.json"
            locked = json.loads(package_lock.read_text()).get("packages", {}).get("node_modules/" + package, {})
            expected = lock["images"][name]
            if locked.get("version") != expected["version"] or locked.get("integrity") != expected["integrity"]:
                raise SystemExit(f"npm dependency lock does not match image lock: {name}")
        mini_lock = (root / "deploy/docker/canvasbench/mini-swe-agent-requirements.lock").read_text()
        mini = lock["images"]["mini-swe-agent"]
        if f"mini-swe-agent=={mini['version']} " not in mini_lock or f"--hash={mini['integrity']}" not in mini_lock:
            raise SystemExit("mini-SWE-agent dependency lock does not match image lock")
        swe_agent_lock = (root / "deploy/docker/canvasbench/swe-agent-requirements.lock").read_text()
        if "swe-rex==1.2.1 " not in swe_agent_lock:
            raise SystemExit("SWE-agent dependency lock does not pin the recommended SWE-ReX runtime")
    return lock


def source_state(revision: str, image: dict, name: str) -> str:
    if name == "usage-proxy":
        return revision
    return f"{revision}+{image['upstream']}#{image['integrity']}"


def expected_labels(name: str, image: dict, state: str) -> dict[str, str]:
    labels = {
        "org.opencontainers.image.source": image["upstream"],
        "org.opencontainers.image.revision": state,
        "org.opencontainers.image.version": image["version"],
        "io.drem.source-state": state,
        "io.drem.canvasbench.harness": name,
        "io.drem.canvasbench.upstream": image["upstream"],
        "io.drem.canvasbench.env-contract": image["env_contract"],
    }
    if image["normalizer"]:
        labels["io.drem.canvasbench.normalizer"] = image["normalizer"]
    return labels


def build_args(lock: dict, name: str, image: dict, state: str) -> list[str]:
    values = {
        "SOURCE_STATE": state,
        "UPSTREAM_SOURCE": image["upstream"],
        "HARNESS_VERSION": image["version"],
        "ENV_CONTRACT": image["env_contract"],
        "NORMALIZER": image["normalizer"],
        "UPSTREAM_INTEGRITY": image.get("integrity", ""),
    }
    if name == "usage-proxy":
        values["GO_BASE_IMAGE"] = lock["base_images"]["golang"]
        values["RUNTIME_BASE_IMAGE"] = lock["base_images"]["runtime"]
    elif name in {"mini-swe-agent", "aider", "openhands", "goose", "swe-agent"}:
        values["PYTHON_BASE_IMAGE"] = lock["base_images"]["python"]
    else:
        values["NODE_BASE_IMAGE"] = lock["base_images"]["node"]
    result: list[str] = []
    for key in sorted(values):
        result.extend(("--build-arg", f"{key}={values[key]}"))
    return result


def inspect_image(tag: str, expected: dict[str, str]) -> None:
    raw = run(["docker", "image", "inspect", tag, "--format", "{{json .Config}}"], capture=True)
    config = json.loads(raw)
    if config.get("User") != "65532:65532":
        raise SystemExit(f"image does not run as uid/gid 65532: {tag}")
    labels = config.get("Labels") or {}
    for key, value in expected.items():
        if labels.get(key) != value:
            raise SystemExit(f"image label mismatch for {tag}: {key}")
    serialized = json.dumps(config, sort_keys=True)
    if re.search(r"(?i)(api[_-]?key|token|secret)[=:][^,}\s]+", serialized):
        raise SystemExit(f"image configuration appears to contain a baked credential: {tag}")


def build(options: argparse.Namespace) -> None:
    root = Path(__file__).resolve().parents[3]
    lock_path = (root / options.lock).resolve() if not Path(options.lock).is_absolute() else Path(options.lock)
    lock = load_lock(lock_path, root)
    repository = options.repository.rstrip("/")
    if not REPOSITORY.fullmatch(repository) or repository.endswith(":latest") or "@" in repository:
        raise SystemExit("repository must be a tag-free OCI repository name")
    if not options.publish:
        raise SystemExit("immutable runtime images require explicit --publish to a trusted registry")
    if run(["git", "status", "--porcelain=v1", "--untracked-files=all"], capture=True):
        raise SystemExit("CanvasBench images require a clean, committed source tree")
    revision = run(["git", "rev-parse", "HEAD"], capture=True)
    source_epoch = run(["git", "show", "-s", "--format=%ct", revision], capture=True)
    lock_sha = hashlib.sha256(lock_path.read_bytes()).hexdigest()
    records: dict[str, dict] = {}
    for name in ORDER:
        image = lock["images"][name]
        state = source_state(revision, image, name)
        tag = f"{repository}/{name}:{image['version']}-{revision[:12]}"
        with tempfile.TemporaryDirectory(prefix="canvasbench-build-") as build_directory:
            metadata = Path(build_directory) / "metadata.json"
            command = [
                "docker", "buildx", "build", "--platform", lock["platform"], "--push",
                "--provenance=false", "--metadata-file", str(metadata),
                "--tag", tag, "--file", str(root / image["dockerfile"]),
                "--build-arg", f"SOURCE_DATE_EPOCH={source_epoch}",
            ]
            command.extend(build_args(lock, name, image, state))
            command.append(str(root))
            run(command)
            build_metadata = json.loads(metadata.read_text())
        digest = build_metadata.get("containerimage.digest", "")
        if not DIGEST.fullmatch(digest):
            raise SystemExit(f"build did not report an immutable digest: {name}")
        image_ref = f"{tag}@{digest}"
        run(["docker", "pull", "--platform", lock["platform"], image_ref])
        inspect_image(image_ref, expected_labels(name, image, state))
        config = {
            "schema": "canvasbench.image-config.v1",
            "image": image_ref,
            "source_state": state,
            "upstream": image["upstream"],
            "upstream_integrity": image.get("integrity", ""),
            "executable": image["executable"],
            "env_contract": image["env_contract"],
            "normalizer": image["normalizer"],
            "lock_sha256": lock_sha,
        }
        records[name] = {**config, "config_sha256": canonical_hash(config)}
    proxy = records["usage-proxy"]
    runtime_config = {
        "schema": "canvasbench.usage-proxy-config.v1",
        "listen": options.proxy_listen,
        "public_base_url": options.proxy_public_base_url,
        "upstream_chat_completions": options.proxy_upstream_url,
        "read_timeout": "30m",
        "write_timeout": "30m",
        "idle_timeout": "30s",
        "image": proxy["image"],
        "source_state": proxy["source_state"],
    }
    proxy["usage_proxy_config_sha256"] = canonical_hash(runtime_config)
    output = {
        "schema": "canvasbench.image-attestation.v1",
        "platform": lock["platform"],
        "revision": revision,
        "lock_sha256": lock_sha,
        "proxy_runtime": runtime_config,
        "images": records,
    }
    destination = Path(options.output).resolve()
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_suffix(destination.suffix + ".tmp")
    temporary.write_text(json.dumps(output, indent=2, sort_keys=True) + "\n")
    os.chmod(temporary, 0o644)
    os.replace(temporary, destination)
    print(destination)


def validate(options: argparse.Namespace) -> None:
    root = Path(__file__).resolve().parents[3]
    lock_path = (root / options.lock).resolve() if not Path(options.lock).is_absolute() else Path(options.lock)
    lock = load_lock(lock_path, root)
    print(json.dumps({
        "schema": lock["schema"],
        "platform": lock["platform"],
        "images": list(lock["images"]),
        "lock_sha256": hashlib.sha256(lock_path.read_bytes()).hexdigest(),
    }, sort_keys=True))


def main() -> None:
    parser = argparse.ArgumentParser()
    subcommands = parser.add_subparsers(dest="command", required=True)
    validation = subcommands.add_parser("validate")
    validation.add_argument("--lock", default="deploy/docker/canvasbench/locks.json")
    command = subcommands.add_parser("build")
    command.add_argument("--lock", default="deploy/docker/canvasbench/locks.json")
    command.add_argument("--repository", default="canvasbench-local")
    command.add_argument("--publish", action="store_true", help="push exact images to the named trusted registry")
    command.add_argument("--output", required=True)
    command.add_argument("--proxy-listen", default="0.0.0.0:8080")
    command.add_argument("--proxy-public-base-url", default="http://canvasbench-usage-proxy:8080/v1")
    command.add_argument("--proxy-upstream-url", default="http://canvasbench-fake-openai:8082/v1/chat/completions")
    options = parser.parse_args()
    if options.command == "validate":
        validate(options)
    elif options.command == "build":
        build(options)


if __name__ == "__main__":
    main()
