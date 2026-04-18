package gq

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Priority represents request scheduling priority.
type Priority int

const (
	// High priority: orch small-ctx calls (classifier, prep).
	High Priority = 0
	// Normal priority: orch large-ctx calls (coder, fixer, reviewer).
	Normal Priority = 1
	// Low priority: C-Suite temps, ad-hoc callers.
	Low Priority = 2

	numPriorities = 3
)

// String returns the lowercase name of the priority.
func (p Priority) String() string {
	switch p {
	case High:
		return "high"
	case Normal:
		return "normal"
	case Low:
		return "low"
	default:
		return "unknown"
	}
}

// ParsePriority converts a string to a Priority. Defaults to Low.
func ParsePriority(s string) Priority {
	switch strings.ToLower(s) {
	case "high":
		return High
	case "normal":
		return Normal
	case "low":
		return Low
	default:
		return Low
	}
}

// QueueItem represents a single request waiting in or being dispatched from
// the queue. Created by the HTTP handler, consumed by the dispatcher.
type QueueItem struct {
	// ID is a unique identifier for logging and metrics.
	ID string

	// CallerID from X-GQ-Caller header (e.g. "coder", "worker-013").
	CallerID string

	// Priority assigned at ingress (from header or default map).
	Priority Priority

	// EffectivePriority may differ from Priority due to aging promotion.
	EffectivePriority Priority

	// EstPromptTokens is a cheap estimate of prompt size for DRR credits.
	EstPromptTokens int

	// Body is the original request body bytes.
	Body []byte

	// Stream is true when the request asks for SSE streaming.
	Stream bool

	// EnqueuedAt records when the item entered the queue.
	EnqueuedAt time.Time

	// Deadline is the latest time this item can remain queued.
	Deadline time.Time

	// Ctx is the request context; cancelled when caller disconnects.
	Ctx context.Context

	// Cancel cancels the item's context.
	Cancel context.CancelFunc

	// RespWriter is the caller's HTTP response writer.
	RespWriter http.ResponseWriter

	// Done is closed by the dispatcher when the response is complete.
	// The error, if any, is stored in Err before closing.
	Done chan struct{}

	// Err holds the dispatch error, set before Done is closed.
	Err error

	// StatusCode holds the upstream HTTP status, set before Done is closed.
	StatusCode int

	// Requeued is set to true after a 429 requeue to prevent loops.
	Requeued bool
}

// NewQueueItem creates a QueueItem from an incoming request.
func NewQueueItem(
	callerID string,
	priority Priority,
	body []byte,
	stream bool,
	deadline time.Time,
	ctx context.Context,
	cancel context.CancelFunc,
	w http.ResponseWriter,
) *QueueItem {
	return &QueueItem{
		ID:                uuid.New().String(),
		CallerID:          callerID,
		Priority:          priority,
		EffectivePriority: priority,
		EstPromptTokens:   EstimateTokens(body),
		Body:              body,
		Stream:            stream,
		EnqueuedAt:        time.Now(),
		Deadline:          deadline,
		Ctx:               ctx,
		Cancel:            cancel,
		RespWriter:        w,
		Done:              make(chan struct{}),
	}
}

// EstimateTokens provides a cheap token count approximation from body size.
// Uses byte-count / 3 as a rough heuristic — good enough for DRR credit
// accounting without importing a tokenizer.
func EstimateTokens(body []byte) int {
	n := len(body) / 3
	if n < 1 {
		n = 1
	}
	return n
}
