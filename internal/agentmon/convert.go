// This file converts typed extract.Event values into IngestRecord
// values ready for shipment to the orchestrator. It is pure: no I/O, no
// mutation of the input event, and returns a zero-value IngestRecord
// (Type == "") when the event does not correspond to a known record
// type so the caller can drop it.

package agentmon

import (
	"github.com/godinj/drem-orchestrator/internal/extract"
)

// Record-type tags match the discriminants accepted by the orchestrator's
// POST /internal/logs handler (see internal/orchhttp/handlers_internal.go).
// Duplicated here rather than imported so agentmon does not depend on
// internal/orchhttp.
const (
	recordTypeCommit     = "commit"
	recordTypePush       = "push"
	recordTypeTestResult = "test_result"
	recordTypeBuildError = "build_error"
	recordTypeHeartbeat  = "heartbeat"
	recordTypeCrash      = "crash"
	recordTypeToolCall   = "tool_call"
)

// toIngestRecord builds an IngestRecord from a parsed extract.Event. The
// Payload map follows the field names the server extracts in
// ingestDetail (sha, branch, message, remote, summary, agent_id, reason,
// tool, target, etc.) so the detail extractor picks them up verbatim.
// Unknown event types produce a zero-value record (Type == "") which
// callers should drop.
func toIngestRecord(containerID, workerID string, ev extract.Event) IngestRecord {
	switch e := ev.(type) {
	case *extract.Commit:
		return IngestRecord{
			Type:        recordTypeCommit,
			ContainerID: containerID,
			WorkerID:    workerID,
			Timestamp:   e.Timestamp,
			Payload: map[string]any{
				"source":  e.Source,
				"sha":     e.SHA,
				"branch":  e.Branch,
				"message": e.Message,
			},
		}
	case *extract.Push:
		return IngestRecord{
			Type:        recordTypePush,
			ContainerID: containerID,
			WorkerID:    workerID,
			Timestamp:   e.Timestamp,
			Payload: map[string]any{
				"source": e.Source,
				"branch": e.Branch,
				"remote": e.Remote,
			},
		}
	case *extract.TestResult:
		return IngestRecord{
			Type:        recordTypeTestResult,
			ContainerID: containerID,
			WorkerID:    workerID,
			Timestamp:   e.Timestamp,
			Payload: map[string]any{
				"source":  e.Source,
				"passed":  e.Passed,
				"summary": e.Summary,
			},
		}
	case *extract.BuildError:
		return IngestRecord{
			Type:        recordTypeBuildError,
			ContainerID: containerID,
			WorkerID:    workerID,
			Timestamp:   e.Timestamp,
			Payload: map[string]any{
				"source":  e.Source,
				"tool":    e.Tool,
				"message": e.Message,
				"file":    e.File,
				"line":    e.Line,
			},
		}
	case *extract.Heartbeat:
		return IngestRecord{
			Type:        recordTypeHeartbeat,
			ContainerID: containerID,
			WorkerID:    workerID,
			Timestamp:   e.Timestamp,
			Payload: map[string]any{
				"source":   e.Source,
				"agent_id": e.AgentID,
			},
		}
	case *extract.Crash:
		return IngestRecord{
			Type:        recordTypeCrash,
			ContainerID: containerID,
			WorkerID:    workerID,
			Timestamp:   e.Timestamp,
			Payload: map[string]any{
				"source":    e.Source,
				"reason":    e.Reason,
				"exit_code": e.ExitCode,
			},
		}
	case *extract.ToolCall:
		return IngestRecord{
			Type:        recordTypeToolCall,
			ContainerID: containerID,
			WorkerID:    workerID,
			Timestamp:   e.Timestamp,
			Payload: map[string]any{
				"source": e.Source,
				"tool":   e.Tool,
				"target": e.Target,
			},
		}
	}
	return IngestRecord{}
}
