#!/usr/bin/env bash
# apply-sglang-patches.sh — Apply Gemma-4 compatibility patches to the
# SGLang install inside this container image.
#
# Adapted from /home/godinj/git/model-tuning/patch-sglang-gemma4.sh
# (host-side script). Differences vs. the host version:
#   - SGLANG_BASE is discovered from the active venv's site-packages
#     instead of being hardcoded to /home/godinj/venvs/sglang.
#   - PATCH_DIR is the build-context directory the Dockerfile COPYed in.
#   - No `source venv/bin/activate` — the Dockerfile already puts
#     /opt/venv/bin on PATH so `python` resolves to the venv's interpreter.
#
# Patches:
#   01  MoE Triton fallback for Marlin-incompatible layer dimensions
#   02  group_size=16 support (Python, marlin_utils.py)
#   03  group_size=16 support (CUDA, gptq_marlin.cuh)
#   04  Non-MoE zero-padding fallback for misaligned vision encoder layers
#   05  GELU activation support in Triton MoE
#   06  .linear suffix handling in should_ignore_layer()
#
# Target: SGLang 0.5.10.post2.dev306 (commit g90ef8ce54)
#
# Idempotent: patches are applied with --forward, so a re-run on an
# already-patched tree is a no-op rather than an error.

set -euo pipefail

EXPECTED_VERSION="0.5.10.post2.dev306"
PATCH_DIR="${PATCH_DIR:-/build/sglang-patches}"

# --- Locate the installed sglang package -----------------------------------
# Use the python on PATH (the Dockerfile puts /opt/venv/bin first). Resolve
# the parent dir of sglang/__init__.py rather than guessing python3.13 —
# keeps the script robust if the python minor version changes.
SGLANG_BASE="$(python -c 'import os, sglang; print(os.path.dirname(sglang.__file__))')"
echo "SGLang install dir: $SGLANG_BASE"

# --- Version check ---------------------------------------------------------
INSTALLED_VERSION="$(python -c 'import importlib.metadata as m; print(m.version("sglang"))')"
# Strip git suffix for comparison (e.g. 0.5.10.post2.dev306+g90ef8ce54).
INSTALLED_BASE="${INSTALLED_VERSION%%+*}"

if [[ "$INSTALLED_BASE" != "$EXPECTED_VERSION" ]]; then
    echo "ERROR: Expected SGLang $EXPECTED_VERSION, got $INSTALLED_BASE" >&2
    echo "       These patches may not be compatible." >&2
    exit 1
fi
echo "SGLang version verified: $INSTALLED_VERSION"

# --- Backup originals ------------------------------------------------------
echo "Creating .bak backups of original files..."
PATCH_FILES=(
    "srt/layers/quantization/compressed_tensors/compressed_tensors.py"
    "srt/layers/quantization/marlin_utils.py"
    "jit_kernel/csrc/gemm/marlin/gptq_marlin.cuh"
    "srt/layers/quantization/compressed_tensors/schemes/compressed_tensors_wNa16.py"
    "srt/layers/quantization/compressed_tensors/schemes/compressed_tensors_wNa16_moe.py"
    "srt/layers/quantization/compressed_tensors/utils.py"
)
for f in "${PATCH_FILES[@]}"; do
    if [[ ! -f "$SGLANG_BASE/$f" ]]; then
        echo "ERROR: target file missing: $SGLANG_BASE/$f" >&2
        echo "       The installed SGLang layout has shifted; patches are stale." >&2
        exit 1
    fi
    if [[ ! -f "$SGLANG_BASE/$f.bak" ]]; then
        cp "$SGLANG_BASE/$f" "$SGLANG_BASE/$f.bak"
        echo "  backed up: $f"
    else
        echo "  .bak exists: $f (skipping)"
    fi
done

# --- Apply patches ---------------------------------------------------------
echo ""
echo "Applying patches from $PATCH_DIR ..."
cd "$SGLANG_BASE"

