# Agent: Delete tmux, worktree, and wtbridge Packages

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is the final cleanup step of the containerization initiative: delete `internal/tmux/`, `internal/worktree/`, and the temporary `internal/wtbridge/` shim that prompt 20 introduced; drop all tmux-related fields from `drem.toml`; remove any residual imports; and verify the constitution check still passes. This prompt runs only after prompts 18, 19, and 20 have landed — those are the prompts that actually stripped the imports Tier 3's additive integration (prompts 12, 13, 15) left in place.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Modified modules" → deleted modules; "Configuration changes"; "Phased rollout" → step 7; user stories 41, 42)
- `docs/containerization/remaining-work.md` (section: Step 8 — what this prompt is)
- `internal/tmux/` (the package you will delete)
- `internal/worktree/` (the package you will delete)
- `internal/wtbridge/` (the package you will delete — introduced by prompt 20 as an intentionally temporary shim)
- `ARCHITECTURE.md` (the `[enforced]` ceilings and import rules — your final check)
- Prompt 18 deliverables (orchestrator production + test files no longer import `internal/worktree`; `WorktreeManager` interface in place)
- Prompt 19 deliverables (agent package no longer imports `internal/tmux`; `internal/merge/` already deleted)
- Prompt 20 deliverables (`cmd/drem/` and `internal/tui/` no longer import `internal/tmux` or `internal/worktree`; `internal/wtbridge/` is the last holder of the `internal/worktree` import)

## Dependencies

This prompt depends on prompts 18, 19, and 20 all being merged. Before starting, verify:

```bash
grep -rn "internal/tmux\|internal/worktree" cmd/ internal/ pkg/ --include="*.go" \
  | grep -v "internal/tmux/\|internal/worktree/\|internal/wtbridge/"
```

The command must return empty. If any other package still imports `tmux` or `worktree`, STOP and flag which prompt (18/19/20) owns the residual — do not proceed with deletion.

A separate confirmation grep should show that `internal/wtbridge/` is the only remaining importer of `internal/worktree/`:

```bash
grep -rn "\"github.com/godinj/drem-orchestrator/internal/worktree\"" cmd/ internal/ pkg/ --include="*.go"
# → only hits inside internal/wtbridge/
```

## Deliverables

### Deletions

#### 1. Remove `internal/tmux/`

Delete the entire directory:

```bash
rm -rf internal/tmux/
```

#### 2. Remove `internal/worktree/`

Delete the entire directory:

```bash
rm -rf internal/worktree/
```

#### 2a. Remove `internal/wtbridge/`

The bridge was introduced in prompt 20 solely to localize the last `internal/worktree/` import. Now that `internal/worktree/` is gone, delete the bridge and wire the orchestrator's `WorktreeManager` directly in `cmd/drem/main.go` against whatever container-backed implementation replaces it (likely nothing — the orchestrator no longer needs a host worktree manager once all consumers are containerized). If any production code still calls `wtbridge.NewManager`, follow the chain and replace with the container path.

```bash
rm -rf internal/wtbridge/
```

Grep confirmation:

```bash
grep -rn "internal/wtbridge" cmd/ internal/ pkg/ --include="*.go"
# → must return empty
```

#### 3. Remove tmux-related fields from `drem.toml` parsing

Locate the config loader (likely `internal/config/` or inline in `cmd/drem/`). Remove:

- Any `[tmux]` section struct
- Any per-agent `tmux_session` or `tmux_window` fields
- Any tmux-related defaults

If the project historically accepted tolerated unknown-key TOML (for forward compatibility), keep that behavior so a developer with an old `drem.toml` gets a warning, not a crash. Emit the warning via `log.Printf` on startup when a `[tmux]` section is observed and suggest removing it.

#### 4. Remove tmux references from existing `drem.toml`

The repo-root `drem.toml` (and any sibling like `haiku-drem.toml`) likely has tmux fields. Remove them by hand, preserving the rest of the file. Commit the cleaned files in the same changeset as the code deletions.

#### 5. Remove tmux references from scripts

Search `scripts/` for shell scripts that use tmux. Files likely involved:

- `scripts/csuite-launch.sh`
- `scripts/csuite-bootstrap.sh`
- `scripts/csuite-spawn-worker.sh`
- any `tmux-*.sh`

For each, determine whether the script is still used in the containerized flow:

- If the script is obsolete (the container launch replaces it), delete the file
- If the script is still referenced by the dev workflow, rewrite the tmux portion as a `docker exec` or `docker compose exec` invocation against the appropriate service

Document each decision in the commit message.

### Verification

#### 6. Import audit

Re-run the import check after deletion:

```bash
grep -rn "internal/tmux\|internal/worktree\|internal/wtbridge" cmd/ internal/ pkg/ --include="*.go"
```

Must return empty. Any residual hit means a consumer was missed in prompts 18/19/20 — fix the consumer rather than leaving a dangling import.

#### 7. Build verification

```bash
go build ./...
go test ./...
```

Both must succeed. The test suite is expected to have been passing after prompts 12/13/15; if it now fails, the failure is a missed consumer and must be fixed in the consumer's owning prompt area, not hacked around here.

#### 8. Constitution check

```bash
bash scripts/check_constitution.sh
```

Must pass. The `[enforced]` ceilings likely include rules about file count in `internal/`; deleting two packages reduces the count, which is safe.

#### 9. Architecture document update

Update `ARCHITECTURE.md` Package Map to remove:

- The `tmux/` entry ("Go wrapper around the tmux CLI...")
- The `worktree/` entry ("Git worktree management...")

Add (or confirm) entries for the new packages that landed across this initiative: `container/`, `extract/`, `gitexec/`, `gitref/`, `kyle/`, `merger/`, `orchhttp/`, `projects/`, `spawner/`, `watchdog/`, `orchclient/`, `orchdto/`.

## Scope Limitation

- **Only delete `internal/tmux/`, `internal/worktree/`, and `internal/wtbridge/`.** `internal/merge/` is already gone (prompt 19); do not recreate a "merge cleanup" action here.
- **Do not touch agent behaviors.** The agent package (prompts 13/19) owns its own migration; if this prompt reveals a residual tmux import there, flag it back to prompt 19's scope rather than editing.
- **Do not rewrite git history.** Commit deletions as straightforward `rm` + updates. The old tmux/worktree code is recoverable from `git log`.
- **No backwards-compatibility shims.** The PRD "No half-finished implementations either" rule applies: do not leave deprecated stub packages, do not re-export types, do not add `// Deprecated:` markers. The code is gone.

## Commit Hygiene

Split the work into logical commits so reviewers can follow:

1. `remove internal/tmux package`
2. `remove internal/worktree package`
3. `remove internal/wtbridge package`
4. `drop tmux config from drem.toml schema`
5. `clean up tmux-shell scripts and drem.toml samples`
6. `update ARCHITECTURE.md package map`

If any single commit breaks the build, fold it into the next. The head of the branch must build and pass tests cleanly.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Build verification: `go build ./... && go test ./...` — both must pass
- Import audit: `grep -rn "internal/tmux\|internal/worktree\|internal/wtbridge" cmd/ internal/ pkg/ --include="*.go"` — must be empty
- Constitution check: `bash scripts/check_constitution.sh` — must pass
- ARCHITECTURE.md package map reflects final state
