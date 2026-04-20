# drem project register --update — Implementation Plan

Status: **not started.** Sibling plan to `plans/worker-subscription-auth.md`
and `plans/worker-prompt-delivery.md`; mirrors their cadence (plan first,
one commit per logical chunk, TDD per layer, docs-as-acceptance-criteria).
Unblocks the T3 canary and every future rollout by making per-project
`~/.drem/projects/<name>/{compose.yml,drem.toml}` regeneration
idempotent and drift-free.

## 0. Goal

Make this operator rollout work with zero hand-patching:

```
drem project register --update drem-orchestrator
docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml up -d
```

...and the compose.yml + drem.toml match current-master templates,
running services keep their SharedToken (same auth for orch ↔
agentmon ↔ csuite-watcher), and any operator-owned state on disk
(`compose.override.yml`, `csuite-run.sh`) is untouched.

## 1. Why now

Master is at `0182167` (worker-prompt-delivery merged). The T3 canary
needs the new `DREM_PROMPT_ROOT_HOST` env var on orch and the
prompts bind-mount the template now renders. Without register-update,
the operator hand-edits `~/.drem/projects/drem-orchestrator/compose.yml`.
That's the third hand-patch this quarter — the first two (the
`DREM_*_URL` env vars and the `[agents.planner]` drem.toml section)
are still visible in the on-disk files as comments saying _"Will be
overwritten by full regen after worker-auth lands"_.

Compounding drift is the problem. Every plan that lands a new
env var, mount, or config key on the template adds another hand-patch
round. This command stops it.

## 2. What changes, what stays

Keep:
- The `drem project register` happy-path for fresh registrations
  (`templateDataFor` → `WriteProjectConfigAt` → `WriteProjectComposeAt`).
  Un-updated behavior is preserved byte-identical.
- `TemplateData` as the sole rendering input. No new `StateSnapshot`
  type; the state this plan preserves lives entirely on existing
  fields plus one on-disk-only field (SharedToken).
- `internal/projects/registry.go` — registry file format is not
  extended. SharedToken and OrchHostPort stay where they already
  live (disk for the token, registry for the port). See §8.3.
- `compose.override.yml` — never touched by any `register` path.
  `WriteProjectComposeAt` scopes to `compose.yml`; the update
  path inherits that scope.

Replace:
- `register` becomes a dispatch that routes to `register-fresh` or
  `register-update` based on the `--update` flag. See §4.

Remove:
- Nothing. The update path is additive; fresh registration stays
  behind its current argv.

Add:
- `--update` flag on `drem project register`. Positional project name
  becomes required when `--update` is set; the flag set otherwise
  matches fresh register (but fresh register rejects `--update`
  collisions — §4.2).
- New `internal/projects/state.go` — defines `StateSnapshot` (the
  state that must survive a regen but isn't in the registry) + a
  `ReadStateFromDisk(homeDir, projectName)` helper that extracts it.
  TDD.
- Conflict-detection layer: render the expected output, diff
  against on-disk; surface drift as warnings (default) or errors
  (`--fail-on-drift`, for CI); overwrite with `--force`.
- `--dry-run` — render + diff, print, do not write.
- `--regenerate-token` escape hatch — intentional new SharedToken
  (requires operator restart of orch + agentmon to re-auth). §8.2
  decides the default behavior when the on-disk token is unreadable.

## 3. Subscription-only ... wait, wrong plan — state-preservation policy

The preservation promise, strict form:

- **SharedToken:** preserved. Workers' claude auth doesn't care, but
  `agentmon → orch /internal/logs` and `csuite-watcher → orch /api/*`
  Bearer-auth against this token. Regenerating it silently means
  every running service starts 401'ing until restarted. So: if the
  on-disk compose has a SharedToken, we extract it, we re-render
  with it, end of story. If we can't find one (corrupt on-disk,
  hand-deleted, never-registered), we fail loud and point the
  operator at `--regenerate-token`.
- **OrchHostPort:** preserved. Registry carries it already
  (`Project.OrchHostPort`). The update path reads the registry and
  renders from it. If the registry value is `0` (legacy pre-allocation
  or the current on-disk drem-orchestrator project — yes, grep
  `~/.drem/projects.toml`), we fall back to whatever the on-disk
  compose had, then to `DefaultOrchHostPort`. See §8.4.
