package agentmon

import (
	"time"

	"github.com/godinj/drem-orchestrator/internal/extract"
)

// transcriptSource is the Source tag stamped onto events parsed from a
// Claude JSONL transcript file.
const transcriptSource = "transcript"

// parseTranscriptLine parses a single JSONL line and extracts tool call
// information. Returns nil if the line is not an assistant message with
// a tool_use block.
//
// The heavy lifting lives in internal/extract. This function is a thin
// adapter that wraps extract.ToolCall values in the agentmon.ToolCall
// struct so that the rest of the agentmon pipeline is unchanged.
func parseTranscriptLine(line []byte) *ToolCall {
	ev := extract.ParseLine(transcriptSource, line, time.Now())
	if ev == nil {
		return nil
	}
	tc, ok := ev.(*extract.ToolCall)
	if !ok {
		return nil
	}
	return &ToolCall{
		Timestamp: tc.Timestamp,
		Tool:      tc.Tool,
		Target:    tc.Target,
	}
}
