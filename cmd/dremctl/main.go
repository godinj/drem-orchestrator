package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

const usage = `usage: dremctl [--orch-url URL] [--project NAME] [--json] <command> [args]

HTTP-only C-Suite persona CLI. Defaults come from DREM_ORCH_URL and DREM_PROJECT.

Commands:
  projects
  tasks [--status STATUS] [--limit N] [--offset N]
  workers
  worker <worker-id>
  history <worker-id>
  events [--since RFC3339] [--limit N]
  logs --container NAME [--follow] [--since RFC3339]
  status
  approve <task-id-prefix>
  reject <task-id-prefix> [--reason TEXT]
  pass <task-id-prefix>
  fail <task-id-prefix>
  answer <task-id-prefix> --body TEXT
  retry <task-id-prefix>
`

const taskPageLimit = 500

type cliConfig struct {
	orchURL string
	project string
	json    bool
}

type envLookup func(string) string

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv envLookup, stdout, stderr io.Writer) error {
	cfg, rest, err := parseGlobalFlags(args, getenv)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("command is required")
	}
	if rest[0] == "help" || rest[0] == "--help" || rest[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return nil
	}
	if strings.TrimSpace(cfg.orchURL) == "" {
		return errors.New("DREM_ORCH_URL is required, or pass --orch-url")
	}

	client := orchclient.New(cfg.orchURL)
	command, commandArgs := rest[0], rest[1:]
	switch command {
	case "projects":
		projects, err := client.ListProjects(ctx)
		if err != nil {
			return err
		}
		return renderProjects(stdout, cfg.json, projects)
	case "tasks":
		if err := requireProject(cfg); err != nil {
			return err
		}
		return handleTasks(ctx, client, cfg, commandArgs, stdout)
	case "workers":
		if err := requireProject(cfg); err != nil {
			return err
		}
		workers, err := client.ListWorkers(ctx, cfg.project)
		if err != nil {
			return err
		}
		return renderWorkers(stdout, cfg.json, workers)
	case "worker":
		return handleWorker(ctx, client, cfg, commandArgs, stdout)
	case "history":
		return handleHistory(ctx, client, cfg, commandArgs, stdout)
	case "events":
		return handleEvents(ctx, client, cfg, commandArgs, stdout)
	case "logs":
		return handleLogs(ctx, client, commandArgs, stdout)
	case "status":
		if err := requireProject(cfg); err != nil {
			return err
		}
		return handleStatus(ctx, client, cfg, stdout)
	case "approve", "reject", "pass", "fail", "answer", "retry":
		if err := requireProject(cfg); err != nil {
			return err
		}
		return handleMutation(ctx, client, cfg, command, commandArgs, stdout)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func parseGlobalFlags(args []string, getenv envLookup) (cliConfig, []string, error) {
	cfg := cliConfig{
		orchURL: getenv("DREM_ORCH_URL"),
		project: getenv("DREM_PROJECT"),
	}
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			cfg.json = true
		case arg == "--orch-url":
			i++
			if i >= len(args) {
				return cfg, nil, errors.New("--orch-url requires a value")
			}
			cfg.orchURL = args[i]
		case strings.HasPrefix(arg, "--orch-url="):
			cfg.orchURL = strings.TrimPrefix(arg, "--orch-url=")
		case arg == "--project":
			i++
			if i >= len(args) {
				return cfg, nil, errors.New("--project requires a value")
			}
			cfg.project = args[i]
		case strings.HasPrefix(arg, "--project="):
			cfg.project = strings.TrimPrefix(arg, "--project=")
		default:
			rest = append(rest, arg)
		}
	}
	return cfg, rest, nil
}

func requireProject(cfg cliConfig) error {
	if strings.TrimSpace(cfg.project) == "" {
		return errors.New("DREM_PROJECT is required for this command, or pass --project")
	}
	return nil
}

func handleTasks(ctx context.Context, client *orchclient.Client, cfg cliConfig, args []string, stdout io.Writer) error {
	fs := newFlagSet("tasks")
	status := fs.String("status", "", "task status filter")
	limit := fs.Int("limit", 0, "result limit")
	offset := fs.Int("offset", 0, "result offset")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: dremctl tasks [--status STATUS] [--limit N] [--offset N]")
	}
	tasks, err := client.ListTasks(ctx, cfg.project, orchclient.TaskFilter{
		Status: *status,
		Limit:  *limit,
		Offset: *offset,
	})
	if err != nil {
		return err
	}
	return renderTasks(stdout, cfg.json, tasks)
}

func handleWorker(ctx context.Context, client *orchclient.Client, cfg cliConfig, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: dremctl worker <worker-id>")
	}
	worker, err := client.GetWorker(ctx, args[0])
	if err != nil {
		return err
	}
	return renderWorker(stdout, cfg.json, worker)
}

func handleHistory(ctx context.Context, client *orchclient.Client, cfg cliConfig, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: dremctl history <worker-id>")
	}
	history, err := client.WorkerHistory(ctx, args[0])
	if err != nil {
		return err
	}
	return renderHistory(stdout, cfg.json, history)
}