- **DevMode:** preserved. Registry carries this too (`Project.DevMode`).
  If not set on registry but set on disk (unlikely — the current
  template branches on `{{if .DevMode}}`, so a dev compose has a
  `/src:ro` mount that the extractor detects and surfaces as drift),
  treat as drift and warn.
- **ContainerImageOverrides:** preserved. Registry-scoped; update path
  reads from registry, re-renders. If the operator has a manual image
  override in compose.yml that doesn't match the registry, that's
  drift — warn but proceed (operator may have deliberately used the
  override-sidecar pattern out of the update path's sight).
- **drem.toml operator sections** (e.g. `[agents.planner]` that
  was hand-patched 2026-04-20): these are expected to move into
  the master template (as the planner section did in commit
  `3900029`). Until they're there, the update path warns if the
  on-disk drem.toml has sections the template doesn't render. See
  §5.3 for the "section allowlist" approach.

**No silent regen.** Every bit of state that isn't regeneratable
from template + registry must be surfaced, either extracted and
preserved, or flagged as drift. The failure mode we're avoiding is
"update worked, then auth broke" because a silent regen rewrote
the token.

## 4. Open design questions — decided

### 4.1 `--update` flag on register, or new `update` subcommand?

**Decided: `--update` flag on `register`.** Reasons:

1. Prior art favors it. `terraform init` and `git config` both use
   verb flags (`--upgrade`, `--add`) for mutating variants of the
   same primitive instead of splintering into `terraform upgrade-init`
   or `git add-config`. `kubectl apply` gets close to the same
   idempotency story as a single verb.
2. Shared argv surface. `--update` reuses `--name`/`--bare`/
   `--language`/`--orch-url` as optional overrides for drift-resolution
   (operator says "yes I actually meant to change OrchURL" → pass
   `--orch-url=...` with `--update --force`). A separate `update`
   subcommand would duplicate the flag set.
3. Less CLI surface area. Keeps `drem project <register|list|remove|show>`
   as the public contract; `--update` is an implementation detail of
   `register`.

Rejected alternative: `drem project update <name>`. Cleaner
separation but forks the flag set and invites operator confusion
("register twice or update?"). The CLI help text for the flag
documents the exact semantic (§6.3).

### 4.2 SharedToken source

**Decided: parse on-disk compose.yml with gopkg.in/yaml.v3
(already a test dep).** Reasons:

- The compose file is the canonical runtime state: every service
  that has the token in its env block reads it from there. Grepping
  the registry is the wrong move because the registry doesn't carry
  the token (confirmed: `~/.drem/projects.toml` has no `shared_token`
  key on the currently-registered drem-orchestrator project).
- A dedicated file (`~/.drem/projects/<name>/shared_token`) would
  be a bigger refactor for negative value — splits the state across
  two files that have to agree, invites a new "file missing but token
  in compose" class of drift.
- Parsing is safe: we extract exactly one string value
  (`services.orch.environment.DREM_AGENTMON_TOKEN`), fail closed if
  absent, and never write the parsed YAML back out (we always
  re-render from template).

Rejected alternatives:
- Read agentmon's local config file — agentmon doesn't persist the
  token; it re-reads env on every container start.
- Persist token in registry (`Project.SharedToken`) — biggest
  refactor, touches every registry consumer, and still leaves the
  compose.yml as the runtime source of truth. Out of scope per the
  plan prompt's "no new registry fields unless strictly required"
  constraint.

### 4.3 Conflict detection — refuse, merge, or overwrite?

**Decided: render + diff; warn by default; `--fail-on-drift` turns
warnings into errors; `--force` acknowledges drift and overwrites.**

Flow:

1. Read on-disk compose.yml + drem.toml.
2. Render expected output from registry + extracted StateSnapshot.
3. Compute a structural diff (YAML-normalized for compose; TOML-
   parsed for drem.toml). Whitespace + comment differences do not
   count as drift — comments are template-generated and
   template-owned.
4. Emit findings via `stdout` as a machine-readable list plus a
   human summary:
   ```
   drift detected in ~/.drem/projects/drem-orchestrator/compose.yml:
     - orch.environment.GIT_CONFIG_COUNT = "1" (on-disk only)
     - orch.environment.GIT_CONFIG_KEY_0 = "safe.directory" (on-disk only)
     - orch.environment.GIT_CONFIG_VALUE_0 = "*" (on-disk only)
   these will be REMOVED by regeneration. pass --force to proceed.
   ```
5. Default (no `--force`, no `--fail-on-drift`): print warnings,
   exit 0 without writing. Operator re-runs with `--force` after
   reviewing. Matches `git rebase`'s "abort, inspect, resume" loop.
6. `--force`: overwrite anyway, print the same summary as an audit
   log entry.
7. `--fail-on-drift`: drift is an error; exit 1 without writing.
   For CI and for cautious operators who want a hard stop.
8. `--dry-run`: render + diff but never write, with or without
   `--force`. Combines freely with `--fail-on-drift`.

Rationale: the operator cares most about "what's about to change
and why." Refusing is too rigid (every update has drift by
definition — that's why we're updating). Silent-overwriting is
what we have today, and it's what we're fixing. A warn-then-force
gate preserves operator agency without being obstructive.

