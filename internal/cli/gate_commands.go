// Package cli implements the headless CLI subcommand for the drem
// orchestrator. It provides programmatic read/write access to the
// orchestrator database for C-Suite agents and temp workers.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// GateClient is the minimum orchclient surface the five gate commands
// (approve, reject, pass, fail, answer) need. It is defined at the
// consumption site so internal/cli does not import pkg/orchclient just
// to name a type — *orchclient.Client satisfies this interface
// incidentally, which is the "interfaces at the consumption site"
// constitution rule in action.
//
// Tests inject a scripted fake; production passes a real
// *orchclient.Client constructed against the containerized
// orchestrator's HTTP API (see cmd/drem/cli_cmd.go). This closes the
// double-writer escape hatch documented in plans/orch-api-gate-mutations.md
// §1 Why: pre-Phase-2, drem cli approve opened the SQLite DB directly
// AND spun up a second in-process orchestrator, contending with the
// container's writer.
type GateClient interface {
	ResolveTaskID(ctx context.Context, project, prefix string) (string, error)
	Approve(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error)
	Reject(ctx context.Context, project string, taskID uuid.UUID, reason string) (orchdto.TaskDTO, error)
	Pass(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error)
	Fail(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error)
	Answer(ctx context.Context, project string, taskID uuid.UUID, body string) (orchdto.TaskDTO, error)
	Retry(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error)
}

// gateHandlers is the dispatcher for the five gate mutation
// subcommands. It owns the HTTP client, the project name (all
// mutations are scoped to the orchestrator's single project), and the
// --json flag so output rendering can branch consistently across
// verbs.
type gateHandlers struct {
	client   GateClient
	project  string
	jsonMode bool
}

// newGateHandlers constructs a gateHandlers with the supplied client,
// project name, and output mode. project must be non-empty; the server
// 404s unknown project names so an empty value here produces a clearer
// error than waiting for the round-trip.
func newGateHandlers(client GateClient, project string, jsonMode bool) *gateHandlers {
	return &gateHandlers{
		client:   client,
		project:  project,
		jsonMode: jsonMode,
	}
}

// DispatchGate is the top-level entry point for the five gate verbs.
// Returns handled=true exactly when subcommand matches one of the gate
// verbs; callers use that signal to distinguish "we handled it" from
// "fall through to the next dispatcher". The error return carries the
// verb's outcome — nil on success, a typed orchclient error on server
// failure, or a usage/validation error before any network call.
func DispatchGate(client GateClient, project string, jsonMode bool, subcommand string, args []string, w io.Writer) (handled bool, err error) {
	gh := newGateHandlers(client, project, jsonMode)
	switch subcommand {
	case "approve":
		return true, gh.handleApprove(args, w)
	case "reject":
		return true, gh.handleReject(args, w)
	case "answer":
		return true, gh.handleAnswer(args, w)
	case "pass":
		return true, gh.handlePass(args, w)
	case "fail":
		return true, gh.handleFail(args, w)
	case "retry":
		return true, gh.handleRetry(args, w)
	default:
		return false, nil
	}
}

// handleApprove resolves the short-prefix to a full UUID via the
// orchclient, then POSTs /approve. The server picks the correct
// transition (plan_review vs test_review) based on the task's current
// status — no client-side branching needed. See
// plans/orch-api-gate-mutations.md §2 for the status-code contract.
func (gh *gateHandlers) handleApprove(args []string, w io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: drem cli approve <task-id>")
	}
	id, err := gh.resolve(args[0])
	if err != nil {
		return err
	}
	dto, err := gh.client.Approve(context.Background(), gh.project, id)
	if err != nil {
		return err
	}
	return gh.render(dto, w)
}

