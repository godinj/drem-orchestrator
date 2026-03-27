package watcher

// Trigger is the contract for all wake-up sources that run in the background.
// Start begins the trigger; Stop halts it and blocks until the background
// goroutine exits.
type Trigger interface {
	Start()
	Stop()
}

// AgentTriggerer schedules an agent turn and reports the outcome.
// Both Deduplicator and LifecycleManager satisfy this interface.
type AgentTriggerer interface {
	TriggerAgent(agent string) TriggerResult
}
