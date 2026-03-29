package serve

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// BearerAuth wraps next with HTTP Bearer Token authentication.
// Requests without a valid "Authorization: Bearer <token>" header receive
// a 401 response with a JSON error body. Token comparison uses crypto/subtle
// to prevent timing side-channel attacks.
func BearerAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			rejectAuth(w, "missing or invalid authorization header")
			return
		}
		got := auth[len(prefix):]
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			rejectAuth(w, "invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func rejectAuth(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
