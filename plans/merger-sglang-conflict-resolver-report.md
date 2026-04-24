# Merger SGLang Conflict Resolver Report

Date: 2026-04-24

## Summary

The merger path now keeps `drem-merger` deterministic while adding an LLM-backed conflict resolution path for real merge conflicts.

`drem-merger` still performs the one-shot feature-to-integration merge, test run, push, and feature-branch delete. It does not call an LLM directly. When the binary reports a conflict, the orchestrator now spawns a fixer-based resolver agent that uses the configured direct tool path: Gemma4 via SGLang through GQ.

## Commits

- `923fedf Configure merger for SGLang Gemma`
- `ee68cfe Spawn resolver for merge conflicts`
- `4d10424 Preserve resolver budget on spawn failure`

## Runtime Workflow

1. A parent task reaches `MERGING`.
2. Orch dispatches the one-shot `drem-merger` container.
3. `drem-merger` attempts the deterministic feature-to-integration merge.
4. If the merge succeeds, existing completion behavior continues.
5. If `drem-merger` returns `FailureReason == "conflict"` with conflict files, orch records resolver context on the task.
6. Orch spawns a fixer-based merge resolver agent.
7. The resolver receives conflict files, diagnosis, affected files, and suggested fix in its prompt.
8. The resolver uses `sglang-direct` with `gemma4-26b` through GQ at `http://gq:8090/v1/chat/completions`.
9. While the resolver is active, orch skips further merger dispatch for that task to avoid repeated merger containers.
10. When the resolver completes, orch clears the assignment and leaves the parent task in `MERGING`.
11. The next merge tick retries deterministic `drem-merger`.
12. If the retry still conflicts after the bounded resolver budget is exhausted, orch falls back to terminal merge-conflict failure.

## Configuration

The local and generated project config now include:

```toml
[agents.merger]
  provider = "sglang-direct"
  model    = "gemma4-26b"
```

Generated project config also points direct tool calls at GQ:

```toml
[direct_tool_agent]
  endpoint = "http://gq:8090/v1/chat/completions"
  model    = "gemma4-26b"
```

## Safety Properties

- `drem-merger` remains deterministic and auditable.
- LLM behavior is isolated to the resolver agent, not the merge/push binary.
- Parent tasks do not terminally fail before one resolver attempt is made.
- Active resolver agents suppress repeated merger dispatch.
- Resolver completion returns the parent to `MERGING` for retry.
- Resolver budget exhaustion preserves existing terminal conflict behavior.
- Resolver spawn failure does not consume resolver budget.
- No SGLang restart was performed.
- No push to origin was performed.

## Verification

Local tests run during implementation and smoke testing:

- `go test ./cmd/drem`
- `go test ./internal/projects ./internal/model`
- `go test -count=1 ./internal/orchestrator`
- `go test -count=1 ./internal/prompt ./internal/model`

Live endpoint verification before the resolver change confirmed:

- `drem-sglang` was up and healthy.
- `drem-gq` was up.
- GQ `/v1/models` reported `gemma4-26b`.
- SGLang `/v1/models` reported `gemma4-26b`.
- Chat completion through GQ returned `gq-ok`.
- Direct SGLang chat completion returned `sglang-ok`.
- GQ metrics were live with breaker closed and slots available.

## C-Suite Attention Points

- Mike: watch for `merge_conflict_resolver_spawned`, `merge_conflict_resolver_completed`, and `merge_conflict_resolver_failed` events during canaries.
- Alex: task recovery behavior is now resolver-first on real merge conflicts, then deterministic merge retry.
- Seth: architecture boundary is deliberate: deterministic merger binary, LLM resolver agent, bounded retry budget.
- Kyle: user-facing wording should say conflict resolution uses Gemma4 via SGLang/GQ; the merger binary itself remains deterministic.

## Open Risks

- The resolver budget is currently a constant set to one attempt. That is safest for avoiding loops but may be too strict for transient resolver failures.
- `spawn_failed` state persistence is best-effort if saving the state itself fails, but budget preservation is safe because the attempt count increments only after successful resolver spawn.

## Mike Read-Only Verification Blocker - 2026-04-24T21:43:37Z

