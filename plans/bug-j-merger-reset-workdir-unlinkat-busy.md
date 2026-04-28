# Bug J — merger resetWorkDir fails on bind-mounted `/work`

**Status:** Option A accepted as the quality-gate shape on 2026-04-26, tightened by Seth at 2026-04-26T16:35:26Z, and proof-cleared for the `/work` EBUSY blocker at 2026-04-26T17:27:45Z.
No longer the active v17 blocker once Seth's proof package is accepted; current v17 blocker is deterministic merge conflict evidence from the active merger image/container.
**Artifact status:** active investigation input.
**Operator directive corrid:** `5b99982e`.
**Metadata updated:** 2026-04-26T18:14:31Z.
**Active investigation owner:** source-capable execution surface pending for scoped post-Bug-J conflict/control-plane path.
**Ops owner:** Mike for guardrails; no blind controlled retry while deterministic conflicts remain unresolved.

## Mike late Bug J hold ACK retained 2026-04-26T18:14:31Z

Mike reported at `2026-04-26T17:57:53Z`, replying to `e6d4a91b`, that Bug J `/work` EBUSY is cleared only narrowly and the active hold remains conflict/control-plane evidence. He rechecked supported surfaces read-only and took no `dremctl pass 6b6eb427`, retry, lifecycle mutation, host-exec expansion, Docker/SGLang action, operator escalation, or additional route.

Kyle rechecked supported surfaces at `2026-04-26T18:14:31Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, project workers remain effectively inactive for the task, and recent events remain the known merger/reconciler loop plus zero-UUID crash evidence. This ACK opens no new action. Mike remains watch-only until Seth/Kyle explicitly re-clear a targeted action or supported surfaces materially change.

## Mike Bug J guardrail ACK retained 2026-04-26T18:11:54Z

Mike reported at `2026-04-26T17:52:48Z`, replying to `e1e038b7`, that guardrails remain active: Bug J `/work` EBUSY is accepted only for the narrow prior blocker, deterministic merge-conflict/control-plane handling remains the active hold on `6b6eb427`, and he will take no lifecycle, pass, retry, escalation, host-exec, Docker/SGLang, product, quality, or coordination action unless Seth/Kyle re-clear a targeted next action.

Kyle rechecked supported surfaces at `2026-04-26T18:11:54Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, project workers remain zero, and recent events remain the known merger/reconciler loop plus zero-UUID crash evidence. This does not reopen the raw Bug J `/work` proof gate and does not clear lifecycle action.

## Mike conflict/control-plane hold ACK retained 2026-04-26T18:10:42Z

Mike reported at `2026-04-26T17:52:02Z`, replying to `c8f2a1d4`, that the Bug J `/work` EBUSY blocker remains cleared only for that prior blocker and that the active gate is now deterministic conflict resolution plus terminal-conflict control-plane proof. He remains watch-only and will not run pass, retry, lifecycle mutation, destructive git/Docker action, credential change, or SGLang restart without operator authorization, the proof package, and Kyle re-clearance.

Kyle rechecked supported surfaces at `2026-04-26T18:10:42Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, project workers remain zero, and recent events remain the known merger/reconciler loop plus zero-UUID crash evidence. This does not reopen the raw Bug J `/work` proof gate and does not clear lifecycle action.

## Seth source-surface blocker retained 2026-04-26T18:09:03Z

Seth reported at `2026-04-26T17:51:57Z`, replying to `4c8d2f1a`, that the Bug J Option A proof package remains intact, but the follow-on conflict/control-plane patch/proof is not available from his current source surface. His container lacks the source checkout and Go toolchain; host-exec denied a read-only git probe; `dremctl logs` returned 503 for orchestrator log streaming.

Kyle rechecked supported surfaces at `2026-04-26T18:09:03Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known merger/reconciler loop plus zero-UUID crash evidence. This does not reopen the raw Bug J `/work` proof gate and does not clear lifecycle action. Kyle routed Mike to provide or identify a source-capable cold-worker/orchestrator route for the scoped post-Bug-J conflict/control-plane pass.

## Seth passive quality context ACK retained 2026-04-26T18:07:01Z

Seth reported at `2026-04-26T17:45:47Z`, replying to `seth-20260426T172418Z-passive-quality-retained-ack`, that the Bug J Option A proof correction for the `/work` EBUSY blocker remains accepted and his quality posture is passive absent a named quality trigger, supported lifecycle/disposition movement, or concrete C-Suite request.

Kyle rechecked supported surfaces at `2026-04-26T18:07:01Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop plus zero-UUID crash evidence. This ACK does not reopen the raw Bug J `/work` proof gate and opens no audit, lifecycle mutation, disposition mutation, host-exec expansion, Docker/SGLang action, operator escalation, or additional C-Suite coordination. Bug J `/work` EBUSY remains proof-cleared only for that blocker; the active hold remains deterministic conflict/control-plane handling.

## Seth passive quality closure ACK retained 2026-04-26T18:05:53Z

Seth reported at `2026-04-26T17:44:20Z`, replying to `seth-20260426T172343Z-passive-quality-closure-ack`, that `b0c7e3a9` remains closure-only, the accepted Bug J Option A proof package remains accepted for the `/work` EBUSY blocker, and the active CanaryV17 hold remains deterministic merge-conflict/control-plane evidence.

Kyle rechecked supported surfaces at `2026-04-26T18:05:53Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop plus zero-UUID crash evidence. This ACK does not reopen the raw Bug J `/work` proof gate and opens no audit, recovery action, lifecycle mutation, controlled pass, retry, host-exec expansion, escalation, or additional C-Suite coordination. Bug J `/work` EBUSY remains proof-cleared only for that blocker; the active hold remains deterministic conflict/control-plane handling.

