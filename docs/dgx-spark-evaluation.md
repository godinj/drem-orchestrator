# DGX Spark Evaluation: Local LLM Serving Upgrade

**Date:** 2026-04-09
**Status:** Research complete, purchase decision pending

---

## Problem

RTX 3090 (24 GiB VRAM) cannot serve Qwen3-Coder 30B A3B at both high context and high speed:
- vLLM: 60 tok/s but maxes out at 72K context (98K with headless + tuning)
- llama-server: 131K context but only 15 tok/s
- Agentic coding workloads routinely consume 80-100K context
- Single-slot llama-server limits us to 1-2 concurrent workers

## Options Evaluated

### RTX 5090 ($2,900-3,500 street)

- 32 GB GDDR7, 1,792 GB/s bandwidth
- ~52 tok/s at 128K context, ~115-147K max (near VRAM ceiling)
- No NVLink — dead end for memory expansion
- Does NOT free the 3090 for other uses
- **Verdict: Wrong buy.** 32 GB is marginal for our context needs, doesn't solve the independence problem.

### Dual RTX 3090 + NVLink (~$800 cards + bridge)

- 48 GB unified VRAM, ~1,900 GB/s combined bandwidth
- ~38-48 tok/s at 128K context, easily handles 131K+
- **Requires new motherboard, case, and PSU** — true cost $1,200-1,500+
- 700W combined GPU power, 3090 unavailable for gaming during inference
- **Verdict: Cost advantage evaporates** once you factor in platform changes.

### DGX Spark ($4,699)

- 128 GB unified LPDDR5x, 273 GB/s bandwidth, GB10 Grace Blackwell SoC
- Completely independent system — sits on desk, serves over LAN
- RTX 3090 stays 100% available for desktop/gaming/streaming
- 240W total power, near-silent (~35 dB)
- In stock and shipping
- Expandable: two units link via ConnectX-7 for 256 GB

## DGX Spark Performance with Our Models

### Qwen3-Coder 30B A3B (MoE, 3.3B active params)

MoE architecture is ideal for the Spark — only 3.3B active params per token means 273 GB/s bandwidth goes far.

| Context Depth | 3090 + vLLM | 3090 + llama-server | DGX Spark + llama.cpp Q8_0 |
|---|---|---|---|
| Short (<32K) | 60 tok/s | 15 tok/s | 52-62 tok/s |
| Medium (32-72K) | 60 tok/s (at limit) | 12-15 tok/s | 35-50 tok/s |
| **Long (80-100K)** | **OOM** | 8-10 tok/s | **25-30 tok/s** |
| 128K | **OOM** | ~8 tok/s | 18-22 tok/s |

**At our actual operating range (80-100K), the Spark is 3x faster than the 3090.**

### Optimal Quantization Strategy

- **Weights:** Q8_0 GGUF (30 GB) — fastest decode on Spark; Q4_K crosses from bandwidth-bound to compute-bound
- **KV cache:** Q8_0 — <5% speed penalty vs FP16, halves memory
- **Avoid Q4_0 KV:** "dequantization cliff" at 64K+ context causes 37% gen speed drop, 92% prefill collapse

### Concurrency Scaling

With Q8_0 weights (30 GB) + Q8_0 KV cache:

| Workers | Context Each | Total Memory Used | Aggregate tok/s (est.) |
|---|---|---|---|
| 1 | 100K | 42 GB / 128 GB | 25-30 |
| 4 | 100K | 50 GB / 128 GB | 60-80 |
| 8 | 50K | 50 GB / 128 GB | 100-150 |
| 16 | 30K | 52 GB / 128 GB | 60-80 |

Even 16 workers use only 52 GB of 128 GB. Memory headroom is massive.

### Planner Model Recommendation

Replace Qwen3.5-27B (dense, 10-18 tok/s on Spark) with **Qwen3.5-35B-A3B** (MoE, 50+ tok/s on Spark). Same architecture class as Qwen3-Coder, better benchmarks, 3x faster.

## Interim Optimizations (RTX 3090)

While awaiting Spark purchase:
1. **Go headless** — reclaim ~888 MiB VRAM from KDE Plasma + apps
2. **Switch to AWQ-4bit weights** — save ~200 MiB vs GPTQ-4bit
3. **Raise gpu-memory-utilization to 0.95** — gain ~1.2 GiB KV budget
4. **Projected vLLM context after all optimizations:** ~94-98K (from 72K baseline)

## Decision

**Recommendation: Purchase DGX Spark.** It solves the memory wall permanently, runs faster than the 3090 at our actual workload depths, enables massive worker concurrency, and keeps the 3090 free for desktop use.

## Detailed Research Reports

- `~/.drem-csuite/kyle/outbox/vllm-context-optimization-research.md` — vLLM tuning analysis
- `~/.drem-csuite/kyle/outbox/rtx5090-vs-dgx-spark-research.md` — Hardware comparison
- `~/.drem-csuite/kyle/outbox/dgx-spark-qwen3-performance-projection.md` — DGX Spark performance deep dive
