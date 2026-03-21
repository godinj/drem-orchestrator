# Fix constitution violations introduced by depth and constitution features

Priority: 10
Labels: constitution, depth, cleanup

The recent depth enforcement and constitution checking features introduced violations
of the very standards they enforce. This task addresses all current constitution
failures and the critical score bridge mismatch in the depth system.

Constitution check currently shows 3 failures out of 10 checks:
1. gofmt non-compliance in score.go and prompts.go
2. prompt.go exceeds the 800-line file length ceiling (968 lines)
3. prompt/ package exceeds the 6-import internal import ceiling (9 imports)
4. orchestrator/ internal imports grew past grandfathered baseline of 35 (now 39)

Additionally, the depth scoring bridge (score_bridge.go) doesn't evaluate DepthMeta
(ModuleBoundaries, InterfaceShapes) — it uses a simple file-coverage ratio instead of
the three-criterion evaluation in score/score.go. This means planners are required to
define module boundaries and interface shapes, but the plan depth gate never actually
checks them.

Acceptance criteria:
- All 10 constitution checks pass (zero failures)
- score_bridge.go delegates to or reimplements the canonical three-criterion depth
  scoring from score/score.go (boundaries, interfaces, deep decomposition)
- prompt.go is split so no resulting file exceeds 800 lines
- prompt/ package uses at most 6 internal imports
- orchestrator/ internal imports do not exceed 35 (grandfathered baseline)
- All existing tests continue to pass
- README or docs updated to reflect any structural changes
