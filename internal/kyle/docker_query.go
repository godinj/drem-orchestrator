package kyle

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// dockerQueryTargets is the allowlist of endpoints Kyle is willing to
// forward to docker-query-proxy. Anything else is rejected at the Kyle
// boundary — a defence-in-depth check in addition to the proxy's own
// allowlist. See PRD §"Kyle read-only surface".
var dockerQueryTargets = map[string]bool{
	"containers":      true,
	"container":       true,
	"container_logs":  true,
	"container_stats": true,
}

// DockerQueryHandler proxies narrow, allowlisted Docker queries to the
// docker-query-proxy sidecar. The handler validates:
//   - a non-empty target query parameter,
//   - target ∈ dockerQueryTargets (no raw Docker verbs),
//   - optional filter/labels/id/since parameters forwarded verbatim,
//
// and then issues a GET to {DockerQueryURL}/{target} with the caller's
// query string. The upstream body is streamed back to the client with the
// upstream Content-Type.
//
// When no dockerQueryURL was configured at Service construction the
// handler returns 503 so operators see a specific error rather than the
// blanket 404 that a missing mux entry would produce.
func (s *Service) DockerQueryHandler(w http.ResponseWriter, r *http.Request) {
	if s.dockerQueryURL == "" {
		http.Error(w, "docker-query-proxy not configured", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	target := q.Get("target")
	if target == "" {
		http.Error(w, "target is required (one of: containers, container, container_logs, container_stats)", http.StatusBadRequest)
		return
	}
	if !dockerQueryTargets[target] {
		http.Error(w, fmt.Sprintf("target %q is not in the allowlist", target), http.StatusForbidden)
		return
	}

	// Reconstruct the upstream URL from the allowlisted path components.
	// id (for /container/:id and /container_logs/:id) and the filter query
	// parameters are forwarded verbatim after stripping the Kyle-specific
	// "target" key.
	id := q.Get("id")
	upstreamPath := targetToPath(target, id)
	if err := validateTargetPath(target, id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	forward := make(url.Values, len(q))
	for k, v := range q {
		if k == "target" || k == "id" {
			continue
		}
		forward[k] = v
	}
	u := strings.TrimRight(s.dockerQueryURL, "/") + upstreamPath
	if len(forward) > 0 {
		u += "?" + forward.Encode()
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	if err != nil {
		http.Error(w, "build upstream request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("kyle docker query upstream failed", "err", err, "url", u)
		http.Error(w, "docker-query-proxy unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		slog.Warn("kyle docker query copy failed", "err", err)
	}
}

// targetToPath maps the allowlist token to the concrete upstream path. The
// docker-query-proxy exposes a narrow REST surface; we encode that mapping
// here so callers do not get to name paths directly.
func targetToPath(target, id string) string {
	switch target {
	case "containers":
		return "/containers"
	case "container":
		return "/containers/" + url.PathEscape(id)
	case "container_logs":
		return "/containers/" + url.PathEscape(id) + "/logs"
	case "container_stats":
		return "/containers/" + url.PathEscape(id) + "/stats"
	}
	return "/containers"
}

// validateTargetPath enforces that targets requiring an id actually have
// one. Without this a caller could request target=container with no id and
// the upstream would 404 on a bare /containers/ — returning a 400 early
// makes the error clear.
func validateTargetPath(target, id string) error {
	if target == "containers" {
		return nil
	}
	if id == "" {
		return fmt.Errorf("id is required for target=%s", target)
	}
	return nil
}