func handleEvents(ctx context.Context, client *orchclient.Client, cfg cliConfig, args []string, stdout io.Writer) error {
	fs := newFlagSet("events")
	sinceRaw := fs.String("since", "", "RFC3339 timestamp")
	limit := fs.Int("limit", 0, "result limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: dremctl events [--since RFC3339] [--limit N]")
	}
	since, err := parseSince(*sinceRaw)
	if err != nil {
		return err
	}
	events, err := client.Events(ctx, since, *limit)
	if err != nil {
		return err
	}
	return renderEvents(stdout, cfg.json, events)
}

func handleLogs(ctx context.Context, client *orchclient.Client, args []string, stdout io.Writer) error {
	fs := newFlagSet("logs")
	container := fs.String("container", "", "container name or ID")
	follow := fs.Bool("follow", false, "follow log output")
	sinceRaw := fs.String("since", "", "RFC3339 timestamp")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(*container) == "" {
		return errors.New("usage: dremctl logs --container NAME [--follow] [--since RFC3339]")
	}
	since, err := parseSince(*sinceRaw)
	if err != nil {
		return err
	}
	rc, err := client.StreamLogs(ctx, *container, since, *follow)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(stdout, rc)
	return err
}

type statusDTO struct {
	Projects        []orchdto.ProjectDTO `json:"projects"`
	TaskCount       int                  `json:"task_count"`
	TasksByStatus   map[string]int       `json:"tasks_by_status"`
	WorkerCount     int                  `json:"worker_count"`
	WorkersByStatus map[string]int       `json:"workers_by_status"`
	RecentEvents    []orchdto.EventDTO   `json:"recent_events"`
}

func handleStatus(ctx context.Context, client *orchclient.Client, cfg cliConfig, stdout io.Writer) error {
	projects, err := client.ListProjects(ctx)
	if err != nil {
		return err
	}
	tasks, err := listAllTasks(ctx, client, cfg.project)
	if err != nil {
		return err
	}
	workers, err := client.ListWorkers(ctx, cfg.project)
	if err != nil {
		return err
	}
	events, err := client.Events(ctx, time.Time{}, 10)
	if err != nil {
		return err
	}
	status := statusDTO{
		Projects:        projects,
		TaskCount:       len(tasks),
		TasksByStatus:   countTasksByStatus(tasks),
		WorkerCount:     len(workers),
		WorkersByStatus: countWorkersByStatus(workers),
		RecentEvents:    events,
	}
	if cfg.json {
		return writeJSON(stdout, status)
	}
	return renderStatus(stdout, status)
}

func listAllTasks(ctx context.Context, client *orchclient.Client, project string) ([]orchdto.TaskDTO, error) {
	var all []orchdto.TaskDTO
	for offset := 0; ; offset += taskPageLimit {
		tasks, err := client.ListTasks(ctx, project, orchclient.TaskFilter{
			Limit:  taskPageLimit,
			Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, tasks...)
		if len(tasks) < taskPageLimit {
			return all, nil
		}
	}
}

func handleMutation(ctx context.Context, client *orchclient.Client, cfg cliConfig, command string, args []string, stdout io.Writer) error {
	reason := ""
	body := ""
	var err error

	if command == "reject" {
		reason, args, err = parseStringOption(args, "reason")
		if err != nil {
			return err
		}
	}
	if command == "answer" {
		body, args, err = parseStringOption(args, "body")
		if err != nil {
			return err
		}
		if strings.TrimSpace(body) == "" {
			return errors.New("--body is required")
		}
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: dremctl %s", mutationUsage(command))
	}

	taskID, err := resolveTaskUUID(ctx, client, cfg.project, args[0])
	if err != nil {
		return err
	}

	var dto orchdto.TaskDTO
	switch command {
	case "approve":
		dto, err = client.Approve(ctx, cfg.project, taskID)
	case "reject":
		dto, err = client.Reject(ctx, cfg.project, taskID, reason)
	case "pass":
		dto, err = client.Pass(ctx, cfg.project, taskID)
	case "fail":
		dto, err = client.Fail(ctx, cfg.project, taskID)
	case "answer":
		dto, err = client.Answer(ctx, cfg.project, taskID, body)
	case "retry":
		dto, err = client.Retry(ctx, cfg.project, taskID)
	default:
		return fmt.Errorf("unknown mutation %q", command)
	}
	if err != nil {
		return err
	}
	return renderMutatedTask(stdout, cfg.json, dto)
}

func mutationUsage(command string) string {
	switch command {
	case "reject":
		return "reject <task-id-prefix> [--reason TEXT]"
	case "answer":
		return "answer <task-id-prefix> --body TEXT"
	default:
		return command + " <task-id-prefix>"
	}
}

func parseStringOption(args []string, name string) (string, []string, error) {
	value := ""
	option := "--" + name
	optionWithEquals := option + "="
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == option:
			i++
			if i >= len(args) {
				return "", nil, fmt.Errorf("%s requires a value", option)
			}
			value = args[i]
		case strings.HasPrefix(arg, optionWithEquals):
			value = strings.TrimPrefix(arg, optionWithEquals)
		default:
			rest = append(rest, arg)
		}
	}
	return value, rest, nil
}

