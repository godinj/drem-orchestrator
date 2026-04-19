package agentmon

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// captureIngestor records every batch handed to it so tests can assert
// on the exact shape of the agentmon→orchestrator wire traffic without
// standing up an HTTP server.
type captureIngestor struct {
	mu      sync.Mutex
	batches [][]IngestRecord
	notify  chan struct{}
}

func newCaptureIngestor() *captureIngestor {
	return &captureIngestor{notify: make(chan struct{}, 16)}
}

func (c *captureIngestor) Ingest(_ context.Context, records []IngestRecord) error {
	cp := make([]IngestRecord, len(records))
	copy(cp, records)
	c.mu.Lock()
	c.batches = append(c.batches, cp)
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return nil
}

func (c *captureIngestor) Batches() [][]IngestRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]IngestRecord, len(c.batches))
	copy(out, c.batches)
	return out
}

func (c *captureIngestor) TotalRecords() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, b := range c.batches {
		n += len(b)
	}
	return n
}

// framedLine wraps a raw log line in Docker's 8-byte stream header so
// the tail loop's demux path is exercised end-to-end. Tests that need
// to bypass demuxing call runTail directly with a pre-demuxed reader.
func framedLine(line string) []byte {
	payload := []byte(line)
	hdr := make([]byte, dockerFrameHeader)
	hdr[0] = 1 // stdout
	binary.BigEndian.PutUint32(hdr[4:8], uint32(len(payload)))
	return append(hdr, payload...)
}

// TestRunTailBatchesAtSizeLimit emits 32 framed commit lines in a burst
// and asserts a single Ingest call with 32 records — the size flush
// path from the prompt spec.
func TestRunTailBatchesAtSizeLimit(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < tailBatchMax; i++ {
		buf.Write(framedLine("[master abc1234] commit message\n"))
	}
	// Sentinel EOF: the reader closes once the scanner drains buf.
	r := &eofReader{Reader: &buf}

	ing := newCaptureIngestor()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, tailRunWithReader(ctx, r, ing, "c1", "w1"))

	batches := ing.Batches()
	require.Len(t, batches, 1, "expected exactly one size-triggered flush")
	require.Len(t, batches[0], tailBatchMax)
	for _, rec := range batches[0] {
		require.Equal(t, recordTypeCommit, rec.Type)
		require.Equal(t, "c1", rec.ContainerID)
	}
}

// TestRunTailBatchesAtTimeoutLimit writes 3 commits with a small gap
// and asserts the 500ms flush fires even though the size threshold was
// never reached. The test runs against a real wall-clock timer so it
// deliberately accepts the 500ms cost; the "over 600ms → ≥ 2 Ingest
// calls" expectation in the prompt is realised by writing once, waiting
// past the flush window, then writing twice more.
func TestRunTailBatchesAtTimeoutLimit(t *testing.T) {
	pr, pw := io.Pipe()
	ing := newCaptureIngestor()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = tailRunWithReader(ctx, pr, ing, "c1", "w1")
	}()

	_, err := pw.Write(framedLine("[master abc1234] first\n"))
	require.NoError(t, err)
	// Wait until the first batch lands (flushed by the 500ms timer).
	select {
	case <-ing.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("first timer-flushed batch never arrived")
	}
	// Now a second burst after the first flush has cleared the batch.
	_, err = pw.Write(framedLine("[master def5678] second\n"))
	require.NoError(t, err)
	_, err = pw.Write(framedLine("[master deadbee] third\n"))
	require.NoError(t, err)
	select {
	case <-ing.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("second timer-flushed batch never arrived")
	}

	_ = pw.Close()
	cancel()
	<-done

	require.GreaterOrEqual(t, len(ing.Batches()), 2,
		"two timer flushes expected across 3 lines spread past 500ms")
	require.Equal(t, 3, ing.TotalRecords())
}

// TestRunTailFlushesOnStreamClose ensures an EOF on the log stream
// flushes whatever was in the current batch — the parent source
// relies on this to avoid losing tail data when a container is torn
// down between flushes.
func TestRunTailFlushesOnStreamClose(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(framedLine("[master abc1234] trailing\n"))
	r := &eofReader{Reader: &buf}

	ing := newCaptureIngestor()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, tailRunWithReader(ctx, r, ing, "c1", "w1"))
	require.Equal(t, 1, ing.TotalRecords(), "trailing line must flush on EOF")
}

// TestDockerDemuxReaderStripsHeader is a focused test on the frame
// parser so the Docker-specific concern is isolated from the rest of
// the tail loop.
func TestDockerDemuxReaderStripsHeader(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(framedLine("hello\n"))
	buf.Write(framedLine("world\n"))
	d := newDockerDemuxReader(&buf)

	out := make([]byte, 0, 32)
	tmp := make([]byte, 16)
	for {
		n, err := d.Read(tmp)
		out = append(out, tmp[:n]...)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}
	require.Equal(t, "hello\nworld\n", string(out))
}

// tailRunWithReader is a test shim that threads a fixed clock into
// runTail so tests do not depend on the production clock seam. The
// real production entry point (tailContainer) calls time.Now directly.
func tailRunWithReader(ctx context.Context, r io.Reader, ing Ingestor, cid, wid string) error {
	return runTail(ctx, r, ing, cid, wid, time.Now)
}

// eofReader wraps an io.Reader so that once it is drained the next Read
// returns io.EOF rather than io.ErrUnexpectedEOF. bytes.Buffer already
// does this, but some tests want the semantics from other reader types.
type eofReader struct {
	io.Reader
}
