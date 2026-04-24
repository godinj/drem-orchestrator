// Command drem-merger runs a single container-local merge and exits. It is
// invoked per task by the warm merger pool described in
// docs/prd-containerization.md.
//
// Flow (see internal/merger for details):
//
//  1. Reset /work (or --work-dir).
//  2. Clone /bare (or --bare-repo) at the integration branch.
//  3. Merge --no-ff the feature branch.
//  4. Run the project's test command via `sh -c <test-cmd>`.
//  5. Push integration to origin on success.
//  6. Delete the feature branch on origin.
//  7. Report the outcome to the orchestrator via /internal/logs.
//
// Registry bookkeeping uses the gitref registry if a DSN is configured,
// otherwise a no-op registry (so tests and dry-runs still work).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/merger"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func main() {
	code, err := run(context.Background(), os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "drem-merger: %v\n", err)
	}
	os.Exit(code)
}

type flags struct {
	workDir           string
	bareRepo          string
	featureBranch     string
	integrationBranch string
	project           string
	taskID            string
	testCmd           string
	orchURL           string
	agentmonToken     string
	dbDSN             string
}

func parseFlags(args []string) (flags, error) {
	var f flags
	fs := flag.NewFlagSet("drem-merger", flag.ContinueOnError)
	fs.StringVar(&f.workDir, "work-dir", "/work", "container-local work directory for the clone")
	fs.StringVar(&f.bareRepo, "bare-repo", "/bare", "bind-mounted bare repository path")
	fs.StringVar(&f.featureBranch, "feature-branch", "", "feature branch to merge (required)")
	fs.StringVar(&f.integrationBranch, "integration-branch", "master", "integration branch to merge into")
	fs.StringVar(&f.project, "project", "", "project identifier (required)")
	fs.StringVar(&f.taskID, "task-id", "", "task identifier (required)")
	fs.StringVar(&f.testCmd, "test-cmd", "", "project test command executed via sh -c (required)")
	fs.StringVar(&f.orchURL, "orch-url", "", "orchestrator base URL for /internal/logs (required)")
	fs.StringVar(&f.agentmonToken, "agentmon-token", "", "bearer token for /internal/logs (required)")
	fs.StringVar(&f.dbDSN, "gitref-db", "", "optional SQLite DSN for the gitref registry")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	var missing []string
	if f.featureBranch == "" {
		missing = append(missing, "--feature-branch")
	}
	if f.project == "" {
		missing = append(missing, "--project")
	}
	if f.taskID == "" {
		missing = append(missing, "--task-id")
	}
	if f.testCmd == "" {
		missing = append(missing, "--test-cmd")
	}
	if f.orchURL == "" {
		missing = append(missing, "--orch-url")
	}
	if f.agentmonToken == "" {
		missing = append(missing, "--agentmon-token")
	}
	if len(missing) > 0 {
		return f, fmt.Errorf("missing required flags: %v", missing)
	}
	return f, nil
}

