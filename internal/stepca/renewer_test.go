package stepca

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type fakeIssuer struct {
	calls atomic.Int32
	fail  atomic.Bool
	life  time.Duration
}

func (f *fakeIssuer) Issue(context.Context, string, []string) (*tls.Certificate, error) {
	f.calls.Add(1)
	if f.fail.Load() {
		return nil, errors.New("CA down")
	}
	now := time.Now()
	return &tls.Certificate{Leaf: &x509.Certificate{NotBefore: now, NotAfter: now.Add(f.life)}}, nil
}

func TestRenewerFailsClosedUntilIssued(t *testing.T) {
	r := &Renewer{Issuer: &fakeIssuer{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if c, err := r.GetCertificate(nil); c != nil || err == nil {
		t.Fatal("GetCertificate must fail before the first certificate")
	}
}

func TestRenewerRenewsAtTwoThirds(t *testing.T) {
	iss := &fakeIssuer{life: 300 * time.Millisecond}
	r := &Renewer{Issuer: iss, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for iss.calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if n := iss.calls.Load(); n < 3 {
		t.Fatalf("issued %d times in 2 s with a 300 ms lifetime; want renewals", n)
	}
	if c, err := r.GetCertificate(nil); c == nil || err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
}

func TestRenewIn(t *testing.T) {
	r := &Renewer{}
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	if d := r.renewIn(now); d != 0 {
		t.Errorf("no certificate: renewIn = %v, want 0", d)
	}
	r.cert.Store(&tls.Certificate{Leaf: &x509.Certificate{NotBefore: now, NotAfter: now.Add(24 * time.Hour)}})
	if d := r.renewIn(now); d != 16*time.Hour {
		t.Errorf("fresh 24 h certificate: renewIn = %v, want 16h", d)
	}
	if d := r.renewIn(now.Add(20 * time.Hour)); d != 0 {
		t.Errorf("past two-thirds: renewIn = %v, want 0", d)
	}
}
