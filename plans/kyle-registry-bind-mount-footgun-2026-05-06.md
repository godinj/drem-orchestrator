# Kyle Registry Bind-Mount Foot-Gun

Status: planning after targeted plan-review rescope
Owner: Kyle as recovery governor; implementation via orchestrator task `8af09a24`
Source corrid: `a83f4d6e`
Updated: 2026-05-06T05:51:35Z

## Mission

Close the containerization follow-up documented in `docs/containerization/remaining-work.md` as "Kyle registry bind-mount foot-gun".

## Problem

`deploy/compose/global.yml` currently bind-mounts `${HOME}/.drem/projects.toml` for Kyle. If the host source is missing, or `sudo` changes `$HOME` to `/root`, Docker Compose can silently create the source path as a root-owned directory. Kyle then crash-loops because `projects.toml` is a directory instead of a file.

## Filed Approach

Task `8af09a24` asks for an explicit `DREM_HOME`-based Kyle registry source and a small Kyle container entrypoint guard that fails fast before Kyle starts when the registry path is missing, not a regular file, or unreadable. A Compose required-file/config primitive is acceptable only if it prevents Docker from creating a directory and preserves a clear operator-readable failure.

## Expected Files

- `deploy/compose/global.yml`
- Kyle Dockerfile or entrypoint under `deploy/docker` or `cmd/drem-kyle`, as appropriate
- `docs/containerization/install.md` or related operator-facing docs if setup behavior changes
- Tests in `deploy/compose`, `deploy/docker`, `cmd/drem-kyle`, or the nearest package covering compose rendering and entrypoint validation

## Requested Evidence

- Missing registry source fails before Kyle starts with a clear message.
- Directory-valued registry source fails before Kyle starts with a clear message.
- Valid `projects.toml` path still starts normally.
- Focused package tests and compose/template tests are run.
- Docs update decision is explicit.
- Every review-gate decision cites supported task evidence.

## First-Turn Evidence

- `dremctl status`: one project, `drem-orchestrator`, recent task set all done before filing.
- `dremctl tasks --limit 40`: no active recent registry bind-mount task observed before filing.
- `dremctl create-task`: created `8af09a24 -> classifying`.
- `dremctl tasks --limit 10`: `8af09a24` remained `classifying`, worker `f8dbe81c-820c-4431-9acd-92a7127c6c81`.
- `dremctl events --limit 20`: `task_created` event at 2026-05-06T04:25:05Z includes the full problem, approach, expected files, and test/docs expectations.

## Next Signal

Watch `8af09a24` for `testing_ready`, `failed`, or `done`. If it reaches another review gate, Kyle may make one bounded approve/pass/fail decision using task evidence.

## Test Review Gate Evidence

- 2026-05-06T04:55:45Z operator prod reported parent `8af09a24` at `test_review`, with test subtasks `b8960dce` and `b3e912e3` done.
- `dremctl tasks --status test_review --limit 10 --json` confirmed `8af09a24-0b92-4f87-9d7d-70c6f77cdaea` at `test_review`.
- `dremctl events --limit 80` showed `b8960dce` and `b3e912e3` reconciled to `done`, then parent `8af09a24` moved from `test_writing` to `test_review` with reason `all test subtasks done`.
- Assessment: the completed test titles directly match the filed evidence needs for compose registry bind-mount regression coverage and Kyle entrypoint registry guard coverage, while implementation/docs subtasks remain backlog for the next phase.
- Action: `dremctl approve 8af09a24` moved the parent from `test_review` to `in_progress` at 2026-05-06T04:56:48Z.

## Testing Ready Hold Evidence

