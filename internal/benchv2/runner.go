package benchv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/agent"
)

type TrialRequest struct {
	Task             TaskSpec
	WorkDir          string
	Seed             int64
	Temperature      float64
	TopP             float64
	TopK             int
	ContextWindow    int
	PreserveThinking bool
	Harness          HarnessConfig
	Runtime          RuntimeAttestation
}

type HarnessRun struct {
	Output      string
	StopReason  string
	Telemetry   Telemetry
	Trace       []agent.TraceEvent
	ServerUsage ServerUsage
	Trajectory  ATIFTrajectory
}

// HarnessAdapter keeps fixtures, hidden oracles, scoring, and result schemas
// independent of one execution layer. DirectToolAdapter is the first adapter;
// third-party harnesses can implement this interface without changing cases.
type HarnessAdapter interface {
	Name() string
	Run(context.Context, TrialRequest) (HarnessRun, error)
}

type DirectToolAdapter struct {
	Endpoint string
}

func (DirectToolAdapter) Name() string { return "direct_tool_agent" }

func (adapter DirectToolAdapter) Run(ctx context.Context, req TrialRequest) (HarnessRun, error) {
	_ = ctx
	if req.Harness.ToolPolicy != ToolPolicyStructured || !taskAllowsToolPolicy(req.Task, ToolPolicyStructured) {
		return HarnessRun{}, fmt.Errorf("direct tool adapter requires an allowed %s tool policy", ToolPolicyStructured)
	}
	var trace bytes.Buffer
	writeScope := append([]string(nil), req.Task.WritePaths...)
	if req.Task.ResultArtifact != "" {
		writeScope = append(writeScope, req.Task.ResultArtifact)
	}
	readScope := append([]string(nil), req.Task.ReadPaths...)
	readScope = append(readScope, req.Task.WritePaths...)
	cfg := agent.DefaultDirectToolAgentConfig()
	cfg.Endpoint = adapter.Endpoint
	cfg.Model = req.Runtime.ModelID
	cfg.Temperature = req.Temperature
	cfg.TopP = req.TopP
	cfg.TopK = req.TopK
	cfg.Seed = &req.Seed
	cfg.MaxCumulativeInputTokens = req.Task.Budget.MaxInputTokens
	cfg.MaxTokens = req.Task.Budget.MaxOutputTokens
	cfg.MaxToolCalls = req.Task.Budget.MaxToolCalls
	cfg.MaxIterations = req.Task.Budget.MaxIterations
	cfg.Timeout = time.Duration(req.Task.Budget.TimeoutSeconds) * time.Second
	cfg.WorkDir = req.WorkDir
	cfg.TraceWriter = &trace
	cfg.ContextLimit = req.ContextWindow
	cfg.ChatTemplateKwargs = map[string]any{"preserve_thinking": req.PreserveThinking}
	cfg.ScopedFiles = writeScope
	cfg.ReadableFiles = readScope
	cfg.DisableBash = true
	cfg.AllowReadOnlyCompletion = !req.Task.RequiredMutation
	cfg.ToolHistoryMode = req.Harness.HistoryMode
	cfg.ToolHistoryKeepRecentExchanges = req.Harness.KeepRecentExchanges
	cfg.ToolHistoryRetentionPct = req.Harness.RetentionThresholdPC
	tools := agent.ToolsForBenchmark(req.Task.Role, writeScope, false)
	outputPath := ""
	if req.Task.ResultArtifact != "" {
		outputPath = filepath.Join(req.WorkDir, req.Task.ResultArtifact)
	}
	result, runErr := agent.RunDirectToolAgent(cfg, req.Task.SystemPrompt, req.Task.UserMessage, tools, outputPath)
	var run HarnessRun
	if result != nil {
		run.Output = result.Output
		run.StopReason = result.StopReason
		run.Telemetry = Telemetry{
			TokensIn: result.TokensIn, TokensOut: result.TokensOut, Iterations: result.Iterations,
			FoldedBytes: result.FoldedBytes, PeakRequestInput: result.PeakRequestInput,
			PeakContextPct: result.FinalContextPct, UniqueReads: result.UniqueReads,
			RedundantReads: result.RedundantReads, DeniedCalls: result.DeniedCalls,
			FirstMutationIteration: result.FirstMutationIteration, FirstMutationMs: result.FirstMutationMs,
			DurationMs: result.Duration.Milliseconds(), MutationObserved: result.MutationObserved,
			CheckpointObserved: result.MutationObserved,
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(trace.String()), "\n") {
		if line == "" {
			continue
		}
		var event agent.TraceEvent
		if json.Unmarshal([]byte(line), &event) == nil {
			run.Trace = append(run.Trace, event)
			run.Telemetry.ToolCalls += len(event.ToolCalls)
		}
	}
	run.ServerUsage = ServerUsage{
		Source: "server_response", RequestsMeasured: len(run.Trace), RequestsTotal: len(run.Trace),
		PromptTokens: run.Telemetry.TokensIn, CompletionTokens: run.Telemetry.TokensOut, Complete: len(run.Trace) > 0,
	}
	run.Trajectory = NormalizeDirectTrace(req.Task.ID, req.Harness, req.Runtime, run.Trace, run.Telemetry)
	return run, runErr
}

type VerifyOutcome struct {
	Passed          bool
	Compiled        bool
	Failures        []string
	ReleaseArtifact *ArtifactEvidence
}

type HostVerifier interface {
	Verify(context.Context, TaskSpec, string, HarnessRun) VerifyOutcome
}

type Runner struct {
	Repo        string
	Repos       map[string]string
	ScratchRoot string
	Adapter     HarnessAdapter
	Verifier    HostVerifier
}

func (runner Runner) RunTrial(ctx context.Context, matrix MatrixSpec, task TaskSpec, trial int) TrialResult {
	result := TrialResult{
		Schema: ResultSchemaVersion, RunID: fmt.Sprintf("%s-%s-%d", matrix.ID, task.ID, trial),
		MatrixID: matrix.ID, TaskID: task.ID, Trial: trial, Harness: matrix.Harness,
		Runtime: matrix.Runtime, Fixture: task.Fixture, Status: "failed",
		Gates: Gates{
			Attested:         ValidateAttestation(matrix.Harness, matrix.Runtime) == nil,
			ArtifactAttested: task.ReleaseArtifactPath == "",
		},
	}
	if trial < 1 || trial > len(matrix.Seeds) {
		result.Error = "trial has no fixed seed"
		return result
	}
	result.Seed = matrix.Seeds[trial-1]
	if task.Status != "runnable" {
		result.Status = "non_runnable"
		result.Error = "hidden canonical artifact is not supplied"
		result.Gates.Failures = []string{result.Error}
		return result
	}
	if !result.Gates.Attested {
		result.Error = "required attestation missing or inference unmeasured"
		result.Gates.Failures = []string{result.Error}
		return result
	}
	repo := runner.Repo
	if candidate := runner.Repos[task.Fixture.RepoID]; candidate != "" {
		repo = candidate
	}
	prepared, err := PrepareFixture(repo, runner.ScratchRoot, task.Fixture)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer prepared.Cleanup()
	result.Gates.OracleIsolated = !pathContainsOracle(prepared.WorkDir)
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(task.Budget.TimeoutSeconds)*time.Second)
	defer cancel()
	run, runErr := runner.Adapter.Run(runCtx, TrialRequest{
		Task: task, WorkDir: prepared.WorkDir, Seed: result.Seed, Temperature: matrix.Temperature,
		TopP: matrix.TopP, TopK: matrix.TopK, ContextWindow: matrix.ContextWindow, PreserveThinking: matrix.PreserveThinking,
		Harness: matrix.Harness, Runtime: matrix.Runtime,
	})
	result.Telemetry = run.Telemetry
	result.ServerUsage = run.ServerUsage
	result.Trajectory = run.Trajectory
	result.StopReason = run.StopReason
	if runErr != nil {
		result.Error = runErr.Error()
	}
	paths, pathErr := ChangedPaths(prepared.WorkDir)
	if pathErr != nil {
		result.Gates.Failures = append(result.Gates.Failures, pathErr.Error())
	}
	result.Gates.ChangedPaths = paths
	result.Gates.ScopePassed = exactPathSet(paths, append(append([]string{}, task.WritePaths...), task.ResultArtifact))
	result.Gates.ReadScopePassed = run.Telemetry.DeniedCalls == 0
	result.Gates.RequiredMutationPassed = (!task.RequiredMutation || run.Telemetry.MutationObserved) && containsPaths(paths, task.RequiredChangedPaths)
	if runner.Verifier != nil {
		verified := runner.Verifier.Verify(ctx, task, prepared.WorkDir, run)
		result.Gates.VerifierPassed = verified.Passed
		result.Gates.Compiled = verified.Compiled
		result.Gates.Failures = append(result.Gates.Failures, verified.Failures...)
		if task.ReleaseArtifactPath != "" {
			if err := validateReleaseArtifact(task.ReleaseArtifactPath, verified.ReleaseArtifact); err != nil {
				result.Gates.Failures = append(result.Gates.Failures, err.Error())
			} else {
				result.Gates.ArtifactAttested = true
				result.ReleaseArtifact = verified.ReleaseArtifact
			}
		}
	}
	if task.InferencePolicy == "required" && (!result.ServerUsage.Complete || !trustedServerUsageSource(result.ServerUsage.Source)) {
		result.Gates.Attested = false
		result.Gates.Failures = append(result.Gates.Failures, "server-reported inference usage is incomplete")
	}
	result.Score = Score(task, &result)
	if result.Score >= 90 && allHardGates(result.Gates) && runErr == nil {
		result.Status = "passed"
	}
	return result
}