Signal:
- Mike checked the supported C-Suite surfaces available in his container and found `dremctl` reachable, with status/tasks/workers/history/events/logs plus lifecycle mutations only.
- `6b6eb427` remains `testing_ready` with `worker=-`; world health remains OK with zero running workers.
- Recent events still show the known merger/reconciler chain and no resolver spawned/completed/failed events.
- There is no supported `dremctl`/orchestrator command available to create or dispatch an arbitrary read-only cold-worker commit investigation, and no exposed endpoint to inspect commit contents for `923fedf`, `ee68cfe`, or `4d10424`.

Decision:
- Treat implementation signoff evidence for `923fedf`, `ee68cfe`, and `4d10424` as blocked on a missing supported verification surface.
- Do not claim resolver implementation signoff from C-Suite until commit contents are inspected through a supported read-only surface or an explicit bounded break-glass directive changes the scope.
- Keep Mike on material-only resolver watch and keep Seth's boundary review separate from implementation signoff.

## Kyle Routing - 2026-04-24T21:30:21Z

Signal:
- Operator reported the merger SGLang conflict resolver path is implemented in commits `923fedf`, `ee68cfe`, and `4d10424`.
- Live world-state surface remains healthy, with zero running workers and one `testing_ready` task at the time of triage.
- Recent events still show the pre-change `6b6eb427` merger/reconciler failure chain; no resolver events were visible in the last 25 events at triage time.

Decision:
- Route Mike to watch canaries specifically for `merge_conflict_resolver_spawned`, `merge_conflict_resolver_completed`, and `merge_conflict_resolver_failed`.
- Route Alex to align product/operator wording around resolver-first recovery followed by deterministic merger retry.
- Route Seth to review the architecture boundary: deterministic `drem-merger`, LLM resolver agent through Gemma4 via SGLang/GQ, and bounded retry budget.
- Use external wording that conflict resolution uses Gemma4 via SGLang/GQ, while the merger binary itself remains deterministic.

## Kyle Processing - 2026-04-24T21:32:42Z

Signal:
- Alex aligned product/operator wording as resolver-first merge recovery: deterministic `drem-merger` still owns merge authority, and the resolver is a bounded fixer-based assist only after a real conflict with conflict files exists.
- Mike acknowledged the resolver canary watch and confirmed no `merge_conflict_resolver_spawned`, `merge_conflict_resolver_completed`, or `merge_conflict_resolver_failed` events were visible in the latest supported-surface check.
- Kyle verified `dremctl status`, `dremctl tasks --limit 20`, and `dremctl events --limit 25`: the orchestrator surface is reachable, zero workers are running, `6b6eb427` remains `testing_ready`, and recent events still show only the pre-change merger/reconciler chain.

Decision:
- Operator-facing wording is now: resolver-first merge recovery uses Gemma4 via SGLang/GQ for bounded conflict repair, then returns to deterministic `drem-merger` for the merge retry.
- Do not describe this as a CanaryV17 reroute or as non-deterministic merger authority unless canary evidence changes.
- Keep Mike on material resolver canary signals: resolver spawned, completed, failed, spawn failure, budget exhaustion, repeated merger dispatch while a resolver is active, evidence risk, or supported-surface change.

## Mike Resolver Watch ACK - 2026-04-24T21:34:05Z

Signal:
- Mike confirmed he owns the merger SGLang resolver canary watch.
- Mike's latest supported-surface check found `dremctl` reachable, zero running workers, one `testing_ready` task, and no `merge_conflict_resolver_spawned`, `merge_conflict_resolver_completed`, or `merge_conflict_resolver_failed` events.
- Kyle independently verified the same current posture through world summary, `dremctl status`, `dremctl tasks --limit 20`, and `dremctl events --limit 25`.

Decision:
- No lifecycle mutation, product reroute, host-exec expansion, or operator escalation is required from this ACK.
- Keep Mike on material resolver canary signals only: resolver spawned/completed/failed, resolver spawn failure, repeated merger dispatch while a resolver is active, resolver budget exhaustion, evidence risk, or supported-surface change.

## Seth Architecture Review - 2026-04-24T21:36:13Z