PATCHES=(
    "01-moe-triton-fallback.patch"
    "02-marlin-groupsize16-python.patch"
    "03-marlin-groupsize16-cuda.patch"
    "04-non-moe-zero-padding.patch"
    "05-gelu-activation.patch"
    "06-ignore-layer-linear-suffix.patch"
)

FAILED=0
for p in "${PATCHES[@]}"; do
    echo -n "  $p ... "
    if patch -p1 --forward --dry-run < "$PATCH_DIR/$p" > /dev/null 2>&1; then
        patch -p1 --forward < "$PATCH_DIR/$p" > /dev/null 2>&1
        echo "applied"
    elif patch -p1 --forward -R --dry-run < "$PATCH_DIR/$p" > /dev/null 2>&1; then
        echo "already applied (skipping)"
    else
        echo "FAILED"
        FAILED=$((FAILED + 1))
    fi
done

if [[ $FAILED -gt 0 ]]; then
    echo "" >&2
    echo "ERROR: $FAILED patch(es) failed to apply." >&2
    exit 1
fi

# --- Verification ----------------------------------------------------------
echo ""
echo "Verifying patches..."
ERRORS=0

# 01: MoE Triton fallback — check for the alignment check import
if grep -q "check_moe_marlin_supports_layer" "$SGLANG_BASE/srt/layers/quantization/compressed_tensors/compressed_tensors.py"; then
    echo "  01 MoE Triton fallback: OK"
else
    echo "  01 MoE Triton fallback: MISSING"; ERRORS=$((ERRORS + 1))
fi

# 02: group_size=16 Python
if grep -q '16, 32, 64, 128' "$SGLANG_BASE/srt/layers/quantization/marlin_utils.py"; then
    echo "  02 group_size=16 (Python): OK"
else
    echo "  02 group_size=16 (Python): MISSING"; ERRORS=$((ERRORS + 1))
fi

# 03: group_size=16 CUDA — check for group_blocks=1 entries
if grep -q 'true, 1, NUM_THREADS' "$SGLANG_BASE/jit_kernel/csrc/gemm/marlin/gptq_marlin.cuh"; then
    echo "  03 group_size=16 (CUDA): OK"
else
    echo "  03 group_size=16 (CUDA): MISSING"; ERRORS=$((ERRORS + 1))
fi

# 04: Non-MoE zero-padding
if grep -q '_needs_output_truncation' "$SGLANG_BASE/srt/layers/quantization/compressed_tensors/schemes/compressed_tensors_wNa16.py"; then
    echo "  04 Non-MoE zero-padding: OK"
else
    echo "  04 Non-MoE zero-padding: MISSING"; ERRORS=$((ERRORS + 1))
fi

# 05: GELU activation
if grep -q '"silu", "gelu"' "$SGLANG_BASE/srt/layers/quantization/compressed_tensors/schemes/compressed_tensors_wNa16_moe.py"; then
    echo "  05 GELU activation: OK"
else
    echo "  05 GELU activation: MISSING"; ERRORS=$((ERRORS + 1))
fi

# 06: .linear suffix
if grep -q 'shard_name + ".linear"' "$SGLANG_BASE/srt/layers/quantization/compressed_tensors/utils.py"; then
    echo "  06 .linear suffix: OK"
else
    echo "  06 .linear suffix: MISSING"; ERRORS=$((ERRORS + 1))
fi

if [[ $ERRORS -gt 0 ]]; then
    echo "" >&2
    echo "ERROR: $ERRORS verification(s) failed." >&2
    exit 1
fi

# --- Import test -----------------------------------------------------------
echo ""
echo "Running import test..."
python -c "from sglang.srt.layers.quantization.compressed_tensors.compressed_tensors import CompressedTensorsConfig; print('Import test: OK')"

echo ""
echo "All 6 patches applied and verified successfully."
