package gq

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Dispatcher runs the main dispatch loop, acquiring slots and proxying
// requests to the upstream SGLang endpoint.
type Dispatcher struct {
	sched   *Scheduler
	breaker *Breaker
	stats   *Stats
	cfg     *Config
	log     *slog.Logger
	client  *http.Client
}

// NewDispatcher creates a dispatcher.
func NewDispatcher(sched *Scheduler, breaker *Breaker, stats *Stats, cfg *Config, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		sched:   sched,
		breaker: breaker,
		stats:   stats,
		cfg:     cfg,
		log:     log,
		client: &http.Client{
			Timeout: cfg.UpstreamTimeout.Duration,
			// Do not follow redirects.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Run starts the dispatch loop. Blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	d.log.Info("dispatcher started", "max_slots", d.cfg.MaxSlots)
	for {
		// Wait for work signal or shutdown.
		select {
		case <-ctx.Done():
			d.log.Info("dispatcher shutting down")
			return
		case <-d.sched.Wake():
		}

		// Try to dispatch as many items as we have slots.
		for {
			if ctx.Err() != nil {
				return
			}

			// Non-blocking slot acquire — if no slots, break to outer wait.
			if err := d.tryAcquireSlot(ctx); err != nil {
				break
			}

			item := d.sched.PickNext()
			if item == nil {
				d.sched.ReleaseSlot()
				break
			}

			go d.dispatch(ctx, item)
		}
	}
}

// tryAcquireSlot attempts a non-blocking slot acquisition.
func (d *Dispatcher) tryAcquireSlot(ctx context.Context) error {
	select {
	case <-d.sched.slots:
		return nil
	default:
		return fmt.Errorf("no slots")
	}
}

func (d *Dispatcher) dispatch(ctx context.Context, item *QueueItem) {
	defer d.sched.ReleaseSlot()
	defer close(item.Done)

	start := time.Now()
	waitDuration := start.Sub(item.EnqueuedAt)
	d.stats.RecordWait(item.Priority, waitDuration)

	// Check if caller already disconnected.
	if item.Ctx.Err() != nil {
		d.stats.RecordCancel()
		d.log.Debug("dispatch skip: caller gone", "id", item.ID[:8])
		return
	}

	// Check circuit breaker.
	if !d.breaker.Allow() {
		item.Err = fmt.Errorf("upstream_unavailable: circuit breaker open")
		item.StatusCode = 503
		writeError(item.RespWriter, http.StatusServiceUnavailable,
			"upstream_unavailable", "circuit breaker open")
		return
	}

	// Build upstream request.
	upstreamURL := d.cfg.Upstream + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(item.Ctx, http.MethodPost, upstreamURL,
		bytes.NewReader(item.Body))
	if err != nil {
		item.Err = err
		item.StatusCode = 500
		writeError(item.RespWriter, http.StatusInternalServerError,
			"internal_error", err.Error())
		d.breaker.RecordFailure()
		d.stats.RecordDispatch(false, 0, 0, 0)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	// Execute upstream request.
	resp, err := d.client.Do(req)
	if err != nil {
		item.Err = err
		item.StatusCode = 502
		writeError(item.RespWriter, http.StatusBadGateway,
			"upstream_error", err.Error())
		d.breaker.RecordFailure()
		d.stats.RecordDispatch(false, 0, 0, 0)
		return
	}
	defer resp.Body.Close()

	duration := time.Since(start)

	// Handle 429 from upstream: requeue once.
	if resp.StatusCode == http.StatusTooManyRequests && !item.Requeued {
		item.Requeued = true
		d.log.Warn("upstream 429, requeuing once", "id", item.ID[:8])
		// Reset the Done channel for the requeue.
		item.Done = make(chan struct{})
		time.Sleep(1 * time.Second)
		d.sched.RequeueAtHead(item)
		// Don't close Done — the new dispatch will handle it.
		// Return without releasing slot (defer does it).
		return
	}

	// Record breaker state.
	if resp.StatusCode >= 500 {
		d.breaker.RecordFailure()
	} else {
		d.breaker.RecordSuccess()
	}

	// Proxy response back to caller.
	if item.Stream && resp.StatusCode == http.StatusOK {
		d.streamResponse(item, resp, duration)
	} else {
		d.copyResponse(item, resp, duration)
	}
}

func (d *Dispatcher) streamResponse(item *QueueItem, resp *http.Response, started time.Duration) {
	// Copy response headers.
	for k, vv := range resp.Header {
		for _, v := range vv {
			item.RespWriter.Header().Add(k, v)
		}
	}
	item.RespWriter.WriteHeader(resp.StatusCode)

	flusher, ok := item.RespWriter.(http.Flusher)
	if !ok {
		// Fallback to non-streaming copy.
		d.copyBody(item, resp)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 256*1024)
	var totalBytes int

	for scanner.Scan() {
		line := scanner.Text()
		totalBytes += len(line)

		_, err := fmt.Fprintf(item.RespWriter, "%s\n", line)
		if err != nil {
			d.log.Warn("stream write error", "id", item.ID[:8], "err", err)
			return
		}
		flusher.Flush()

		// Detect end of stream.
		if strings.HasPrefix(line, "data: [DONE]") {
			break
		}
	}

	duration := time.Since(time.Now().Add(-started))
	estTokensOut := totalBytes / 4 // rough estimate for output tokens
	d.stats.RecordDispatch(true, duration.Milliseconds(), int64(item.EstPromptTokens), int64(estTokensOut))
	item.StatusCode = resp.StatusCode

	if d.cfg.Stats.LogSlowReq.Duration > 0 && started > d.cfg.Stats.LogSlowReq.Duration {
		d.log.Warn("slow request",
			"id", item.ID[:8],
			"caller", item.CallerID,
			"duration", started.Round(time.Millisecond),
			"est_tokens_in", item.EstPromptTokens,
		)
	}
}

func (d *Dispatcher) copyResponse(item *QueueItem, resp *http.Response, started time.Duration) {
	// Copy response headers.
	for k, vv := range resp.Header {
		for _, v := range vv {
			item.RespWriter.Header().Add(k, v)
		}
	}
	item.RespWriter.WriteHeader(resp.StatusCode)

	d.copyBody(item, resp)

	d.stats.RecordDispatch(resp.StatusCode < 400,
		started.Milliseconds(), int64(item.EstPromptTokens), 0)
	item.StatusCode = resp.StatusCode

	if d.cfg.Stats.LogSlowReq.Duration > 0 && started > d.cfg.Stats.LogSlowReq.Duration {
		d.log.Warn("slow request",
			"id", item.ID[:8],
			"caller", item.CallerID,
			"duration", started.Round(time.Millisecond),
		)
	}
}

func (d *Dispatcher) copyBody(item *QueueItem, resp *http.Response) {
	_, err := io.Copy(item.RespWriter, resp.Body)
	if err != nil {
		d.log.Warn("response copy error", "id", item.ID[:8], "err", err)
	}
}