Signal:
- Seth returned a conditional architecture pass, not implementation signoff.
- The reported boundary satisfies the safety bar if implemented as described: deterministic `drem-merger` retains merge/test/push/delete authority, LLM behavior is isolated to a fixer resolver, active resolver suppression prevents repeated dispatch, resolver completion returns the parent to `MERGING`, and resolver budget increments only after successful spawn.
- Seth could not inspect commits `923fedf`, `ee68cfe`, or `4d10424` because his container lacks a repo checkout and read-only `host-exec git ... show` was allowlist-denied.
- World summary at `2026-04-24T21:36:00Z` reports health OK, zero running workers, 30 failed workers, 107 in-flight tasks, and one `testing_ready` task.

Decision:
- Kyle routed Mike to seek implementation-level verification through a supported `dremctl`/orchestrator/cold-worker read-only path.
- Do not claim implementation signoff until commit evidence is inspected.
- Do not widen host-exec, mutate `6b6eb427`, restart SGLang, or use legacy tmux/temp-worker paths without an explicit operator-scoped directive.

## Mike Resolver Watch Reaffirmed - 2026-04-24T21:38:41Z

Signal:
- Mike reaffirmed ownership of the material-only ops watch for merger SGLang resolver canary signals.
- Kyle checked the current supported surfaces: world health is OK, `dremctl` is reachable, zero workers are running, `6b6eb427` remains `testing_ready`, and the latest event window still shows no resolver spawned/completed/failed signal.

Decision:
- No lifecycle mutation, product reroute, host-exec expansion, SGLang restart, or operator escalation is required from this ACK.
- Keep Mike on material-only resolver watch: resolver spawned/completed/failed, resolver spawn failure, resolver budget exhaustion, repeated merger dispatch while a resolver is active, evidence risk, or supported-surface change.

## Mike Material-Only Watch ACK - 2026-04-24T21:41:10Z

Signal:
- Mike acknowledged the resolver canary posture again and confirmed material-only watch remains active.
- Kyle checked the current supported surfaces: world health is OK, `dremctl status` is reachable, zero workers are running, and one task remains `testing_ready`; no new resolver event signal was reported in this ACK.

Decision:
- No lifecycle mutation, product reroute, host-exec expansion, blind retry, SGLang restart, or operator escalation is required from this signal.
- Keep Mike on material-only resolver canary reporting: resolver spawned/completed/failed, resolver spawn failure, repeated merger dispatch while a resolver is active, resolver budget exhaustion, evidence risk, or supported-surface change.

## Seth Boundary ACK - 2026-04-24T21:41:53Z

Signal:
- Seth acknowledged the merger resolver boundary review remains bounded to four architecture questions: deterministic `drem-merger` isolation, resolver-active dispatch suppression, resolver completion returning the parent to `MERGING`, and resolver budget preservation when spawn fails.
- Seth explicitly did not grant implementation signoff and will report architecture findings on commits `923fedf`, `ee68cfe`, and `4d10424` separately before the boundary is treated as verified.

Decision:
- Treat this as scope confirmation only.
- No lifecycle mutation, host-exec expansion, SGLang restart, legacy tmux/temp-worker route, or operator-facing verification claim is implied by this ACK.

## Alex Product Boundary ACK - 2026-04-24T21:42:29Z

Signal:
- Alex recorded CanaryV17 as closed from the product side with no reroute, mutation, or escalation open.
- Kyle checked the current supported surfaces: world health is OK, `dremctl status` is reachable, zero workers are running, `6b6eb427` remains `testing_ready` with `worker=-`, and the latest event window still shows no resolver spawned/completed/failed signal.

Decision:
- No product routing, lifecycle mutation, host-exec expansion, blind retry, SGLang restart, or operator escalation is required from this ACK.
- Keep Mike on material-only resolver canary reporting and Seth on implementation-evidence review before any verification claim.

## Seth Scope Confirmation ACK - 2026-04-24T21:46:51Z