## Mike passive resolver-closure ACK retained 2026-04-26T18:04:35Z

Mike reported at `2026-04-26T17:43:41Z`, replying to `2026-04-26T17:43:08Z-kyle-84407795.md`, that passive resolver-closure remains context only and no lifecycle, recovery, host-exec, Docker/SGLang, product, quality, operator, worker-lane, or coordination action is opened from the ACK.

Kyle rechecked supported surfaces at `2026-04-26T18:04:35Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop plus zero-UUID crash evidence. This ACK does not reopen the raw Bug J `/work` proof gate. Bug J `/work` EBUSY remains proof-cleared only for that blocker; the active hold remains deterministic conflict/control-plane handling.

## Seth Bug J direct-fix ACK retained 2026-04-26T18:02:29Z

Seth reported at `2026-04-26T17:40:08Z`, replying to `a7d4c2e9`, that Kyle's retained disposition stands: the earlier Bug J Option A direct-fix ACK is retained, the later `/work` EBUSY proof package remains accepted, and raw proof-pending is not reopened.

Kyle rechecked supported surfaces at `2026-04-26T18:02:29Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop plus zero-UUID crash evidence. No `dremctl pass`, retry, lifecycle mutation, host-exec expansion, Docker/SGLang action, operator escalation, product route, quality reroute, or additional C-Suite coordination is open from this ACK. Bug J `/work` EBUSY remains proof-cleared; the active hold remains deterministic merge-conflict/control-plane handling.

## Mike closure-passive context ACK retained 2026-04-26T17:59:22Z

Mike reported at `2026-04-26T17:36:37Z`, replying to `0b56710c`, that closure-passive context remains passive only and that no lifecycle mutation, controlled pass, retry, recovery action, host-exec expansion, Docker/SGLang action, product route, quality route, worker-lane request, operator escalation, or additional C-Suite coordination is open.

Kyle rechecked supported surfaces at `2026-04-26T17:59:22Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop plus zero-UUID crash evidence. This ACK does not reopen the raw Bug J `/work` proof-pending gate. Mike remains watch-only until Seth/Kyle explicitly re-clear a targeted ops path or supported surfaces materially change.

## Mike late Bug J hold ACK retained 2026-04-26T17:56:36Z

Mike reported at `2026-04-26T17:32:53Z`, replying to `e6d4a91b`, that he retained the hold and will not run `dremctl pass 6b6eb427`, retry, or make any lifecycle mutation from that message.

Kyle rechecked supported surfaces at `2026-04-26T17:56:36Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop plus zero-UUID crash evidence. No lifecycle mutation, controlled pass, retry, host-exec expansion, Docker/SGLang action, operator escalation, product route, quality reroute, or additional C-Suite coordination is open from this ACK. Mike remains watch-only until Seth/Kyle explicitly re-clear a targeted action or supported surfaces materially change.

## Seth late Bug J Option A ACK retained 2026-04-26T17:55:35Z

Seth reported at `2026-04-26T17:34:00Z`, replying to `c9a4e2b1`, that he agrees Bug J Option A `/work` preservation proof is no longer the active blocker and that the current no-pass hold rests on deterministic merge-conflict/control-plane evidence from `6b6eb427`.

Kyle rechecked supported surfaces at `2026-04-26T17:55:35Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop plus zero-UUID crash evidence. No lifecycle mutation, controlled pass, retry, Mike ops action, host-exec expansion, Docker/SGLang action, operator escalation, product route, additional delegation, or broader clearance is open from this ACK. Seth remains quality owner for the deterministic conflict/control-plane clearance path.

## Seth historical standby ACK retained 2026-04-26T17:54:17Z

Seth reported at `2026-04-26T17:31:38Z`, replying to `a7f3c9d2`, that the stale standby ACK is historical context only and should not reopen the earlier Bug J no-pass condition.

Kyle rechecked supported surfaces at `2026-04-26T17:54:17Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop plus zero-UUID crash evidence. No lifecycle mutation, controlled pass, retry, host-exec expansion, Docker/SGLang action, operator escalation, product route, quality reroute, or additional C-Suite coordination is open from this ACK. The active hold remains deterministic merge conflicts plus terminal-conflict control-plane behavior.

## Mike Seth-clearance watch ACK retained 2026-04-26T17:53:17Z

Mike reported at `2026-04-26T17:32:03Z`, replying to `e3b91a6c`, that Bug J Option A stays accepted only for the narrow `/work` EBUSY blocker. The active no-pass hold remains deterministic merge conflicts and terminal-conflict control-plane behavior.

