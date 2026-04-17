# drembench run — 2026-04-16T19:16:04-07:00

Model: `gemma4-26b`

## Per-task aggregates

| task | trials | pass | stop | max_iter | api_err | avg_iter | avg_tok_in | avg_tok_out | avg_dur_ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| hard-refactor | 5 | 5 | 5 | 0 | 0 | 7.6 | 11830 | 562 | 7437 |
| medium-test | 5 | 5 | 5 | 0 | 0 | 8.0 | 11256 | 395 | 5446 |
| trivial-doc | 5 | 5 | 5 | 0 | 0 | 5.2 | 5705 | 254 | 3284 |

## Per-trial detail

| task | trial | iters | tok_in | tok_out | dur_ms | finish | verified | err |
|---|---:|---:|---:|---:|---:|---|:-:|---|
| hard-refactor | 1 | 7 | 10134 | 393 | 6558 | stop | ✓ |  |
| hard-refactor | 2 | 7 | 10159 | 858 | 10191 | stop | ✓ |  |
| hard-refactor | 3 | 8 | 13495 | 697 | 8958 | stop | ✓ |  |
| hard-refactor | 4 | 9 | 15225 | 467 | 6424 | stop | ✓ |  |
| hard-refactor | 5 | 7 | 10137 | 393 | 5057 | stop | ✓ |  |
| medium-test | 1 | 8 | 11256 | 327 | 6312 | stop | ✓ |  |
| medium-test | 2 | 8 | 11256 | 408 | 5096 | stop | ✓ |  |
| medium-test | 3 | 8 | 11256 | 424 | 5374 | stop | ✓ |  |
| medium-test | 4 | 8 | 11256 | 408 | 5209 | stop | ✓ |  |
| medium-test | 5 | 8 | 11256 | 408 | 5241 | stop | ✓ |  |
| trivial-doc | 1 | 6 | 6893 | 334 | 5276 | stop | ✓ |  |
| trivial-doc | 2 | 5 | 5408 | 228 | 2788 | stop | ✓ |  |
| trivial-doc | 3 | 5 | 5408 | 233 | 2744 | stop | ✓ |  |
| trivial-doc | 4 | 5 | 5408 | 253 | 2977 | stop | ✓ |  |
| trivial-doc | 5 | 5 | 5408 | 223 | 2639 | stop | ✓ |  |