Signal:
- Seth confirmed Kyle's note is treated as scope confirmation only, not implementation signoff.
- Seth's active review boundary remains limited to deterministic `drem-merger` isolation, resolver-active dispatch suppression, resolver completion returning the parent to `MERGING`, and resolver budget preservation when spawn fails.
- Seth will deliver separate architecture findings for commits `923fedf`, `ee68cfe`, and `4d10424` before the boundary is characterized as verified.
- Current world summary remains healthy: zero running workers, 30 failed workers, 107 in-flight tasks, and one `testing_ready` task.

Decision:
- No lifecycle mutation, broader host-exec authority, SGLang restart authority, legacy tmux/temp-worker route, or operator-facing verification claim is implied by this ACK.
- Keep Seth on architecture verification for `923fedf`, `ee68cfe`, and `4d10424`; keep Mike on material resolver canary signals only.

## Alex CanaryV17 Boundary ACK - 2026-04-24T21:47:30Z

Signal:
- Alex acknowledged the CanaryV17 product boundary and confirmed no Alex-side reroute or lifecycle action is open.
- Live world summary remains consistent with that boundary: health OK, zero running workers, 30 failed workers, 107 in-flight tasks, and one `testing_ready` task.
- Alex will wait for material resolver evidence through Mike and implementation-evidence review from Seth before treating CanaryV17 as verified.

Decision:
- No product reroute, lifecycle mutation, host-exec expansion, retry, SGLang action, or operator escalation is required from this ACK.
- Keep Mike on material resolver canary signals and Seth on implementation-evidence review before any verification claim.

## Mike Resolver Material-Only ACK - 2026-04-24T21:48:32Z

Signal:
- Mike acknowledged the resolver canary lane remains material-only.
- Named material triggers remain resolver spawned, resolver completed, resolver failed, resolver spawn failure, resolver budget exhaustion, repeated merger dispatch while a resolver is active, evidence risk, or supported-surface change.
- Live world summary at `2026-04-24T21:48:21Z` remains healthy: zero running workers, 30 failed workers, 107 in-flight tasks, and one `testing_ready` task.

Decision:
- No lifecycle mutation, product reroute, host-exec expansion, SGLang restart, or operator escalation is required from this ACK.
- Keep Mike on material-only resolver canary reporting and keep Seth's implementation-evidence review separate before any verification claim.

## Mike CanaryV17 Containment Watch ACK - 2026-04-24T21:49:04Z

Signal:
- Mike acknowledged continued CanaryV17 evidence-preservation containment on material triggers only: evidence at risk, unexpected retry or merge movement, supported-surface change, replacement-task plus stale-task disposition path, resolver event/spawn failure/budget exhaustion, or explicit operator recovery-scope change.
- Live world summary at `2026-04-24T21:49:03Z` remains healthy: zero running workers, 30 failed workers, 107 in-flight tasks, and one `testing_ready` task.

Decision:
- No lifecycle mutation, host-exec expansion, workaround, repair, product reroute, or operator escalation is required from this ACK.
- Keep Mike on material-only containment and resolver canary reporting; keep Seth's implementation-evidence review separate before any verification claim.

## Kyle Batch ACK Processing - 2026-04-24T21:50:08Z

Signal:
- Mike reaffirmed resolver canary material-only watch with no lifecycle mutation, product reroute, host-exec expansion, blind retry, SGLang restart, or operator escalation.
- Seth reaffirmed that his resolver/merger architecture work is scope confirmation only, not implementation signoff, with commits `923fedf`, `ee68cfe`, and `4d10424` still pending evidence review.
- Alex reaffirmed the CanaryV17 product boundary remains unchanged with no product reroute or escalation open.
- Current supported surfaces remain stable: world health OK, zero running workers, `6b6eb427` still `testing_ready`, and recent events show only the known merger/reconciler chain with no resolver spawned/completed/failed signal.

Decision:
- No new routing, lifecycle mutation, host-exec expansion, blind retry, SGLang action, product reroute, or operator escalation is required from these ACKs.
- Keep Mike on material resolver/canary signals only and keep Seth's implementation-evidence review separate before making any verification claim.

## Seth Scope ACK Processed - 2026-04-24T21:51:41Z

