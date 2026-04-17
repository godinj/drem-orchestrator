# drembench run — 2026-04-15T05:55:01-07:00

Model: `gemma4-26b`

## Per-task aggregates

| task | trials | pass | stop | max_iter | api_err | avg_iter | avg_tok_in | avg_tok_out | avg_dur_ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| real-constraint-gate-exhaustion | 2 | 0 | 0 | 1 | 1 | 27.5 | 738728 | 52868 | 1050667 |

## Per-trial detail

| task | trial | iters | tok_in | tok_out | dur_ms | finish | verified | err |
|---|---:|---:|---:|---:|---:|---|:-:|---|
| real-constraint-gate-exhaustion | 1 | 25 | 981509 | 98989 | 1993512 | api_error | ✗ | API call failed at iteration 25: API call: Post "http://loca… |
| real-constraint-gate-exhaustion | 2 | 30 | 495948 | 6746 | 107823 | max_iter | ✗ | exceeded max iterations (30) |
