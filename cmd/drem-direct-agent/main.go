package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/agent"
)

const pushStderrTailBytes = 2048

type gitCommandError struct {
	Args     []string `json:"-"`
	ExitCode int      `json:"exit_code"`
	Output   string   `json:"-"`
	Stderr   string   `json:"-"`
}

func (e *gitCommandError) Error() string {
	return fmt.Sprintf("git %s: exit %d", strings.Join(e.Args, " "), e.ExitCode)
}

type pushDiagnostic struct {
	Kind       string `json:"kind"`
	Branch     string `json:"branch"`
	Remote     string `json:"remote"`
	Refspec    string `json:"refspec"`
	ExitCode   int    `json:"exit_code"`
	StderrTail string `json:"stderr_tail"`
	LocalSHA   string `json:"local_sha,omitempty"`
	RemoteSHA  string `json:"remote_sha,omitempty"`
}

func main() {
	role := flag.String("role", envDefault("DREM_AGENT", "coder"), "agent role")
	promptPath := flag.String("prompt", os.Getenv("DREM_PROMPT_PATH"), "prompt file path")
	workDir := flag.String("workdir", envDefault("DREM_WORKDIR", "."), "workspace directory")
	flag.Parse()

	if strings.TrimSpace(*promptPath) == "" {
		log.Fatal("--prompt or DREM_PROMPT_PATH is required")
	}
	promptBytes, err := os.ReadFile(*promptPath)
	if err != nil {
		log.Fatalf("read prompt: %v", err)
	}

	cfg := agent.DefaultDirectToolAgentConfig()
	cfg.Endpoint = envDefault("DREM_DIRECT_ENDPOINT", cfg.Endpoint)
	cfg.Model = envDefault("DREM_MODEL", cfg.Model)
	cfg.WorkDir = *workDir
	cfg.MaxTokens = envInt("DREM_DIRECT_MAX_TOKENS", cfg.MaxTokens)
	cfg.MaxIterations = envInt("DREM_DIRECT_MAX_ITERATIONS", cfg.MaxIterations)
	cfg.MaxCumulativeInputTokens = envInt("DREM_DIRECT_MAX_CUMULATIVE_INPUT_TOKENS", cfg.MaxCumulativeInputTokens)
	cfg.Temperature = envFloat("DREM_DIRECT_TEMPERATURE", cfg.Temperature)
	cfg.Timeout = envDuration("DREM_DIRECT_TIMEOUT", cfg.Timeout)
	cfg.BashTimeout = envDuration("DREM_DIRECT_BASH_TIMEOUT", cfg.BashTimeout)
	cfg.ContextLimit = envInt("DREM_DIRECT_CONTEXT_LIMIT", cfg.ContextLimit)
	cfg.ContextWarnPct = envInt("DREM_DIRECT_CONTEXT_WARN_PCT", cfg.ContextWarnPct)
	cfg.ContextStopPct = envInt("DREM_DIRECT_CONTEXT_STOP_PCT", cfg.ContextStopPct)
	cfg.ChatTemplateKwargs = envJSONMap("DREM_DIRECT_CHAT_TEMPLATE_KWARGS")
	cfg.ToolArgumentsFormat = envDefault("DREM_DIRECT_TOOL_ARGUMENTS_FORMAT", cfg.ToolArgumentsFormat)
	cfg.ToolHistoryMode = envDefault("DREM_DIRECT_TOOL_HISTORY_MODE", cfg.ToolHistoryMode)
	cfg.ToolHistoryKeepRecentExchanges = envInt("DREM_DIRECT_TOOL_HISTORY_KEEP_RECENT", cfg.ToolHistoryKeepRecentExchanges)
	cfg.ToolHistoryRetentionPct = envInt("DREM_DIRECT_TOOL_HISTORY_RETENTION_PCT", cfg.ToolHistoryRetentionPct)
	cfg.GQCaller = envDefault("DREM_GQ_CALLER", *role)
	cfg.GQPriority = strings.TrimSpace(os.Getenv("DREM_GQ_PRIORITY"))
	cfg.JournalPath = strings.TrimSpace(os.Getenv("DREM_DIRECT_JOURNAL_PATH"))
	cfg.RequireJournalResume = strings.EqualFold(strings.TrimSpace(os.Getenv("DREM_DIRECT_REQUIRE_JOURNAL_RESUME")), "true")
	cfg.AllowReadOnlyCompletion = strings.EqualFold(strings.TrimSpace(os.Getenv("DREM_DIRECT_ALLOW_READ_ONLY_COMPLETION")), "true")
	cfg.ProtectExistingFiles = strings.EqualFold(strings.TrimSpace(os.Getenv("DREM_DIRECT_PROTECT_EXISTING_FILES")), "true")

	traceFile, err := openTrace()
	if err != nil {
		log.Printf("trace disabled: %v", err)
	} else if traceFile != nil {
		defer traceFile.Close()
		cfg.TraceWriter = traceFile
	}

	systemPrompt := systemPromptForRole(*role)
	startSHA := gitValue(*workDir, "rev-parse", "HEAD")
	scopedFiles := envJSONStrings("DREM_SCOPED_FILES_JSON")
	cfg.ScopedFiles = scopedFiles
	cfg.RequiredMutationFiles = envJSONStrings("DREM_REQUIRED_MUTATION_FILES_JSON")
	defaultReadBudget := 0
	defaultToolBudget := 0
	defaultPreMutationInputBudget := 0
	if (*role == "coder" || *role == "fixer") && len(scopedFiles) > 0 {
		defaultReadBudget = 2
		defaultToolBudget = 12
		defaultPreMutationInputBudget = 20_000
	}
	cfg.MaxReadsBeforeMutation = envInt("DREM_DIRECT_MAX_READS_BEFORE_MUTATION", defaultReadBudget)
	cfg.MaxToolCalls = envInt("DREM_DIRECT_MAX_TOOL_CALLS", defaultToolBudget)
	cfg.MaxInputTokensBeforeMutation = envInt("DREM_DIRECT_MAX_INPUT_TOKENS_BEFORE_MUTATION", defaultPreMutationInputBudget)
	cfg.OnIteration = func(iteration, tokensIn, tokensOut, contextPct int) {
		_, _ = fmt.Fprintf(os.Stderr, "drem-direct-agent-progress: iteration=%d tokens_in=%d tokens_out=%d context_pct=%d\n",
			iteration+1, tokensIn, tokensOut, contextPct)
	}
	result, runErr := agent.RunDirectToolAgent(cfg, systemPrompt, string(promptBytes), agent.ToolsForRoleScope(*role, scopedFiles), "")
	if result != nil {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", strings.TrimSpace(result.Output))
		_, _ = fmt.Fprintf(os.Stderr, "drem-direct-agent: iterations=%d tokens_in=%d tokens_out=%d peak_request_input=%d resumed_turns=%d folded_bytes=%d duration=%s stop_reason=%s\n",
			result.Iterations, result.TokensIn, result.TokensOut, result.PeakRequestInput, result.ResumedTurns, result.FoldedBytes, result.Duration, result.StopReason)
	}
	if runErr != nil {
		if result != nil && boundedStopWithWork(*workDir, startSHA, result) {
			log.Printf("direct tool agent stopped at %s with repository changes; preserving work for deterministic gates: %v", result.StopReason, runErr)
			if err := finalizeGit(*workDir); err != nil {
				log.Fatalf("finalize bounded direct-agent work: %v", err)
			}
			return
		}
		log.Fatalf("direct tool agent failed: %v", runErr)
	}
	if err := finalizeGit(*workDir); err != nil {
		log.Fatalf("finalize git work: %v", err)
	}
}