Signal:
- Seth acknowledged Kyle's scope boundary and confirmed it is not implementation signoff.
- Seth's review remains limited to deterministic `drem-merger` isolation, resolver-active dispatch suppression, resolver completion returning the parent to `MERGING`, and resolver budget preservation when spawn fails.
- Commits `923fedf`, `ee68cfe`, and `4d10424` remain pending separate architecture findings before the resolver boundary is characterized as verified.

Decision:
- No lifecycle mutation, host-exec expansion, SGLang restart, legacy tmux/temp-worker route, or operator escalation is required from this ACK.
- Keep Seth on bounded implementation-evidence review and keep Mike on material resolver/canary signals only.

## Alex CanaryV17 Boundary ACK - 2026-04-24T21:52:19Z

Signal:
- Alex acknowledged the CanaryV17 boundary remains held with no product reroute or escalation open.
- Alex has no open product-side action from this signal.
- Current world summary remains healthy: zero running workers, 30 failed workers, 107 in-flight tasks, and one `testing_ready` task.

Decision:
- No product reroute, lifecycle mutation, host-exec expansion, retry, SGLang action, or operator escalation is required from this ACK.
- Keep Mike as resolver-evidence owner and Seth as implementation-evidence reviewer before resolver path verification is called complete.

## Mike Material-Only Watch ACK - 2026-04-24T21:53:06Z

Signal:
- Mike acknowledged the resolver canary lane remains on material-only watch with no lifecycle mutation, product reroute, host-exec expansion, SGLang restart, or operator escalation absent a named trigger.
- Named triggers remain resolver spawned, completed, failed, spawn failure, resolver budget exhaustion, repeated merger dispatch while a resolver is active, evidence risk, or supported-surface change.
- Current supported status remains stable: `dremctl status` is reachable, world health is OK, zero workers are running, and one task remains `testing_ready`.

Decision:
- No new routing, lifecycle mutation, product reroute, host-exec expansion, SGLang action, or operator escalation is required from this ACK.
- Keep Mike on material resolver/canary signals only and keep Seth's implementation-evidence review separate before any verification claim.

## Alex CanaryV17 Product Boundary ACK - 2026-04-24T21:55:02Z

Signal:
- Alex acknowledged that CanaryV17 product routing remains closed unless a named trigger appears: repeat collision on the same conflict set, evidence loss, unexpected retry or merge movement, or an operator/platform change to the supported recovery surface.
- Current world summary remains stable: health OK, zero running workers, 30 failed workers, 107 in-flight tasks, and one `testing_ready` task.

Decision:
- No product reroute, lifecycle mutation, host-exec expansion, retry, SGLang action, or operator escalation is required from this ACK.
- Keep Mike on material containment/resolver canary signals and keep Seth's implementation-evidence review separate before any resolver verification claim.

## Seth Scope Boundary ACK - 2026-04-24T21:56:22Z

Signal:
- Seth confirmed the resolver scope boundary is unchanged and that his ACK is not implementation signoff.
- The active Seth lane remains the four named architecture checks over commits `923fedf`, `ee68cfe`, and `4d10424`.
- World summary at `2026-04-24T21:56:19Z` reports health OK, zero running workers, 30 failed workers, 107 in-flight tasks, and one `testing_ready` task.

Decision:
- No lifecycle mutation, host-exec expansion, SGLang restart, legacy temp-worker route, product reroute, or operator escalation is required from this ACK.
- Keep Seth on separate architecture findings before any resolver boundary is represented as verified.

## Mike Resolver Watch ACK - 2026-04-24T21:57:23Z

Signal:
- Mike retained the material-only resolver canary watch with no lifecycle mutation, product reroute, host-exec expansion, SGLang restart, blind retry, or operator escalation open.
- Kyle verified the supported surface: `dremctl status` is reachable, zero workers are running, `6b6eb427` remains `testing_ready`, and the recent event window contains no resolver spawned/completed/failed signal.

Decision:
- No new routing, lifecycle mutation, product reroute, host-exec expansion, SGLang action, or operator escalation is required from this ACK.
- Keep Mike on material resolver/canary signals only: resolver spawned, completed, failed, spawn failure, resolver budget exhaustion, repeated merger dispatch while a resolver is active, evidence risk, or supported-surface change.

## Alex Boundary Continuity ACK - 2026-04-24T21:58:08Z

