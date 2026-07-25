#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../../.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/canvasbench-image-tools.XXXXXX")
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM
mkdir -p "$TEST_ROOT/bin" "$TEST_ROOT/state"

python3 - "$TEST_ROOT/bin/git" "$TEST_ROOT/bin/docker" <<'PY'
import os
from pathlib import Path
import sys

git_path, docker_path = map(Path, sys.argv[1:])
git_path.write_text('''#!/bin/sh
set -eu
case "$1 $2" in
  "status --porcelain=v1") exit 0 ;;
  "rev-parse HEAD") printf '%s\\n' aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ;;
  "show -s") printf '%s\\n' 1700000000 ;;
  *) printf 'unexpected fake git invocation: %s\\n' "$*" >&2; exit 2 ;;
esac
''')
docker_path.write_text('''#!/usr/bin/env python3
import hashlib
import json
import os
from pathlib import Path
import re
import sys

args = sys.argv[1:]
state = Path(os.environ["FAKE_DOCKER_STATE"])
if args[:2] == ["buildx", "build"]:
    def value(flag):
        return args[args.index(flag) + 1]
    tag = value("--tag")
    metadata = Path(value("--metadata-file"))
    build_args = {}
    for index, item in enumerate(args):
        if item == "--build-arg":
            key, val = args[index + 1].split("=", 1)
            build_args[key] = val
    name = tag.rsplit("/", 1)[-1].split(":", 1)[0]
    labels = {
        "org.opencontainers.image.source": build_args["UPSTREAM_SOURCE"],
        "org.opencontainers.image.revision": build_args["SOURCE_STATE"],
        "org.opencontainers.image.version": build_args["HARNESS_VERSION"],
        "io.drem.source-state": build_args["SOURCE_STATE"],
        "io.drem.canvasbench.harness": name,
        "io.drem.canvasbench.upstream": build_args["UPSTREAM_SOURCE"],
        "io.drem.canvasbench.env-contract": build_args["ENV_CONTRACT"],
    }
    if build_args["NORMALIZER"]:
        labels["io.drem.canvasbench.normalizer"] = build_args["NORMALIZER"]
    key = hashlib.sha256(tag.encode()).hexdigest()
    (state / (key + ".json")).write_text(json.dumps({"User": "65532:65532", "Labels": labels}))
    replacement = metadata.with_suffix(".replacement")
    replacement.write_text(json.dumps({"containerimage.digest": "sha256:" + hashlib.sha256((tag + "-digest").encode()).hexdigest()}))
    os.replace(replacement, metadata)
    sys.exit(0)
if args[:2] == ["image", "inspect"]:
    tag = args[2].split("@", 1)[0]
    key = hashlib.sha256(tag.encode()).hexdigest()
    print((state / (key + ".json")).read_text())
    sys.exit(0)
if args[:1] == ["pull"]:
    sys.exit(0)
raise SystemExit("unexpected fake docker invocation: " + " ".join(args))
''')
os.chmod(git_path, 0o755)
os.chmod(docker_path, 0o755)
PY

PATH="$TEST_ROOT/bin:$PATH" FAKE_DOCKER_STATE="$TEST_ROOT/state" \
  python3 "$SCRIPT_DIR/image_tool.py" build \
  --repository localhost:5000/canvasbench \
  --publish \
  --output "$TEST_ROOT/attestation.json"

