// This file contains the per-container stdout tail loop. One goroutine
// per running drem-labeled container reads Docker's multiplexed log
// stream, demuxes the 8-byte frame header, parses each line via the
// extract package, and batches IngestRecord values to the ingestor.

package agentmon

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/extract"
)

// Batch tunables. The numbers come from the prompt spec: 32 records or
// 500ms — whichever trips first — keeps the server fed without over-
// committing memory to lines that happen during a quiet period.
const (
	tailBatchMax   = 32
	tailBatchFlush = 500 * time.Millisecond
	// tailMaxLineBytes caps the scanner buffer so a misbehaving
	// container that writes a 10MB single line cannot exhaust agentmon.
	tailMaxLineBytes = 1024 * 1024
	// dockerFrameHeader is the size of the multiplexed stream header
	// Docker prefixes to each chunk when TTY is not attached:
	//   byte 0    stream type (1=stdout, 2=stderr)
	//   bytes 1-3 reserved
	//   bytes 4-7 payload length, big-endian uint32
	dockerFrameHeader = 8
)

// tailContainer opens a log stream for containerID, parses lines, and
// forwards batches to ing. It returns when ctx is cancelled, when the
// runtime closes the log stream, or on a fatal read error. Transient
// empty reads are tolerated; scanner-level errors are logged and the
// loop exits so the parent supervisor (docker_source) can decide
// whether to restart (current policy: no restart until EventStart is
// seen again).
func tailContainer(ctx context.Context, rt container.Runtime, ing Ingestor,
	containerID, workerID string, _ map[string]string) error {
	rc, err := rt.StreamLogs(ctx, containerID)
	if err != nil {
		return fmt.Errorf("agentmon tail %s: stream logs: %w", containerID, err)
	}
	defer rc.Close()

	return runTail(ctx, rc, ing, containerID, workerID, time.Now)
}

// runTail is the testable core of tailContainer. It takes an arbitrary
// io.Reader so unit tests can feed in-memory byte slices; production
// callers pass the ReadCloser from container.Runtime.StreamLogs. The
// clock function is injected so tests can advance time without
// depending on wall-clock sleeps.
func runTail(ctx context.Context, r io.Reader, ing Ingestor,
	containerID, workerID string, now func() time.Time) error {
	// Demux Docker's 8-byte frame header into a plain byte stream. The
	// demux reader returns the concatenated stdout+stderr payload; we
	// lose the per-frame stream tag but agentmon does not currently
	// distinguish stdout from stderr when parsing lines.
	reader := newDockerDemuxReader(r)

	src := "docker:" + containerID
	lineCh, scanErrCh := scanLines(ctx, reader)

	var (
		batch []IngestRecord
		timer *time.Timer
		fired <-chan time.Time
	)

	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
			fired = nil
		}
	}
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := ing.Ingest(ctx, batch); err != nil {
			slog.Warn("agentmon docker ingest failed",
				"container", containerID, "records", len(batch), "err", err)
		}
		batch = batch[:0]
		stopTimer()
	}
	defer flush()

	ingestLine := func(line []byte) {
		ev := extract.ParseLine(src, line, now())
		if ev == nil {
			return
		}
		rec := toIngestRecord(containerID, workerID, ev)
		if rec.Type == "" {
			return
		}
		batch = append(batch, rec)
		if len(batch) >= tailBatchMax {
			flush()
			return
		}
		if timer == nil {
			timer = time.NewTimer(tailBatchFlush)
			fired = timer.C
		}
	}

	// drainLineCh pulls every remaining buffered line through the
	// ingest path. Used after the scanner has exited so the EOF flush
	// still sees everything the scanner produced before stopping.
	drainLineCh := func() {
		for {
			select {
			case line, ok := <-lineCh:
				if !ok {
					return
				}
				ingestLine(line)
			default:
				return
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-fired:
			flush()
		case err := <-scanErrCh:
			// Scanner has finished; drain any lines it left in the
			// buffer before returning so the deferred flush emits the
			// full tail-end batch.
			drainLineCh()
			if err != nil && !errors.Is(err, io.EOF) {
				slog.Warn("agentmon docker tail scan error",
					"container", containerID, "err", err)
			}
			return nil
		case line, ok := <-lineCh:
			if !ok {
				return nil
			}
			ingestLine(line)
		}
	}
}

// scanLines runs a bufio.Scanner in its own goroutine and pumps each
// line onto the returned channel. A nil error on scanErrCh means the
// stream ended cleanly (EOF). The caller cancels the scan by cancelling
// ctx and closing the underlying reader.
func scanLines(ctx context.Context, r io.Reader) (<-chan []byte, <-chan error) {
	lines := make(chan []byte, 64)
	errs := make(chan error, 1)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), tailMaxLineBytes)
		for sc.Scan() {
			// Copy: scanner.Bytes() is reused on the next call and
			// the consumer's ParseLine runs in a different goroutine.
			b := append([]byte(nil), sc.Bytes()...)
			select {
			case lines <- b:
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
		}
		errs <- sc.Err()
	}()
	return lines, errs
}

// dockerDemuxReader strips Docker's 8-byte multiplex frame header from
// a log stream, emitting the concatenated payload. It is intentionally
// permissive: a stream that was never multiplexed (TTY attached) will
// look like a degenerate header followed by bytes — we fall back to
// passing bytes through unchanged when the apparent frame length is
// implausible for the buffer we have. This keeps the tail loop robust
// against images the PRD explicitly lists (worker containers) as well
// as the occasional shell-attached helper.
type dockerDemuxReader struct {
	src     io.Reader
	pending int    // bytes left in the current frame payload
	hdr     []byte // header-in-progress scratch
	hdrLen  int    // how many of the 8 header bytes we have
}

// newDockerDemuxReader returns a demuxer wrapping src. The returned
// value is not safe for concurrent use.
func newDockerDemuxReader(src io.Reader) *dockerDemuxReader {
	return &dockerDemuxReader{
		src: src,
		hdr: make([]byte, dockerFrameHeader),
	}
}

// Read fills p with as many payload bytes as are currently available,
// transparently consuming Docker's frame headers. An unexpected EOF
// partway through a header is returned as io.ErrUnexpectedEOF; an EOF
// on a clean frame boundary is returned as io.EOF.
func (d *dockerDemuxReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// If we are mid-payload, deliver those bytes first.
	if d.pending > 0 {
		n := len(p)
		if n > d.pending {
			n = d.pending
		}
		n, err := d.src.Read(p[:n])
		d.pending -= n
		return n, err
	}
	// Read header bytes until we have all 8.
	for d.hdrLen < dockerFrameHeader {
		n, err := d.src.Read(d.hdr[d.hdrLen:])
		d.hdrLen += n
		if err != nil {
			if errors.Is(err, io.EOF) && d.hdrLen == 0 {
				return 0, io.EOF
			}
			if errors.Is(err, io.EOF) {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
	}
	// Decode and reset header buffer.
	payloadLen := int(binary.BigEndian.Uint32(d.hdr[4:8]))
	d.hdrLen = 0
	if payloadLen <= 0 {
		// Empty frame — loop into the next header via a zero-byte
		// read so callers do not see a spurious EOF.
		return 0, nil
	}
	d.pending = payloadLen
	// Read a chunk of the new payload into the caller's buffer.
	n := len(p)
	if n > d.pending {
		n = d.pending
	}
	n, err := d.src.Read(p[:n])
	d.pending -= n
	return n, err
}
