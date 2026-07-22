package db_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/db"
)

// TestTasksProjectCreatedIndexUsed is W2.1's regression test. It opens
// a freshly-initialized orchestrator DB (which should run the composite
// index migration for tasks(project_id, created_at DESC)) and uses
// SQLite's EXPLAIN QUERY PLAN to prove the ListTasks query — a project-
// scoped newest-first lookup — hits idx_tasks_project_created rather
// than a full scan or generic per-column index.
//
// If this test fails with "SCAN tasks" in the plan output, the
// migration did not land; if it fails with a different index name, the
// migration's name drifted from the plan-doc spec.
func TestTasksProjectCreatedIndexUsed(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	gdb, err := db.Init(dbPath)
	require.NoError(t, err)

	sqlDB, err := gdb.DB()
	require.NoError(t, err)

	// Mirror handleListTasks' query shape exactly: SELECT tasks WHERE
	// project_id = ? ORDER BY created_at DESC LIMIT ?. The status filter
	// is an optional AND; the newest-first limit is the hot path we care
	// about. EXPLAIN QUERY PLAN surfaces which index (if any) SQLite
	// picks.
	rows, err := sqlDB.Query(`EXPLAIN QUERY PLAN
		SELECT id FROM tasks
		WHERE project_id = ?
		ORDER BY created_at DESC
		LIMIT 100`, "00000000-0000-0000-0000-000000000000")
	require.NoError(t, err)
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	require.NoError(t, rows.Err())

	planStr := plan.String()
	require.Contains(t, planStr, "idx_tasks_project_created",
		"EXPLAIN QUERY PLAN did not use idx_tasks_project_created; plan was:\n%s", planStr)
}

func TestAttemptEventsMigrationCreatesQueryIndexes(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	gdb, err := db.Init(dbPath)
	require.NoError(t, err)

	sqlDB, err := gdb.DB()
	require.NoError(t, err)

	var tableName string
	require.NoError(t, sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'attempt_events'`).Scan(&tableName))
	require.Equal(t, "attempt_events", tableName)

	rows, err := sqlDB.Query(`PRAGMA index_list(attempt_events)`)
	require.NoError(t, err)
	defer rows.Close()

	indexes := map[string]struct{}{}
	for rows.Next() {
		var seq int
		var name string
		var unique int
		var origin string
		var partial int
		require.NoError(t, rows.Scan(&seq, &name, &unique, &origin, &partial))
		indexes[name] = struct{}{}
	}
	require.NoError(t, rows.Err())

	for _, name := range []string{
		"idx_attempt_events_task_id",
		"idx_attempt_events_attempt_id",
		"idx_attempt_events_state",
		"idx_attempt_events_type",
		"idx_attempt_events_created_at",
		"idx_attempt_events_task_created",
		"idx_attempt_events_attempt_created",
		"idx_attempt_events_state_created",
		"idx_attempt_events_type_created",
	} {
		require.Contains(t, indexes, name)
	}
}

func TestDeliveryArtifactMigrationCreatesCurrentArtifactConstraint(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	gdb, err := db.Init(dbPath)
	require.NoError(t, err)

	sqlDB, err := gdb.DB()
	require.NoError(t, err)

	var definition string
	require.NoError(t, sqlDB.QueryRow(`
		SELECT sql FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_delivery_artifact_current_task'
	`).Scan(&definition))
	require.Contains(t, definition, "CREATE UNIQUE INDEX")
	require.Contains(t, definition, "delivery_artifacts(task_id)")
	require.Contains(t, definition, "WHERE invalidated_at IS NULL")
}

func TestTaskMigrationBackfillsStateVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	legacyGDB, err := db.Init(dbPath)
	require.NoError(t, err)
	legacy, err := legacyGDB.DB()
	require.NoError(t, err)
	_, err = legacy.Exec(`
		INSERT INTO projects(id, name, bare_repo_path)
		VALUES ('project-1', 'legacy', '/tmp/legacy.git');
		INSERT INTO tasks(id, project_id, title, description, status)
		VALUES ('task-1', 'project-1', 'legacy task', 'before state CAS', 'backlog');
		ALTER TABLE tasks DROP COLUMN state_version;
	`)
	require.NoError(t, err)
	require.NoError(t, legacy.Close())

	gdb, err := db.Init(dbPath)
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)

	var version uint64
	require.NoError(t, sqlDB.QueryRow(`SELECT state_version FROM tasks WHERE id = 'task-1'`).Scan(&version))
	require.Equal(t, uint64(1), version)
}

func TestDatabaseStartupFailsClosedOnMultipleCurrentArtifacts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "invalid-delivery.db")
	gdb, err := db.Init(dbPath)
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)

	_, err = sqlDB.Exec(`
		INSERT INTO projects(id, name, bare_repo_path)
		VALUES ('project-1', 'invalid-delivery', '/tmp/invalid-delivery.git');
		INSERT INTO tasks(id, project_id, title, description, status)
		VALUES ('task-1', 'project-1', 'invalid delivery', 'duplicate current artifacts', 'verification_ready');
		DROP INDEX idx_delivery_artifact_current_task;
		INSERT INTO delivery_artifacts(
			id, task_id, artifact_version, branch, commit_sha, base_branch, base_sha,
			preliminary_evidence, creator_actor, creator_source, created_at, updated_at
		) VALUES
			('artifact-1', 'task-1', 1, 'feature/one', '1111111111111111111111111111111111111111',
			 'master', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', '{}', 'orchestrator', 'test', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			('artifact-2', 'task-1', 2, 'feature/two', '2222222222222222222222222222222222222222',
			 'master', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', '{}', 'orchestrator', 'test', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
	`)
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = db.Init(dbPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "create current delivery artifact index")
}
