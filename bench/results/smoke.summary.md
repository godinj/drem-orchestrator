# drembench run — 2026-04-14T13:37:04-07:00

Model: `gemma4-26b`

## Per-task aggregates

| task | trials | pass | stop | max_iter | api_err | avg_iter | avg_tok_in | avg_tok_out | avg_dur_ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| hard-refactor | 1 | 1 | 1 | 0 | 0 | 21.0 | 41917 | 1627 | 22873 |
| medium-test | 1 | 1 | 1 | 0 | 0 | 8.0 | 7736 | 285 | 5065 |
| trivial-doc | 1 | 1 | 1 | 0 | 0 | 6.0 | 4158 | 129 | 2077 |

## Per-trial detail

| task | trial | iters | tok_in | tok_out | dur_ms | finish | verified | err |
|---|---:|---:|---:|---:|---:|---|:-:|---|
| hard-refactor | 1 | 21 | 41917 | 1627 | 22873 | stop | ✓ |  |
| medium-test | 1 | 8 | 7736 | 285 | 5065 | stop | ✓ |  |
| trivial-doc | 1 | 6 | 4158 | 129 | 2077 | stop | ✓ |  |