Kyle rechecked supported surfaces at `2026-04-26T17:53:17Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop plus zero-UUID crash evidence. No lifecycle mutation, controlled pass, retry, host-exec expansion, Docker/SGLang action, worker-lane change, operator escalation, product route, quality reroute, or additional C-Suite coordination is open from this ACK. Mike remains watch-only until Seth/Kyle explicitly re-clear a targeted action or supported surfaces materially change.

## Mike guardrail ACK retained 2026-04-26T17:52:02Z

Mike reported at `2026-04-26T17:31:16Z`, replying to `3f9a2c7b`, that Seth's later proof package is accepted for the narrow Bug J `/work` EBUSY blocker and supersedes the proof-pending portion of Mike's earlier hold.

Kyle rechecked supported surfaces at `2026-04-26T17:52:02Z`: world health is OK, `dremctl status` is reachable, and `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`. No lifecycle mutation, controlled pass, retry, host-exec expansion, Docker/SGLang action, operator escalation, product route, quality reroute, or additional C-Suite coordination is open from this ACK. Mike remains on guardrails until Seth/Kyle explicitly re-clear one targeted next action.

## Seth post-Bug-J conflict/control-plane recommendation 2026-04-26T17:50:44Z

Seth reported at `2026-04-26T17:30:15Z`, replying to `b4e9a301`, that Bug J Option A remains proof-cleared and is not the active blocker. The active work is a scoped conflict/control-plane path for `6b6eb427`.

The recommended conflict resolution is intentionally narrow: resolve `cmd/drem/orchhttp_server.go`, `cmd/drem/orchhttp_server_test.go`, `internal/spawner/types.go`, `internal/projects/template.go`, and `internal/projects/template_test.go` to current master unless a diff proves a direct CanaryV17 line is required; reapply only the CanaryV17 model/type and direct test payload if present. Do not import stale HTTP, spawner, or template refactors.

The required control-plane ordering is stricter than the earlier Bug J validation retry gate: terminal conflict handling must precede or travel with conflict resolution before any Mike pass. Existing Bug J authorization does not cover this scope, so Kyle routed the authorization ask to the operator and kept the no-pass hold in force.

## Mike controlled-pass recommendation accepted 2026-04-26T17:49:13Z

Mike reported at `2026-04-26T17:29:35Z`, replying to `a7f1c9d2`, that no pass is useful before the deterministic conflict/control-plane patch. Kyle accepted that recommendation after rechecking supported status surfaces: `6b6eb427` remains `testing_ready` with `worker=-`, health is OK, and recent events still show the known merger/reconciler loop and zero-UUID crash record.

This does not reopen Bug J Option A. The `/work` EBUSY blocker remains proof-cleared, but Seth must explicitly confirm the Option A invariants still hold in any conflict/control-plane patch before Kyle re-clears one controlled pass. Mike remains watch-only until that report and explicit Kyle re-clearance.

## Stale Mike passive resolver-watch ACK retained 2026-04-26T17:48:10Z

Mike's `2026-04-26T17:27:10Z` ACK, replying to `c57e4efa`, is retained as passive send-time ops context only. Supported surfaces checked at `2026-04-26T17:48:10Z` still show world/orchestrator health OK and task `6b6eb427` at `testing_ready` with `worker=-`.

No lifecycle/disposition mutation, `dremctl pass`, retry, recovery action, host-exec expansion, Docker/SGLang action, product route, quality reroute, worker-lane request, operator escalation, or additional C-Suite coordination is open from this ACK. Its proof-pending wording is stale relative to the accepted Bug J Option A proof package and does not reopen the `/work` EBUSY proof gate; the active hold remains deterministic merge-conflict/control-plane evidence.

## Stale Mike Bug J no-pass ACK retained 2026-04-26T17:46:52Z

Mike's `2026-04-26T17:26:01Z` ACK, replying to `a8d4f2c9`, is retained as passive send-time ops context only. Supported surfaces checked at `2026-04-26T17:46:52Z` still show world/orchestrator health OK and task `6b6eb427` at `testing_ready` with `worker=-`.

No lifecycle/disposition mutation, `dremctl pass`, retry, host-exec expansion, Docker/SGLang action, product route, quality reroute, operator escalation, or additional C-Suite coordination is open from this ACK. Its proof-pending wording is stale relative to the later accepted Bug J Option A proof package and does not reopen the `/work` EBUSY proof gate; the active hold remains deterministic merge-conflict/control-plane evidence.

## Stale Seth Bug J Option A hold ACK retained 2026-04-26T17:45:45Z

Seth's `2026-04-26T17:23:44Z` ACK, replying to `2026-04-26T17:23:07Z-kyle-58681280.md`, is retained as passive send-time quality context only. Supported surfaces checked at `2026-04-26T17:45:45Z` still show world/orchestrator health OK and task `6b6eb427` at `testing_ready` with `worker=-`.

No audit, lifecycle/disposition mutation, `dremctl pass`, retry, cold-worker request, recovery action, host-exec expansion, Docker/SGLang action, operator escalation, or additional C-Suite coordination is open from this ACK. Its proof-pending wording is stale relative to the later accepted Bug J Option A proof package and does not reopen the `/work` EBUSY proof gate; the active hold remains deterministic merge-conflict/control-plane evidence.

## Late Seth proof package retained 2026-04-26T17:40:40Z

## Stale Seth passive quality ACK retained 2026-04-26T17:44:48Z

Seth's `2026-04-26T17:24:18Z` ACK, replying to `2026-04-26T17:24:11Z-kyle-963e48ff.md`, is retained as passive send-time quality context only. Supported surfaces checked at `2026-04-26T17:44:48Z` still show world/orchestrator health OK and task `6b6eb427` at `testing_ready` with `worker=-`.

No audit, lifecycle/disposition mutation, `dremctl pass`, retry, cold-worker request, recovery action, host-exec expansion, Docker/SGLang action, operator escalation, or additional C-Suite coordination is open from this ACK. Its proof-pending wording is stale relative to the later accepted Bug J Option A proof package and does not reopen the `/work` EBUSY proof gate; the active hold remains deterministic merge-conflict/control-plane evidence.

## Seth passive quality closure ACK retained 2026-04-26T17:43:24Z

Seth's `2026-04-26T17:23:43Z` ACK for the `b0c7e3a9` closure thread is retained as passive closure-only quality context. Supported surfaces checked at `2026-04-26T17:43:24Z` still show world/orchestrator health OK and task `6b6eb427` at `testing_ready` with `worker=-`.

No audit, lifecycle/disposition mutation, `dremctl pass`, retry, cold-worker request, recovery action, host-exec expansion, Docker/SGLang action, operator escalation, or additional C-Suite coordination is open from this ACK. Its proof-pending wording does not reopen the raw Bug J gate because the later proof package has already been accepted for the `/work` EBUSY blocker; the active hold remains deterministic merge-conflict/control-plane evidence.

## Mike passive resolver-closure ACK retained 2026-04-26T17:42:42Z

Mike's `2026-04-26T17:22:26Z` ACK, replying to `4a7c2e91`, is retained as passive resolver-closure context only. Supported surfaces checked at `2026-04-26T17:42:42Z` still show world/orchestrator health OK and task `6b6eb427` at `testing_ready` with `worker=-`.

No lifecycle/disposition mutation, `dremctl pass`, retry, recovery action, host-exec expansion, Docker/SGLang action, product route, quality route, operator escalation, or additional C-Suite coordination is open from this ACK. The active lane remains deterministic merge-conflict/control-plane evidence after Bug J `/work` EBUSY proof clearance; Seth owns that quality/control-plane path, and Mike stays on ops guardrails until a targeted action is re-cleared or supported surfaces materially change.

Seth's critical report at `2026-04-26T17:21:27Z`, replying to `8f4a2c9b`, matches the already accepted Bug J Option A proof state. The source shape, targeted merger tests, regression coverage, and active merger image/container evidence remain sufficient to clear the `/work` EBUSY blocker.

Decision: retain the proof package as authoritative Bug J evidence, but do not open a lifecycle mutation from it. The active `6b6eb427` hold remains deterministic merge conflicts in `cmd/drem/orchhttp_server.go`, `cmd/drem/orchhttp_server_test.go`, `internal/projects/template.go`, `internal/projects/template_test.go`, and `internal/spawner/types.go`; no blind `dremctl pass` or retry is expected to complete cleanly until that path is handled.

## Seth proof package accepted 2026-04-26T17:27:45Z

Seth reported that Bug J Option A is proven enough to clear the `/work` EBUSY blocker. Verified source shape preserves `workDir`, removes only children below it, and has `cloneBranch` populate the existing directory. Regression coverage includes `TestResetWorkDir_PreservesDirectoryInode` and stale child cleanup for regular files, nested directories, and a dotfile, plus `TestCloneBranch_IntoExistingEmptyDir`.

Seth reported passing targeted checks for `./internal/merger` and the reset/clone tests. The repo-wide constitution still has unrelated residual failures, and full `go test ./...` timed out via host-exec, so Kyle is not treating this as whole-repo clearance.

Active runtime evidence from `localhost:5000/drem-merger:latest` and merger container `3c7f0543fd68` showed the container had `/work` mounted and reached merge logic. Its failure was `merge produced conflicts` / `failure_reason":"conflict"`, not `merger: reset work dir` or `unlinkat //work: device or resource busy`.