python3 "$SCRIPT_DIR/canary_tool.py" validate --attestation "$TEST_ROOT/attestation.json"
grep -q 'type=volume,src={secret_volume},dst=/run/secrets,readonly' "$SCRIPT_DIR/canary_tool.py"
grep -q '"--rm", "--interactive", "--network", "none"' "$SCRIPT_DIR/canary_tool.py"
grep -q '"--auth-type", "openai"' "$SCRIPT_DIR/canary_tool.py"
grep -q 'MSWEA_CONFIGURED=true' "$SCRIPT_DIR/context/mini-swe-agent/mini-wrapper.sh"
grep -q 'MSWEA_COST_TRACKING=ignore_errors' "$SCRIPT_DIR/context/mini-swe-agent/mini-wrapper.sh"
grep -q 'volume", "rm", "--force", secret_volume' "$SCRIPT_DIR/canary_tool.py"
if grep -q '"--publish", "127.0.0.1::8080"' "$SCRIPT_DIR/canary_tool.py"; then
  echo "internal canary proxy unexpectedly publishes a host port" >&2
  exit 1
fi
if grep -q 'type=bind,src={admin_file}' "$SCRIPT_DIR/canary_tool.py"; then
  echo "admin token unexpectedly uses a host bind mount" >&2
  exit 1
fi
python3 - "$SCRIPT_DIR/canary_tool.py" <<'PY'
import importlib.util
from pathlib import Path
import subprocess
import sys

spec = importlib.util.spec_from_file_location("canvasbench_canary_tool", Path(sys.argv[1]))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
def fake_run(command, **_kwargs):
    assert command[:3] == ["docker", "run", "--rm"]
    assert command[command.index("--network") + 1] == "isolated"
    assert "type=volume,src=secrets,dst=/run/secrets,readonly" in command
    return subprocess.CompletedProcess(command, 0, '{"ok": true}\n', "")

module.run = fake_run
assert module.docker_admin_json("python@sha256:digest", "isolated", "secrets", "http://proxy/admin") == {"ok": True}
PY
python3 - "$SCRIPT_DIR/fake_openai.py" <<'PY'
import importlib.util
import json
from pathlib import Path
import sys

spec = importlib.util.spec_from_file_location("canvasbench_fake_openai", Path(sys.argv[1]))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

plain = module.non_stream_response({"model": "canary"})
assert plain["choices"][0]["message"]["content"] == "CANVASBENCH_CANARY_OK"
assert plain["choices"][0]["finish_reason"] == "stop"

mini = module.non_stream_response({
    "model": "canary",
    "tools": [{"type": "function", "function": {"name": "bash"}}],
})
choice = mini["choices"][0]
assert choice["finish_reason"] == "tool_calls"
call = choice["message"]["tool_calls"][0]
assert call["function"]["name"] == "bash"
command = json.loads(call["function"]["arguments"])["command"]
assert "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT" in command
assert "CANVASBENCH_CANARY_OK" in command
PY
python3 - "$TEST_ROOT/attestation.json" <<'PY'
import json
from pathlib import Path
import re
import sys

document = json.loads(Path(sys.argv[1]).read_text())
assert document["schema"] == "canvasbench.image-attestation.v1"
assert set(document["images"]) == {"usage-proxy", "opencode", "qwen-code", "mini-swe-agent", "pi", "aider", "openhands", "goose"}
for name, image in document["images"].items():
    assert re.fullmatch(r".+@sha256:[0-9a-f]{64}", image["image"]), (name, image["image"])
    assert ":latest" not in image["image"]
    assert re.fullmatch(r"[0-9a-f]{64}", image["config_sha256"])
serialized = json.dumps(document).lower()
for secret in ("api_key=", "authorization: bearer", "admin.token="):
    assert secret not in serialized
PY
"$SCRIPT_DIR/../../../scripts/test-canvasbench-remote.sh"

if PATH="$TEST_ROOT/bin:$PATH" FAKE_DOCKER_STATE="$TEST_ROOT/state" \
  python3 "$SCRIPT_DIR/image_tool.py" build --repository example:latest --output "$TEST_ROOT/rejected.json" \
  >"$TEST_ROOT/rejected.stdout" 2>"$TEST_ROOT/rejected.stderr"; then
  echo "floating repository unexpectedly passed" >&2
  exit 1
fi
grep -q "tag-free OCI repository" "$TEST_ROOT/rejected.stderr"
