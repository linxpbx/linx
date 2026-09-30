package reach

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

func TestRecords(t *testing.T) {
	public := netip.MustParseAddr("94.200.1.10")
	dns := map[string][]string{
		"example.com":      {"94.200.1.10"},
		"turn.example.com": {"94.200.1.9"},
	}
	checker := func(door string, home netip.Addr) *Checker {
		return &Checker{Domain: "example.com", Door: door, Home: home, Kept: map[string]bool{UseSIP: true},
			NameServers: func(context.Context, string) (string, []string, error) {
				return "example.com", []string{"curitiba.ns.porkbun.com", "maceio.ns.porkbun.com"}, nil
			},
			Lookup: func(_ context.Context, name string) ([]string, error) {
				if name == "sip.example.com" {
					return nil, errors.New("timeout")
				}
				return dns[name], nil
			},
			PublicIP: func(context.Context) (netip.Addr, error) { return public, nil },
			Now:      func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) },
		}
	}
	ctx := context.Background()

	// At home, behind a proxy: the public names at the public address,
	// sip. at this server.
	r := checker("proxy", netip.MustParseAddr("192.168.1.212")).Records(ctx)
	if r.Zone != "example.com" || r.Company != "Porkbun" || len(r.NameServers) != 2 || len(r.Records) != 3 || r.CheckedAt.IsZero() {
		t.Fatalf("records %+v", r)
	}
	for i, want := range []Record{
		{Use: UseWeb, Name: "example.com", Type: "A", Value: "94.200.1.10", State: RecordOK},
		{Use: UseTURN, Name: "turn.example.com", Type: "A", Value: "94.200.1.10", State: RecordWrong},
		{Use: UseSIP, Name: "sip.example.com", Type: "A", Value: "192.168.1.212", State: RecordError, Kept: true},
	} {
		got := r.Records[i]
		if got.Use != want.Use || got.Name != want.Name || got.Type != want.Type || got.Value != want.Value || got.State != want.State || got.Kept != want.Kept {
			t.Errorf("record %d: %+v, want %+v", i, got, want)
		}
	}

	// A rented server: no sip.; home only: every name at this server.
	if r := checker("linx-443", netip.MustParseAddr("127.0.0.1")).Records(ctx); len(r.Records) != 2 {
		t.Errorf("rented: %+v", r.Records)
	}
	if r := checker("home-only", netip.MustParseAddr("192.168.1.212")).Records(ctx); len(r.Records) != 3 || r.Records[0].Value != "192.168.1.212" || r.Records[0].State != RecordWrong {
		t.Errorf("home only: %+v", r.Records)
	}

	// No public address to compare, and not in DNS yet.
	c := checker("proxy", netip.Addr{})
	c.PublicIP = func(context.Context) (netip.Addr, error) { return netip.Addr{}, errors.New("offline") }
	delete(dns, "example.com")
	r = c.Records(ctx)
	if len(r.Records) != 2 || r.Records[0].State != RecordMissing || r.Records[1].State != RecordUnknown || r.Records[1].Value != "" {
		t.Errorf("no public address: %+v", r.Records)
	}

	// Name servers not found: still the list.
	c.NameServers = func(context.Context, string) (string, []string, error) { return "", nil, errors.New("no zone") }
	if r := c.Records(ctx); r.Company != "" || r.NameServers == nil || len(r.Records) != 2 {
		t.Errorf("no name servers: %+v", r)
	}
}