Decision: Bug J Option A is no longer the active blocker for `6b6eb427`. Do not run a blind `dremctl pass` for completion because the visible blocker is now deterministic conflicts in `cmd/drem/orchhttp_server.go`, `cmd/drem/orchhttp_server_test.go`, `internal/projects/template.go`, `internal/projects/template_test.go`, and `internal/spawner/types.go`.

## Late Seth direct-ownership ACK retained 2026-04-26T17:38:32Z

Seth's `2026-04-26T17:19:14Z` ACK, replying to `8f4a2c9b`, is retained as accurate for the direct Bug J Option A implementation lane opened by the operator. He accepted the scoped constraints: preserve `/work`, remove only children including dotfiles and nested directories, populate the existing workdir, add a delete/recreate-failing regression, run targeted merger tests plus gofmt/constitution checks, and return active merger image/container proof.

Kyle does not reopen the raw Bug J proof-pending gate from this ACK because Seth's later proof package was already accepted at `2026-04-26T17:27:45Z`. Supported surfaces checked at `2026-04-26T17:38:32Z` still show world/orchestrator health OK and task `6b6eb427` at `testing_ready` with `worker=-`. No lifecycle mutation, `dremctl pass`, retry, host-exec expansion, Docker/SGLang action, or operator escalation is open from this ACK. The remaining active hold is deterministic merge-conflict/control-plane evidence.

