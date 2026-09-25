package certs

import (
	"errors"
	"testing"
	"time"
)

func TestServingCertFollowsRenewal(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	sc := &ServingCert{Dir: s.Dir}
	if _, err := sc.Current(); !errors.Is(err, ErrNoCertificate) {
		t.Fatalf("before any deploy: %v, want ErrNoCertificate", err)
	}

	start := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	for i := range 3 {
		exp := start.AddDate(0, 3, i)
		chain, key := selfSigned(t, "*.pbx.example.com", exp)
		if err := s.Deploy(chain, key, Meta{Names: []string{"*.pbx.example.com"}, NotAfter: exp, IssuedAt: start.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		c, err := sc.Current()
		if err != nil {
			t.Fatal(err)
		}
		if !c.Leaf.NotAfter.Equal(exp) {
			t.Fatalf("deploy %d: serving a certificate expiring %v, want %v", i, c.Leaf.NotAfter, exp)
		}
		again, _ := sc.Current()
		if again != c {
			t.Fatalf("deploy %d: reloaded an unchanged certificate", i)
		}
	}
}
