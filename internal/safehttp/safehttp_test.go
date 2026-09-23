package safehttp

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func allow(prefixes []string, hosts ...string) AllowlistSource {
	var l Allowlist
	for _, p := range prefixes {
		l.Prefixes = append(l.Prefixes, netip.MustParsePrefix(p))
	}
	l.Hosts = hosts
	return func(context.Context) (Allowlist, error) { return l, nil }
}

func TestPolicyCheck(t *testing.T) {
	own := []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")}
	tests := []struct {
		name      string
		host      string
		addrs     []string
		allowlist AllowlistSource
		wantErr   bool
		wantAllow bool // the refusal can be fixed by allowlisting
	}{
		{"public v4", "example.com", []string{"93.184.215.14"}, nil, false, false},
		{"public v6", "example.com", []string{"2606:2800:21f:cb07:6820:80da:af6b:8b2c"}, nil, false, false},
		{"rfc1918", "nas", []string{"192.168.1.10"}, nil, true, true},
		{"rfc1918 allowlisted by range", "nas", []string{"192.168.1.10"}, allow([]string{"192.168.1.0/24"}), false, false},
		{"rfc1918 allowlisted by host", "nas.home.arpa", []string{"192.168.1.10"}, allow(nil, "NAS.home.arpa."), false, false},
		{"other host not allowlisted", "evil.example", []string{"192.168.1.10"}, allow(nil, "nas.home.arpa"), true, true},
		{"cgnat", "ts", []string{"100.100.1.1"}, nil, true, true},
		{"ula", "x", []string{"fd12::1"}, nil, true, true},
		{"link-local v6", "x", []string{"fe80::1%eth0"}, nil, true, true},
		{"loopback", "localhost", []string{"127.0.0.1"}, allow([]string{"127.0.0.0/8"}), true, false},
		{"loopback v6", "localhost", []string{"::1"}, allow([]string{"::/0"}), true, false},
		{"metadata even if link-local allowlisted", "m", []string{"169.254.169.254"}, allow([]string{"169.254.0.0/16"}), true, false},
		{"metadata by host allowlist", "metadata.google.internal", []string{"169.254.169.254"}, allow(nil, "metadata.google.internal"), true, false},
		{"unspecified", "x", []string{"0.0.0.0"}, allow([]string{"0.0.0.0/0"}), true, false},
		{"own network even if allowlisted", "pg", []string{"172.20.0.5"}, allow([]string{"172.16.0.0/12"}), true, false},
		{"v4-mapped private", "x", []string{"::ffff:10.0.0.1"}, nil, true, true},
		{"v4-mapped loopback", "x", []string{"::ffff:127.0.0.1"}, allow([]string{"::/0"}), true, false},
		{"nat64 metadata", "x", []string{"64:ff9b::a9fe:a9fe"}, allow([]string{"::/0"}), true, false},
		{"nat64 public", "x", []string{"64:ff9b::5db8:d70e"}, nil, false, false},
		{"6to4 private", "x", []string{"2002:c0a8:0101::1"}, nil, true, true},
		{"multicast", "x", []string{"224.0.0.1"}, allow([]string{"0.0.0.0/0"}), true, false},
		{"broadcast", "x", []string{"255.255.255.255"}, allow([]string{"0.0.0.0/0"}), true, false},
		{"one private answer taints all", "rebind", []string{"93.184.215.14", "10.0.0.1"}, nil, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var addrs []netip.Addr
			for _, a := range tt.addrs {
				addrs = append(addrs, netip.MustParseAddr(a))
			}
			p := Policy{Own: own, Allowlist: tt.allowlist}
			err := p.Check(context.Background(), tt.host, addrs)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Check() error = %v, want error %v", err, tt.wantErr)
			}
			var b *BlockedError
			if err != nil {
				if !errors.As(err, &b) {
					t.Fatalf("error %T isn't a *BlockedError", err)
				}
				if b.Allowlistable != tt.wantAllow {
					t.Errorf("Allowlistable = %v, want %v", b.Allowlistable, tt.wantAllow)
				}
			}
		})
	}
}

func TestPolicyAllowlistErrorFailsClosed(t *testing.T) {
	p := Policy{Allowlist: func(context.Context) (Allowlist, error) { return Allowlist{}, errors.New("db down") }}
	if err := p.Check(context.Background(), "nas", []netip.Addr{netip.MustParseAddr("10.0.0.1")}); err == nil {
		t.Error("Check() succeeded although the allowlist couldn't be read")
	}
	// Public addresses don't need the allowlist.
	if err := p.Check(context.Background(), "x", []netip.Addr{netip.MustParseAddr("1.1.1.1")}); err != nil {
		t.Errorf("Check(public) = %v", err)
	}
}

