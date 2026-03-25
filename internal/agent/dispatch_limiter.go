package agent

import "time"

// dispatchLimiter gates agent dispatch based on rate-limit policy.
// Defined at the consumption site (internal/agent) per the constitution's
// "interfaces at consumption sites" rule. The ratelimit.Limiter in
// internal/ratelimit implements this interface.
type dispatchLimiter interface {
	// Allow checks whether a dispatch is permitted right now.
	// Returns (true, 0) if allowed, or (false, retryAfter) with a
	// suggested wait duration when the caller should back off.
	Allow() (ok bool, retryAfter time.Duration)

	// Record notes that a dispatch occurred, advancing the sliding window.
	Record()
}

// SetDispatchLimiter attaches an optional rate limiter to the Runner.
// When set, CanSpawn also consults the limiter before allowing a spawn,
// and spawnNewAgent calls Record on successful dispatch.
// Pass nil to remove the limiter (restores default behavior).
func (r *Runner) SetDispatchLimiter(dl dispatchLimiter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatchLimiter = dl
}
