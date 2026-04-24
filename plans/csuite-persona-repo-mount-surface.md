# csuite persona containers — repo mount surface expansion

status: **proposed** (Kyle drafted 2026-04-23, operator to ratify)
scope: persona containers (csuite-kyle, csuite-mike, csuite-alex, csuite-seth)
origin: Mike surface audit, corrid `7c1f9204`, 2026-04-23T18:56:06Z

## Problem

Phase 1 of the Ross retirement bundle hit a hard blocker in the
artifact-production flow: the persona producing diffs (csuite-mike in
that case) has no read access to the files the diffs must cover.

Mike's audit of his container surface:

- `/home/drem/orch-plans` — ro (plans + design docs only)
- `/home/drem/.drem-csuite/mike` — rw (mailbox)
- `/run/secrets/csuite-watcher-token` — ro
- `/home/drem/.claude/.credentials.json` — ro

Absent: no `docker` binary, no drem-orchestrator repo mount, no
`deploy/`, no `docs/csuite-agents/prompts/` path. Only image-baked
copy at `/opt/csuite/prompts/` which is image-layer scope, doesn't
match host, doesn't persist across rebuild.

csuite-kyle has the same gap (§2a of the container-kyle-transition
plan specifies rw on `orch-plans`, not the orchestrator repo).
csuite-alex and csuite-seth presumably mirror.

Consequence: the ratified "artifact-production + operator-execution"
model doesn't work for repo-touching tasks. Phase 1 of Ross retirement
had to fall back to "operator drafts from logical spec."

## Proposal

Add two mounts to each persona container in
`~/.drem/projects/drem-orchestrator/compose.yml`:

1. **ro bind on the drem-orchestrator repo root** — read-only. Exposes
   `docs/`, `deploy/`, compose files, persona prompt sources, repo
   layout. Enables diff generation against the real tree.
2. **rw bind on a persona-scoped scratch dir** (e.g.
   `~/.drem-csuite/<persona>/scratch/`) — for staging diffs, bundles,
   or multi-file artifacts before they land in the mailbox.

## Why

- Makes the artifact-production model actually executable. The persona
  that owns a concern (Mike for ops, Seth for quality) can produce
  real diffs against the real tree, not synthesized-from-logical-spec
  approximations.
- Preserves the audit trail. Personas still do NOT have write access
  to the repo; the operator remains the sole commit authority. The
  extra ro mount is observational only.
- Fixes a symmetric gap across the pod (Kyle/Mike/Alex/Seth all have
  the same blind spot). One compose edit covers the whole team.

## Risks

- **Broader blast radius on a persona misbehavior.** A persona with
  ro read access to the full repo has more information to reason
  over, including secrets-shaped files. Mitigation: ro-only + existing
  `.gitignore` coverage of secrets (already in place for the host
  tree). Personas cannot exfiltrate via git because they have no
  remote push path.
- **Image-baked prompt drift.** Currently each persona's system prompt
  is image-baked under `/opt/csuite/prompts/`. After this change, the
  host copy at `docs/csuite-agents/prompts/<persona>.md` is also
  visible via the ro mount. A persona could read a version newer than
  its own runtime. Mitigation: document the invariant (image-copy is
  the runtime prompt; host copy is the next-rebuild prompt).
- **Chicken-and-egg for the first bundle.** Adding the mount is
  itself a compose edit, which needs to ship before the mount
  becomes usable. This is operator-authored regardless (compose
  edits are human gate); proposal is to ship this ahead of the next
  repo-touching artifact task.

## Ship sequence

1. Operator authors compose diff adding the two mounts to csuite-mike
   (or all four personas in one pass — preferred, atomic).
2. Operator runs `docker compose up -d --no-deps csuite-mike` (and/or
   the others) to recreate with new mounts.
3. Smoke test: route a small artifact-production request to Mike that
   requires reading one repo file. Verify Mike can read and produce a
   correct diff.
4. Flag this plan as shipped. Update `container-kyle-transition.md`
   §2a to reflect the new mount shape.

## Follow-up not in scope

- Giving personas rw on the repo. No — operator is the commit gate.
- Giving personas a docker socket. No — infra is operator's.
- docker-over-SSH workarounds. No — solves nothing that a ro mount
  doesn't already solve.

## Related

- `plans/container-kyle-transition.md` §2a (rw-mount spec)
- `orch-plans/container-kyle-transition.md` (if present)
- Ross retirement bundle (Phase 1 blocked on this gap)
- Mike's surface audit, corrid `7c1f9204`
