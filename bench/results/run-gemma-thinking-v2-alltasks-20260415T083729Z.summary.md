# drembench run — 2026-04-15T01:50:55-07:00

Model: `gemma4-26b`

## Per-task aggregates

| task | trials | pass | stop | max_iter | api_err | avg_iter | avg_tok_in | avg_tok_out | avg_dur_ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| real-constraint-gate-exhaustion | 1 | 1 | 0 | 1 | 0 | 30.0 | 747820 | 7286 | 205313 |
| real-csuite-inbox-cli | 1 | 0 | 0 | 1 | 0 | 30.0 | 357727 | 13956 | 194750 |
| real-retry-suppression | 1 | 0 | 0 | 1 | 0 | 30.0 | 831254 | 23917 | 404307 |

## Per-trial detail

| task | trial | iters | tok_in | tok_out | dur_ms | finish | verified | err |
|---|---:|---:|---:|---:|---:|---|:-:|---|
| real-constraint-gate-exhaustion | 1 | 30 | 747820 | 7286 | 205313 | max_iter | ✓ | exceeded max iterations (30) |
| real-csuite-inbox-cli | 1 | 30 | 357727 | 13956 | 194750 | max_iter | ✗ | exceeded max iterations (30) |
| real-retry-suppression | 1 | 30 | 831254 | 23917 | 404307 | max_iter | ✗ | exceeded max iterations (30) |