// boundedStopWithWork preserves complete response checkpoints. The direct
// loop applies the entire threshold-crossing tool batch before returning a
// token/context stop, so those changes can safely proceed to deterministic
// gates. Truncated and no-progress turns remain failed attempts.
func boundedStopWithWork(workDir, startSHA string, result *agent.DirectToolAgentResult) bool {
	if result == nil || len(result.PendingMutationRepairs) > 0 || len(result.MissingRequiredMutations) > 0 {
		return false
	}
	switch result.StopReason {
	case agent.DirectToolStopReasonMaxIterations, agent.DirectToolStopReasonContextLimit,
		agent.DirectToolStopReasonTokenBudget, agent.DirectToolStopReasonToolBudget:
	default:
		return false
	}
	if _, err := os.Stat(filepath.Join(workDir, ".git")); err != nil {
		return false
	}
	out, err := git(workDir, "status", "--porcelain")
	if err != nil {
		return false
	}
	if strings.TrimSpace(out) != "" {
		return true
	}
	// Agents are instructed to commit before finishing. A bounded stop after
	// that commit leaves a clean tree, so compare with the SHA captured before
	// inference instead of discarding the completed artifact and retrying the
	// same work. finalizeGit will push the commit if the model did not.
	return startSHA != "" && gitValue(workDir, "rev-parse", "HEAD") != startSHA
}

func systemPromptForRole(role string) string {
	switch role {
	case "reviewer":
		return "You are a code reviewer. Inspect the repository, report concrete findings, and avoid modifying files."
	case "fixer":
		return "You are a fixer agent. Make the smallest correct code changes requested by the prompt and verify them when possible."
	default:
		return "You are a coder agent. Make the smallest correct code changes requested by the prompt. Task-specific instructions override any generic checklist in the prompt. If the task is a tiny metadata/artifact change or explicitly says not to test, do not run tests; write the requested file, commit it, and finish."
	}
}

