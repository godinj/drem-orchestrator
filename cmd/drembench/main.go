// drembench runs the DirectToolAgent against a set of synthetic coding tasks
// to measure completion rate, iteration counts, token usage, and verifier
// pass rate. It is intended to diagnose harness-level behavior (e.g. the
// iter-30 cap problem) without requiring the full orchestrator.
//
// Usage:
//
//	drembench -tasks bench/tasks -runs 5 -out bench/results/run.csv
//
// Each task spec is a JSON file describing the fixture to copy, the system
// prompt and user message, the role (for tool selection), and a verifier
// shell command that runs inside the scratch directory after the agent
// finishes.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/agent"
)

// TaskSpec is the on-disk description of a benchmark task.
type TaskSpec struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	FixtureDir   string `json:"fixture_dir"`
	Role         string `json:"role"`
	SystemPrompt string `json:"system_prompt"`
	UserMessage  string `json:"user_message"`
	VerifierCmd  string `json:"verifier_cmd"`
}

// TrialResult holds the measured outcome of one agent run.
type TrialResult struct {
	Task         string
	Trial        int
	Iterations   int
	TokensIn     int
	TokensOut    int
	DurationMs   int64
	FinishReason string // "stop" | "max_iter" | "api_error" | "other_error"
	ErrorMsg     string
	VerifierPass bool
	VerifierOut  string
}

