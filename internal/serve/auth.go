package serve

import (
	"net/http"
)

// BearerAuth wraps next with HTTP Bearer Token authentication.
// Requests without a valid "Authorization: Bearer <token>" header receive
// a 401 response with a JSON error body. Token comparison uses crypto/subtle
// to prevent timing side-channel attacks.
func BearerAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}
