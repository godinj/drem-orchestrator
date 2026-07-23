package orchestrator

import "context"

// reconcileOnStartup consumes terminal worker results that the authoritative
// spawner still knows about. It deliberately does not infer failure from a
// worker being absent: a spawner restart, delayed registration, or temporary
// inventory gap is not a typed terminal result and must not respawn or mutate
// a task.
func (o *Orchestrator) reconcileOnStartup(ctx context.Context) error {
	o.reconcileWorkerAttemptLifecycles(ctx)
	return nil
}