func trustedServerUsageSource(source string) bool {
	return source == "server_response" || source == ServerUsageSourceProxy
}

func pathContainsOracle(workDir string) bool {
	found := false
	_ = filepath.WalkDir(workDir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && strings.Contains(strings.ToLower(entry.Name()), "oracle") {
			found = true
		}
		return nil
	})
	return found
}

func exactPathSet(actual, allowed []string) bool {
	want := map[string]bool{}
	for _, path := range allowed {
		if path != "" {
			want[filepath.ToSlash(filepath.Clean(path))] = true
		}
	}
	for _, path := range actual {
		if !want[filepath.ToSlash(filepath.Clean(path))] {
			return false
		}
	}
	return true
}

func containsPaths(actual, required []string) bool {
	have := map[string]bool{}
	for _, path := range actual {
		have[filepath.ToSlash(filepath.Clean(path))] = true
	}
	for _, path := range required {
		if !have[filepath.ToSlash(filepath.Clean(path))] {
			return false
		}
	}
	return true
}

func allHardGates(g Gates) bool {
	return g.VerifierPassed && g.Compiled && g.ScopePassed && g.ReadScopePassed &&
		g.OracleIsolated && g.Attested && g.RequiredMutationPassed && g.ArtifactAttested
}

func validateReleaseArtifact(wantPath string, evidence *ArtifactEvidence) error {
	if evidence == nil {
		return fmt.Errorf("host verifier did not attest the required Release artifact")
	}
	digest, err := hex.DecodeString(evidence.SHA256)
	if evidence.Kind != "release_binary" || filepath.ToSlash(filepath.Clean(evidence.Path)) != filepath.ToSlash(filepath.Clean(wantPath)) ||
		err != nil || len(digest) != sha256.Size || evidence.SHA256 != strings.ToLower(evidence.SHA256) || evidence.SizeBytes <= 0 {
		return fmt.Errorf("host verifier returned invalid Release artifact evidence")
	}
	return nil
}

func SortedPaths(paths []string) []string {
	copyPaths := append([]string(nil), paths...)
	sort.Strings(copyPaths)
	return copyPaths
}

func taskAllowsToolPolicy(task TaskSpec, policy string) bool {
	for _, allowed := range task.AllowedToolPolicies {
		if allowed == policy {
			return true
		}
	}
	return false
}
