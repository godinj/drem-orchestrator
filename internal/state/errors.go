package state

import "errors"

// ErrStaleTransition marks a lifecycle mutation that lost a concurrent
// compare-and-swap or observed a task outside its expected source state.
var ErrStaleTransition = errors.New("stale task state")