### 4.4 drem.toml preservation policy

**Decided: render from master template only; surface hand-edited
sections as drift.** Rationale:

- Today's on-disk drem.toml has `[agents.planner]` as a hand-patch
  from 2026-04-20. The master template ALSO has it now
  (commit `3900029`, the planner walkthrough unification). So the
  "drift" turns into a no-op for drem-orchestrator — the update
  path re-renders the planner section from template and the content
  matches.
- For a hypothetical future hand-patch that _isn't_ in the template
  (operator experiments with a different planner model), the drift
  warning is the right signal: "your hand-edit is about to be
  overwritten; pass `--force` to proceed or file a plan to add this
  knob to the template."
- A `drem.override.toml` sibling file was considered and rejected.
  Orch only loads `/var/lib/drem/drem.toml`; adding override-merge
  logic to orch's `LoadConfig` is a separate, bigger refactor
  (touches every drem.toml reader, complicates the no-network
  boundary of the orch container). The compose.override.yml pattern
  works because docker compose natively merges overlays; there's
  no equivalent for TOML without writing it.

So: policy-level, hand-edits to drem.toml are always drift. Fine for
T3; revisit when operators want legitimate per-host config knobs
that shouldn't be in git.

## 5. Architecture

```
drem project register --update drem-orchestrator [--dry-run] [--force] [--fail-on-drift]
    │
    ▼
cmd/drem/project.go::cmdProjectRegister
    │  parses --update and routes to cmdProjectRegisterUpdate
    ▼
cmd/drem/project.go::cmdProjectRegisterUpdate
    │  1. load registry → Project entry
    │  2. readStateFromDisk(homeDir, projectName) → StateSnapshot
    │     (SharedToken, observed OrchHostPort if registry=0, …)
    │  3. build TemplateData from (registry entry + state snapshot)
    │  4. render expected compose.yml + drem.toml
    │  5. diff against on-disk (YAML-normalized + TOML-parsed)
    │  6. if drift and !force: print warnings, maybe exit 1 (--fail-on-drift) or 0
    │  7. if dry-run: print diff summary, exit 0 (regardless of drift)
    │  8. write compose.yml + drem.toml atomically (tmp + rename, matches Save)
    │  9. print summary: "updated <project>: 3 changed keys, SharedToken preserved"
    ▼
internal/projects/state.go
    │  StateSnapshot holds the on-disk-only state.
    │  ReadStateFromDisk parses compose.yml + drem.toml and returns it.
    │
internal/projects/template.go — unchanged (Render + WriteProjectComposeAt)
internal/projects/config_template.go — unchanged (RenderConfig + WriteProjectConfigAt)
```

Host filesystem layout (unchanged):

```
$HOME/.drem/
    projects.toml                              # registry (existing)
    projects/<name>/
        compose.yml                            # regenerated by update
        drem.toml                              # regenerated by update
        compose.override.yml                   # operator-owned, UNTOUCHED
        csuite-run.sh                          # operator-owned, UNTOUCHED
        prompts/                               # worker-prompt-delivery
```

### 5.1 StateSnapshot shape

```go
// StateSnapshot holds per-project state that isn't in the registry and
// must survive a regen of compose.yml / drem.toml. Callers produce one
// via ReadStateFromDisk before re-rendering, and feed the fields into
// TemplateData so the rendered output preserves the running services'
// auth + addressing.
type StateSnapshot struct {
    // SharedToken is services.orch.environment.DREM_AGENTMON_TOKEN from
    // the on-disk compose.yml. Empty when the file is missing or doesn't
    // declare the env key — ReadStateFromDisk returns the empty string
    // and leaves the fail-closed decision to the caller.
    SharedToken string

    // ObservedOrchHostPort is the first component of
    // services.orch.ports[0] in the on-disk compose.yml, split around
    // ":". Zero when the file is missing or has no port mapping.
    // Used as a fallback when the registry's Project.OrchHostPort is
    // also zero (the current drem-orchestrator case).
    ObservedOrchHostPort int
}
```

