# drembench run — 2026-04-16T18:55:42-07:00

Model: `gemma4-26b`

## Per-task aggregates

| task | trials | pass | stop | max_iter | api_err | avg_iter | avg_tok_in | avg_tok_out | avg_dur_ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| hard-refactor | 3 | 3 | 3 | 0 | 0 | 7.0 | 10732 | 549 | 7384 |
| medium-test | 3 | 3 | 3 | 0 | 0 | 8.3 | 11787 | 361 | 5755 |
| trivial-doc | 3 | 3 | 3 | 0 | 0 | 17.7 | 29985 | 532 | 8207 |

## Per-trial detail

| task | trial | iters | tok_in | tok_out | dur_ms | finish | verified | err |
|---|---:|---:|---:|---:|---:|---|:-:|---|
| hard-refactor | 1 | 7 | 10209 | 288 | 5461 | stop | ✓ |  |
| hard-refactor | 2 | 7 | 10996 | 680 | 8537 | stop | ✓ |  |
| hard-refactor | 3 | 7 | 10992 | 678 | 8156 | stop | ✓ |  |
| medium-test | 1 | 8 | 11256 | 327 | 6418 | stop | ✓ |  |
| medium-test | 2 | 9 | 12849 | 347 | 5692 | stop | ✓ |  |
| medium-test | 3 | 8 | 11256 | 408 | 5156 | stop | ✓ |  |
| trivial-doc | 1 | 22 | 41819 | 708 | 12033 | stop | ✓ |  |
| trivial-doc | 2 | 14 | 20554 | 410 | 6282 | stop | ✓ |  |
| trivial-doc | 3 | 17 | 27582 | 479 | 6307 | stop | ✓ |  |