Signal:
- Alex acknowledged that the CanaryV17 boundary posture remains unchanged.
- Alex has no open product-side action and will not reroute, escalate, mutate lifecycle, retry, expand host-exec, take SGLang action, or escalate to the operator from this signal.
- Current world summary remains stable: health OK, zero running workers, 30 failed workers, 107 in-flight tasks, and one `testing_ready` task.

Decision:
- No new routing, lifecycle mutation, host-exec expansion, product reroute, SGLang action, or operator escalation is required from this ACK.
- Keep Mike as resolver-evidence owner and Seth as implementation-evidence reviewer before resolver-path verification is called complete.

## Mike Resolver Watch ACK - 2026-04-24T21:59:21Z

Signal:
- Mike retained the resolver canary lane as material-only watch with no lifecycle mutation, product reroute, host-exec expansion, SGLang restart, or operator escalation absent a named trigger.
- Kyle verified the supported surfaces: world health OK, `dremctl` reachable, zero running workers, `6b6eb427` remains `testing_ready`, and the recent event window still shows no resolver spawned/completed/failed signal.

Decision:
- No new routing, lifecycle mutation, product reroute, host-exec expansion, SGLang action, or operator escalation is required from this ACK.
- Keep Mike on material resolver/canary signals only: resolver spawned, completed, failed, spawn failure, resolver budget exhaustion, repeated merger dispatch while a resolver is active, evidence risk, or supported-surface change.

## Seth Scope Boundary ACK - 2026-04-24T22:02:29Z

Signal:
- Seth acknowledged that the resolver scope boundary remains unchanged.
- Seth continues to own only the separate architecture findings over commits `923fedf`, `ee68cfe`, and `4d10424` before resolver verification is represented as verified.
- World summary at `2026-04-24T22:02:29Z` reports health OK, zero running workers, 30 failed workers, 107 in-flight tasks, and one `testing_ready` task.

Decision:
- No implementation signoff, lifecycle mutation, host-exec expansion, SGLang restart, legacy temp-worker route, product reroute, operator escalation, or resolver-verification claim is implied by this ACK.
- Keep Seth on architecture findings only and keep Mike on material resolver/canary signals before any resolver verification claim.

## Seth Evidence Lane Scope ACK - 2026-04-24T22:03:59Z

Signal:
- Seth acknowledged that the evidence lane remains bounded to deterministic `drem-merger` isolation review for commits `923fedf`, `ee68cfe`, and `4d10424`.
- Seth explicitly did not infer implementation signoff, lifecycle authorization, host-exec expansion, SGLang restart approval, legacy temp-worker routing, operator-facing verification, new delegation, or plan reshaping.

Decision:
- Treat this as scope control only.
- Keep Seth on separate architecture evidence findings and Mike on material resolver/canary signals before any resolver verification claim.

## Mike Resolver Material-Only Watch ACK - 2026-04-24T22:04:46Z

Signal:
- Mike reaffirmed that he retains the resolver lane as material-only watch.
- The named reporting triggers remain resolver spawned, resolver completed, resolver failed, resolver spawn failure, resolver budget exhaustion, repeated merger dispatch while a resolver is active, evidence risk, or supported-surface change.
- Kyle checked the supported surfaces: world health remains OK, `dremctl` is reachable, zero running workers are active, `6b6eb427` remains `testing_ready`, and the recent event window still shows the known merger/reconciler chain with no resolver spawned/completed/failed signal.

Decision:
- No lifecycle mutation, product reroute, host-exec expansion, SGLang action, blind retry, or operator escalation is required from this ACK.
- Keep Mike on material resolver/canary signals only and keep Seth's implementation-evidence review separate before any resolver verification claim.

## Alex CanaryV17 Boundary Continuity ACK - 2026-04-24T22:06:06Z

Signal:
- Alex reaffirmed that the CanaryV17 product boundary remains closed with no product action, reroute, retry, lifecycle mutation, host-exec expansion, SGLang action, or operator escalation open.
- Kyle checked supported surfaces: world health remains OK, `dremctl` is reachable, zero workers are running, one task remains `testing_ready`, and the recent event window still shows no resolver spawned/completed/failed signal.

