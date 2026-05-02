# Task Brief: orch-stale-cleanup-2026-05-02

## Objective

Act as Mike's temporary read-only worker for the orchestrator stale task cleanup. Confirm the stale task set and report whether each row is safely cleanup-only or still current operational work.

## Boundaries

- Do not mutate orchestrator tasks.
- Do not run `retry`, `pass`, `fail`, `approve`, `reject`, or `archive`.
- Do not edit repository files.
- Do not use raw SQLite writes.
- Use supported read-only surfaces first: `dremctl status`, `dremctl tasks`, `dremctl events`, and Kyle `/world/summary`.
- If a command fails, record the exact command and error.

## Task Set

Inspect these task IDs:

- `39536776-49aa-4670-8e12-b1962b8ede8b`
- `c2caa6ef-1978-41c2-a253-512318410924`
- `1f9bd806-66f3-441f-84d4-006fcf201306`
- `8e54eae8-8235-429d-a585-b29453784aac`
- `daf89cd9-aa1f-4ebf-bec3-f7e3f4451b14`
- `d72fe085-b7bd-4762-8dc6-9bcc2032bfa6`
- `c5f5d7c4-8df9-4e60-91f5-66ef8152bf74`
- `44d99768-5b55-4f83-9d3b-7858581a6a2c`
- `0e79d985-0d32-419f-97c2-371cda0ea87c`
- `772aad4b-50fa-46f5-9d2a-8e4c4a835fa2`

## Report Format

Write a concise report to Mike with:

- Current status counts.
- Per-task classification: `archive-supported`, `break-glass-required`, or `do-not-clean`.
- Evidence that the successful standard proof `2e75708a-1f93-4d51-a83c-2585a4dfbf5c` remains `done`.
- Any unsupported-surface gaps.

Send the report with `csuite_send "$WORKER_ID" mike "orch stale cleanup worker report" high report "<body>"` if available. Also append the report to your worker `state.md`.
