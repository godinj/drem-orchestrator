package spawner

import (
	"context"
	"encoding/binary"
	"io"
	"regexp"
	"strconv"

	"github.com/godinj/drem-orchestrator/internal/container"
)

const (
	workerUsageTailLines = 200
	workerUsageMaxBytes  = 256 << 10
)

var directWorkerUsagePattern = regexp.MustCompile(
	`drem-direct-agent:\s+iterations=(\d+)\s+tokens_in=(\d+)\s+tokens_out=(\d+)\s+duration=\S+\s+stop_reason=([^\s\x00]*)`,
)

var directWorkerProgressPattern = regexp.MustCompile(
	`drem-direct-agent-progress:\s+iteration=(\d+)\s+tokens_in=(\d+)\s+tokens_out=(\d+)\s+context_pct=(\d+)`,
)

func terminalWorkerStatus(status container.Status) bool {
	return status == container.StatusExited || status == container.StatusDead || status == container.StatusRemoved
}

func (s *Service) readWorkerUsage(ctx context.Context, containerID string) *WorkerUsage {
	rc, err := s.Runtime.StreamLogs(ctx, containerID, container.LogOptions{TailLines: workerUsageTailLines})
	if err != nil {
		return nil
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, workerUsageMaxBytes))
	if err != nil {
		return nil
	}
	return parseWorkerUsage(demuxDockerLogPayload(body))
}

// demuxDockerLogPayload removes Docker's eight-byte stdout/stderr frame
// headers. Fake/TTY runtimes return plain text, which is preserved unchanged.
func demuxDockerLogPayload(body []byte) []byte {
	var out []byte
	for offset := 0; offset < len(body); {
		if len(body)-offset < 8 || body[offset] > 2 || body[offset+1] != 0 || body[offset+2] != 0 || body[offset+3] != 0 {
			return body
		}
		size := int(binary.BigEndian.Uint32(body[offset+4 : offset+8]))
		start, end := offset+8, offset+8+size
		if size < 0 || end > len(body) {
			return body
		}
		out = append(out, body[start:end]...)
		offset = end
	}
	return out
}

func parseWorkerUsage(body []byte) *WorkerUsage {
	matches := directWorkerUsagePattern.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		return parseWorkerProgress(body)
	}
	last := matches[len(matches)-1]
	iterations, err1 := strconv.Atoi(string(last[1]))
	tokensIn, err2 := strconv.Atoi(string(last[2]))
	tokensOut, err3 := strconv.Atoi(string(last[3]))
	if err1 != nil || err2 != nil || err3 != nil {
		return nil
	}
	usage := &WorkerUsage{
		Iterations: iterations,
		TokensIn:   tokensIn,
		TokensOut:  tokensOut,
		StopReason: string(last[4]),
	}
	if progress := parseWorkerProgress(body); progress != nil {
		usage.ContextPct = progress.ContextPct
	}
	return usage
}

func parseWorkerProgress(body []byte) *WorkerUsage {
	matches := directWorkerProgressPattern.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	last := matches[len(matches)-1]
	iterations, err1 := strconv.Atoi(string(last[1]))
	tokensIn, err2 := strconv.Atoi(string(last[2]))
	tokensOut, err3 := strconv.Atoi(string(last[3]))
	contextPct, err4 := strconv.Atoi(string(last[4]))
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return nil
	}
	return &WorkerUsage{Iterations: iterations, TokensIn: tokensIn, TokensOut: tokensOut, ContextPct: contextPct}
}