### 5.2 ReadStateFromDisk

```go
// ReadStateFromDisk parses ~/.drem/projects/<projectName>/compose.yml
// and returns the StateSnapshot. Missing files yield a zero snapshot
// without error — the caller decides whether to fail closed based on
// required-fields semantics (e.g. SharedToken must be non-empty for
// an update to succeed without --regenerate-token). Unparseable YAML
// is an error.
func ReadStateFromDisk(homeDir, projectName string) (StateSnapshot, error)
```

One function, narrow interface, YAML parsing contained. Tested directly:
fixtures include the current drem-orchestrator on-disk compose.yml +
drem.toml plus synthetic corrupt variants.

### 5.3 Drift detection

Implemented as `internal/projects/drift.go` with a pure function:

```go
// Diff compares the rendered-expected bytes against on-disk bytes and
// returns a human-readable list of drift entries. No decisions — the
// CLI layer decides warn vs error based on --force / --fail-on-drift.
func Diff(rendered, onDisk []byte, kind FileKind) []DriftEntry

type FileKind int

const (
    FileKindCompose FileKind = iota // YAML-normalized structural diff
    FileKindDremToml                // TOML-parsed structural diff
)

type DriftEntry struct {
    Path    string  // dotted path (services.orch.environment.FOO)
    Kind    string  // "added", "removed", "changed"
    WasValue string  // what's on disk (empty for added)
    NewValue string  // what the template produces (empty for removed)
}
```

Structural diff strips comments and whitespace — those are template-
owned. Key ordering is also normalized. The diff reports additions
from the template's side (operator gets a new mount) and removals
from the on-disk side (operator loses a hand-patched env var).

## 6. Shape of the new CLI surface

### 6.1 `cmdProjectRegister` gains a `--update` flag

```go
fs := flag.NewFlagSet("project register", flag.ContinueOnError)
name := fs.String("name", "", "project name (required for --update; optional otherwise if positional given)")
// ... existing flags ...
update := fs.Bool("update", false, "regenerate per-project files from current templates, preserving on-disk state")
dryRun := fs.Bool("dry-run", false, "print what would change without writing")
force := fs.Bool("force", false, "overwrite hand-patched drift without prompting")
failOnDrift := fs.Bool("fail-on-drift", false, "exit non-zero if any drift is detected (for CI)")
regenerateToken := fs.Bool("regenerate-token", false, "deliberately rotate SharedToken (restart orch+agentmon after)")
```

### 6.2 Routing

`cmdProjectRegister` reads `*update` after parsing:
- `*update == false`: current fresh-register path. Reject
  `--force`, `--dry-run`, `--fail-on-drift`, `--regenerate-token`
  as "only valid with --update" (§8.1).