func main() {
	var (
		tasksDir = flag.String("tasks", "bench/tasks", "directory of task spec JSON files")
		runs     = flag.Int("runs", 5, "trials per task")
		outCSV   = flag.String("out", "bench/results/run.csv", "output CSV path")
		endpoint = flag.String("endpoint", "http://localhost:8081/v1/chat/completions", "SGLang endpoint")
		model    = flag.String("model", "gemma4-26b", "model id")
		maxIter  = flag.Int("max-iter", 30, "max iterations per agent run")
		timeout  = flag.Duration("timeout", 180*time.Second, "API call timeout")
		bashTO   = flag.Duration("bash-timeout", 60*time.Second, "bash tool timeout")
		repoRoot = flag.String("repo", ".", "repo root (for fixture paths)")
		scratch  = flag.String("scratch", "bench/scratch", "scratch dir for runs")
		traceDir = flag.String("trace-dir", "", "if set, write per-trial JSON-lines trace files here")
	)
	flag.Parse()

	absRepo, err := filepath.Abs(*repoRoot)
	if err != nil {
		log.Fatalf("resolve repo: %v", err)
	}

	specs, err := loadTasks(filepath.Join(absRepo, *tasksDir))
	if err != nil {
		log.Fatalf("load tasks: %v", err)
	}
	if len(specs) == 0 {
		log.Fatalf("no task specs found in %s", *tasksDir)
	}
	fmt.Printf("Loaded %d task specs. Running %d trials each = %d total runs.\n", len(specs), *runs, len(specs)*(*runs))

	if err := os.MkdirAll(filepath.Join(absRepo, *scratch), 0o755); err != nil {
		log.Fatalf("mkdir scratch: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(absRepo, *outCSV)), 0o755); err != nil {
		log.Fatalf("mkdir out: %v", err)
	}

	var results []TrialResult
	for _, spec := range specs {
		for trial := 1; trial <= *runs; trial++ {
			fmt.Printf("\n=== %s trial %d/%d ===\n", spec.Name, trial, *runs)
			scratchDir := filepath.Join(absRepo, *scratch, fmt.Sprintf("%s-%d", spec.Name, trial))
			fixtureAbs := filepath.Join(absRepo, spec.FixtureDir)

			if err := resetScratch(scratchDir, fixtureAbs); err != nil {
				fmt.Printf("  reset scratch failed: %v\n", err)
				continue
			}

			cfg := agent.DirectToolAgentConfig{
				Endpoint:      *endpoint,
				Model:         *model,
				MaxTokens:     2048,
				Temperature:   0.1,
				Timeout:       *timeout,
				MaxIterations: *maxIter,
				WorkDir:       scratchDir,
				BashTimeout:   *bashTO,
			}

			var traceFile *os.File
			if *traceDir != "" {
				absTraceDir := filepath.Join(absRepo, *traceDir)
				if err := os.MkdirAll(absTraceDir, 0o755); err != nil {
					fmt.Printf("  mkdir trace dir: %v\n", err)
				} else {
					p := filepath.Join(absTraceDir, fmt.Sprintf("%s-%d.jsonl", spec.Name, trial))
					f, err := os.Create(p)
					if err != nil {
						fmt.Printf("  create trace: %v\n", err)
					} else {
						traceFile = f
						cfg.TraceWriter = f
					}
				}
			}

			tools := agent.ToolsForRole(spec.Role)

			start := time.Now()
			res, runErr := agent.RunDirectToolAgent(cfg, spec.SystemPrompt, spec.UserMessage, tools, "")
			runDur := time.Since(start)
			if traceFile != nil {
				traceFile.Close()
			}

			tr := TrialResult{
				Task:       spec.Name,
				Trial:      trial,
				DurationMs: runDur.Milliseconds(),
			}
			if res != nil {
				tr.Iterations = res.Iterations
				tr.TokensIn = res.TokensIn
				tr.TokensOut = res.TokensOut
			}
			tr.FinishReason = classifyFinish(runErr, tr.Iterations, *maxIter)
			if runErr != nil {
				tr.ErrorMsg = runErr.Error()
			}

			// Run verifier
			pass, vout := runVerifier(scratchDir, spec.VerifierCmd)
			tr.VerifierPass = pass
			tr.VerifierOut = truncate(vout, 400)

			fmt.Printf("  iters=%d tokens_in=%d tokens_out=%d dur=%s finish=%s verifier=%v\n",
				tr.Iterations, tr.TokensIn, tr.TokensOut, runDur.Round(time.Millisecond), tr.FinishReason, tr.VerifierPass)
			results = append(results, tr)
		}
	}

	if err := writeCSV(filepath.Join(absRepo, *outCSV), results); err != nil {
		log.Fatalf("write csv: %v", err)
	}
	fmt.Printf("\nWrote %d rows to %s\n", len(results), *outCSV)

	printSummary(results)

	sumPath := strings.TrimSuffix(filepath.Join(absRepo, *outCSV), ".csv") + ".summary.md"
	if err := writeSummary(sumPath, results, *model); err != nil {
		log.Printf("write summary: %v", err)
	} else {
		fmt.Printf("Summary: %s\n", sumPath)
	}
}

func loadTasks(dir string) ([]TaskSpec, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var specs []TaskSpec
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var s TaskSpec
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		specs = append(specs, s)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil
}

func resetScratch(dst, src string) error {
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("rm %s: %w", dst, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("cp", "-r", src, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp: %v: %s", err, out)
	}
	return nil
}

func classifyFinish(err error, iter, maxIter int) string {
	if err == nil {
		return "stop"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "exceeded max iterations"):
		return "max_iter"
	case strings.Contains(msg, "API call failed"), strings.Contains(msg, "API returned status"):
		return "api_error"
	default:
		return "other_error"
	}
}

func runVerifier(workDir, cmd string) (bool, string) {
	if cmd == "" {
		return false, "(no verifier)"
	}
	c := exec.Command("bash", "-c", cmd)
	c.Dir = workDir
	out, err := c.CombinedOutput()
	return err == nil, string(out)
}

func writeCSV(path string, rows []TrialResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{
		"task", "trial", "iterations", "tokens_in", "tokens_out",
		"duration_ms", "finish_reason", "verifier_pass", "error_msg",
	}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := w.Write([]string{
			r.Task,
			strconv.Itoa(r.Trial),
			strconv.Itoa(r.Iterations),
			strconv.Itoa(r.TokensIn),
			strconv.Itoa(r.TokensOut),
			strconv.FormatInt(r.DurationMs, 10),
			r.FinishReason,
			strconv.FormatBool(r.VerifierPass),
			truncate(r.ErrorMsg, 200),
		}); err != nil {
			return err
		}
	}
	return nil
}

