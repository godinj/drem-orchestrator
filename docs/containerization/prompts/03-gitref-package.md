# Agent: Git Reference Package

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 1 foundation work for the containerization initiative: create a small Git reference package that replaces the current worktree package's branch-tracking responsibilities with zero host-filesystem operations. Workers now clone into their own container filesystem, so the orchestrator no longer manages worktrees on disk — it tracks branch names and metadata only.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Modules to be built or modified" → Git reference package; "Container filesystem model"; user stories 10, 11, 42)
- `internal/worktree/manager.go` (current worktree creation API — understand what is being left behind; do NOT delete this file here, prompt 17 handles removal)
- `internal/worktree/git.go` (current git-operation helpers — some of these will be retained for the merger to use in-container, but this package owns none of that logic)
- `internal/testutil/testutil_git.go` (`SetupBareRepo`, `CommitFile` helpers you will reuse in tests)
- `internal/testutil/testutil.go` (`NewTestDB`, `NewTestDBWithModels` — use these)
- `internal/model/` (understand the existing GORM model conventions — hooks consolidation, field naming)

## Deliverables

### New files (`internal/gitref/`)

#### 1. `model.go`

GORM model for a tracked branch reference, replacing the implicit on-disk worktree record.

- `type BranchRef struct { ID uint; BareRepoPath string; Project string; TaskID string; AgentType string; Branch string; Status Status; CreatedAt time.Time; UpdatedAt time.Time }`
- `type Status string` with constants `StatusActive`, `StatusMerged`, `StatusDeleted`
- GORM tags: unique index on `(BareRepoPath, Branch)`; index on `(Project, Status)`
- `func (BranchRef) TableName() string` returns `"branch_refs"`

Register the model in `internal/db/migrate.go` (or whatever the project's migration helper is — match the pattern used for existing models in `internal/model/`).

#### 2. `registry.go`

Thin CRUD surface over the model. No filesystem writes.

- `type Registry struct { db *gorm.DB }`
- `func NewRegistry(db *gorm.DB) *Registry`
- `func (r *Registry) Register(ctx context.Context, ref *BranchRef) error` — upsert by `(BareRepoPath, Branch)`. Returns an error if another active branch with the same name exists for the same bare repo.
- `func (r *Registry) MarkMerged(ctx context.Context, id uint) error`
- `func (r *Registry) MarkDeleted(ctx context.Context, id uint) error`
- `func (r *Registry) Get(ctx context.Context, id uint) (*BranchRef, error)` — returns `gorm.ErrRecordNotFound` unwrapped for not-found
- `func (r *Registry) FindByBranch(ctx context.Context, bareRepo, branch string) (*BranchRef, error)`
- `func (r *Registry) ListByProject(ctx context.Context, project string, status Status) ([]BranchRef, error)` — empty `status` means all

#### 3. `git.go`

Read-only helpers against a bare repository. No write operations; no filesystem mutations outside of running `git` subcommands with `GIT_DIR=<bare>`.

- `func BranchExists(ctx context.Context, bareRepo, branch string) (bool, error)` — shell out to `git --git-dir=<bareRepo> show-ref --verify refs/heads/<branch>`; return false on exit 1
- `func HeadCommit(ctx context.Context, bareRepo, branch string) (string, error)` — `git --git-dir=<bareRepo> rev-parse refs/heads/<branch>`
- `func DefaultBranch(ctx context.Context, bareRepo string) (string, error)` — read `HEAD` symref

Use `os/exec.CommandContext`, always with an explicit timeout budget; wrap errors with branch and repo context.

### Tests

#### 4. `registry_test.go`

- `Register` inserts a new row and makes it queryable via `FindByBranch`
- `Register` returns a meaningful error when a duplicate active branch is inserted for the same bare repo
- `Register` allows the same branch name across different bare repos (multi-project scenario)
- `MarkMerged` + `MarkDeleted` state transitions are persisted
- `ListByProject` filters correctly by status

Use `testutil.NewTestDBWithModels(t, &gitref.BranchRef{})` and `testutil.SetupBareRepo(t)` for bare-repo paths in the fixtures.

#### 5. `git_test.go`

- `BranchExists` returns `true` for a branch created via `testutil.CommitFile` + `git push <bare> <branch>`, `false` for an unknown branch
- `HeadCommit` returns a 40-char hex SHA for an existing branch
- `DefaultBranch` returns `"master"` or `"main"` depending on what `SetupBareRepo` initialises

## Scope Limitation

- No worktree creation. No `git worktree add`, `git worktree remove`. No checkout. The orchestrator never creates working copies on the host after this package lands.
- No merge logic. The merger package (prompt 09) does merges inside its own container using its own clone.
- Do not delete `internal/worktree/`. Prompt 17 coordinates that removal once every consumer has migrated.
- Do not import `internal/container/`. Git reference tracking is a pure Go concern; it does not know about Docker.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package name: `gitref`
- GORM hooks consolidated per `ARCHITECTURE.md` — do not scatter `BeforeSave` hooks across files
- File-length and function-count ceilings from `ARCHITECTURE.md` apply
- Tests: `testify/require`, use existing `internal/testutil` helpers; do not create new ad-hoc DB or repo setup functions
- Build verification: `go build ./internal/gitref/... && go test ./internal/gitref/...`
- Constitution check: `bash scripts/check_constitution.sh`
