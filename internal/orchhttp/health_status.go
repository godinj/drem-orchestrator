package orchhttp

func branchGateResolved(status string) bool {
	switch status {
	case "verification_ready", "host_rework", "integration_ready", "merging", "done", "cancelled":
		return true
	default:
		return false
	}
}