func resolveTaskUUID(ctx context.Context, client *orchclient.Client, project, prefix string) (uuid.UUID, error) {
	full, err := client.ResolveTaskID(ctx, project, prefix)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.Parse(full)
	if err != nil {
		return uuid.Nil, fmt.Errorf("orchestrator returned malformed UUID %q: %w", full, err)
	}
	return id, nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parseSince(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("--since must be RFC3339: %w", err)
	}
	return t, nil
}

func writeJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func renderProjects(w io.Writer, jsonMode bool, projects []orchdto.ProjectDTO) error {
	if jsonMode {
		return writeJSON(w, projects)
	}
	for _, p := range projects {
		fmt.Fprintf(w, "%s\t%s\tworkers=%d\t%s\n", p.Name, p.Language, p.WorkerCount, p.OrchURL)
	}
	return nil
}

func renderTasks(w io.Writer, jsonMode bool, tasks []orchdto.TaskDTO) error {
	if jsonMode {
		return writeJSON(w, tasks)
	}
	for _, t := range tasks {
		fmt.Fprintf(w, "%s\t%s\t%s\tworker=%s\n", shortID(t.ID), t.Status, t.Title, dash(t.AssignedWorker))
	}
	return nil
}

func renderWorkers(w io.Writer, jsonMode bool, workers []orchdto.WorkerDTO) error {
	if jsonMode {
		return writeJSON(w, workers)
	}
	for _, worker := range workers {
		fmt.Fprintf(w, "%s\t%s\t%s\tbranch=%s\ttask=%s\n",
			shortID(worker.ID), worker.Status, worker.AgentType, dash(worker.Branch), dash(worker.CurrentTask))
	}
	return nil
}

func renderWorker(w io.Writer, jsonMode bool, worker orchdto.WorkerDTO) error {
	if jsonMode {
		return writeJSON(w, worker)
	}
	fmt.Fprintf(w, "id: %s\n", worker.ID)
	fmt.Fprintf(w, "status: %s\n", worker.Status)
	fmt.Fprintf(w, "project: %s\n", worker.Project)
	fmt.Fprintf(w, "agent_type: %s\n", worker.AgentType)
	fmt.Fprintf(w, "container_id: %s\n", dash(worker.ContainerID))
	fmt.Fprintf(w, "branch: %s\n", dash(worker.Branch))
	fmt.Fprintf(w, "current_task: %s\n", dash(worker.CurrentTask))
	fmt.Fprintf(w, "started_at: %s\n", formatTime(worker.StartedAt))
	fmt.Fprintf(w, "last_heartbeat: %s\n", formatTime(worker.LastHeartbeat))
	return nil
}

func renderHistory(w io.Writer, jsonMode bool, history orchdto.WorkerHistoryDTO) error {
	if jsonMode {
		return writeJSON(w, history)
	}
	fmt.Fprintf(w, "worker: %s\n", history.WorkerID)
	for _, event := range history.Events {
		fmt.Fprintf(w, "%s\t%s\texit=%d\t%s\n", formatTime(event.Timestamp), event.Kind, event.ExitCode, event.Detail)
	}
	return nil
}

func renderEvents(w io.Writer, jsonMode bool, events []orchdto.EventDTO) error {
	if jsonMode {
		return writeJSON(w, events)
	}
	for _, event := range events {
		payload := strings.TrimSpace(string(event.Payload))
		if payload == "" {
			payload = "{}"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", formatTime(event.Timestamp), event.Type, payload)
	}
	return nil
}

func renderStatus(w io.Writer, status statusDTO) error {
	fmt.Fprintf(w, "projects: %d\n", len(status.Projects))
	for _, p := range status.Projects {
		fmt.Fprintf(w, "  %s (%s) workers=%d orch=%s\n", p.Name, p.Language, p.WorkerCount, p.OrchURL)
	}
	fmt.Fprintf(w, "tasks: %d %s\n", status.TaskCount, formatCounts(status.TasksByStatus))
	fmt.Fprintf(w, "workers: %d %s\n", status.WorkerCount, formatCounts(status.WorkersByStatus))
	fmt.Fprintln(w, "recent_events:")
	for _, event := range status.RecentEvents {
		fmt.Fprintf(w, "  %s %s\n", formatTime(event.Timestamp), event.Type)
	}
	return nil
}

func renderMutatedTask(w io.Writer, jsonMode bool, task orchdto.TaskDTO) error {
	if jsonMode {
		return writeJSON(w, task)
	}
	fmt.Fprintf(w, "task %s -> %s\n", shortID(task.ID), task.Status)
	return nil
}

func countTasksByStatus(tasks []orchdto.TaskDTO) map[string]int {
	counts := make(map[string]int)
	for _, task := range tasks {
		counts[task.Status]++
	}
	return counts
}

func countWorkersByStatus(workers []orchdto.WorkerDTO) map[string]int {
	counts := make(map[string]int)
	for _, worker := range workers {
		counts[worker.Status]++
	}
	return counts
}

func formatCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+strconv.Itoa(counts[key]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func dash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}
