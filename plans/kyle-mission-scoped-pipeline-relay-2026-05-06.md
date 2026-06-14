# Kyle Mission-Scoped Pipeline Relay

Status: test review held; missing full relay acceptance evidence
Owner: Kyle as mission governor; implementation via orchestrator task `31858ad9`
Source corrid: `b91e6a42`
Updated: 2026-05-06T05:13:14Z

## Mission

Give Kyle mission-scoped pipeline visibility so he can drive Kyle-owned or Kyle-sponsored missions to completion without long-polling inside persona turns and without receiving the full raw operations event stream.

## Role Split

Mike remains the broad pipeline operations manager. Mike should continue receiving broad operational events such as worker deaths, stuck tasks, scheduler starvation, merger issues, log gaps, container failures, capacity pressure, and retry loops.

Kyle is the mission governor. Kyle should receive only mission-lifecycle events for tasks he owns, created, or sponsors, plus explicit mission-impacting escalations from Mike, Seth, or Alex.

Seth remains quality gate advisor. Alex remains product and scope advisor.

## Filed Approach

Task `31858ad9` asks for the smallest correct implementation: extend `drem-ops-relay` if the existing routing model fits cleanly, otherwise add a small `drem-mission-relay`.

Preferred tracing is durable task metadata such as `mission_owner` or `governor`, plus `mission_corrid` for threading. The filed task includes a first-slice marker in the task description: `MISSION METADATA: mission_owner=kyle; governor=kyle; mission_corrid=b91e6a42; mission_kind=mission-scoped-pipeline-relay`. If that marker is used as the tracer bullet, durable metadata remains a named follow-up.

## Kyle Wake Criteria

Kyle should be woken for Kyle-created or Kyle-owned task events when a task reaches:

- `plan_review`
- `test_review`
- `testing_ready`
- `failed`
- `done`

Kyle should also be woken when a Kyle-owned task shows no progress after a bounded SLA, or when Mike, Seth, or Alex explicitly escalates a mission-impacting blocker.

## Message Contract

Relay messages to Kyle must include:

- Task ID
- Event or status
- Current task snapshot
- Mission owner or governor
- Mission corrid
- A bounded-turn instruction: one event, one assessment/action, one outbox, exit

## Acceptance Criteria

- Kyle gets event-driven wakeups for Kyle-owned mission lifecycle gates without all raw ops noise.
- Mike still receives broad operational events, or existing broad relay behavior remains intact.
- Kyle relay messages contain enough context for a one-turn decision or action.
- Tests cover routing Kyle-owned mission gates to Kyle.
- Tests cover non-routing of unrelated ops noise to Kyle.
- Tests cover Mike still receiving broad ops events or the existing Mike relay behavior remaining intact.
- Docs or plan guidance explain the role split, mission ownership metadata, mission corrid threading, and bounded-turn requirement.

## First-Turn Evidence

- `dremctl status`: one project, `drem-orchestrator`; before filing there were 828 tasks with `8af09a24` in `plan_review`.
- `dremctl tasks --limit 200`: no active equivalent mission-scoped relay task observed.
- `dremctl create-task`: created `31858ad9 -> classifying`.
- `dremctl tasks --limit 10`: after filing, `31858ad9` advanced to `planning` and `8af09a24` remained at `plan_review`.
- `dremctl approve 8af09a24`: approved the existing bind-mount mission plan gate, moving it to `test_writing`.

## Plan Gate Evidence

- 2026-05-06T04:43:58Z operator prod reported `31858ad9` at `plan_review` and requested one bounded assessment/action for that task only.
- `dremctl tasks --status plan_review --limit 10 --json` confirmed `31858ad9-913e-4e45-a2f3-8c51f920e3e7` was the active `plan_review` task.
- `dremctl events --limit 40` showed the task-created description carried mission metadata, Kyle/Mike/Seth/Alex role split, Kyle lifecycle routing scope, Mike broad-ops preservation, required relay message payload, tests for routing/non-routing and Mike behavior, docs expectations, and safety exclusions.
- Assessment: plan satisfies the mission acceptance criteria for the plan-review gate.
- Action: `dremctl approve 31858ad9` moved the task to `test_writing` at 2026-05-06T04:45:21Z.
- Verification: `dremctl tasks --limit 20` showed `31858ad9` in `test_writing` and generated mission relay subtasks for ownership metadata, mission routing, relay event handling, relay entrypoint/command coverage, integration coverage, and governance docs/checks.

## Next Signal

Watch `31858ad9` for a revised `test_review`, `testing_ready`, `failed`, or `done`. Until this relay exists, operator prods are still needed for Kyle to see those events. Also continue watching `8af09a24` separately for `testing_ready`, `failed`, or `done`.

## Test Review Gate Evidence

- 2026-05-06T04:55:45Z operator prod reported parent `31858ad9` at `test_review`, with relay test subtask `e042c90a` done and remaining relay subtasks backlog.
- `dremctl tasks --status test_review --limit 10 --json` confirmed `31858ad9-913e-4e45-a2f3-8c51f920e3e7` at `test_review`.
- `dremctl tasks --limit 80 --json` showed `e042c90a` done, while acceptance-relevant relay coverage remained backlog: `be6077c4` mission routing unit tests, `d4dab2fd` relay command tests, `a0230f21` relay integration coverage, `63d31e39` governance documentation checks, and implementation/routing tasks.
- Assessment: evidence was insufficient to advance the test-review gate because the filed acceptance criteria require routing Kyle-owned mission gates to Kyle, non-routing of unrelated ops noise, Mike broad-ops preservation, relay command/integration coverage, and governance documentation guidance. Only ownership metadata model test evidence was present.
- Action: `dremctl reject 31858ad9 --reason ...` moved the parent from `test_review` back to `test_writing` at 2026-05-06T04:56:48Z, rejected `e042c90a`, and cloned revision task `b40c8413`, now `in_progress`.

## Second Test Review Hold Evidence

- 2026-05-06T05:11:42Z operator prod reported parent `31858ad9` back at `test_review` after redone ownership metadata test subtask `b40c8413` completed.
- `dremctl tasks --status test_review --limit 20 --json` confirmed `31858ad9-913e-4e45-a2f3-8c51f920e3e7` at `test_review`, updated 2026-05-06T05:07:43Z.
- `dremctl tasks --limit 120 --json` showed `b40c8413` done and prior `e042c90a` rejected, while acceptance-relevant relay work remained backlog: `be6077c4` mission routing unit tests, `7840c593` mission routing logic, `a0230f21` relay integration coverage, `6ffdf076` relay event handling, `d4dab2fd` relay command tests, `c864576c` relay entrypoint or mode, `63d31e39` governance documentation checks, and `d3b8d2c0` mission governance relay contract docs.
- Assessment: the new evidence still satisfies only the ownership metadata test slice. It does not satisfy the full acceptance bar for Kyle-owned mission gate routing, unrelated-noise non-routing, Mike broad-ops preservation, relay command/integration/event coverage, or governance contract documentation.
- Action: held at `test_review` and did not approve. I also did not repeat the reject mutation in this turn because the previous reject path already cloned only the ownership metadata test, and another identical reject would likely churn `b40c8413` rather than produce the missing acceptance evidence.
- Next signal: one of the missing relay acceptance subtasks moves from backlog to done with evidence, the parent is explicitly rescoped, or `31858ad9` leaves `test_review`.
