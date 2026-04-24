# host-exec deferred follow-ups

**Status:** parked — single cleanup bundle, sequenced by Alex on the next host-exec touch.
**Owner sign-off:** Seth (quality/audit) endorses the bundling strategy.
**Captured:** 2026-04-23 by Kyle after PATCH thread close.

When host-exec sees its next pass, these three items ship together as one cleanup:

## 1. Asymmetry comment block in `main.go`

Above the two load blocks (allowlist load / denylist load) that currently behave asymmetrically on the "file present, zero patterns" state, add a comment explaining the distinction:

> The allowlist is a positive-permission gate: empty file = nothing permitted, fail closed.
> The denylist is a negative-veto overlay: empty file = nothing vetoed, allowlist remains the sole gate.
> The load-bearing line is **file-missing vs. file-present-zero-patterns**, not the pattern count.

This prevents the next reader from "normalizing" the two loads into one shared helper that loses the asymmetry. Seth flagged this as the cleanest articulation of the distinction written down for this codebase — worth preserving in-code before the pattern gets copied.

## 2. Log-on-empty-denylist

When the denylist file is present but parses to zero patterns, emit an audit-visible log line at load time:

```go
log.Printf("denylist %s loaded with 0 patterns (vetoing nothing; allowlist is sole gate)", denyPath)
```

Rationale: "empty by choice" is currently invisible. This is the kind of silent configuration drift that bites during an incident six months later when someone wonders why the denylist "isn't doing anything." The log line makes the intentional-empty state auditable without changing behavior.

## 3. Per-stream-cap

(Previously parked — carried forward with this bundle.) The per-stream output cap behavior still needs a sequencing decision from Alex. No new spec work required; ships with the other two when Alex schedules the pass.

## Sequencing rules

- Single cleanup pass — three separate host-exec touches at this point generates more noise than signal.
- Alex owns the "when."
- Seth specs the diff once sequencing lands; ping him.
- Any policy-shaped signal surfaced by smoke (allowlist/denylist behavior, exec-scope drift) routes through Seth *before* any posture change, even if it delays this bundle.

## Current state

- PATCH to `main.go` is staged (allowlist fail-closed fix).
- Smoke pending on corrid `7e1a4c9d`.
- Bundle lives at `orch-plans/host-exec-artifacts/`, spec at `orch-plans/host-exec-daemon-option-a.md`.
- Thread closed across Kyle/Seth as of 2026-04-23 ~20:45Z.