- 2026-05-06T05:21:09Z operator prod reported `8af09a24` at `testing_ready`, all subtasks apparently done, and fixer `fixer-8af0-63db` attached after repeated `/bare` push failures and an `exit status 128` crash.
- `dremctl status` showed one `testing_ready` task and recent events dominated by `8af09a24` heartbeat, commit, build_error, push, and crash activity.
- `dremctl tasks --limit 50` and `dremctl tasks --status testing_ready --limit 10 --json` confirmed `8af09a24-0b92-4f87-9d7d-70c6f77cdaea` remains `testing_ready`, updated at 2026-05-06T05:21:54Z, with assigned worker `b51feaf7-1f3a-466c-bff5-da68c9fec1f0`.
- `dremctl events --limit 100` showed `fixer-8af0-63db` spawned at 05:15:14Z, heartbeats through 05:21:15Z, repeated `build_error` messages `failed to push some refs to '/bare'` at 05:17:15Z, 05:18:15Z, and 05:20:15Z, a crash `exit status 128` at 05:19:04Z, and then a successful commit event at 05:21:10Z on `feature/8af09a24-fix-kyle-registry-bind-mount-directory-c`.
- `dremctl logs --container c3fc95b93fd046624b9871117af665d33ac2f89a301a930cf7507d7f2434772f --since 2026-05-06T05:17:00Z` failed with `status 502: stream logs: container logs: Cannot connect to the Docker daemon at unix:///var/run/docker.sock`; no container-log evidence was used for the gate call.
- Assessment: hold. The task is nominally `testing_ready`, but supported events show the fixer path is still alive and recovering after push/crash failures, with a fresh commit and continuing heartbeats. Passing or failing the gate now risks racing recovery and would not be evidence-based.
- Action: no gate or recovery mutation this turn.
- Next signal: watch for `8af09a24` to move to `done`, `failed`, leave `testing_ready`, or emit a stable post-recovery event sequence with no further `/bare` push failures.

## Bounded Reassessment Gate Action

- 2026-05-06T05:33:55Z operator requested one bounded reassessment because parent `8af09a24` remained `testing_ready` after all bind-mount subtasks were done and no new events had appeared since the 05:21:15Z fixer heartbeat.
- `dremctl status` before mutation showed one `testing_ready` task, one `test_review` task, and zero active project workers for `drem-orchestrator`; recent events still ended at the `fixer-8af0-63db` heartbeat and successful commit sequence.
- `dremctl tasks --status testing_ready --limit 10 --json` confirmed `8af09a24-0b92-4f87-9d7d-70c6f77cdaea` was still `testing_ready`, updated around 05:35Z, with assigned worker `b51feaf7-1f3a-466c-bff5-da68c9fec1f0`.
- `dremctl events --since 2026-05-06T05:21:16Z --limit 50` returned no events before the gate mutation, so the prior recovery-race hold no longer had live event support. `dremctl worker b51feaf7` and `dremctl worker fixer-8af0-63db` returned 404/not found while status showed zero active project workers, consistent with a stale assignment rather than live fixer work.
- Assessment: supported testing gate action was available. The child evidence matched the filed acceptance bar: compose bind-mount regression coverage, Kyle entrypoint guard coverage, DREM_HOME bind-mount implementation, entrypoint validation implementation, docs consistency test, and DREM_HOME/operator docs were all `done`.
- Action: `dremctl pass 8af09a24` at 2026-05-06T05:35:30Z moved the parent from `testing_ready` to `merging` with event details `action=test_passed`.
- Verification: `dremctl tasks --status merging --limit 10 --json` showed `8af09a24` in `merging`; `dremctl events --since 2026-05-06T05:35:30Z --limit 20` showed the test-pass status change and spawned merger `merger-8af0-51c8` for the task.
- Secondary scope: `31858ad9` remains `test_review` and was not approved; missing mission routing, relay integration/command/event handling, Mike broad-ops preservation, and governance docs subtasks remain backlog.
- Next signal: watch `8af09a24` for merger terminal state, specifically `done` or a `failed`/merge-error event after 2026-05-06T05:35:30Z. No operator action is required unless it stays `merging` without merger progress or emits a merger failure; then the next supported action is a bounded recovery pass using `dremctl events`, `tasks`, and either `retry` or Mike routing depending on the failure evidence.

## Merge Failure Recovery Retry Evidence

