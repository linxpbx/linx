package certs

import (
	"fmt"
	"net/http"
	"strings"
)

// MetricsHandler serves the manager's (and, if f isn't nil, the DNS
// follower's) stats in Prometheus text format. It listens on linx-private
// only (compose), like every other metrics endpoint.
func MetricsHandler(m *Manager, f *Follower) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s := m.Snapshot()
		var b strings.Builder
		metric := func(name, typ, help string, v float64, labels string) {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s%s %.0f\n", name, help, name, typ, name, labels, v)
		}
		if !s.NotAfter.IsZero() {
			// Names are validated hostnames and issuers are fixed IDs: no escaping needed.
			labels := fmt.Sprintf(`{names=%q,issuer=%q}`, strings.Join(m.Config.Names(), ","), s.Issuer)
			metric("linx_cert_expiry_timestamp_seconds", "gauge",
				"Expiry time of the deployed public certificate.", float64(s.NotAfter.Unix()), labels)
		}
		if !s.LastSuccess.IsZero() {
			metric("linx_cert_last_renewal_timestamp_seconds", "gauge",
				"Time the last certificate was issued and deployed.", float64(s.LastSuccess.Unix()), "")
		}
		metric("linx_cert_renewal_failures_total", "counter",
			"Renewal attempts where every certificate authority failed.", float64(s.Failures), "")
		if f != nil {
			fs := f.Snapshot()
			if !fs.LastSuccess.IsZero() {
				metric("linx_dns_records_last_update_timestamp_seconds", "gauge",
					"Time the public names were last written or confirmed.", float64(fs.LastSuccess.Unix()), "")
			}
			metric("linx_dns_records_failures_total", "counter",
				"Failed attempts to keep the public names up to date.", float64(fs.Failures), "")
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(b.String()))
	})
}
