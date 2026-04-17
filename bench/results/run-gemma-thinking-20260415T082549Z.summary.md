# drembench run — 2026-04-15T01:29:54-07:00

Model: `gemma4-26b`

## Per-task aggregates

| task | trials | pass | stop | max_iter | api_err | avg_iter | avg_tok_in | avg_tok_out | avg_dur_ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| real-constraint-gate-exhaustion | 1 | 0 | 0 | 1 | 0 | 30.0 | 758763 | 6824 | 245073 |

## Per-trial detail

| task | trial | iters | tok_in | tok_out | dur_ms | finish | verified | err |
|---|---:|---:|---:|---:|---:|---|:-:|---|
| real-constraint-gate-exhaustion | 1 | 30 | 758763 | 6824 | 245073 | max_iter | ✗ | exceeded max iterations (30) |
