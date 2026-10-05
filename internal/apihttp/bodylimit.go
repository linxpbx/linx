package apihttp

import "net/http"

// MaxBodyBytes is the request body size limit for every API endpoint
// (docs/API.md §2).
const MaxBodyBytes = 1 << 20 // 1 MiB

// LimitBody caps request bodies at MaxBodyBytes. A client that declares an
// oversized Content-Length is turned away at once with 413, before any
// handler runs — both so nothing reads a byte of it, and so the caller is
// told the real reason. Without that up-front check the cap still holds
// (MaxBytesReader stops the read at the limit), but the first thing to read
// the body is the OpenAPI validator's security phase, which would surface
// "body too large" as a misleading 401. A body with no declared length is
// still capped by MaxBytesReader as it is read.
func LimitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > MaxBodyBytes {
			WriteProblem(w, http.StatusRequestEntityTooLarge, "request_too_large",
				"That request is larger than Linx accepts.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}
