package main

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/dbsecret"
)

func TestParseCertdMetrics(t *testing.T) {
	// The exact shape internal/certs.MetricsHandler writes.
	m := &certs.Manager{}
	body := metricsBody(t, m)
	if got := parseCertdMetrics(body); !got.Expiry.IsZero() || got.Failures != 0 {
		t.Errorf("fresh manager: %+v", got)
	}
}

func TestParseCertdMetricsRealHandler(t *testing.T) {
	text := `# HELP linx_cert_expiry_timestamp_seconds Expiry time of the deployed public certificate.
# TYPE linx_cert_expiry_timestamp_seconds gauge
linx_cert_expiry_timestamp_seconds{names="pbx.example.com",issuer="letsencrypt"} 1700000000
# HELP linx_cert_last_renewal_timestamp_seconds Time the last certificate was issued and deployed.
# TYPE linx_cert_last_renewal_timestamp_seconds gauge
linx_cert_last_renewal_timestamp_seconds 1699000000
# HELP linx_cert_renewal_failures_total Renewal attempts where every certificate authority failed.
# TYPE linx_cert_renewal_failures_total counter
linx_cert_renewal_failures_total 3
`
	got := parseCertdMetrics(text)
	if got.Expiry.Unix() != 1700000000 || got.Failures != 3 {
		t.Errorf("parseCertdMetrics() = %+v", got)
	}
}

func metricsBody(t *testing.T, m *certs.Manager) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	certs.MetricsHandler(m, nil).ServeHTTP(rec, req)
	b, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func testEngine(t *testing.T) (*alert.Engine, *fakeAlertStore) {
	t.Helper()
	st := newFakeAlertStore()
	var key [dbsecret.KeySize]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	sender := &alert.Sender{Client: &http.Client{}, Sealer: dbsecret.NewSealer(key), Now: time.Now}
	return &alert.Engine{Store: st, Sender: sender, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, st
}

func TestCheckCertdFiresWhenExpiringSoon(t *testing.T) {
	engine, st := testEngine(t)
	tenant := uuid.Must(uuid.NewV7())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		soon := time.Now().Add(5 * 24 * time.Hour).Unix()
		w.Write([]byte("linx_cert_expiry_timestamp_seconds{} " + strconv.FormatInt(soon, 10) + "\nlinx_cert_renewal_failures_total 7\n"))
	}))
	defer srv.Close()

	checkCertd(context.Background(), srv.Client(), srv.URL, engine, tenant, slog.New(slog.NewTextHandler(io.Discard, nil)))

	alerts, err := st.ListAlerts(context.Background(), tenant, alert.StatusOpen, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].Key != certAlertKey || alerts[0].Severity != alert.SeverityCritical {
		t.Fatalf("alerts = %+v", alerts)
	}
}

func TestCheckCertdWarnsAt21Days(t *testing.T) {
	engine, st := testEngine(t)
	tenant := uuid.Must(uuid.NewV7())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("linx_cert_expiry_timestamp_seconds{} " + strconv.FormatInt(time.Now().Add(15*24*time.Hour).Unix(), 10) + "\n"))
	}))
	defer srv.Close()
	checkCertd(context.Background(), srv.Client(), srv.URL, engine, tenant, slog.New(slog.NewTextHandler(io.Discard, nil)))
	alerts, _ := st.ListAlerts(context.Background(), tenant, alert.StatusOpen, nil, 10)
	if len(alerts) != 1 || alerts[0].Severity != alert.SeverityWarning {
		t.Fatalf("alerts = %+v, want one warning at 15 days left", alerts)
	}
}

func TestCheckCertdResolvesWhenHealthy(t *testing.T) {
	engine, st := testEngine(t)
	tenant := uuid.Must(uuid.NewV7())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// First: expiring soon, fires.
	soon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("linx_cert_expiry_timestamp_seconds{} " + strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10) + "\n"))
	}))
	defer soon.Close()
	checkCertd(context.Background(), soon.Client(), soon.URL, engine, tenant, log)

	// Then: renewed, resolves.
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("linx_cert_expiry_timestamp_seconds{} " + strconv.FormatInt(time.Now().Add(60*24*time.Hour).Unix(), 10) + "\n"))
	}))
	defer healthy.Close()
	checkCertd(context.Background(), healthy.Client(), healthy.URL, engine, tenant, log)

	alerts, err := st.ListAlerts(context.Background(), tenant, "", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].Status != alert.StatusResolved {
		t.Fatalf("alerts = %+v", alerts)
	}
}

func TestCheckCertdNoCertYet(t *testing.T) {
	engine, st := testEngine(t)
	tenant := uuid.Must(uuid.NewV7())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("linx_cert_renewal_failures_total 0\n"))
	}))
	defer srv.Close()
	checkCertd(context.Background(), srv.Client(), srv.URL, engine, tenant, slog.New(slog.NewTextHandler(io.Discard, nil)))
	alerts, _ := st.ListAlerts(context.Background(), tenant, "", nil, 10)
	if len(alerts) != 0 {
		t.Fatalf("alerts = %+v, want none before a certificate exists", alerts)
	}
}

func TestCheckCertdUnreachable(t *testing.T) {
	engine, st := testEngine(t)
	tenant := uuid.Must(uuid.NewV7())
	checkCertd(context.Background(), &http.Client{Timeout: time.Second}, "http://127.0.0.1:1/metrics",
		engine, tenant, slog.New(slog.NewTextHandler(io.Discard, nil)))
	alerts, _ := st.ListAlerts(context.Background(), tenant, "", nil, 10)
	if len(alerts) != 0 {
		t.Fatalf("alerts = %+v, want none when certd can't be reached", alerts)
	}
}