- `*update == true`: route to `cmdProjectRegisterUpdate`. The
  positional project name is required in this mode (supports
  `drem project register --update drem-orchestrator` per the
  prompt's acceptance criteria). `--name` is accepted as an alias.

### 6.3 Help text

```
drem project register [--update] [flags] [--] [<name>]

Fresh registration:
  drem project register --name NAME --bare PATH --language go|cpp --orch-url URL

Update an already-registered project from current templates:
  drem project register --update NAME
  drem project register --update NAME --dry-run
  drem project register --update NAME --force
  drem project register --update NAME --fail-on-drift
  drem project register --update NAME --regenerate-token

Preserves SharedToken, OrchHostPort, DevMode, ContainerImageOverrides.
compose.override.yml is NEVER touched.
```

## 7. Files touched

### New files
- `plans/drem-project-register-update.md` — this file.
- `internal/projects/state.go` — `StateSnapshot` + `ReadStateFromDisk`.
- `internal/projects/state_test.go` — TDD suite for state extraction:
  happy path (full compose.yml with shared token + port), missing
  file (zero snapshot, no error), unparseable YAML (error),
  compose.yml with no DREM_AGENTMON_TOKEN (empty SharedToken, no error),
  compose.yml with no ports block (zero OrchHostPort, no error),
  malformed port string like `"not-a-port"` (zero, no error).
- `internal/projects/drift.go` — `Diff`, `FileKind`, `DriftEntry`.
- `internal/projects/drift_test.go` — TDD for structural diff:
  whitespace-only difference is not drift, added env key is drift,
  removed env key is drift, changed SharedToken value is drift,
  TOML section addition/removal, TOML nested table changes.

### Modified files
- `cmd/drem/project.go` — add `--update`, `--dry-run`, `--force`,
  `--fail-on-drift`, `--regenerate-token` flags;
  `cmdProjectRegisterUpdate` handler; update `dispatchProject` help
  text; positional-name handling when `--update` is set.
- `cmd/drem/project_test.go` — 6+ new tests:
  - `TestProjectUpdate_PreservesSharedToken` — register fresh,
    rewrite compose.yml in-place with a newer template, update,
    assert shared token byte-identical.
  - `TestProjectUpdate_DryRunDoesNotWrite` — update --dry-run,
    assert files unchanged on disk.
  - `TestProjectUpdate_FailsWhenTokenMissing` — update against a
    compose.yml stripped of DREM_AGENTMON_TOKEN, expect loud error,
    no writes.
  - `TestProjectUpdate_RegenerateTokenRotates` — update with
    `--regenerate-token` against a compose.yml that has a token,
    assert rendered output has a DIFFERENT token.
  - `TestProjectUpdate_ForceOverwritesDrift` — update against a
    compose.yml with a hand-patched env var, expect warning and
    overwrite when `--force` set.
  - `TestProjectUpdate_FailOnDriftErrors` — update against drifted
    compose, no `--force`, with `--fail-on-drift`, expect non-zero exit.
  - `TestProjectUpdate_IsIdempotent` — register, update, update
    again — the second update produces byte-identical output
    (no spurious drift, no spurious writes).
  - `TestProjectUpdate_RejectsRegenerateTokenWithoutUpdate` —
    the `--regenerate-token` flag is only meaningful with `--update`;
    fresh register rejects it.
  - `TestProjectUpdate_LeavesComposeOverrideAlone` — touch
    compose.override.yml + csuite-run.sh with sentinel content,
    update, assert both files byte-identical.
- `docs/containerization/install.md` — new subsection under Step 6
  walking the operator through the regeneration flow. Cross-link
  from the existing "Known wiring gap" note at line 310 (the gap
  goes away with this plan).
- `plans/containerization.md` — Phase 4 acceptance-criteria row
  added: `drem project register --update NAME` regenerates compose
  + drem.toml idempotently, preserving SharedToken.
- `plans/drem-project-register-update.md` — mark done in Phase 3.
- `plans/worker-prompt-delivery.md` — strike the implicit "depend on
  operator hand-edit" assumption in the rollout step 3 (it already
  references `drem project register --update`; no change needed,
  but re-confirm once landed).

## 8. Commit sequence (mirrors worker-prompt-delivery cadence)

1. **`docs(plans): drem project register-update plan`** — this file
   only. No code.
2. **`feat(projects): extract on-disk state for regeneration`** —
   `internal/projects/state.go` + `state_test.go`. Pure addition,
   no caller yet. TDD: write state_test first (happy + 5 negative
   paths), then state.go. YAML parsing via gopkg.in/yaml.v3 (already
   a transitive dep for tests — need to add to non-test code; go
   mod tidy confirms).
3. **`feat(projects): structural diff for compose + drem.toml`** —
   `internal/projects/drift.go` + `drift_test.go`. Also a pure
   addition. TDD: diff_test first, drift.go second. Key normalization,
   comment stripping, dotted-path key paths.
4. **`feat(cli): drem project register --update`** — wire the flags
   onto `cmdProjectRegister`, add `cmdProjectRegisterUpdate`, hook
   up state + drift, call `WriteProjectCompose/ConfigAt` on the
   render side. Tests: the 9 from §7 above, organized by behavior.
   No change to fresh register.
5. **`feat(cli): register --update flags (dry-run, force, fail-on-drift)`** —
   the three flag variants, tested. Can be folded into commit 4 if
   the diff lands small (expect ~300 LoC added). Decide at rebase time.
6. **`feat(cli): register --regenerate-token escape hatch`** —
   separate commit to keep auth-affecting changes reviewable in
   isolation. Tests: RegenerateTokenRotates, FailsWhenTokenMissing,
   RejectsRegenerateTokenWithoutUpdate.
7. **`docs(containerization): document register --update flow`** —
   install.md walkthrough subsection + remove the "Known wiring
   gap" note that referenced the carried TODO.
