// Package health provides the liveness endpoint shared by every Linx service.
package health

import (
	"encoding/json"
	"net/http"

	"linxpbx.com/linx/internal/version"
)

// Handler answers GET /healthz with the service name and version.
func Handler(service string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"service": service,
			"version": version.Version,
		})
	})
}
