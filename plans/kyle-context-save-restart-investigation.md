# Kyle Context Save/Restart Slowness — Investigation Plan

## RESOLVED — 2026-04-22

Resolved by the container-Kyle transition (see
`plans/container-kyle-transition.md`, Phase 1 at commit 2639508).
Each container-Kyle turn is stateless: the poller reads the inbox
message, invokes `claude -p` with the kyle.md prompt, writes a reply,
and exits the turn. Context growth across operator dialogue is no
longer a concern for the canonical always-on Kyle — the investigation's
original "interactive Kyle session context creeps toward compaction"
premise does not apply to container-Kyle at all.

Interactive-Kyle sessions (operator-invoked) still retain context
within a single session, but the coexistence rules in
`docs/csuite-agents/prompts/kyle.md` §Runtime mode direct operator
→ Kyle traffic specifically to interactive mode where session-bounded
context is the intended behaviour. Save-and-restart remains available
for long interactive sessions via the existing
`csuite-save-and-restart` skill.

This investigation is closed. No further code or plan work needed
under Q12.

---

**Seed date:** 2026-04-22
**Status:** Investigation plan (no code changes yet).
**Owner:** Kyle (CEO persona) owns the problem; delegated investigation TBD.
**Upstream reference:** User-story #80. Operator Q12 (Seth pass-2 §5 ambiguity) agreed this
warrants its own plan doc.

---

## Problem statement

The Kyle CEO persona's `/csuite-save-and-restart` flow is slow. Operator observes it taking
noticeably longer than expected for a save-state-and-replace operation. No baseline measurement
is on record. Slow enough that the operator flagged it during the 2026-04-22 world-state
alignment session.

This is distinct from `drem-kyle` (the Go binary at `cmd/drem-kyle/`). The csuite Kyle persona
is a Claude Code container instance whose state lives under `~/.drem-csuite/kyle/`.

---

## What "slow" could mean (three hypotheses)

### Hypothesis A: state.md size

**Claim:** `~/.drem-csuite/kyle/state.md` (or equivalent session summary file Kyle writes
during save) has grown to a size where either writing it or loading it on restart is
IO-limited or parse-limited.

**Evidence to gather:**
- Current byte size of `state.md` / any equivalent save artifacts.
- Growth curve over recent sessions (git-log of the file if tracked, or mtime audit).
- Whether save writes the whole file or appends.
- Whether restart reads it end-to-end or only the tail.

**Falsifier:** File is small (< 100 KB) and reads/writes are sub-second. Hypothesis rejected.

### Hypothesis B: claude-cli context-window rendering

**Claim:** When Kyle restarts, the CLI harness reconstructs the conversation by reading
prior-session transcripts into the context window. If the harness rehydrates everything
verbatim instead of the save-context digest, restart is proportional to total historical
turn-count.

**Evidence to gather:**
- What does the Kyle container entrypoint actually do on start? Does it pass `--resume`, load
  a specific transcript, or start fresh?
- `ls -la` of `~/.drem-csuite/kyle/` and any CLI-level transcript store.
- Is there duplicate work between the save-context markdown and the CLI's own history?

**Falsifier:** Kyle starts fresh from the restart-context.md only; no CLI transcript replay.
Hypothesis rejected.

### Hypothesis C: watcher-restart latency

**Claim:** The slow step isn't Kyle per se — it's the `csuite-watcher` or the persona
container recycle. The save completes quickly, but the new container takes time to pass
healthchecks / process its inbox / acknowledge.

**Evidence to gather:**
- Timestamps from the last `/csuite-save-and-restart` run: save start, save complete,
  container stop, container start, first new-Kyle turn.
- Watcher state during restart (currently Restarting(1), which would skew any measurement).
- Compose `depends_on` + healthcheck times for the Kyle path.

**Falsifier:** Kyle's turn-1-after-restart latency is normal (<30s); perceived slowness is
elsewhere. Hypothesis rejected.

---

## Investigation steps

1. **Instrument the next save/restart.** Collect wall-clock timestamps at each phase:
   - save invocation → state.md written
   - save complete → container stop
   - container stop → container start
   - container start → first poll cycle
   - first poll → first turn processed
2. **Size audit.** Measure `~/.drem-csuite/kyle/` footprint, per-file and total.
3. **Container entrypoint review.** Read the Kyle container's startup script. What does it
   load, in what order, with what defaults?
4. **Compare vs Seth/Alex/Mike restart time.** Those personas restarted ~1h ago (per the
   restart context). Is Kyle genuinely an outlier, or are all csuite restarts slow?
5. **One-shot profile.** If nothing above yields a root cause, run the save/restart under
   `strace -c` or equivalent on the container entrypoint and take the top-3 syscalls.

---

## Likely outcome shapes

- **If Hypothesis A:** introduce a bounded-size state.md (last N sessions only) with archival
  of older content; Kyle's restart-context writer trims automatically.
- **If Hypothesis B:** drop any CLI transcript replay; Kyle restarts from `restart-context.md`
  alone (which is already the intent — verify it's the reality).
- **If Hypothesis C:** the fix isn't Kyle-local; file it as a watcher or compose plan doc.
- **If something else:** file a new plan doc with findings.

---

## Out of scope for this investigation

- Rewriting the save-and-restart mechanism entirely.
- Containerization work (Kyle already runs as a Go binary + Claude CLI; no pivot pending).
- Bulk cleanup of historical state files (that's a separate housekeeping plan).

---

## Not scheduled into Q2 pods until root cause known

Per operator's Q12 answer — this warrants a plan doc, not a pod slot yet. Sequencing decision
comes after the investigation returns.