func printSummary(rows []TrialResult) {
	type agg struct {
		trials, stop, maxIter, apiErr, otherErr, pass int
		sumIter, sumTokIn, sumTokOut, sumDurMs        int64
	}
	byTask := map[string]*agg{}
	for _, r := range rows {
		a, ok := byTask[r.Task]
		if !ok {
			a = &agg{}
			byTask[r.Task] = a
		}
		a.trials++
		switch r.FinishReason {
		case "stop":
			a.stop++
		case "max_iter":
			a.maxIter++
		case "api_error":
			a.apiErr++
		default:
			a.otherErr++
		}
		if r.VerifierPass {
			a.pass++
		}
		a.sumIter += int64(r.Iterations)
		a.sumTokIn += int64(r.TokensIn)
		a.sumTokOut += int64(r.TokensOut)
		a.sumDurMs += r.DurationMs
	}
	fmt.Println("\n=== SUMMARY ===")
	fmt.Printf("%-16s %6s %6s %8s %8s %10s %10s %10s %10s\n",
		"task", "trials", "pass", "stop", "max_iter", "avg_iter", "avg_tok_in", "avg_tok_out", "avg_dur")
	tasks := make([]string, 0, len(byTask))
	for k := range byTask {
		tasks = append(tasks, k)
	}
	sort.Strings(tasks)
	for _, k := range tasks {
		a := byTask[k]
		fmt.Printf("%-16s %6d %6d %8d %8d %10.1f %10.0f %10.0f %8dms\n",
			k, a.trials, a.pass, a.stop, a.maxIter,
			float64(a.sumIter)/float64(a.trials),
			float64(a.sumTokIn)/float64(a.trials),
			float64(a.sumTokOut)/float64(a.trials),
			a.sumDurMs/int64(a.trials),
		)
	}
}

func writeSummary(path string, rows []TrialResult, model string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# drembench run — %s\n\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "Model: `%s`\n\n", model)
	fmt.Fprintf(&b, "## Per-task aggregates\n\n")
	fmt.Fprintf(&b, "| task | trials | pass | stop | max_iter | api_err | avg_iter | avg_tok_in | avg_tok_out | avg_dur_ms |\n")
	fmt.Fprintf(&b, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	type agg struct {
		trials, stop, maxIter, apiErr, pass     int
		sumIter, sumTokIn, sumTokOut, sumDurMs  int64
	}
	byTask := map[string]*agg{}
	for _, r := range rows {
		a, ok := byTask[r.Task]
		if !ok {
			a = &agg{}
			byTask[r.Task] = a
		}
		a.trials++
		switch r.FinishReason {
		case "stop":
			a.stop++
		case "max_iter":
			a.maxIter++
		case "api_error":
			a.apiErr++
		}
		if r.VerifierPass {
			a.pass++
		}
		a.sumIter += int64(r.Iterations)
		a.sumTokIn += int64(r.TokensIn)
		a.sumTokOut += int64(r.TokensOut)
		a.sumDurMs += r.DurationMs
	}
	tasks := make([]string, 0, len(byTask))
	for k := range byTask {
		tasks = append(tasks, k)
	}
	sort.Strings(tasks)
	for _, k := range tasks {
		a := byTask[k]
		n := float64(a.trials)
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %.1f | %.0f | %.0f | %d |\n",
			k, a.trials, a.pass, a.stop, a.maxIter, a.apiErr,
			float64(a.sumIter)/n, float64(a.sumTokIn)/n, float64(a.sumTokOut)/n, a.sumDurMs/int64(a.trials))
	}

	fmt.Fprintf(&b, "\n## Per-trial detail\n\n")
	fmt.Fprintf(&b, "| task | trial | iters | tok_in | tok_out | dur_ms | finish | verified | err |\n")
	fmt.Fprintf(&b, "|---|---:|---:|---:|---:|---:|---|:-:|---|\n")
	for _, r := range rows {
		verify := "✗"
		if r.VerifierPass {
			verify = "✓"
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %s | %s | %s |\n",
			r.Task, r.Trial, r.Iterations, r.TokensIn, r.TokensOut,
			r.DurationMs, r.FinishReason, verify, truncate(r.ErrorMsg, 60))
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