8. **`docs(plans): mark register-update done + tick containerization`** —
   update this plan's status header; tick the containerization
   Phase 4 row; strike the operator-hand-patch references in the
   install-log.md (if any) and in compose.yml's TODO-comment
   (actually removed at rebuild time, not at plan time).

~8 commits, ~800-1200 LoC net, tests + docs dominate. ~1.5 days focused.

## 9. Rollout

Operator, post-merge:

1. Build + push `drem-orch:latest` if §8 touches orch-facing code
   (it doesn't — this is CLI + `internal/projects` only). No image
   rebuild needed.
2. Run:
   ```
   drem project register --update drem-orchestrator --dry-run
   ```
   Expect: drift report listing the three `GIT_CONFIG_*` hand-patches
   from the current on-disk compose.yml (they'll be REMOVED by the
   update — master's orch image now has safe.directory baked in via
   commit a separate plan should land; until then the operator keeps
   them via `--force` OR updates the template to include them).

3. Iterate: either fold the hand-patches into the template (separate
   follow-up commit: update project-compose.yml.tmpl to include
   `GIT_CONFIG_*` on orch, if still needed), then re-run update
   --dry-run to confirm no drift, OR run:
   ```
   drem project register --update drem-orchestrator --force
   ```
   which overwrites. Operator chooses.

4. ```
   docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml up -d
   ```
   Services restart with the new template output. SharedToken byte-
   identical to pre-update → no auth break.

5. Unblock T3 canary — the new `DREM_PROMPT_ROOT_HOST` env and
   prompts bind-mount are now in place without hand-patching.

## 10. Open questions (residual)

### 10.1 Reject orthogonal flags outside --update?

`--dry-run` without `--update` is easy to argue for ("show me what
fresh register would do") but adds surface area. Decision: reject in
this plan to keep the fresh-register path byte-identical. Revisit if
operator feedback asks for it.

### 10.2 Multi-project update

`drem project register --update` without a positional name could
iterate over every registered project. Out of scope for this plan —
single-project usage covers the current rollout, and bulk operations
tend to hide drift in noise. File if observed demand warrants.

### 10.3 `--regenerate-token` and the `compose.override.yml`

Rotating the token means re-auth for every service that reads it.
Today that's orch + agentmon + csuite-watcher. The override file
doesn't touch `DREM_AGENTMON_TOKEN` / `DREM_BEARER_TOKEN`, so a
rotation is idempotent at the override-file boundary. Documented
in install.md; nothing to wire.

### 10.4 Atomic update of two files

compose.yml + drem.toml must both land or both fail (otherwise the
bind-mount at orch boot mismatches). Implement: render both in-memory,
validate both, then write both via `os.Rename` (atomic per-file; the
caller sequences them and rolls back the first write if the second
fails). Simple enough to inline in `cmdProjectRegisterUpdate`.

### 10.5 What happens to the registry entry?

Unchanged. The update path does NOT mutate `~/.drem/projects.toml`.
Registration updates (OrchURL, language changes) go through fresh
register + manual registry edit, consistent with the prompt's
non-goals constraint.

### 10.6 Fresh-file fallback

If compose.yml exists but drem.toml is missing (or vice-versa), the
update path treats the missing file as "needs rebuild" and proceeds
using the registry + state snapshot from the present file. If BOTH
are missing, the update path fails with "not registered per on-disk
state — run `drem project register` fresh." The registry-entry
presence is not sufficient; the update path assumes a working on-disk
layout to extract state from. The fresh-file-on-disk case is the
operator's `rm -rf ~/.drem/projects/drem-orchestrator/*.yml` recovery
scenario; they probably want fresh register, not update.

## 11. Non-goals / hands-off

- Do NOT actually run `register --update` on
  `~/.drem/projects/drem-orchestrator/` — operator does the rollout.
- Do NOT rebuild docker images.
- Do NOT push to origin.
- Do NOT change fresh `register` initial-registration behavior.
- Do NOT touch the registry file format or Project struct.
- Do NOT add a TOML-overlay mechanism to orch's `LoadConfig`.

## 12. Related plans

- `plans/worker-subscription-auth.md` — the plan whose rollout
  first needed register-update and documented it as a carry-forward
  TODO.
- `plans/worker-prompt-delivery.md` — immediate blocked-on-this plan.
  Its rollout step 3 references `drem project register --update`
  directly.
- `plans/containerization.md` — Phase 4 acceptance criteria gain a
  new row once this lands.
- `docs/containerization/install.md` — Step 6 walkthrough extended
  with the regeneration subsection.