## Late Seth proof-pending ACK processed 2026-04-26T17:32:49Z

Seth's `2026-04-26T17:13:28Z` ACK replying to `c9a4e2b1` is retained as accurate for the gate state visible when sent: no pass or retry should happen without full Bug J Option A proof, and Mike should remain ops/watch-only.

Kyle does not reopen the raw Bug J `/work` EBUSY proof-pending gate from this stale ACK because Seth's later proof package was accepted at `2026-04-26T17:27:45Z`. The remaining no-pass hold is still active, but the basis is now deterministic merge-conflict/control-plane evidence rather than missing Bug J Option A proof. Kyle rechecked supported surfaces at `2026-04-26T17:32:49Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler sequence. No lifecycle mutation, retry, host-exec expansion, Docker/SGLang action, operator escalation, or additional delegation is open from this ACK.

## Late Mike hold ACK processed 2026-04-26T17:30:09Z

Mike's `2026-04-26T17:11:04Z` ACK under `3f9a2c7b` is retained as accurate for the pre-proof gate at the time it was sent: he ran no pass and kept `6b6eb427` held. Current Kyle plan state supersedes only the proof-pending portion: Seth's later proof package cleared Bug J's `/work` EBUSY blocker, but the no-blind-pass hold remains because the current blocker is deterministic merge conflict/control-plane evidence.

Kyle rechecked supported surfaces at `2026-04-26T17:30:09Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop. No lifecycle mutation, retry, host-exec action, Docker/SGLang action, operator escalation, or additional delegation is opened from this stale ACK.

## Second late Mike proof-pending ACK processed 2026-04-26T17:31:56Z

Mike's `2026-04-26T17:12:12Z` ACK replying to `af1fcd15` is retained as correct for the information Mike had when sent: no pass was run and the hold on `6b6eb427` was preserved. Kyle does not reopen the raw Bug J Option A proof-pending gate from this stale ACK because the later accepted Seth proof package already cleared the `/work` EBUSY blocker.

Kyle rechecked supported surfaces at `2026-04-26T17:31:55Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop. No lifecycle mutation, controlled pass, retry, host-exec expansion, Docker/SGLang action, operator escalation, or additional delegation is opened from this ACK. The active hold remains deterministic merge-conflict/control-plane evidence, with Mike on ops guardrails only after Seth/Kyle re-clear a targeted next action.

## Mike passive resolver-watch ACK retained 2026-04-26T17:26:05Z

Mike reported at `2026-04-26T17:08:10Z`, replying to `c57e4efa`, that passive resolver-watch context remains separate from the active CanaryV17 recovery lane. Kyle rechecked supported surfaces at `2026-04-26T17:26:05Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop.

No lifecycle mutation, controlled pass, retry, recovery action, host-exec expansion, Docker/SGLang action, product route, quality route, worker-lane request, operator escalation, or additional C-Suite coordination is open from this ACK. The no-pass hold remains active until Bug J Option A preserves `/work`, regression evidence proves mount-point preservation, the active merger image/container includes the fix, and Seth/Kyle quality clearance exists.

## Mike no-pass hold ACK retained 2026-04-26T17:24:57Z

Mike reported at `2026-04-26T17:06:40Z`, replying to `7f2a9c31`, that `6b6eb427` remains held in `testing_ready` and he will not pass or retry until Seth clears visible Bug J Option A proof. Mike saw no implementation commit, changed-file set, source/test proof, or active merger image/container evidence from supported ops surfaces.

Kyle rechecked supported surfaces at `2026-04-26T17:24:57Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop. No lifecycle mutation, retry, host-exec, Docker/SGLang action, operator escalation, or additional delegation was opened from this ACK. The no-pass hold remains active; Seth remains proof-clearance owner and Mike remains ops owner only after clearance.

## Seth hold ACK retained 2026-04-26T17:22:38Z

Seth reported at `2026-04-26T17:04:50Z`, replying to `3f8a6c2b`, that Bug J Option A remains blocked pending Mike's implementation/proof package. The required proof is unchanged: the regression must prove `/work` mount-point preservation, not merely post-reset existence, and no controlled `dremctl pass 6b6eb427` is cleared.

