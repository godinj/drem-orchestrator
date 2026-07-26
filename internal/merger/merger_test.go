package merger_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/merger"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// fakeReporter records every Report call so tests can assert that Merger
// always reports exactly once, including on failure paths.
type fakeReporter struct {
	mu     sync.Mutex
	calls  []reporterCall
	retErr error
}

type reporterCall struct {
	Project string
	TaskID  string
	Result  merger.MergeResult
}

func (r *fakeReporter) Report(_ context.Context, project, taskID string, res merger.MergeResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, reporterCall{Project: project, TaskID: taskID, Result: res})
	return r.retErr
}

func (r *fakeReporter) Calls() []reporterCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]reporterCall, len(r.calls))
	copy(out, r.calls)
	return out
}

// fakeRegistry records MarkMerged / MarkDeleted calls without talking to
// gorm. The merger test suite never needs to resolve BranchRef IDs because
// the merger interface is expressed in terms of (bareRepo, branch).
type fakeRegistry struct {
	mu          sync.Mutex
	merged      []branchKey
	deleted     []branchKey
	markMergErr error
	markDelErr  error
}

type branchKey struct {
	BareRepo string
	Branch   string
}

func (f *fakeRegistry) MarkMerged(_ context.Context, bareRepo, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markMergErr != nil {
		return f.markMergErr
	}
	f.merged = append(f.merged, branchKey{bareRepo, branch})
	return nil
}

func (f *fakeRegistry) MarkDeleted(_ context.Context, bareRepo, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markDelErr != nil {
		return f.markDelErr
	}
	f.deleted = append(f.deleted, branchKey{bareRepo, branch})
	return nil
}

func (f *fakeRegistry) MergedCalls() []branchKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]branchKey, len(f.merged))
	copy(out, f.merged)
	return out
}

func (f *fakeRegistry) DeletedCalls() []branchKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]branchKey, len(f.deleted))
	copy(out, f.deleted)
	return out
}

// ---------------------------------------------------------------------------
// Fixture helpers (non-git). Git helpers live in testutil.
// ---------------------------------------------------------------------------

// featureScenario holds the paths and branch names common to every merger
// test case so each test body stays focused on the assertions.
type featureScenario struct {
	BareRepo    string
	Integration string
	Feature     string
	WorkDir     string
	BaseSHA     string
	FeatureSHA  string
}

