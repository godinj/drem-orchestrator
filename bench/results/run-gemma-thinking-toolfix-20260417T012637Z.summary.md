# drembench run — 2026-04-16T18:34:58-07:00

Model: `gemma4-26b`

## Per-task aggregates

| task | trials | pass | stop | max_iter | api_err | avg_iter | avg_tok_in | avg_tok_out | avg_dur_ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| real-constraint-gate-exhaustion | 3 | 0 | 0 | 3 | 0 | 30.0 | 895232 | 8151 | 167012 |

## Per-trial detail

| task | trial | iters | tok_in | tok_out | dur_ms | finish | verified | err |
|---|---:|---:|---:|---:|---:|---|:-:|---|
| real-constraint-gate-exhaustion | 1 | 30 | 958248 | 1400 | 61958 | max_iter | ✗ | exceeded max iterations (30) |
| real-constraint-gate-exhaustion | 2 | 30 | 988971 | 19475 | 361481 | max_iter | ✗ | exceeded max iterations (30) |
| real-constraint-gate-exhaustion | 3 | 30 | 738476 | 3578 | 77598 | max_iter | ✗ | exceeded max iterations (30) |
