package kyle

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// logRequests is a transparent middleware that records method, path,
// status, and elapsed time at info level. The status code is captured via
// a statusRecorder because http.ResponseWriter does not expose it.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("kyle request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// recoverPanics is the outermost middleware: a panic anywhere downstream is
// converted to a 500 and an error-level log entry so one bad request does
// not take the server down.
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("kyle panic",
					"err", rec,
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
				)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder wraps an http.ResponseWriter and remembers the first
// status code written. Subsequent calls still propagate to the underlying
// writer (mirroring http.ResponseWriter semantics); the remembered value
// is used by logRequests for its "status" field.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status before delegating.
func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
