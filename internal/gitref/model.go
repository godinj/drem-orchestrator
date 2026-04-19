// Package gitref tracks feature and agent branch names referenced from the
// orchestrator's SQLite database. It replaces the host-side worktree registry
// from the legacy host-session packages with a pure metadata surface: no
// filesystem writes, no host-side checkout, no git worktree add.
//
// Workers clone their branch into a container filesystem at startup and
// discard the clone when the task completes (see docs/prd-containerization.md,
// user stories 10, 11, and 42). The orchestrator therefore only needs the
// bare-repo path plus branch name to route work; everything else lives inside
// the short-lived worker container.
package gitref

import "time"

// Status is the lifecycle state of a tracked branch reference.
type Status string

// Status values for BranchRef.Status.
const (
	// StatusActive marks a branch with live work against the bare repository.
	StatusActive Status = "active"
	// StatusMerged marks a branch whose commits have been merged into the
	// integration branch by the merger service.
	StatusMerged Status = "merged"
	// StatusDeleted marks a branch that has been pruned from the bare
	// repository (for example after a successful merge or a rejection).
	StatusDeleted Status = "deleted"
)

// BranchRef records a single tracked branch inside a bare repository. It is
// the database projection of the logical "this feature/agent owns this
// branch" relation; no filesystem state is implied.
type BranchRef struct {
	ID           uint   `gorm:"primaryKey"`
	BareRepoPath string `gorm:"not null;uniqueIndex:idx_branch_refs_repo_branch,priority:1;index:idx_branch_refs_project_status,priority:1"`
	Project      string `gorm:"not null;index:idx_branch_refs_project_status,priority:1"`
	TaskID       string `gorm:"index"`
	AgentType    string
	Branch       string `gorm:"not null;uniqueIndex:idx_branch_refs_repo_branch,priority:2"`
	Status       Status `gorm:"not null;default:active;index:idx_branch_refs_project_status,priority:2"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName returns the concrete table name for BranchRef. The default
// pluralisation ("branch_refs") matches GORM's inference, but declaring it
// explicitly guards against accidental schema drift if the type is renamed.
func (BranchRef) TableName() string {
	return "branch_refs"
}
