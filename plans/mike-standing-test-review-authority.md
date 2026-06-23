# Mike Standing Test-Review Authority

Status: active  
Owner: operator  
Accepted by: operator  
Effective: indefinitely, until superseded by a newer operator directive

## Delegation

Mike has standing autonomous authority to decide and mutate every `test_review`
gate for every registered Drem project.

This delegation applies to all current and future tasks. Mike does not need a
per-task operator or Kyle message before acting on a bare `ops-relay`
notification that a task entered `test_review`.

## Allowed Actions

Mike may run:

```bash
dremctl approve <task-id-prefix>
dremctl reject <task-id-prefix> --reason "<specific reason>"
```

Approval moves the task from `test_review` to `in_progress`. Rejection moves it
back to `test_writing` with feedback for revised tests.

## Decision Criteria

Mike should approve when the written tests appear to match the approved task
intent and there are no operationally significant blockers.

Mike should reject when any of these are true:

- The test branch includes unrelated or destructive source, config, prompt,
  plan, trace, or generated-artifact changes.
- The tests do not cover the task intent or acceptance criteria.
- Verification is missing, failed for reasons attributable to the task, or is
  too incomplete to trust.
- The test work appears to depend on missing branches, missing worktrees,
  stale artifacts, or inconsistent task state.
- The test contract would likely cause implementation agents to ship scope
  drift, unsafe behavior, or unreviewable work.

If evidence is incomplete but the risk is low, Mike may approve and report the
residual risk. If evidence is incomplete and the risk affects correctness,
scope, or repository hygiene, Mike should reject with concrete feedback.

## Reporting Requirements

After every autonomous mutation, Mike must reply to `operator` with:

- Task ID and title.
- Prior gate: `test_review`.
- Action taken: `approve` or `reject`.
- Authorization source: this standing authority artifact.
- Evidence inspected.
- Exact `dremctl` result or blocker.
- For rejections, the feedback given to the next test-writing pass.

## Limits

This delegation does not grant Mike authority to:

- Approve or reject `plan_review` gates.
- Pass or fail `testing_ready` gates beyond whatever separate authority already
  exists for that gate.
- Modify project source code directly.
- Use destructive git or Docker commands, force push, change credentials,
  disclose secrets, or restart `drem-sglang`.
- Override a newer operator directive.
