# drembench run — 2026-04-14T13:42:00-07:00

Model: `gemma4-26b`

## Per-task aggregates

| task | trials | pass | stop | max_iter | api_err | avg_iter | avg_tok_in | avg_tok_out | avg_dur_ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| hard-refactor | 1 | 1 | 0 | 1 | 0 | 30.0 | 85790 | 3293 | 38509 |
| medium-test | 1 | 1 | 1 | 0 | 0 | 8.0 | 7363 | 273 | 3771 |
| trivial-doc | 1 | 1 | 1 | 0 | 0 | 6.0 | 4188 | 122 | 1524 |

## Per-trial detail

| task | trial | iters | tok_in | tok_out | dur_ms | finish | verified | err |
|---|---:|---:|---:|---:|---:|---|:-:|---|
| hard-refactor | 1 | 30 | 85790 | 3293 | 38509 | max_iter | ✓ | exceeded max iterations (30) |
| medium-test | 1 | 8 | 7363 | 273 | 3771 | stop | ✓ |  |
| trivial-doc | 1 | 6 | 4188 | 122 | 1524 | stop | ✓ |  |
