# Kyle Autonomous Recovery V1

Status: implementation-ready plan
Owner: Kyle
Scope: autonomous recovery for `testing_ready` pipeline blockage

## Goal

Make Kyle able to operate without fresh operator input when an overarching mission is active. V1 focuses on keeping the DREM orchestrator pipeline moving when tasks are blocked in `testing_ready`.

The standing mission is: keep the C-Suite, orchestrator pipeline, and relevant services unblocked enough to continue useful work.

## Shared Decisions

- Kyle acts as a bounded recovery governor, not as an unrestricted operator.
- Kyle may perform bounded autonomous mutations when the action is tied to an active mission and matches policy.
- Kyle uses supported `dremctl`, HTTP, and orchestrator surfaces first.
- Kyle may use unsupported break-glass surfaces only when supported surfaces cannot unblock the mission.
- Break-glass actions are self-confirmed by policy, not operator-confirmed, but must record why supported recovery failed.
- Repeated use of the same break-glass recovery class must auto-file work to add a supported surface.
- Hard-ban areas remain escalation-only: credentials/secrets, force-push, destructive git reset/clean, broad Docker compose recreation, `drem-sglang` restart, and unclear product intent.
- Ambiguity is delegated to the owning C-Suite persona with a deadline and expected evidence, not escalated to the operator by default.
- Operator notification is summary-only unless a hard blocker or escalation-only action is reached.

## V1 Recovery Class

V1 handles `testing_ready` blockers end-to-end.

The recovery loop is:

```text
observe -> classify -> decide -> act/delegate -> verify -> audit -> follow up/escalate
```

## Classification Policy

For a task in `testing_ready`, Kyle classifies the blocker from supported surfaces:

- Infra/tooling-looking gate failure: one autonomous retry is allowed.
- Real code/test failure: request or file a focused remediation/fixer through the orchestrator path.
- Uncertain failure: delegate investigation to Mike or Seth with a deadline and expected evidence.
- Supported surface unavailable: use break-glass only if the mission is blocked and the action matches policy.

## Default Budgets

Per blocker class, Kyle gets:

- One retry.
- One remediation filing or delegation.
- One break-glass attempt.

After budget exhaustion, Kyle escalates or delegates further according to policy.

## Break-Glass Policy

Allowed break-glass categories:

- Allowlisted host commands for inspection and narrow repairs.
- Direct DB lifecycle repair for task state/assignment issues when supported APIs cannot express the required cleanup.
- Scoped non-sglang Docker actions with `--no-deps` when service health blocks the mission.

Before any break-glass action, Kyle must record:

- The active mission.
- The supported surface attempted.
- The exact blocker returned by that surface.
- The break-glass rule being used.
- The intended mutation and rollback or follow-up path.

Break-glass must not delete task, worker, event, or comment history unless separately escalated and approved.

## Mission Source

Kyle's autonomous loop should read a durable mission file, initially `~/.drem-csuite/kyle/mission.md` or an equivalent Kyle-owned mounted file. The mission defines:

- Overarching goal.
- Allowed actions.
- Break-glass limits.
- Budgets.
- Escalation-only action classes.
- Reporting cadence.

## Command Surface

Implement the recovery engine behind `dremctl` first:

```bash
dremctl kyle recover --dry-run
dremctl kyle recover --apply
```

The dry run must print a decision table containing:

- Task ID.
- Current state.
- Observed evidence.
- Classification.
- Policy rule.
- Proposed action.
- Whether the action is supported or break-glass.

The apply mode may execute only actions that match policy and budget.

## Audit Requirements

Every autonomous mutation must emit both:

- A structured audit event.
- A task comment.

The audit payload must include:

- `actor: kyle`.
- Policy rule used.
- Observed evidence.
- Supported surface or break-glass path used.
- Action taken.
- Result.
- Next follow-up or escalation condition.

## Runtime Loop After V1

After the command is proven, Kyle's autonomous loop should be both periodic and event-triggered:

- Periodic checks catch stale or missed events.
- Event-triggered checks react quickly to gate blockage, worker death, service degradation, or C-Suite communication failures.

Kyle should send operator summaries only when meaningful progress happens or escalation is required.

Kyle turns must remain bounded. If Kyle needs to monitor task progress, he should observe once, record follow-up state, write an outbox/state update when appropriate, and exit. Do not run long `sleep`/polling loops inside a single `opencode run` turn. Use `plans/kyle-poller-diagnostics.md` when Kyle appears silent or stale.

## Acceptance Criteria

V1 is accepted when live `testing_ready` blockers can be handled without fresh operator input such that each blocker leaves `testing_ready` into one of:

- Merger/progressing state.
- Focused remediation/fixer work through the orchestrator path.
- Delegated investigation with deadline and expected evidence.

Acceptance also requires:

- Dry-run output for the current live blockers.
- Apply mode for at least one allowlisted action.
- Structured audit event and task comment for each applied action.
- Budget enforcement tests.
- Break-glass self-confirmation path represented in tests, even if not exercised live.
- No autonomous execution of escalation-only actions.

## Implementation Slices

1. Add the `dremctl kyle recover` command surface and dry-run output.
2. Implement `testing_ready` observation and classification from supported task/event surfaces.
3. Implement policy and budget enforcement for retry, remediation/delegation, and break-glass decisions.
4. Implement apply mode for the first allowlisted recovery action.
5. Add structured audit event and task comment recording.
6. Add tests for dry-run classification, apply policy, budgets, audit payloads, and forbidden escalation-only actions.
7. Run live dry-run against current `testing_ready` blockers and then live apply only if the selected action matches policy.

## Open Implementation Notes

- Keep `drem-kyle` read-only. Autonomy belongs in the Kyle persona/recovery command path, not the global read model service.
- Prefer adding supported API/CLI surfaces over normalizing direct DB mutation.
- If a break-glass class succeeds, immediately file follow-up work to add the supported surface that would have made break-glass unnecessary.
- Diagnose container-Kyle silence with `plans/kyle-poller-diagnostics.md` before assuming the watcher missed delivery.
