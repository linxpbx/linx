package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
)

// certdMetricsURL is linx-certd's fixed internal metrics endpoint, on
// linx-private (compose). Not admin-supplied, so this bypasses the
// SSRF-guarded outbound client the same way the database connection does:
// the guard exists for URLs an admin typed in, not Linx's own service mesh
// (which the guard would in fact refuse, since linx-private is one of
// Linx's own networks).
const certdMetricsURL = "http://certd:8081/metrics"

// certExpiryWarning: the deployed certificate is treated as "renewal is
// failing" once it's this close to expiry (docs/API.md §5 "certificate
// renewal failure"). Let's Encrypt certs are renewed starting around 30
// days out, so still not renewed at 14 days means real trouble, not just
// an ordinary retry.
const certExpiryWarning = 14 * 24 * time.Hour

const certAlertKey = "cert.renewal_failed"

// pollCertd checks linx-certd's certificate status every interval and
// fires or resolves certAlertKey. It's a source ADR-029 lists as shipping
// with certd; DDNS update failure and disk/storage don't have anywhere to
// read that signal from yet (docs/THREAT_MODEL.md), so they're not wired
// up in this slice.
func pollCertd(ctx context.Context, client *http.Client, engine *alert.Engine, tenant uuid.UUID, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		checkCertd(ctx, client, certdMetricsURL, engine, tenant, log)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func checkCertd(ctx context.Context, client *http.Client, url string, engine *alert.Engine, tenant uuid.UUID, log *slog.Logger) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Warn("couldn't reach linx-certd for its certificate status", "err", err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return
	}
	m := parseCertdMetrics(string(body))
	if m.Expiry.IsZero() {
		return // no certificate issued yet (fresh install): nothing to alert on
	}
	if time.Until(m.Expiry) < certExpiryWarning {
		msg := fmt.Sprintf(
			"The certificate expires on %s and hasn't renewed yet (%d renewal attempts have failed since certd started). Check `linx doctor` and the certd container's logs.",
			m.Expiry.Format("2 January 2006"), m.Failures)
		if err := engine.Fire(ctx, tenant, certAlertKey, alert.SeverityCritical,
			"Certificate renewal is failing", msg, ""); err != nil && ctx.Err() == nil {
			log.Error("firing certificate alert failed", "err", err)
		}
		return
	}
	if err := engine.Resolve(ctx, tenant, certAlertKey); err != nil && ctx.Err() == nil {
		log.Error("resolving certificate alert failed", "err", err)
	}
}

type certdStats struct {
	Expiry   time.Time
	Failures int
}

// parseCertdMetrics reads the handful of lines internal/certs.MetricsHandler
// writes (Prometheus text format), ignoring everything else.
func parseCertdMetrics(text string) certdStats {
	var s certdStats
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		name, value, ok := splitMetricLine(line)
		if !ok {
			continue
		}
		switch name {
		case "linx_cert_expiry_timestamp_seconds":
			if sec, err := strconv.ParseFloat(value, 64); err == nil {
				s.Expiry = time.Unix(int64(sec), 0)
			}
		case "linx_cert_renewal_failures_total":
			if n, err := strconv.Atoi(value); err == nil {
				s.Failures = n
			}
		}
	}
	return s
}

// splitMetricLine splits a Prometheus text line ("name{labels} value" or
// "name value") into its metric name and value, skipping comments.
func splitMetricLine(line string) (name, value string, ok bool) {
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	i := strings.LastIndexByte(line, ' ')
	if i < 0 {
		return "", "", false
	}
	name, value = line[:i], line[i+1:]
	if j := strings.IndexByte(name, '{'); j >= 0 {
		name = name[:j]
	}
	return name, value, true
}