func openTrace() (io.WriteCloser, error) {
	agentID := envDefault("DREM_AGENT_ID", "direct")
	shortID := agentID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	// Traces are runtime evidence, not source artifacts. Keeping them outside
	// the checkout prevents the watchdog or finalizeGit from committing an
	// observability file when an agent exits early.
	traceDir := envDefault("DREM_TRACE_DIR", filepath.Join(os.TempDir(), "drem-agent-traces"))
	if err := os.MkdirAll(traceDir, 0o700); err != nil {
		return nil, fmt.Errorf("create trace directory: %w", err)
	}
	path := filepath.Join(traceDir, "agent-trace-"+shortID+".jsonl")
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
}

func finalizeGit(workDir string) error {
	branch := strings.TrimSpace(os.Getenv("DREM_BRANCH"))
	if branch == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(workDir, ".git")); err != nil {
		return nil
	}
	if out, err := git(workDir, "status", "--porcelain"); err != nil {
		return fmt.Errorf("git status: %w: %s", err, out)
	} else if strings.TrimSpace(out) != "" {
		if out, err := git(workDir, "add", "-A"); err != nil {
			return fmt.Errorf("git add: %w: %s", err, out)
		}
		if out, err := git(workDir, "commit", "-m", "Commit direct agent changes"); err != nil {
			return fmt.Errorf("git commit: %w: %s", err, out)
		}
	}
	remote := "origin"
	refspec := "HEAD:" + branch
	if out, err := git(workDir, "push", remote, refspec); err != nil {
		diag := buildPushDiagnostic(workDir, remote, branch, refspec, err, out)
		writePushDiagnostic(workDir, diag)
		if diag.Kind == "duplicate" {
			log.Printf("git push duplicate: branch=%s remote=%s local_sha=%s remote_sha=%s", branch, remote, diag.LocalSHA, diag.RemoteSHA)
			return nil
		}
		return fmt.Errorf("git push: %w: %s", err, out)
	}
	return nil
}

func git(workDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.String() + stderr.String()
	if err != nil {
		var exitErr *exec.ExitError
		exitCode := -1
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		return out, &gitCommandError{Args: args, ExitCode: exitCode, Output: out, Stderr: stderr.String()}
	}
	return out, nil
}

func buildPushDiagnostic(workDir, remote, branch, refspec string, pushErr error, pushOutput string) pushDiagnostic {
	diag := pushDiagnostic{
		Kind:       "unknown",
		Branch:     branch,
		Remote:     remote,
		Refspec:    refspec,
		ExitCode:   -1,
		StderrTail: tailString(pushOutput, pushStderrTailBytes),
	}
	var gitErr *gitCommandError
	if errors.As(pushErr, &gitErr) {
		diag.ExitCode = gitErr.ExitCode
		diag.StderrTail = tailString(gitErr.Stderr, pushStderrTailBytes)
	}
	diag.LocalSHA = gitValue(workDir, "rev-parse", "HEAD")
	diag.RemoteSHA = gitValue(workDir, "ls-remote", remote, "refs/heads/"+branch)
	if fields := strings.Fields(diag.RemoteSHA); len(fields) > 0 {
		diag.RemoteSHA = fields[0]
	}
	diag.Kind = classifyPushFailure(diag.LocalSHA, diag.RemoteSHA, diag.StderrTail)
	return diag
}

func classifyPushFailure(localSHA, remoteSHA, stderr string) string {
	if localSHA != "" && remoteSHA != "" && localSHA == remoteSHA {
		return "duplicate"
	}
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "fetch first") ||
		strings.Contains(lower, "non-fast-forward") ||
		strings.Contains(lower, "stale info") ||
		strings.Contains(lower, "failed to push some refs") {
		return "stale_ref_or_race"
	}
	return "unknown"
}

func gitValue(workDir string, args ...string) string {
	out, err := git(workDir, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func writePushDiagnostic(workDir string, diag pushDiagnostic) {
	data, err := json.Marshal(diag)
	if err != nil {
		log.Printf("git push diagnostic marshal: %v", err)
		return
	}
	path := filepath.Join(workDir, "agent-push-diagnostic.json")
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		log.Printf("git push diagnostic write: %v", err)
	}
	log.Printf("git push diagnostic: %s", data)
}

func tailString(s string, maxBytes int) string {
	trimmed := strings.TrimSpace(s)
	if maxBytes <= 0 || len(trimmed) <= maxBytes {
		return trimmed
	}
	return string(bytes.TrimSpace([]byte(trimmed[len(trimmed)-maxBytes:])))
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("invalid %s=%q, using %d", key, value, fallback)
		return fallback
	}
	return parsed
}

func envJSONStrings(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		log.Printf("invalid %s JSON; preserving unscoped tool set: %v", key, err)
		return nil
	}
	result := values[:0]
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func envFloat(key string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		log.Printf("invalid %s=%q, using %g", key, value, fallback)
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		log.Printf("invalid %s=%q, using %s", key, value, fallback)
		return fallback
	}
	return parsed
}

func envJSONMap(key string) map[string]any {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		log.Printf("invalid %s JSON: %v", key, err)
		return nil
	}
	return parsed
}
