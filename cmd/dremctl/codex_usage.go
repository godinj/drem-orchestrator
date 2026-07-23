package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func handleCodexGoalUsage(ctx context.Context, client *orchclient.Client, cfg cliConfig, args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: dremctl codex-usage <task-id-prefix> --goal-objective TEXT --goal-status complete|blocked --tokens-used N --elapsed-ms N [--thread ID] [--idempotency-key KEY]")
	}
	taskID, err := resolveTaskUUID(ctx, client, cfg.project, args[0])
	if err != nil {
		return err
	}
	values := map[string]string{}
	allowed := map[string]bool{
		"--goal-objective": true, "--goal-status": true, "--tokens-used": true,
		"--elapsed-ms": true, "--thread": true, "--captured-at": true, "--idempotency-key": true,
	}
	for i := 1; i < len(args); i += 2 {
		if i+1 >= len(args) || !strings.HasPrefix(args[i], "--") {
			return errors.New("Codex goal usage flags require a value")
		}
		if !allowed[args[i]] {
			return fmt.Errorf("unknown Codex goal usage flag %s", args[i])
		}
		values[args[i]] = args[i+1]
	}
	tokens, err := strconv.ParseInt(values["--tokens-used"], 10, 64)
	if err != nil || tokens < 0 {
		return errors.New("--tokens-used must be a non-negative integer")
	}
	elapsed, err := strconv.ParseInt(values["--elapsed-ms"], 10, 64)
	if err != nil || elapsed <= 0 {
		return errors.New("--elapsed-ms must be a positive integer")
	}
	actor := strings.TrimSpace(cfg.actor)
	if !strings.HasPrefix(actor, "codex:") {
		return errors.New("Codex goal usage requires --actor codex:<thread-id> or CODEX_THREAD_ID")
	}
	threadID := strings.TrimSpace(values["--thread"])
	if threadID == "" {
		threadID = strings.TrimPrefix(actor, "codex:")
	}
	key := strings.TrimSpace(values["--idempotency-key"])
	if key == "" {
		key = "codex-goal-usage:" + taskID.String() + ":" + threadID
	}
	var capturedAt time.Time
	if raw := strings.TrimSpace(values["--captured-at"]); raw != "" {
		capturedAt, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return fmt.Errorf("--captured-at must be RFC3339: %w", err)
		}
	}
	req := orchdto.SubmitCodexGoalUsageRequest{
		Actor: actor, ThreadID: threadID, GoalObjective: values["--goal-objective"],
		GoalStatus: values["--goal-status"], TokensUsed: tokens, ElapsedMS: elapsed,
		UsageCapturedAt: capturedAt, IdempotencyKey: key,
	}
	usage, err := client.SubmitCodexGoalUsage(ctx, cfg.project, taskID, req)
	if err != nil {
		return err
	}
	if cfg.json {
		return writeJSON(stdout, usage)
	}
	fmt.Fprintf(stdout, "codex_usage_id=%s task_id=%s thread_id=%s goal_status=%s tokens_used=%d elapsed_ms=%d\n",
		usage.ID, usage.TaskID, usage.ThreadID, usage.GoalStatus, usage.TokensUsed, usage.ElapsedMS)
	return nil
}
