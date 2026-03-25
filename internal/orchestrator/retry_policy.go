package orchestrator

import "time"

// MaxMergeRetries is the maximum number of times the orchestrator will retry
// a transient merge failure before failing the task.
const MaxMergeRetries = 5

// mergeRetryBaseDelay is the base delay for exponential backoff between
// merge retry attempts.
const mergeRetryBaseDelay = 10 * time.Second

// RetryPolicy encapsulates backoff math and retry cap for merge retries.
// Delay grows exponentially: baseDelay * 2^(attempt-1), capped at maxDelay.
type RetryPolicy struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
}

// DefaultMergeRetryPolicy returns the standard retry policy for merge operations.
func DefaultMergeRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries: MaxMergeRetries,
		BaseDelay:  mergeRetryBaseDelay,
		MaxDelay:   5 * time.Minute,
	}
}

// Delay returns the backoff duration for the given attempt number (1-based).
// Formula: baseDelay * 2^(attempt-1), capped at maxDelay.
func (p RetryPolicy) Delay(attempt int) time.Duration {
	if attempt < 1 {
		return p.BaseDelay
	}
	d := p.BaseDelay
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	if p.MaxDelay > 0 && d > p.MaxDelay {
		return p.MaxDelay
	}
	return d
}

// Exhausted returns true if the attempt count has reached or exceeded MaxRetries.
func (p RetryPolicy) Exhausted(attempt int) bool {
	return attempt >= p.MaxRetries
}
