package apihttp

import "net/http"

// NoStore marks every API response "Cache-Control: no-store": several carry
// a secret shown only once (API keys, OAuth client secrets, webhook secrets,
// device SIP logins), and no API response is meant to be kept by a cache or
// proxy on the way.
func NoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
