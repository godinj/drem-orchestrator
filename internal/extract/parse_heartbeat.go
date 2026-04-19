package extract

import (
	"bytes"
	"strings"
	"time"
)

// heartbeatMarker is the fixed prefix written by the watchdog binary baked
// into worker images (see prompt 04 in the containerization plan).
const heartbeatMarker = "DREM-HEARTBEAT"

// matchHeartbeat recognises watchdog liveness markers of the form
//
//	DREM-HEARTBEAT agent_id=<id>
//
// Additional key=value fields after agent_id are tolerated and ignored.
func matchHeartbeat(src string, line []byte, now time.Time) (Event, bool) {
	idx := bytes.Index(line, []byte(heartbeatMarker))
	if idx < 0 {
		return nil, false
	}
	rest := string(line[idx+len(heartbeatMarker):])
	agentID := extractKV(rest, "agent_id")
	return &Heartbeat{
		Timestamp: now,
		Source:    src,
		AgentID:   agentID,
	}, true
}

// extractKV pulls a single key=value field from a whitespace-separated
// key=value string. Returns the empty string if the key is not present.
// The value is terminated by the first whitespace character after the
// '=' so values may not contain spaces in this format.
func extractKV(s, key string) string {
	needle := key + "="
	for {
		idx := strings.Index(s, needle)
		if idx < 0 {
			return ""
		}
		// Require that the match is at the start or preceded by whitespace
		// so we do not accidentally match a suffix like "x_agent_id=".
		if idx > 0 {
			prev := s[idx-1]
			if prev != ' ' && prev != '\t' {
				s = s[idx+len(needle):]
				continue
			}
		}
		rest := s[idx+len(needle):]
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			return rest
		}
		return rest[:end]
	}
}
