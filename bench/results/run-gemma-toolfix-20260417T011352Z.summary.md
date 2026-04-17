# drembench run — 2026-04-16T18:24:19-07:00

Model: `gemma4-26b`

## Per-task aggregates

| task | trials | pass | stop | max_iter | api_err | avg_iter | avg_tok_in | avg_tok_out | avg_dur_ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| real-constraint-gate-exhaustion | 2 | 0 | 0 | 1 | 1 | 29.5 | 1080226 | 9174 | 313546 |

## Per-trial detail

| task | trial | iters | tok_in | tok_out | dur_ms | finish | verified | err |
|---|---:|---:|---:|---:|---:|---|:-:|---|
| real-constraint-gate-exhaustion | 1 | 29 | 1120555 | 17336 | 565648 | api_error | ✗ | API call failed at iteration 29: API call: Post "http://loca… |
| real-constraint-gate-exhaustion | 2 | 30 | 1039896 | 1011 | 61445 | max_iter | ✗ | exceeded max iterations (30) |
