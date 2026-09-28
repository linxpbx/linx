package main

import (
	"net"
	"net/http"
	"strings"

	"linxpbx.com/linx/internal/certs"
)

// legacyHosts sends the web app's old names (meet.<domain>, api.<domain>,
// before it moved to the domain itself on 2026-09-28) to the same path on
// https://<domain>, so old bookmarks and links keep working while a front
// door still passes those names through. Everything else goes to next.
func legacyHosts(domain string, next http.Handler) http.Handler {
	if domain == "" {
		return next
	}
	old := make(map[string]bool, len(certs.LegacyHosts))
	for _, h := range certs.LegacyHosts {
		old[h+"."+domain] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if old[strings.ToLower(host)] {
			http.Redirect(w, r, "https://"+domain+r.URL.RequestURI(), http.StatusPermanentRedirect)
			return
		}
		next.ServeHTTP(w, r)
	})
}