Kyle rechecked supported surfaces at `2026-04-26T17:22:38Z`: world health is OK, `dremctl status` is reachable, `dremctl tasks --limit 20` still shows `6b6eb427` at `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop. No lifecycle mutation, retry, operator escalation, or additional delegation was opened from this ACK.

## Operator authorized Seth direct fix ownership 2026-04-26T17:18:24Z

The operator directed Kyle under `30c3f899`: "ok, so let's have seth design the fix or even implement it himself." Kyle routed that directive to Seth as a scoped authorization for Bug J Option A. Seth may either return a concrete design/proof package or directly implement the fix, while preserving the accepted constraints: keep `/work` itself, remove only children inside it including dotfiles and nested directories, clone/populate into the existing workdir, add a regression that fails on delete/recreate, run targeted merger tests plus gofmt/constitution checks, and provide active merger image/container proof before Mike performs any controlled retry.

This directive does not authorize `dremctl pass 6b6eb427`, repeated retries, destructive git/Docker actions, credential changes, SGLang restart, or expansion into the terminal-failure control-plane patch unless Seth reports that those are required for the Bug J proof package.

## Mike duplicate hold ACK retained 2026-04-26T17:13:21Z

Mike reported under an inbound without its own `corrid`, replying to `b6c3f912` at `2026-04-26T16:56:22Z`, that the `6b6eb427` hold remains retained pending Bug J Option A proof and Seth clearance. He performed no `dremctl pass 6b6eb427`, retry, lifecycle mutation, host-exec, Docker/SGLang action, or operator escalation.

Kyle rechecked supported surfaces at `2026-04-26T17:13:21Z`: world health is OK, `dremctl status` is reachable, `6b6eb427` remains `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop. No lifecycle mutation was performed. The no-pass hold remains active; Seth remains proof-clearance owner and Mike remains ops owner only after clearance.

## Seth Option A quality ACK retained 2026-04-26T17:12:30Z

Seth reported under `c9a4e2b1` at `2026-04-26T16:55:18Z` that Bug J Option A remains the controlling quality gate: preserve `/work`, remove only children inside `/work`, clone/populate into the existing workdir, require targeted regression coverage plus standard checks, and require proof that the active merger image/container includes the fix before any retry.

Kyle rechecked supported surfaces at `2026-04-26T17:12:30Z`: world health is OK, `dremctl status` is reachable, `6b6eb427` remains `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop. No lifecycle mutation was performed. The no-pass hold remains active; Mike remains ops owner only after Seth clears the full proof package. Bug J-b remains separate observability debt unless missing merger result-log upload blocks post-fix analysis.

## Mike hold ACK retained 2026-04-26T17:11:34Z

Mike reported under `af1fcd15` at `2026-04-26T16:54:22Z` that he is retaining the `6b6eb427` hold at `testing_ready`, made no lifecycle mutation, and will not run `dremctl pass 6b6eb427` until Bug J Option A is proven in source, targeted regressions, standard checks, and the active merger image/container runtime.

Kyle rechecked supported surfaces at `2026-04-26T17:11:34Z`: world health is OK, `dremctl status` is reachable, `6b6eb427` remains `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop. No pass/retry is authorized from this ACK. Seth remains proof-clearance owner; Mike remains ops owner only after clearance.

## Seth merger investigation result 2026-04-26T17:09:35Z

Seth reported under `seth-merger-investigation-20260426` that Option A remains the right and sufficient fix shape for Bug J itself: preserve the `/work` mount point, remove only its children, and populate the already-existing `/work` directory. Do not accept delete/recreate, EBUSY suppression, mount-topology changes, or reconciler changes as part of the Bug J patch.

Retry clearance remains narrow. Before Mike may run one controlled `dremctl pass 6b6eb427`, proof must exist that source and regression coverage enforce mount-point preservation through same device/inode before and after reset or a real bind-mount harness; stale children including dotfiles and nested directories are removed; clone/populate targets the existing directory; relevant merger tests, gofmt, and constitution checks pass; and the active merger image/container path includes the fix. Any failure after that single retry becomes a named blocker; no repeated pass loop.

Seth does not require the separate metadata/terminal-failure patch before that one Bug J validation retry if the proof package is present and Mike is watching. The metadata/terminal-failure work is still required before broader no-pass-hold removal or declaring the merger/reconciler path recovered. Bug J-b (`/internal/logs` 401) remains observability debt unless reporter failure becomes fatal or prevents enough evidence capture.

## Seth proof-gate ACK retained 2026-04-26T17:03:38Z

Seth reported at `2026-04-26T16:48:25Z`, replying to `seth-bug-j-option-a-gate-20260426`, that the Bug J Option A gate is correctly recorded and that he will clear or reject only after Mike returns the implementation commit or changed-file set with source shape, regression proof, test/constitution/gofmt evidence, and active merger image/container evidence.

Kyle rechecked supported surfaces at `2026-04-26T17:03:38Z`: world health is OK, `dremctl status` is reachable, `6b6eb427` remains `testing_ready` with `worker=-`, supported worker output shows no active project execution, and recent events remain the known 2026-04-24 merger/reconciler loop. No lifecycle mutation, controlled pass, operator escalation, or additional delegation is open from this ACK.

Decision: retain the no-pass hold. The controlling gate still requires preserving the `/work` mount point, populating the existing directory, and proving a regression that would fail under delete/recreate rather than merely proving `/work` exists afterward. Mike remains responsible for returning the proof package; Seth remains responsible for clearance before any controlled `dremctl pass 6b6eb427`.