func TestCheckURL(t *testing.T) {
	for _, u := range []string{"https://example.com/hook", "https://example.com:8443/a?b=c", "https://[2001:db8::1]/x"} {
		if _, err := CheckURL(u); err != nil {
			t.Errorf("CheckURL(%q) = %v", u, err)
		}
	}
	for _, u := range []string{"http://example.com/", "ftp://example.com", "https:///path", "https://user:pw@example.com/", "example.com/hook", "https:opaque", "://bad"} {
		if _, err := CheckURL(u); err == nil {
			t.Errorf("CheckURL(%q) succeeded", u)
		}
	}
}

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return f[host], nil
}

func testServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, pool
}

func TestClientBlocksLoopback(t *testing.T) {
	called := false
	srv, roots := testServer(t, func(http.ResponseWriter, *http.Request) { called = true })
	c := NewClient(Policy{}, Options{RootCAs: roots})
	_, err := c.Get(srv.URL)
	var b *BlockedError
	if !errors.As(err, &b) {
		t.Fatalf("Get() error = %v, want *BlockedError", err)
	}
	if called {
		t.Error("the server was reached")
	}
}

func TestClientBlocksResolvedPrivateAddress(t *testing.T) {
	res := fakeResolver{"nas.example": {netip.MustParseAddr("10.1.2.3")}}
	c := NewClient(Policy{}, Options{Resolver: res})
	_, err := c.Get("https://nas.example/")
	var b *BlockedError
	if !errors.As(err, &b) || !b.Allowlistable {
		t.Fatalf("Get() error = %v, want an allowlistable *BlockedError", err)
	}
	if !strings.Contains(Describe(err), "outbound allowlist") {
		t.Errorf("Describe() = %q", Describe(err))
	}
}

func TestClientRefusesHTTP(t *testing.T) {
	c := NewClient(Policy{allowLoopback: true}, Options{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("plain http server reached")
	}))
	defer srv.Close()
	if _, err := c.Get(srv.URL); err == nil || !strings.Contains(Describe(err), "https://") {
		t.Errorf("Get(http) error = %v", err)
	}
}

func TestClientConnectsToCheckedAddressAndVerifiesTLS(t *testing.T) {
	srv, roots := testServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	// httptest's certificate is for example.com and 127.0.0.1.
	res := fakeResolver{"example.com": {netip.MustParseAddr("127.0.0.1")}}
	c := NewClient(Policy{allowLoopback: true}, Options{Resolver: res, RootCAs: roots})
	resp, err := c.Get("https://example.com:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Errorf("status = %d", resp.StatusCode)
	}

	// Without the test root the certificate isn't trusted.
	c = NewClient(Policy{allowLoopback: true}, Options{Resolver: res})
	_, err = c.Get("https://example.com:" + port + "/")
	if err == nil || !strings.Contains(Describe(err), "certificate") {
		t.Errorf("untrusted certificate: error = %v (%q)", err, Describe(err))
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	srv, roots := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			t.Error("redirect followed")
		}
		http.Redirect(w, r, "/next", http.StatusFound)
	})
	c := NewClient(Policy{allowLoopback: true}, Options{RootCAs: roots})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302", resp.StatusCode)
	}
}

func TestClientIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	srv, roots := testServer(t, func(w http.ResponseWriter, r *http.Request) {})
	c := NewClient(Policy{allowLoopback: true}, Options{RootCAs: roots})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get() = %v (was the proxy used?)", err)
	}
	resp.Body.Close()
}

func TestAllowlistable(t *testing.T) {
	for _, p := range []string{"192.168.1.0/24", "10.0.0.0/8", "192.168.1.5/32", "fd00::/8", "100.64.0.0/10", "169.254.10.0/24"} {
		if !Allowlistable(netip.MustParsePrefix(p)) {
			t.Errorf("Allowlistable(%s) = false", p)
		}
	}
	for _, p := range []string{"0.0.0.0/0", "8.0.0.0/8", "192.168.0.0/15", "127.0.0.0/8", "::/0", "224.0.0.0/4", "172.0.0.0/8", "169.254.169.254/32"} {
		if Allowlistable(netip.MustParsePrefix(p)) {
			t.Errorf("Allowlistable(%s) = true", p)
		}
	}
}

func TestCheckHost(t *testing.T) {
	res := fakeResolver{"nas.example": {netip.MustParseAddr("10.1.2.3")}, "pub.example": {netip.MustParseAddr("1.1.1.1")}}
	p := Policy{}
	if err := p.CheckHost(context.Background(), res, "nas.example"); err == nil {
		t.Error("CheckHost(private) succeeded")
	}
	if err := p.CheckHost(context.Background(), res, "pub.example"); err != nil {
		t.Errorf("CheckHost(public) = %v", err)
	}
	if err := p.CheckHost(context.Background(), res, "unknown.example"); err != nil {
		t.Errorf("CheckHost(unresolvable) = %v", err)
	}
	if err := p.CheckHost(context.Background(), res, "169.254.169.254"); err == nil {
		t.Error("CheckHost(metadata literal) succeeded")
	}
}