- 2026-05-06T05:44:49Z operator reported `8af09a24` failed after merge pre-push tests and requested one bounded supported recovery action, preferring `dremctl retry` if policy allowed.
- `dremctl status` showed one failed task and zero active project workers. `dremctl tasks --status failed --limit 10 --json` confirmed `8af09a24-0b92-4f87-9d7d-70c6f77cdaea` was `failed`, updated 2026-05-06T05:37:00Z.
- `dremctl events --since 2026-05-06T05:35:00Z --limit 30` showed Kyle's prior `testing_ready -> merging` pass at 05:35:30Z, merger `merger-8af0-51c8` spawned at 05:35:34Z, `merge_result` at 05:36:58Z with `success:false`, `failure_reason: tests_failed`, and parent `merging -> failed` at 05:37:00Z with reason `merge aborted: pre-push tests failed`.
- Failure evidence was path/layout-related in the merger test environment: `tests/cmd/drem-kyle/TestEntrypointRegistryGuard` attempted `/work/tests/cmd/drem-kyle/entrypoint.sh` and got `No such file or directory`, and `tests/deploy/TestKyleComposeRegistryBindMount` reported `Compose file not found: ../deploy/compose/global.yml`.
- Assessment: retry was a safe supported recovery action. The failure was a deterministic test-path/layout defect after merge-time full-suite execution, and the pipeline reliability policy allows remediation/retry for merger `tests_failed` while budget remains.
- Action: `dremctl retry 8af09a24` returned `task 8af09a24 -> backlog` at 2026-05-06T05:45:48Z.
- Verification: subsequent `dremctl events --since 2026-05-06T05:37:00Z --limit 30` showed `failed -> backlog` with `action=retry`, then `backlog -> planning -> plan_review` at 2026-05-06T05:45:53Z. `dremctl tasks --limit 30 --json` showed `8af09a24` at `plan_review` and `31858ad9` still at `test_review`; no approval was made for `31858ad9`.
- Next signal: review the new `8af09a24` plan gate only for a targeted remediation that fixes the test layout assumptions for `tests/cmd/drem-kyle` and `tests/deploy`. Keep `31858ad9` held unless the missing relay/routing/integration/docs scope is scheduled or the parent is explicitly rescoped with evidence.

## Targeted Retry Plan Review Rescope

- 2026-05-06T05:49:53Z operator requested one bounded Kyle governor turn for `8af09a24` at `plan_review`, scoped only to merger test-environment path/layout assumptions in `tests/cmd/drem-kyle` and `tests/deploy`; `31858ad9` was explicitly out of approval scope.
- `dremctl status` showed one `plan_review` task, one `test_review` task, and zero project workers. `dremctl tasks --status plan_review --limit 10 --json` confirmed `8af09a24-0b92-4f87-9d7d-70c6f77cdaea` at `plan_review`, updated 2026-05-06T05:45:53Z. `dremctl tasks --limit 120 --json` showed `31858ad9` still at `test_review` with relay/routing/integration/docs subtasks still backlog.
- `dremctl events --since 2026-05-06T05:45:40Z --limit 120 --json` showed only retry and status transitions for `8af09a24`: `failed -> backlog` by user retry, then `backlog -> planning -> plan_review`. It did not expose any new plan/subtask evidence targeting the two merger failures.
- Assessment: not sound to approve. The supported evidence showed task state, prior merger failure, and retry transition, but not a scoped plan for fixing `tests/cmd/drem-kyle/TestEntrypointRegistryGuard` pathing or `tests/deploy/TestKyleComposeRegistryBindMount` compose-file lookup.
- Action: `dremctl reject 8af09a24 --reason ...` moved `8af09a24` from `plan_review` to `planning` at 2026-05-06T05:51:29Z. Feedback required the smallest remediation: fix `tests/cmd/drem-kyle` entrypoint discovery for merger/full-suite layout, fix `tests/deploy` compose-file discovery for merger/full-suite layout, avoid reworking DREM_HOME/registry guard/docs unless directly required, and provide focused evidence for `go test ./tests/cmd/drem-kyle ./tests/deploy` under the merger layout while preserving existing package tests.
- Verification: `dremctl tasks --limit 30 --json` showed `8af09a24` at `planning`, `31858ad9` still at `test_review`, and no approval for `31858ad9`.
- Next signal: watch `8af09a24` for a revised `plan_review` carrying visible targeted remediation evidence, or later `test_review`/`testing_ready` once the scoped path fixes are scheduled and completed.

## Second Targeted Retry Plan Review Rescope

