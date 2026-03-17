package agentmon

import "strings"

// detectPhase analyzes recent tool calls to determine the current agent phase.
// It examines the last 20 calls and classifies them into one of:
// "exploring", "implementing", "testing", or "fixing".
func detectPhase(calls []ToolCall) string {
	window := calls
	if len(window) > 20 {
		window = window[len(window)-20:]
	}
	if len(window) == 0 {
		return "exploring"
	}

	var readCount, editCount, testCount int
	// Track edit-test cycles: Edit followed by Bash(test) pairs.
	var editTestCycles int
	for i, c := range window {
		switch c.Tool {
		case "Read", "Grep", "Glob":
			readCount++
		case "Edit", "Write":
			editCount++
		case "Bash":
			if isTestCommand(c.Target) {
				testCount++
				// Check if previous call was an Edit/Write (edit-test cycle).
				if i > 0 && (window[i-1].Tool == "Edit" || window[i-1].Tool == "Write") {
					editTestCycles++
				}
			}
		}
	}

	// If we see repeated edit-test cycles, the agent is fixing.
	if editTestCycles >= 2 {
		return "fixing"
	}

	// If tests are running, the agent is testing.
	if testCount > 0 {
		return "testing"
	}

	// If edits are present, the agent is implementing.
	if editCount > 0 {
		return "implementing"
	}

	return "exploring"
}

// isTestCommand returns true if the target string looks like a test command.
func isTestCommand(target string) bool {
	lower := strings.ToLower(target)
	return strings.Contains(lower, "go test") ||
		strings.Contains(lower, "npm test") ||
		strings.Contains(lower, "pytest") ||
		strings.Contains(lower, "cargo test")
}

// detectStuck analyzes recent tool calls for stuck patterns.
// Returns a score (0=ok, 1=warning, 2=stuck) and a hint string.
func detectStuck(calls []ToolCall) (score int, hint string) {
	if len(calls) < 5 {
		return 0, ""
	}

	window := calls
	if len(window) > 10 {
		window = window[len(window)-10:]
	}

	// Check: same tool+target repeated >5 times in last 10 calls.
	type action struct {
		tool, target string
	}
	actionCounts := make(map[action]int)
	for _, c := range window {
		actionCounts[action{c.Tool, c.Target}]++
	}
	for a, count := range actionCounts {
		if count > 5 {
			return 1, "repeating same action: " + a.tool + " on " + a.target
		}
	}

	// Check: all last 10 calls target the same file.
	if len(window) >= 10 {
		firstTarget := window[0].Target
		if firstTarget != "" {
			allSame := true
			for _, c := range window[1:] {
				if c.Target != firstTarget {
					allSame = false
					break
				}
			}
			if allSame {
				return 1, "dwelling on " + firstTarget
			}
		}
	}

	// Check: Edit -> Bash(test) -> Edit -> Bash(test) cycle > 3 times.
	var editTestLoops int
	var lastEditTarget string
	for i := 0; i < len(window)-1; i++ {
		c := window[i]
		next := window[i+1]
		if (c.Tool == "Edit" || c.Tool == "Write") && next.Tool == "Bash" && isTestCommand(next.Target) {
			if lastEditTarget == "" {
				lastEditTarget = c.Target
			}
			if c.Target == lastEditTarget {
				editTestLoops++
			} else {
				lastEditTarget = c.Target
				editTestLoops = 1
			}
		}
	}
	if editTestLoops > 3 {
		return 2, "edit-test loop on " + lastEditTarget
	}

	return 0, ""
}
