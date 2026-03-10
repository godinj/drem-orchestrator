# Documentation Index

## Architecture

- [Go Rewrite Design](go-rewrite/DESIGN.md) — Full architecture spec: models, state machine, tmux integration, orchestrator loop, TUI, merge pipeline
- [Go Rewrite Build Prompts](go-rewrite/prompts/) — Agent prompts used to build the Go codebase (8 prompts, 4 tiers)

## Current Work

- [Merge Reliability PRD](merge-overhaul/prd-merge-reliability.md) — Active roadmap for >90% merge success rate (11 fixes)
- [Merge Reliability Findings](merge-reliability-findings.md) — Root cause analysis from 6 production features backing the PRD
- [Merge Overhaul Prompts](merge-overhaul/prompts/) — Agent prompts implementing the reliability fixes (6 prompts, 2 tiers)
- [Reconcile Overhaul](../feature/reconcile-overhaul.md) — Reconcile function redesign (currently disabled)

## Future Work

- [Planner Decomposition Strategy](merge-overhaul/planner-decomposition-strategy.md) — 7 improvements to planner prompts and plan validation
- [New Agent Types](merge-overhaul/new-agent-types.md) — Merge Resolver, Reviewer, Fixer, Integrator agent designs

## Archived (Python era)

Historical docs from the original Python/FastAPI/React version. Superseded by the Go rewrite.

- [Original Python Build Prompts](archived/python-original/) — 10-tier agent plan for the Python orchestrator
- [Python Planner Agent](archived/python-planner-agent/) — Planner integration prompts (Python-specific)
- [Python Log Viewer](archived/python-log-viewer/) — Clarification gate and log viewer feature (React UI)
