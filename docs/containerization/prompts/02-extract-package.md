# Agent: Log Extraction Package

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 1 foundation work for the containerization initiative: extract the log-line-to-typed-event parsing logic into a standalone package so that both the existing transcript-tailing path and the new Docker stdout subscription path (prompt 11) can share it.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Modules to be built or modified" → extraction package; "Agentmon absorbs log shipping"; user stories 16, 26, 27)
- `internal/agentmon/parse.go` (current transcript parser — patterns you will generalize)
- `internal/agentmon/agentmon.go` (how parsed events feed the rest of the pipeline today)
- `internal/agentmon/exitlog.go` (crash-detection heuristics already in the tree)
- `ARCHITECTURE.md` (file-length ceilings, no-cyclic-imports rules)

## Deliverables

### New files (`internal/extract/`)

#### 1. `event.go`

Typed event structs emitted by the parser. Each event carries a `Timestamp` and a `Source` string (e.g. `"stdout"`, `"transcript"`) so downstream consumers know where it came from.

- `type Event interface { isEvent() }` — unexported marker method for exhaustive switch coverage
- `type Commit struct { Timestamp time.Time; Source string; SHA string; Branch string; Message string }`
- `type Push struct { Timestamp time.Time; Source string; Branch string; Remote string }`
- `type TestResult struct { Timestamp time.Time; Source string; Passed bool; Summary string }`
- `type BuildError struct { Timestamp time.Time; Source string; Tool string; Message string; File string; Line int }`
- `type Heartbeat struct { Timestamp time.Time; Source string; AgentID string }`
- `type Crash struct { Timestamp time.Time; Source string; Reason string; ExitCode int }`
- `type ToolCall struct { Timestamp time.Time; Source string; Tool string; Target string }` — replaces the current `ToolCall` in `internal/agentmon/parse.go`
- Each type gets a pointer-receiver `isEvent()` method. Do not export the method.

#### 2. `parse.go`

- `func ParseLine(src string, line []byte, now time.Time) Event` — returns a parsed `Event` or `nil` if the line matches nothing. `src` is the source tag copied into `Event.Source`. `now` is injected for testability (callers pass `time.Now()`).
- Internal matchers for each event type. Prefer explicit prefix/regex checks over regex-heavy scans. Each matcher returns `(Event, bool)`.
- For commit detection, match lines matching `git commit ... [<sha>]` AND lines containing `"[<branch> <sha>]"` on git's own stdout.
- For push detection, match `"To <remote>"` followed by branch ref lines, or `"git push ..."` command echoes.
- For test results, recognize the existing conventions used by `internal/agentmon/parse.go` plus `ok`/`FAIL` lines from `go test` output.
- For build errors, recognize `go build` compiler errors (`<file>:<line>:<col>: <msg>`), `cargo build` errors, and C/C++ compiler errors (for the C++ worker image).
- For heartbeats, recognize the marker the watchdog binary emits (prompt 04 will write lines like `DREM-HEARTBEAT agent_id=<id>`).
- For crashes, match `panic:`, `runtime error:`, `signal SIGSEGV`, and non-zero `exit status N` lines.

#### 3. `parse_test.go`

Table-driven tests. Given a source tag and a line, assert the returned event's type and field values, or assert `nil` for non-matching input. Use `testify/require`.

Include at minimum:

- `Commit` recognized from git stdout
- `Push` recognized from git stdout
- `TestResult{Passed: true}` from `ok <pkg> <duration>`
- `TestResult{Passed: false}` from `FAIL <pkg>` and `--- FAIL: Test...`
- `BuildError` from `foo.go:12:3: undefined: bar`
- `BuildError` from a C++ compiler error
- `Heartbeat` from the watchdog marker format
- `Crash` from `panic:`
- `Crash` from `signal SIGSEGV`
- `nil` for unrelated prose

## Migration

#### 4. `internal/agentmon/parse.go`

This file currently contains `ToolCall` and `parseTranscriptLine`. After the extraction package exists, migrate `parseTranscriptLine` to call `extract.ParseLine(...)` and wrap the result for the agentmon pipeline. Keep the file; just thin it out. The test file `internal/agentmon/agentmon_test.go` must still pass.

Do not change the agentmon ingestion flow or the transcript-tailing behavior. The only change to agentmon here is that it delegates parsing. The new Docker-subscription input path will be added in prompt 11.

## Scope Limitation

- Pure parsing only. No I/O, no goroutines, no Docker access. `ParseLine` takes bytes in and returns a value.
- Do not introduce a dependency on `internal/container`. Extraction must be usable by callers that never touch Docker (the transcript tailer).
- Do not introduce a dependency on `internal/model` (GORM types). This package has zero dependencies outside the standard library.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package name: `extract`
- Follow file-length and function-count ceilings in `ARCHITECTURE.md`. If a single matcher grows beyond ~40 lines, split into a per-type file (`parse_commit.go`, `parse_build.go`, etc.)
- Tests use `github.com/stretchr/testify/require`
- Build verification: `go build ./internal/extract/... ./internal/agentmon/... && go test ./internal/extract/... ./internal/agentmon/...`
- Constitution check: `bash scripts/check_constitution.sh`
