# drem-canvas task 6033a8f4 follow-ups

Task `6033a8f4-e749-4c63-84c7-3e5f27d5dc23` exposed several orchestration problems while trying to complete the normal test-writing and review flow.

## Problems observed

- Test-review rejection spawned duplicate replacement test-writing subtasks for the same lane.
- Existing-work dedup marked rejected replacement subtasks as complete from stale prior work.
- Retrying the top-level task preserved stale child rows and stale rejection/failure context.
- Retrying the top-level task preserved contaminated feature branch state from earlier attempts.
- Existing-plan recovery could resume with `Plan != nil` but no parent feature branch.
- Test-writing could reach scheduling with a missing parent feature worktree and fail with `branch_missing`.
- Direct Gemma coder workers repeatedly failed the same scoped test-writing subtask before producing mergeable commits.
- Some preserved worker branches had no useful diff, while others contained unrelated destructive artifacts.
- Mike sometimes did not promptly act on parent `test_review` gates despite standing `test_review` authority.
- Worker branch evidence could include unrelated artifacts such as `.claude/settings.json` deletion, `plan.json` deletion, prompt changes, and `agent-trace*.jsonl` files.

## Fixes already landed in orchestrator

- Test-review rejection now marks replacement subtasks with `skip_existing_work_dedup`.
- Replacement source rows are deduplicated by canonical test-writing lane.
- Top-level retry clears stale failure and rejection context.
- Top-level retry cancels and detaches stale children.
- Top-level retry clears stale feature branch state.
- Existing-plan recovery recreates the parent feature worktree when missing.
- Test-writing self-heals a missing parent feature worktree before scheduling.

## Remaining follow-up candidates

- Add a supported endpoint or command to adopt a preserved scoped worker commit into a subtask without direct database repair.
- Add branch hygiene checks before worker evidence reaches `test_review`.
- Make direct worker failure reasons actionable when merge reconciliation fails.
- Improve Mike watcher prioritization for parent-level `test_review` events.
- Add cleanup for stale worker records and preserved no-diff branches.
