package certs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
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

// DNSStatus is what /dns says about the records linx-certd keeps right,
// for the control plane's DNS records card (GET /system/dns-records). It
// listens on linx-private only, like /metrics.
type DNSStatus struct {
	// Following: certd keeps records right (LINX_DNS_RECORDS is set).
	Following bool   `json:"following"`
	Company   string `json:"company,omitempty"`
	DNSChange
	// Checked is the last time the records were written or confirmed.
	Checked  time.Time `json:"checked,omitzero"`
	Failures int       `json:"failures,omitempty"`
	// Error is the last try's reason when it failed: the DNS company's
	// own words, for the admin.
	Error string `json:"error,omitempty"`
}

// DNSStatusHandler serves DNSStatus as JSON.
func DNSStatusHandler(c Config, f *Follower) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		st := DNSStatus{}
		if f != nil {
			fs := f.Snapshot()
			st = DNSStatus{Following: true, Company: c.Provider, DNSChange: f.Client.State.LastChange(),
				Checked: fs.LastSuccess, Failures: fs.Failures, Error: fs.LastError}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(st)
	})
}