// setupFeatureScenario creates a bare repo, clones it, adds a diverging
// feature branch that touches a different file so tests can control
// conflict vs non-conflict cases by selecting the file written. The
// integration branch is whatever SetupBareRepo produced (master or main).
func setupFeatureScenario(t *testing.T, featureContent, featureFile string) featureScenario {
	t.Helper()
	bareRepo := testutil.SetupBareRepo(t)

	// Discover the integration branch from the bare repo.
	integration, err := testutil.RunGit(
		[]string{"symbolic-ref", "--short", "HEAD"}, bareRepo)
	require.NoError(t, err, "detect integration branch")

	// Use a fresh clone to create the feature branch so we never stomp on
	// any in-progress merger workspace.
	prep := filepath.Join(t.TempDir(), "prep")
	_, err = testutil.RunGit([]string{"clone", bareRepo, prep}, "")
	require.NoError(t, err, "clone prep")
	_, err = testutil.RunGit([]string{"config", "user.email", "t@t.com"}, prep)
	require.NoError(t, err)
	_, err = testutil.RunGit([]string{"config", "user.name", "T"}, prep)
	require.NoError(t, err)

	feature := "feature/ft1"
	_, err = testutil.RunGit([]string{"checkout", "-b", feature}, prep)
	require.NoError(t, err, "create feature branch")

	testutil.CommitFile(t, prep, featureFile, featureContent, "feat: add "+featureFile)

	_, err = testutil.RunGit([]string{"push", "origin", feature}, prep)
	require.NoError(t, err, "push feature")
	baseSHA, err := testutil.RunGit([]string{"rev-parse", integration}, bareRepo)
	require.NoError(t, err, "resolve integration sha")
	featureSHA, err := testutil.RunGit([]string{"rev-parse", feature}, bareRepo)
	require.NoError(t, err, "resolve feature sha")

	return featureScenario{
		BareRepo:    bareRepo,
		Integration: integration,
		Feature:     feature,
		WorkDir:     filepath.Join(t.TempDir(), "work"),
		BaseSHA:     baseSHA,
		FeatureSHA:  featureSHA,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestMerger_HappyPath_MergesAndDeletesFeature(t *testing.T) {
	scn := setupFeatureScenario(t, "hello\n", "feature.txt")

	rep := &fakeReporter{}
	reg := &fakeRegistry{}

	m := &merger.Merger{
		WorkDir:  scn.WorkDir,
		BareRepo: scn.BareRepo,
		TestCmd:  "sh -c 'exit 0'",
		Reporter: rep,
		Registry: reg,
	}

	res, err := m.Merge(context.Background(), merger.MergeRequest{
		FeatureBranch:     scn.Feature,
		IntegrationBranch: scn.Integration,
		Project:           "drem",
		TaskID:            "task-1",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.True(t, res.Success, "expected success; conflicts=%v output=%s", res.Conflicts, res.TestOutput)
	require.True(t, res.TestsPassed)
	require.Empty(t, res.Conflicts)
	require.NotEmpty(t, res.MergedSHA, "MergedSHA should be populated on success")

	// Integration HEAD on the bare repo should match MergedSHA.
	gotSHA, gitErr := testutil.RunGit(
		[]string{"rev-parse", scn.Integration}, scn.BareRepo)
	require.NoError(t, gitErr)
	require.Equal(t, gotSHA, res.MergedSHA, "integration HEAD should equal reported SHA")
	subject, gitErr := testutil.RunGit([]string{"log", "-1", "--format=%s", scn.Integration}, scn.BareRepo)
	require.NoError(t, gitErr)
	require.Equal(t, "orchestrator: integrate accepted scoped change", subject)

	// Feature branch should be gone from the bare repo.
	featureOut, _ := testutil.RunGit(
		[]string{"show-ref", "--verify", "refs/heads/" + scn.Feature}, scn.BareRepo)
	require.Empty(t, featureOut, "feature branch should be deleted; got %s", featureOut)

	// Registry transitions.
	require.Len(t, reg.MergedCalls(), 1, "MarkMerged should be called exactly once")
	require.Len(t, reg.DeletedCalls(), 1, "MarkDeleted should be called exactly once")
	require.Equal(t, scn.Feature, reg.MergedCalls()[0].Branch)
	require.Equal(t, scn.Feature, reg.DeletedCalls()[0].Branch)

	// Reporter was invoked exactly once with the success payload.
	require.Len(t, rep.Calls(), 1, "Reporter should fire exactly once")
	require.True(t, rep.Calls()[0].Result.Success)
	require.Equal(t, "drem", rep.Calls()[0].Project)
	require.Equal(t, "task-1", rep.Calls()[0].TaskID)
}

func TestMerger_RejectsDriftFromAuthorizedSHAsBeforeMutation(t *testing.T) {
	t.Run("base", func(t *testing.T) {
		scn := setupFeatureScenario(t, "hello\n", "feature.txt")
		m := &merger.Merger{WorkDir: scn.WorkDir, BareRepo: scn.BareRepo, TestCmd: "true"}
		res, err := m.Merge(context.Background(), merger.MergeRequest{
			FeatureBranch: scn.Feature, IntegrationBranch: scn.Integration,
			Project: "drem", TaskID: "base-drift",
			ExpectedFeatureSHA: scn.FeatureSHA, ExpectedBaseSHA: strings.Repeat("f", 40),
		})
		require.ErrorContains(t, err, "integration base drift")
		require.Equal(t, "stale_evidence", res.FailureReason)
		got, gitErr := testutil.RunGit([]string{"rev-parse", scn.Integration}, scn.BareRepo)
		require.NoError(t, gitErr)
		require.Equal(t, scn.BaseSHA, got)
	})

	t.Run("feature", func(t *testing.T) {
		scn := setupFeatureScenario(t, "hello\n", "feature.txt")
		m := &merger.Merger{WorkDir: scn.WorkDir, BareRepo: scn.BareRepo, TestCmd: "true"}
		res, err := m.Merge(context.Background(), merger.MergeRequest{
			FeatureBranch: scn.Feature, IntegrationBranch: scn.Integration,
			Project: "drem", TaskID: "feature-drift",
			ExpectedFeatureSHA: strings.Repeat("f", 40), ExpectedBaseSHA: scn.BaseSHA,
		})
		require.ErrorContains(t, err, "feature commit drift")
		require.Equal(t, "stale_evidence", res.FailureReason)
		got, gitErr := testutil.RunGit([]string{"rev-parse", scn.Integration}, scn.BareRepo)
		require.NoError(t, gitErr)
		require.Equal(t, scn.BaseSHA, got)
	})
}

func TestMerger_MergeConflict_ReportsWithoutDeleting(t *testing.T) {
	// Both branches modify the same file (README.md, which SetupBareRepo
	// creates on the integration branch).
	bareRepo := testutil.SetupBareRepo(t)
	integration, err := testutil.RunGit(
		[]string{"symbolic-ref", "--short", "HEAD"}, bareRepo)
	require.NoError(t, err)

	// Produce a diverging commit on integration.
	intPrep := filepath.Join(t.TempDir(), "int")
	_, err = testutil.RunGit([]string{"clone", bareRepo, intPrep}, "")
	require.NoError(t, err)
	_, _ = testutil.RunGit([]string{"config", "user.email", "t@t.com"}, intPrep)
	_, _ = testutil.RunGit([]string{"config", "user.name", "T"}, intPrep)
	testutil.CommitFile(t, intPrep, "README.md", "# integration-line\n", "integration edit")
	_, err = testutil.RunGit([]string{"push", "origin", integration}, intPrep)
	require.NoError(t, err)

	// Feature branch also edits README.md at the same spot, diverging from
	// the pre-integration-edit base.
	featPrep := filepath.Join(t.TempDir(), "feat")
	_, err = testutil.RunGit(
		[]string{"clone", "--branch", integration, bareRepo, featPrep}, "")
	require.NoError(t, err)
	_, _ = testutil.RunGit([]string{"config", "user.email", "t@t.com"}, featPrep)
	_, _ = testutil.RunGit([]string{"config", "user.name", "T"}, featPrep)
	// Rewind to before the integration-edit so the feature has a conflicting
	// change against the now-advanced integration tip.
	_, err = testutil.RunGit([]string{"reset", "--hard", "HEAD~1"}, featPrep)
	require.NoError(t, err)

	feature := "feature/conflict"
	_, err = testutil.RunGit([]string{"checkout", "-b", feature}, featPrep)
	require.NoError(t, err)
	testutil.CommitFile(t, featPrep, "README.md", "# feature-line\n", "feature edit")
	_, err = testutil.RunGit([]string{"push", "origin", feature}, featPrep)
	require.NoError(t, err)

	rep := &fakeReporter{}
	reg := &fakeRegistry{}

	m := &merger.Merger{
		WorkDir:  filepath.Join(t.TempDir(), "work"),
		BareRepo: bareRepo,
		TestCmd:  "sh -c 'exit 0'",
		Reporter: rep,
		Registry: reg,
	}

	res, err := m.Merge(context.Background(), merger.MergeRequest{
		FeatureBranch:     feature,
		IntegrationBranch: integration,
		Project:           "drem",
		TaskID:            "task-conflict",
	})
	require.NoError(t, err, "a file-level conflict should not be a hard error")
	require.NotNil(t, res)
	require.False(t, res.Success, "conflict must not be treated as success")
	require.Equal(t, "conflict", res.FailureReason)
	require.Contains(t, res.Conflicts, "README.md")

	// Feature branch must still exist on the bare repo.
	out, gitErr := testutil.RunGit(
		[]string{"show-ref", "--verify", "refs/heads/" + feature}, bareRepo)
	require.NoError(t, gitErr, "feature branch should still exist")
	require.NotEmpty(t, out)

	// No registry bookkeeping should have happened on a conflict.
	require.Empty(t, reg.MergedCalls(), "MarkMerged should NOT be called on conflict")
	require.Empty(t, reg.DeletedCalls(), "MarkDeleted should NOT be called on conflict")

	// Reporter should still fire exactly once with failure.
	require.Len(t, rep.Calls(), 1)
	require.False(t, rep.Calls()[0].Result.Success)
}

func TestMerger_TestsFail_DoesNotPushOrDelete(t *testing.T) {
	// Clean merge — but tests fail. Integration should NOT be pushed and
	// the feature branch should NOT be deleted. We chose the "tests run
	// AFTER the merge commit, workspace is disposable so no rollback is
	// needed" semantics: the stale merge commit only exists in the
	// container workspace and is thrown away with the directory.
	scn := setupFeatureScenario(t, "hello\n", "feature.txt")

	// Record the integration HEAD before the merge attempt for later
	// comparison.
	preSHA, err := testutil.RunGit(
		[]string{"rev-parse", scn.Integration}, scn.BareRepo)
	require.NoError(t, err)

	rep := &fakeReporter{}
	reg := &fakeRegistry{}

	m := &merger.Merger{
		WorkDir:  scn.WorkDir,
		BareRepo: scn.BareRepo,
		TestCmd:  "sh -c 'exit 1'",
		Reporter: rep,
		Registry: reg,
	}

	res, err := m.Merge(context.Background(), merger.MergeRequest{
		FeatureBranch:     scn.Feature,
		IntegrationBranch: scn.Integration,
		Project:           "drem",
		TaskID:            "task-testfail",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.Success)
	require.False(t, res.TestsPassed)
	require.Equal(t, "tests_failed", res.FailureReason)
	require.Empty(t, res.Conflicts)

	// Integration HEAD should NOT have moved.
	postSHA, err := testutil.RunGit(
		[]string{"rev-parse", scn.Integration}, scn.BareRepo)
	require.NoError(t, err)
	require.Equal(t, preSHA, postSHA, "integration must not be pushed on test failure")

	// Feature branch must still exist.
	out, _ := testutil.RunGit(
		[]string{"show-ref", "--verify", "refs/heads/" + scn.Feature}, scn.BareRepo)
	require.NotEmpty(t, out, "feature branch should still exist on test failure")

	require.Empty(t, reg.MergedCalls())
	require.Empty(t, reg.DeletedCalls())

	require.Len(t, rep.Calls(), 1)
	require.False(t, rep.Calls()[0].Result.Success)
}

func TestMerger_Idempotent_AlreadyMerged(t *testing.T) {
	scn := setupFeatureScenario(t, "hello\n", "feature.txt")

	rep := &fakeReporter{}
	reg := &fakeRegistry{}
	m := &merger.Merger{
		WorkDir:  scn.WorkDir,
		BareRepo: scn.BareRepo,
		TestCmd:  "sh -c 'exit 0'",
		Reporter: rep,
		Registry: reg,
	}

	// First invocation succeeds and deletes the feature branch.
	res1, err := m.Merge(context.Background(), merger.MergeRequest{
		FeatureBranch:     scn.Feature,
		IntegrationBranch: scn.Integration,
		Project:           "drem",
		TaskID:            "task-idem",
	})
	require.NoError(t, err)
	require.True(t, res1.Success)

	// Second invocation: the feature branch is gone from origin. We expect
	// Success=true, no test re-run, and no additional registry calls.
	res2, err := m.Merge(context.Background(), merger.MergeRequest{
		FeatureBranch:     scn.Feature,
		IntegrationBranch: scn.Integration,
		Project:           "drem",
		TaskID:            "task-idem",
	})
	require.NoError(t, err)
	require.NotNil(t, res2)
	require.True(t, res2.Success, "second invocation should short-circuit to success")
	require.Empty(t, res2.Conflicts)
	require.NotEmpty(t, res2.MergedSHA)
	// TestOutput is a no-op marker on the short-circuit path; make sure we
	// did NOT execute the test command again (which would have produced an
	// empty string on success, but the short-circuit path writes a marker).
	// The important assertion is simply that registry was not re-invoked.
	require.Len(t, reg.MergedCalls(), 1, "MarkMerged should only fire on the first merge")
	require.Len(t, reg.DeletedCalls(), 1)

	// Reporter fires once per invocation, so after two calls we expect two
	// reporter entries both with Success=true.
	require.Len(t, rep.Calls(), 2)
	require.True(t, rep.Calls()[0].Result.Success)
	require.True(t, rep.Calls()[1].Result.Success)
}

func TestMerger_ValidatesRequest(t *testing.T) {
	rep := &fakeReporter{}
	m := &merger.Merger{
		WorkDir:  t.TempDir(),
		BareRepo: t.TempDir(),
		Reporter: rep,
		Registry: merger.NoopRegistry{},
	}
	_, err := m.Merge(context.Background(), merger.MergeRequest{})
	require.Error(t, err)
	// Reporter should still fire on a validation error so callers get a
	// record of the attempt.
	require.Len(t, rep.Calls(), 1)
	require.False(t, rep.Calls()[0].Result.Success)
}

// TestMerger_RunsBeforeDeadline ensures a quick merge completes well within
// a generous bound. It is a regression guard against accidental hangs in the
// default code paths.
func TestMerger_RunsBeforeDeadline(t *testing.T) {
	scn := setupFeatureScenario(t, "hello\n", "feature.txt")
	m := &merger.Merger{
		WorkDir:  scn.WorkDir,
		BareRepo: scn.BareRepo,
		TestCmd:  "sh -c 'exit 0'",
		Reporter: merger.NoopReporter{},
		Registry: merger.NoopRegistry{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.Merge(ctx, merger.MergeRequest{
			FeatureBranch:     scn.Feature,
			IntegrationBranch: scn.Integration,
			Project:           "drem",
			TaskID:            "t",
		})
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("merger did not complete: %v", ctx.Err())
	}
}