- 2026-05-06T05:54:48Z operator requested one bounded Kyle governor turn because `8af09a24` returned to `plan_review` after the prior rejection/rescope at 2026-05-06T05:51:29Z. Scope remained limited to the merger/full-suite layout assumptions in `tests/cmd/drem-kyle` and `tests/deploy`; `31858ad9` remained explicitly out of approval scope.
- `dremctl status` showed one `plan_review` task, one `test_review` task, and zero project workers. `dremctl tasks --status plan_review --limit 10 --json` confirmed `8af09a24-0b92-4f87-9d7d-70c6f77cdaea` at `plan_review`, updated 2026-05-06T05:51:52Z. `dremctl tasks --limit 120 --json` showed `31858ad9-913e-4e45-a2f3-8c51f920e3e7` still at `test_review` with mission relay routing, integration, command, event-handling, and docs subtasks still backlog.
- `dremctl events --since 2026-05-06T05:51:20Z --limit 120 --json` showed only the prior `plan_review -> planning` rejection and the orchestrator `planning -> plan_review` transition for `8af09a24`; it did not expose any new targeted plan/subtask evidence for fixing the two merger failures.
- Assessment: not sound to approve. The visible evidence still did not show a scoped remediation plan for making `tests/cmd/drem-kyle` locate the Kyle entrypoint without hard-coding `/work/tests/cmd/drem-kyle/entrypoint.sh`, or making `tests/deploy` locate `deploy/compose/global.yml` without relying on `../deploy/compose/global.yml`.
- Action: `dremctl reject 8af09a24 --reason ...` moved `8af09a24` from `plan_review` back to `planning` at 2026-05-06T05:56:06Z. Feedback repeated the smallest acceptable scope and required visible plan/subtask evidence plus focused proof that `go test ./tests/cmd/drem-kyle ./tests/deploy` passes under the merger layout while preserving existing package tests.
- Verification: `dremctl tasks --limit 30 --json` showed `8af09a24` at `planning`, `31858ad9` still at `test_review`, and no approval for `31858ad9`. `dremctl events --since 2026-05-06T05:51:20Z --limit 30 --json` showed the new `plan_review -> planning` rejection at 2026-05-06T05:56:06Z.
- Next signal: watch for `8af09a24` to return to `plan_review` with visible targeted remediation evidence for both path/layout fixes, or route a blocker if the planner cannot expose plan details through the supported `dremctl` status/events/tasks surfaces.

## Retry Test Review Gate Action

- 2026-05-06T06:19:16Z operator requested one bounded mission-governor turn for `8af09a24` at `test_review`, scoped to the merger/full-suite test layout assumptions and explicitly excluding approval of `31858ad9`.
- `dremctl status` showed two `test_review` tasks and zero project workers. `dremctl tasks --status test_review --limit 20 --json` confirmed `8af09a24-0b92-4f87-9d7d-70c6f77cdaea` and `31858ad9-913e-4e45-a2f3-8c51f920e3e7` at `test_review`.
- `dremctl tasks --limit 160 --json` showed the retry test subtasks done: `71146cb3` add install documentation coverage, `315346ca` add compose registry mount tests, `e1bf1d20` add entrypoint registry guard tests, and `2c0e7f37` add integration regression tests for pre-push paths. It also showed the original DREM_HOME bind mount, registry guard, and docs subtasks from the prior pass still done.
- `dremctl events --since 2026-05-06T06:00:00Z --limit 300` showed the four retry subtasks moved through in-progress to done and parent `8af09a24` moved from `test_writing` to `test_review` with reason `all test subtasks done`.
- `dremctl logs` for the retry worker containers failed with `status 502: stream logs: container logs: Cannot connect to the Docker daemon at unix:///var/run/docker.sock`; no container-log evidence was used for the gate call.
- Assessment: sufficient for the `test_review` gate under the operator's scope. The completed retry subtasks directly target the prior merger path/layout failures and preserve the original DREM_HOME bind mount, registry guard behavior, and docs scope.
- Action: attempted the operator-requested `dremctl pass 8af09a24`, but the orchestrator rejected it with `wrong status: task in status "test_review", expected one of [testing_ready]`. Because this was a test-review gate, the supported equivalent mutation was `dremctl approve 8af09a24`, which moved the parent from `test_review` to `in_progress` at 2026-05-06T06:19:03Z.
- Verification: `dremctl tasks --limit 40 --json` showed `8af09a24` at `in_progress`; `dremctl events --since 2026-05-06T06:12:00Z --limit 60 --json` showed `test_review -> in_progress` with action `test_review_approved`. `31858ad9` remained `test_review` and was not approved.
- Next signal: watch `8af09a24` for implementation progress, `testing_ready`, or failure. Keep `31858ad9` held until its relay subtasks provide the missing acceptance evidence or the operator gives a separate scoped directive.