// run executes a single merge. It returns the desired process exit code and
// an error for logging. Exit codes:
//
//	0 — merge succeeded and integration was pushed.
//	2 — merge produced conflicts.
//	3 — tests failed after a clean merge.
//	4 — push or delete failed.
//	1 — structural / configuration error.
func run(ctx context.Context, args []string) (int, error) {
	f, err := parseFlags(args)
	if err != nil {
		return 1, err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	reporter := newHTTPReporter(f.orchURL, f.agentmonToken, logger)
	registry, regCleanup, regErr := newRegistry(f.dbDSN, logger)
	if regErr != nil {
		// Non-fatal — fall back to a no-op registry and continue. A desynced
		// registry is less harmful than a stuck merge.
		logger.Warn("failed to open gitref registry; falling back to no-op", "error", regErr)
		registry = merger.NoopRegistry{}
	}
	defer regCleanup()

	m := &merger.Merger{
		WorkDir:  f.workDir,
		BareRepo: f.bareRepo,
		TestCmd:  f.testCmd,
		Reporter: reporter,
		Registry: registry,
		Logger:   logger,
	}

	result, mergeErr := m.Merge(ctx, merger.MergeRequest{
		FeatureBranch:     f.featureBranch,
		IntegrationBranch: f.integrationBranch,
		Project:           f.project,
		TaskID:            f.taskID,
	})
	if mergeErr != nil {
		logger.Error("merge failed", "error", mergeErr)
		return exitCodeFor(result), mergeErr
	}
	logger.Info("merge finished",
		"success", result.Success,
		"tests_passed", result.TestsPassed,
		"conflicts", result.Conflicts,
		"merged_sha", result.MergedSHA,
		"failure_reason", result.FailureReason,
	)
	return exitCodeFor(result), nil
}

// exitCodeFor maps a MergeResult to the process exit code used by the
// merger pool dispatcher.
func exitCodeFor(r *merger.MergeResult) int {
	if r == nil {
		return 1
	}
	if r.Success {
		return 0
	}
	switch r.FailureReason {
	case "conflict":
		return 2
	case "tests_failed":
		return 3
	case "push_failed":
		return 4
	default:
		return 1
	}
}

// ---------------------------------------------------------------------------
// Reporter — POSTs merge_result records to <orch-url>/internal/logs.
// ---------------------------------------------------------------------------

type httpReporter struct {
	baseURL string
	token   string
	client  *http.Client
	logger  *slog.Logger
}

func newHTTPReporter(baseURL, token string, logger *slog.Logger) merger.MergeReporter {
	return &httpReporter{
		baseURL: baseURL,
		token:   token,
		client:  &http.Client{Timeout: 30 * time.Second},
		logger:  logger,
	}
}

// mergeLogRecord is the /internal/logs payload shape. It deliberately uses
// snake_case JSON keys so Kyle can index on them without per-record
// transformation.
type mergeLogRecord struct {
	Type        string    `json:"type"`
	ContainerID string    `json:"container_id,omitempty"`
	WorkerID    string    `json:"worker_id,omitempty"`
	Project     string    `json:"project"`
	TaskID      string    `json:"task_id"`
	Success     bool      `json:"success"`
	Reason      string    `json:"failure_reason,omitempty"`
	Conflicts   []string  `json:"conflicts,omitempty"`
	TestsOK     bool      `json:"tests_passed"`
	MergedSHA   string    `json:"merged_sha,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	TestOutput  string    `json:"test_output,omitempty"`
}

func (r *httpReporter) Report(ctx context.Context, project, taskID string, res merger.MergeResult) error {
	if r.baseURL == "" {
		return nil
	}
	hostname, _ := os.Hostname()
	rec := mergeLogRecord{
		Type:        "merge_result",
		ContainerID: hostname,
		WorkerID:    os.Getenv("DREM_WORKER_ID"),
		Project:     project,
		TaskID:      taskID,
		Success:     res.Success,
		Reason:      res.FailureReason,
		Conflicts:   res.Conflicts,
		TestsOK:     res.TestsPassed,
		MergedSHA:   res.MergedSHA,
		StartedAt:   res.StartedAt,
		FinishedAt:  res.FinishedAt,
		TestOutput:  truncate(res.TestOutput, 64*1024),
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode merge_result: %w", err)
	}
	body, err := json.Marshal(orchdto.IngestRequest{Records: []json.RawMessage{raw}})
	if err != nil {
		return fmt.Errorf("encode merge_result batch: %w", err)
	}

	url := r.baseURL + "/internal/logs"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		req.Header.Set("X-Drem-Agentmon-Token", r.token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("post merge_result: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("merge_result POST %s returned %d: %s", url, resp.StatusCode, snippet)
	}
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n[truncated]"
}

// ---------------------------------------------------------------------------
// Registry shim — adapts (bareRepo, branch) onto gitref.Registry's ID-based
// interface by resolving the ID via FindByBranch.
// ---------------------------------------------------------------------------

type gitrefShim struct {
	reg    *gitref.Registry
	logger *slog.Logger
}

// newRegistry opens the SQLite DSN (if supplied) and returns a shim plus a
// cleanup function. When dsn is empty it returns a no-op registry with a
// no-op cleanup.
func newRegistry(dsn string, logger *slog.Logger) (merger.GitrefRegistry, func(), error) {
	if dsn == "" {
		return merger.NoopRegistry{}, func() {}, nil
	}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, func() {}, fmt.Errorf("open gitref db %s: %w", dsn, err)
	}
	sqlDB, err := db.DB()
	cleanup := func() {
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	}
	if err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("unwrap gitref db: %w", err)
	}
	return &gitrefShim{reg: gitref.NewRegistry(db), logger: logger}, cleanup, nil
}

func (s *gitrefShim) MarkMerged(ctx context.Context, bareRepo, branch string) error {
	ref, err := s.reg.FindByBranch(ctx, bareRepo, branch)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.Warn("gitref not found; skipping MarkMerged", "bare_repo", bareRepo, "branch", branch)
			return nil
		}
		return err
	}
	return s.reg.MarkMerged(ctx, ref.ID)
}

func (s *gitrefShim) MarkDeleted(ctx context.Context, bareRepo, branch string) error {
	ref, err := s.reg.FindByBranch(ctx, bareRepo, branch)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.Warn("gitref not found; skipping MarkDeleted", "bare_repo", bareRepo, "branch", branch)
			return nil
		}
		return err
	}
	return s.reg.MarkDeleted(ctx, ref.ID)
}
