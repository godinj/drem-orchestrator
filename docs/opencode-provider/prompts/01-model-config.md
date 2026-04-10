# Agent: Model & Config — Provider Type

You are working on the `master` branch of drem-orchestrator, a Go-based orchestrator that spawns Claude Code agents in git worktrees. Your task is adding a `provider` concept so agents can use OpenCode (a local LLM CLI) instead of Claude Code.

## Context

Read these specs before starting:
- `opencode-plan.md` (sections: Changes 1–3, 8 — Model layer, Config layer, Profile layer, DB model)
- `internal/model/agentconfig.go` (current AgentCLIConfig — add ProviderType + Provider field)
- `cmd/drem/config.go` (current AgentConfig/Config structs — add Provider and OpenCodeBin fields)
- `cmd/drem/config_profiles.go` (current ProfileConfig — add Provider override)
- `internal/model/models.go` (current Agent struct — add Provider column)

## Deliverables

### Modified files

#### 1. `internal/model/agentconfig.go`

Add provider type and split CLIArgs by provider.

- Add `ProviderType` string type with constants:
  - `ProviderClaude ProviderType = "claude"`
  - `ProviderOpenCode ProviderType = "opencode"`
- Add `Provider ProviderType` field to `AgentCLIConfig`
- Add `EffectiveProvider() ProviderType` method — returns `ProviderClaude` when Provider is empty
- Split `CLIArgs()` into provider-specific branches:
  - Claude (default): `["--model", X, "--effort", Y]` — unchanged from current behavior
  - OpenCode: `["--model", X, "--variant", Y, "--format", "json", "--agent", "build"]`
  - For OpenCode: the `Effort` field maps to `--variant` (same string values)
  - For both: omit `--model` when Model is empty, omit `--effort`/`--variant` when Effort is empty

#### 2. `cmd/drem/config.go`

Add provider to agent config and OpenCode binary path to global config.

- Add `Provider string` field to `AgentConfig` struct with TOML tag `toml:"provider"`
- Add `OpenCodeBin string` field to `Config` struct with TOML tag `toml:"opencode_bin"`
- Update `ForAgentType()` to pass `Provider` through: set `AgentCLIConfig.Provider` to `model.ProviderType(ac.Provider)`
- Update `DefaultConfig()`: add `OpenCodeBin: "opencode"` to the returned Config
- Update `SupervisorCLIConfig()` and `InteractiveSupervisorCLIConfig()` — no Provider field needed (supervisors stay Claude-only for now)

#### 3. `cmd/drem/config_profiles.go`

Add provider override in profile resolution.

- Add `Provider string` field to `ProfileConfig` role entries — but `ProfileConfig` uses `AgentConfig` which already gets the new `Provider` field from step 2, so no struct change needed here
- In `ForAgentTypeWithProfile()`, add provider override after the existing model/effort overrides:
  ```go
  if override.Provider != "" {
      base.Provider = model.ProviderType(override.Provider)
  }
  ```

#### 4. `internal/model/models.go`

Add provider field to the Agent DB model.

- Add `Provider string` field to `Agent` struct with tags: `gorm:"column:provider;default:''"` 
- Empty string means Claude (backwards compatible — all existing rows are Claude agents)
- Place it near the existing `ModelID` and `Effort` fields

## Scope Limitation

Do NOT modify `runner.go`, `process.go`, `hook.go`, `monitor.go`, or `main.go`. Those are handled by other agents. Only touch the four files listed above.

## Conventions

- Package names match directory: `model`, `main`
- No unnecessary comments on obvious code
- Build verification: `cd /home/godinj/git/drem-orchestrator.git/master && go vet ./... && go test ./...`
