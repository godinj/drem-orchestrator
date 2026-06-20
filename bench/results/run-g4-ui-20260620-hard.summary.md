# drembench run — 2026-06-20T11:56:53-07:00

Model: `gemma4-26b`

## Per-task aggregates

| task | trials | pass | stop | max_iter | api_err | avg_iter | avg_tok_in | avg_tok_out | avg_dur_ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| hard-refactor | 3 | 3 | 3 | 0 | 0 | 8.7 | 13690 | 836 | 11481 |

## Per-trial detail

| task | trial | iters | tok_in | tok_out | dur_ms | finish | verified | err |
|---|---:|---:|---:|---:|---:|---|:-:|---|
| hard-refactor | 1 | 7 | 10406 | 521 | 7444 | stop | ✓ |  |
| hard-refactor | 2 | 9 | 14932 | 1596 | 21452 | stop | ✓ |  |
| hard-refactor | 3 | 10 | 15733 | 392 | 5547 | stop | ✓ |  |
