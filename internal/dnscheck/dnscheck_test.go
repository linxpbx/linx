package dnscheck

import (
	"context"
	"net"
	"testing"

	"github.com/miekg/dns"
)

func serve(t *testing.T, records map[string][]dns.RR) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(q)
		rrs, ok := records[q.Question[0].Name]
		if !ok {
			m.Rcode = dns.RcodeNameError
		}
		m.Answer = rrs
		_ = w.WriteMsg(m)
	})}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return pc.LocalAddr().String()
}

func rr(t *testing.T, s string) dns.RR {
	r, err := dns.NewRR(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLookupA(t *testing.T) {
	addr := serve(t, map[string][]dns.RR{
		"meet.example.com.":  {rr(t, "meet.example.com. 60 IN A 203.0.113.5"), rr(t, "meet.example.com. 60 IN A 203.0.113.5")},
		"two.example.com.":   {rr(t, "two.example.com. 60 IN A 198.51.100.7"), rr(t, "two.example.com. 60 IN A 203.0.113.5")},
		"empty.example.com.": nil,
	})
	r := Resolver{Server: addr}
	ctx := context.Background()
	for name, want := range map[string]string{
		"MEET.example.com": "[203.0.113.5]", "two.example.com": "[198.51.100.7 203.0.113.5]",
		"empty.example.com": "[]", "missing.example.com": "[]",
	} {
		got, err := r.LookupA(ctx, name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if s := fmtList(got); s != want {
			t.Errorf("%s: %s, want %s", name, s, want)
		}
	}
	if _, err := (Resolver{Server: "127.0.0.1:1", Timeout: 200e6}).LookupA(ctx, "meet.example.com"); err == nil {
		t.Error("no server: no error")
	}
}

func fmtList(l []string) string {
	s := "["
	for i, a := range l {
		if i > 0 {
			s += " "
		}
		s += a
	}
	return s + "]"
}
