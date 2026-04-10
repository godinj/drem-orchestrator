#!/usr/bin/env bash
# generate-repo-map.sh — Generate a compact repository map for LLM context.
# Outputs repo-map.md in the project root.
#
# Uses Go's built-in tooling (go list, go doc) to extract package docs,
# type definitions, and function signatures. The output is capped at ~5K
# tokens (~20K characters) to fit in a single context window.
#
# Usage: bash scripts/generate-repo-map.sh
set -euo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

OUT="repo-map.md"
MAX_CHARS=20000
DATE=$(date +%Y-%m-%d)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

# Extract the short-path portion (cmd/foo or internal/foo/bar) from a full
# import path like github.com/.../internal/foo.
short_path() {
    local imp="$1"
    if [[ "$imp" == *"/cmd/"* ]]; then
        printf '%s' "${imp##*/cmd/}" | sed 's|^|cmd/|'
    elif [[ "$imp" == *"/internal/"* ]]; then
        printf '%s' "${imp#*internal/}" | sed 's|^|internal/|'
    else
        printf '%s' "$imp"
    fi
}

# ---------------------------------------------------------------------------
# Collect package metadata via go list -json
# ---------------------------------------------------------------------------

declare -A PKG_DOC

while IFS='|' read -r import doc; do
    PKG_DOC["$import"]="$doc"
done < <(
    go list -json ./cmd/... ./internal/... 2>/dev/null \
    | jq -r '[.ImportPath, (.Doc // "" | split("\n") | .[0] | .[0:80])] | join("|")' \
      2>/dev/null || true
)

# ---------------------------------------------------------------------------
# Level 1 — Package Structure
# ---------------------------------------------------------------------------

level1=""
level1+="## Package Structure"$'\n'
level1+='```'$'\n'

# cmd/ packages
for imp in $(go list ./cmd/... 2>/dev/null | sort); do
    sp=$(short_path "$imp")
    doc="${PKG_DOC[$imp]:-}"
    if [[ -n "$doc" ]]; then
        level1+="$(printf '%-30s -- %s' "$sp/" "$doc")"$'\n'
    else
        level1+="${sp}/"$'\n'
    fi
done

# internal/ packages
for imp in $(go list ./internal/... 2>/dev/null | sort); do
    sp=$(short_path "$imp")
    doc="${PKG_DOC[$imp]:-}"
    if [[ -n "$doc" ]]; then
        level1+="$(printf '%-30s -- %s' "$sp/" "$doc")"$'\n'
    else
        level1+="${sp}/"$'\n'
    fi
done

level1+='```'$'\n'

# ---------------------------------------------------------------------------
# Level 2 — Key Type Definitions
# ---------------------------------------------------------------------------

level2=$'\n'"## Key Types"$'\n'

for imp in $(go list ./internal/... 2>/dev/null | sort); do
    sp=$(short_path "$imp")
    # Get compact struct summaries (field names only) and full interfaces
    types_out=$(go doc -all "./$sp" 2>/dev/null | awk '
        /^type [A-Z][a-zA-Z0-9_]* struct \{/ {
            name = $2; brace = 1; n = 0; flist = ""
            while (brace > 0 && (getline > 0)) {
                if ($0 ~ /^\}/) { brace--; continue }
                if ($0 ~ /^[[:space:]]*\/\//) continue
                gsub(/`[^`]*`/, "", $0)
                gsub(/\/\/.*/, "", $0)
                match($0, /[A-Za-z_][A-Za-z0-9_]*/)
                if (RSTART > 0) {
                    if (n > 0) flist = flist ", "
                    flist = flist substr($0, RSTART, RLENGTH)
                    n++
                }
            }
            if (n > 0) printf "type %s struct { %s }\n", name, flist
        }
        /^type [A-Z][a-zA-Z0-9_]* interface \{/ {
            brace = 1
            printf "%s\n", $0
            while (brace > 0 && (getline > 0)) {
                if ($0 ~ /^\}/) { print "}"; brace--; continue }
                if ($0 ~ /^[[:space:]]*\/\//) continue
                if ($0 ~ /[a-zA-Z]/) print $0
            }
            print ""
        }
    ' 2>/dev/null || true)

    if [[ -n "$types_out" ]]; then
        level2+=$'\n'"### $sp"$'\n''```go'$'\n'"${types_out}"$'\n''```'$'\n'
    fi
done

# ---------------------------------------------------------------------------
# Level 3 — Function Signatures
# ---------------------------------------------------------------------------

level3=$'\n'"## Function Signatures"$'\n'

for imp in $(go list ./cmd/... ./internal/... 2>/dev/null | sort); do
    sp=$(short_path "$imp")
    sigs=$(go doc -short "./$sp" 2>/dev/null | grep -E '^func ' || true)
    if [[ -n "$sigs" ]]; then
        level3+=$'\n'"### $sp"$'\n''```go'$'\n'"${sigs}"$'\n''```'$'\n'
    fi
done

# ---------------------------------------------------------------------------
# Assemble and truncate
# ---------------------------------------------------------------------------

header="# drem-orchestrator Repository Map"$'\n'"Generated: ${DATE}"$'\n'

output="${header}"$'\n'"${level1}"$'\n'"${level2}"$'\n'"${level3}"

# Truncate if over budget
if (( ${#output} > MAX_CHARS )); then
    output="${output:0:$MAX_CHARS}"$'\n\n'"..."$'\n\n'"_[Truncated to stay under ${MAX_CHARS} characters]_"
fi

printf '%s\n' "$output" > "$OUT"

chars=${#output}
approx_tokens=$(( chars / 4 ))
echo "Wrote ${OUT} (${chars} chars, ~${approx_tokens} tokens)"