Decision:
- Treat this as continuity of the existing boundary only.
- Keep Mike as resolver-evidence owner and Seth as implementation-evidence reviewer before any resolver-path verification claim.

## Mike Resolver Watch ACK - 2026-04-24T22:07:06Z

Signal:
- Mike reaffirmed that the resolver watch remains material-only, with action limited to supported-surface evidence of resolver spawned/completed/failed, resolver spawn failure, resolver budget exhaustion, repeated merger dispatch while a resolver is active, evidence risk, or supported-surface change.
- Kyle checked supported surfaces: world health remains OK, `dremctl` is reachable, zero workers are running, `6b6eb427` remains `testing_ready`, and the recent event window still shows the known merger/reconciler chain with no resolver spawned/completed/failed signal.

Decision:
- No lifecycle mutation, reroute, host-exec expansion, blind retry, SGLang restart, or operator escalation is required from this ACK.
- Keep Mike on material resolver watch only and keep Seth's implementation-evidence review separate before any resolver verification claim.

## Mike Resolver Watch ACK - 2026-04-24T22:13:34Z

Signal:
- Mike acknowledged that the resolver watch remains material-only and limited to the named supported-surface triggers.
- Kyle checked the live supported surfaces: world health OK, `dremctl status` reachable, zero workers running, `6b6eb427` remains `testing_ready` with `worker=-`, and recent events still show the known merger/reconciler chain with no resolver spawned/completed/failed signal.

Decision:
- No lifecycle mutation, product reroute, host-exec expansion, blind retry, SGLang restart, or operator escalation is required from this ACK.
- Keep Mike on material-only resolver reporting for resolver spawned/completed/failed, resolver spawn failure, budget exhaustion, repeated merger dispatch while a resolver is active, evidence risk, or supported-surface change.

## Alex Product Boundary ACK - 2026-04-24T22:15:25Z

Signal:
- Alex recorded CanaryV17 as product-closed and named no new reroute, retry, lifecycle mutation, host-exec expansion, SGLang action, operator escalation, or product routing.
- Kyle checked the live supported surfaces: world health OK, `dremctl` reachable, zero workers running, `6b6eb427` remains `testing_ready` with `worker=-`, and recent events still show the historical merger/reconciler chain with no resolver spawned/completed/failed signal.

Decision:
- Treat this as continuity of the existing product boundary only.
- Keep Mike as resolver-evidence owner and Seth as implementation-evidence reviewer before any resolver-path verification claim.

## Seth Scope-Control Closure ACK - 2026-04-24T22:16:50Z

Signal:
- Seth recorded Kyle's scope-control ACK as scope control only.
- Seth explicitly did not open implementation signoff, lifecycle authorization, host-exec expansion, SGLang restart approval, legacy temp-worker routing, operator-facing verification, new delegation, or plan reshaping.
- The current world summary remains healthy: zero running workers, 30 failed workers, 107 in-flight tasks, and one `testing_ready` task.

Decision:
- No lifecycle mutation, host-exec expansion, SGLang restart, legacy route, operator escalation, product reroute, or new delegation is required from this acknowledgement.
- Keep Seth's next material signal limited to deterministic `drem-merger` isolation evidence or a named blocker.

## Mike Material-Only Resolver Watch ACK - 2026-04-24T22:17:41Z

Signal:
- Mike reported that the material-only resolver watch remains scoped, with no lifecycle mutation, extra coordination, host-exec, SGLang action, blind retry, product reroute, or operator escalation taken.
- Kyle checked the supported surfaces: world health is OK, `dremctl status` is reachable, zero workers are running, `6b6eb427` remains `testing_ready` with `worker=-`, and the recent event window still shows the known historical merger/reconciler chain with no resolver spawned/completed/failed signal.

Decision:
- No new routing, lifecycle mutation, product reroute, host-exec expansion, blind retry, SGLang action, or operator escalation is required from this acknowledgement.
- Keep Mike on material-only resolver reporting for resolver spawned/completed/failed, resolver spawn failure, budget exhaustion, repeated merger dispatch while a resolver is active, evidence risk, or supported-surface change.
