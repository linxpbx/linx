package apihttp

import "net/http"

// MaxBodyBytes is the request body size limit for every API endpoint
// (docs/API.md §2).
const MaxBodyBytes = 1 << 20 // 1 MiB

// LimitBody caps request bodies at MaxBodyBytes; a larger body fails while
// being read, before it reaches validation or a handler.
func LimitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}
