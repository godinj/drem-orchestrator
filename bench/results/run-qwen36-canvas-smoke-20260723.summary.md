# drembench run — 2026-07-23T17:37:29-07:00

Model: `qwen3.6-27b-code`

## Per-task aggregates

| task | trials | pass | stop | max_iter | api_err | avg_iter | avg_tok_in | avg_tok_out | avg_dur_ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| canvas-cpp-lower-zone-smoke | 3 | 3 | 3 | 0 | 0 | 5.3 | 5706 | 482 | 18597 |

## Per-trial detail

| task | trial | iters | tok_in | tok_out | dur_ms | finish | verified | err |
|---|---:|---:|---:|---:|---:|---|:-:|---|
| canvas-cpp-lower-zone-smoke | 1 | 5 | 5391 | 482 | 18408 | stop | ✓ |  |
| canvas-cpp-lower-zone-smoke | 2 | 6 | 6319 | 465 | 18644 | stop | ✓ |  |
| canvas-cpp-lower-zone-smoke | 3 | 5 | 5407 | 499 | 18739 | stop | ✓ |  |