// handleReject parses --reason (optional), resolves the prefix, and
// POSTs /reject. An empty reason is forwarded verbatim as
// {"reason":""} — the server distinguishes "field missing" from
// "field empty" so the CLI passes user intent through unchanged.
func (gh *gateHandlers) handleReject(args []string, w io.Writer) error {
	reason, remaining := parseFlag(args, "reason")
	if len(remaining) == 0 {
		return fmt.Errorf("usage: drem cli reject <task-id> [--reason=REASON]")
	}
	id, err := gh.resolve(remaining[0])
	if err != nil {
		return err
	}
	dto, err := gh.client.Reject(context.Background(), gh.project, id, reason)
	if err != nil {
		return err
	}
	return gh.render(dto, w)
}

// handlePass POSTs /pass after resolving the short-prefix. Status
// validation is the server's responsibility; a non-testing_ready task
// surfaces as ErrWrongStatus from orchclient.
func (gh *gateHandlers) handlePass(args []string, w io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: drem cli pass <task-id>")
	}
	id, err := gh.resolve(args[0])
	if err != nil {
		return err
	}
	dto, err := gh.client.Pass(context.Background(), gh.project, id)
	if err != nil {
		return err
	}
	return gh.render(dto, w)
}

// handleFail POSTs /fail after resolving the short-prefix. Symmetric
// to handlePass; server decides whether the current status accepts
// the transition.
func (gh *gateHandlers) handleFail(args []string, w io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: drem cli fail <task-id>")
	}
	id, err := gh.resolve(args[0])
	if err != nil {
		return err
	}
	dto, err := gh.client.Fail(context.Background(), gh.project, id)
	if err != nil {
		return err
	}
	return gh.render(dto, w)
}

// handleRetry POSTs /retry after resolving the short-prefix. Only
// status=failed is accepted server-side; a non-failed task surfaces
// as ErrWrongStatus from orchclient. See
// plans/v15-v16-failed-task-recovery.md for the recovery workflow.
func (gh *gateHandlers) handleRetry(args []string, w io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: drem cli retry <task-id>")
	}
	id, err := gh.resolve(args[0])
	if err != nil {
		return err
	}
	dto, err := gh.client.Retry(context.Background(), gh.project, id)
	if err != nil {
		return err
	}
	return gh.render(dto, w)
}

// handleAnswer parses --body (required, non-empty after trim) and
// POSTs /answer. The client short-circuits an empty body before any
// network call; here we validate again defensively so the error
// message is CLI-shaped ("--body is required") rather than
// orchclient-shaped.
func (gh *gateHandlers) handleAnswer(args []string, w io.Writer) error {
	body, remaining := parseFlag(args, "body")
	if len(remaining) == 0 {
		return fmt.Errorf("usage: drem cli answer <task-id> --body=BODY")
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("--body is required")
	}
	id, err := gh.resolve(remaining[0])
	if err != nil {
		return err
	}
	dto, err := gh.client.Answer(context.Background(), gh.project, id, body)
	if err != nil {
		return err
	}
	return gh.render(dto, w)
}

// resolve maps a short-prefix (or full UUID) arg to a uuid.UUID the
// orchclient gate methods can accept. Delegates to
// orchclient.ResolveTaskID so prefix-expansion logic stays in one
// place — see plans/orch-api-gate-mutations.md §Q2 (Seth's design
// review: "three-copy threshold").
func (gh *gateHandlers) resolve(prefix string) (uuid.UUID, error) {
	full, err := gh.client.ResolveTaskID(context.Background(), gh.project, prefix)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.Parse(full)
	if err != nil {
		return uuid.Nil, fmt.Errorf("orchestrator returned malformed UUID %q: %w", full, err)
	}
	return id, nil
}

// render writes the post-transition TaskDTO to w. In JSON mode the DTO
// is emitted verbatim so downstream scripts can consume it; in human
// mode a one-liner with short-ID + status keeps terminal output
// compact. Callers that want the full task detail should follow up
// with `drem cli task <id>`.
func (gh *gateHandlers) render(dto orchdto.TaskDTO, w io.Writer) error {
	if gh.jsonMode {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(dto)
	}
	short := dto.ID
	if len(short) > 8 {
		short = short[:8]
	}
	_, err := fmt.Fprintf(w, "task %s -> %s\n", short, dto.Status)
	return err
}