## Mike retained hold ACK 2026-04-26T16:55:10Z

Mike reported under `b6c3f912`, replying to `af1fcd15`, that the Bug J Option A hold remains retained and no lifecycle mutation was run. Kyle rechecked supported surfaces at `2026-04-26T16:55:10Z`: world health is OK, `dremctl status` is reachable, `6b6eb427` remains `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop.

The hold remains active until Bug J Option A is fixed, covered, and proven present in the active merger image/container. Seth proof-package request remains outstanding; Mike owns ops retry only after that proof exists or supported surfaces materially change.

## Seth quality ACK retained 2026-04-26T16:54:13Z

Seth acknowledged Kyle's `c9a4e2b1` accepted gate and recorded Bug J Option A as the controlling quality gate: preserve `/work`, remove only children inside `/work`, clone/populate into the existing workdir, require targeted regression coverage plus standard checks, and require proof that the active merger image/container includes the fix before retry.

Kyle rechecked supported surfaces at `2026-04-26T16:54:13Z`: world health is OK, `dremctl status` is reachable, `6b6eb427` remains `testing_ready` with `worker=-`, and recent supported events remain the known 2026-04-24 merger/reconciler loop. No lifecycle mutation is authorized from this ACK. The no-pass hold remains active until the full proof package exists; Bug J-b remains separate observability debt unless missing merger result-log upload blocks post-fix analysis.

## Mike ops hold ACK 2026-04-26T16:53:02Z

Mike acknowledged Kyle's `d4a9c7e2` handoff under `af1fcd15` and confirmed that he will not run `dremctl pass 6b6eb427` until the full Bug J Option A proof package exists. Kyle retained the hold after checking supported surfaces: world health is OK, `dremctl status` is reachable, no project workers are running, `6b6eb427` remains `testing_ready` with `worker=-`, and recent events remain the known 2026-04-24 merger/reconciler loop.

Required proof before ops retry remains: source shape, targeted regressions proving `/work` mount-point preservation and populate-into-existing-directory behavior, standard checks, and active merger image/container runtime evidence. Mike owns at most one controlled pass after proof; Seth owns current investigation/fix proof clearance.

## Operator-directed Seth investigation 2026-04-26T16:51:07Z

The operator directed Kyle to have Seth investigate the merger issues and adjust artifact metadata to facilitate that work. This artifact is now marked as an active Seth investigation input, not only a passive quality-gate record.

Seth should use this Bug J evidence together with `strategic-goal-canaryv17-working.md`, `p0-merger-reconciler-loop-6b6eb427.md`, and `p0-merger-terminal-failure-control-plane.md` to determine whether Option A remains sufficient, whether the zero-UUID merger evidence or reconciler requeue behavior changes the acceptance shape, and whether Bug J-b (`/internal/logs` 401) materially blocks investigation quality. The no-pass hold remains active until Seth reports back and Mike has proof the cleared fix is present in the active merger image/container.

## Seth gate tightening 2026-04-26T16:35:26Z

Seth kept the CanaryV17 pass blocked and clarified that the regression must prove mount-point preservation, not merely that `/work` exists after reset.

Required acceptance shape:

- `resetWorkDir` removes the children of `/work`, including dotfiles and nested directories, while preserving `/work` itself.
- Clone/populate writes into the already-existing `/work` directory. Any delete/recreate of `workDir`, including `os.RemoveAll(workDir)` followed by recreate, fails the gate.
- The targeted regression proves mount-point preservation. Preferred proof is same device/inode before and after reset, or a real bind-mount harness if available. A test that only asserts existence is insufficient.
- The regression also proves prior children are removed before clone/populate and that populate succeeds into the existing directory.
- Test discipline still applies: no local duplicated DB/git helper factories outside `internal/testutil`; keep the test targeted to merger reset/populate behavior unless an existing integration harness already covers the mount case cheaply.

Bug J-b, the merger `/internal/logs` 401, remains separate observability debt unless the implementation changes the reporter failure into a fatal exit. Post-fix verification can rely on merger exit/status, `dremctl events/status`, and the final task transition.

## Quality gate accepted 2026-04-26

Seth approved Option A as the smallest safe fix: reset must remove the contents of `/work` while preserving `/work` itself, and clone/populate must target the already-existing directory.

Do not accept variants that delete/recreate `/work`, ignore arbitrary `RemoveAll` errors, change worker mount layout, or expand into reconciler behavior as part of Bug J.

Before Mike retries `dremctl pass 6b6eb427`, require proof that:

- `resetWorkDir` enumerates children under the workdir and removes those paths only while preserving the root path as usable.
- Clone/populate writes into the existing workdir, such as `git clone <repo> .` from inside `/work` or equivalent init/fetch/checkout behavior.
- Regression coverage proves reset deletes stale contents while leaving the root workdir intact and usable.
- Regression coverage proves clone/populate uses the existing workdir path rather than removing/recreating it, preferably by checking the same device/inode before and after reset or by using a real bind-mount harness.
- Relevant merger tests and constitution/gofmt checks pass.
- The active merger image/container path includes the fix, not only a source commit.

Bug J-b (`/internal/logs` 401) remains separate observability debt unless post-fix failure analysis becomes impossible without merger result-log upload.

## Symptom

Every `/pass` gate on task v17 (`6b6eb427`) drives the orch to spawn
a `drem-merger` container, which exits 1 with:

```
{"time":"...","level":"ERROR","msg":"merge failed",
 "error":"merger: reset work dir: remove /work: unlinkat //work:
          device or resource busy"}
drem-merger: merger: reset work dir: remove /work: unlinkat //work:
             device or resource busy
```

Orch then logs `merge aborted: misc exit from merger (code=1)` and
reconciler cycles the task back through `all subtasks done, testing
ready` → `testing_ready fixer failed, needs human review` loop on a
5s cadence. Identical failure signature on every merger container
sampled (`inspiring_booth`, `serene_spence`, `unruffled_jepsen`) —
present at least 39h before this filing; this is NOT a Session N
regression.

## Root cause

`internal/merger/merger.go:348-362`:

```go
func resetWorkDir(workDir string) error {
    if err := os.RemoveAll(workDir); err != nil {
        return fmt.Errorf("remove %s: %w", workDir, err)
    }
    ...
}
```

`workDir` defaults to `/work` (`cmd/drem-merger/main.go:62`). The
merger container bind-mounts the feature worktree onto `/work`
itself, not a subdirectory. `os.RemoveAll("/work")` eventually
`unlinkat`s the mount point — which the kernel refuses because the
path is an active mount (EBUSY).

Diagnostically: `os.RemoveAll` walks the tree and successfully
unlinks children, then tries to unlink `/work` itself and fails. So
the contents probably DO get cleared before the final error — but
the non-zero exit from `resetWorkDir` aborts the whole merge before
the subsequent `cloneBranch` even starts.

## Why it didn't fire before Bug H

Before Bug H (`3fdcb85`), the merger CLI rejected empty `--test-cmd`
and failed BEFORE reaching `resetWorkDir`. That's why operators saw
"merger crash on empty TestCmd" as the visible symptom. Bug H moved
the fail-close/fail-fast to the orch side, so merger invocations now
reach runtime and hit this second, pre-existing failure.

Bug H was necessary but not sufficient. Merger-library scoreboard
item 10 ("merger library empty-TestCmd hardening") was already on
the Session N+1 shortlist for related reasons — J belongs to the
same cluster.

## Secondary 401

Same run also logs:

```
"merge result reporter failed",
"error":"merge_result POST http://orch:8080/internal/logs
         returned 401: unauthorized"
```

Cosmetic (the merger still does the merge; it just can't report
result-logs back). But symptom of an auth gap on `/internal/logs`
— unclear whether the merger's bearer token path was ever wired for
this endpoint, or the endpoint's auth changed and merger wasn't
updated. Track as Bug J-b.

## Fix options

**A. Remove contents, not the mount point** (preferred, minimal):
Rewrite `resetWorkDir` to list children of `workDir` and
`os.RemoveAll` each, leaving the mount point itself intact. Matches
the container reality.

```go
func resetWorkDir(workDir string) error {
    entries, err := os.ReadDir(workDir)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            // Parent creation handled below.
            if p := parentDir(workDir); p != "" && p != "." {
                return os.MkdirAll(p, 0o755)
            }
            return nil
        }
        return fmt.Errorf("read %s: %w", workDir, err)
    }
    for _, e := range entries {
        if err := os.RemoveAll(filepath.Join(workDir, e.Name())); err != nil {
            return fmt.Errorf("remove %s: %w", e.Name(), err)
        }
    }
    return nil
}
```

Then `cloneBranch` needs to clone INTO workDir (not clone workDir
itself) — a shape change: `git clone <repo> .` from inside workDir,
or `git -C workDir init && git fetch && git checkout`. Two-line
change in `cloneBranch`, plus a host test that mirrors a mounted
workDir.

**B. Treat EBUSY on the mount point as non-fatal.** Fragile; errors
on tree-walk error propagation. Not recommended.

**C. Change the container's mount topology** so `/work` is a
directory inside the container and the bind lands on a parent. Out
of scope of the merger library fix; requires compose-template
change + coordination with the orch's worker-spawner.

## Test plan

1. Reproduction unit test in `internal/merger/merger_test.go` using
   a tmpdir that simulates a "can't unlink the root" condition
   (e.g., via a symlink and a chmod trick, or a genuine bind mount
   in a test harness if one exists).
2. Integration reproduction via the existing e2e harness by
   passing a pre-created workDir that must remain after resetWorkDir.
3. Manual: unblock v17 (`6b6eb427`) — `/pass` → expect merging →
   done, no roll-back.

## Recommendation

Option A. One-function change in the merger library, one additional
unit test, one shape tweak in `cloneBranch`. Belongs in
Session N+1 alongside scoreboard item 10. Operator greenlight gate.

## Scope this is NOT

- Does not touch orch's `buildMergerArgv` or `inferTestCommand`
  (Bug H shipped correctly).
- Does not touch the 401 on `/internal/logs` (Bug J-b, separate
  ticket).
