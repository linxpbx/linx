package installer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// fakeCompanyHost is how the containers reach this machine, where the
// fake company runs (Docker's host-gateway, on Linux and Docker Desktop).
const fakeCompanyHost = "host.docker.internal"

// fakeCompany stands in for a DNS company in the install suite's DNS-01
// run: the parts of Hetzner's API that Linx's own client uses, over HTTPS
// with a certificate from the test's own authority, and the zone's name
// server (SOA, A and TXT) answering from the same records, which Pebble
// and certd both ask.
type fakeCompany struct {
	zone, token string
	caPEM       []byte
	api         *httptest.Server
	dnsPort     int

	mu     sync.Mutex
	rrsets map[string][]string // "name TYPE" (relative, "@" for the zone) → values as sent (TXT quoted)
}

func startFakeCompany(t *testing.T, zone, token string) *fakeCompany {
	f := &fakeCompany{zone: zone, token: token, rrsets: map[string][]string{}}
	ca, caKey := testCA(t)
	f.caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{fakeCompanyHost},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	// On every address: the containers come from Docker's networks.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	f.api = &httptest.Server{Listener: ln, Config: &http.Server{Handler: f.handler(t), ReadHeaderTimeout: 10 * time.Second},
		TLS: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}}
	f.api.StartTLS()
	t.Cleanup(f.api.Close)

	// UDP and TCP on one port (Pebble asks over TCP).
	var pc net.PacketConn
	var tl net.Listener
	for range 20 {
		if pc, err = net.ListenPacket("udp", "0.0.0.0:0"); err != nil {
			t.Fatal(err)
		}
		f.dnsPort = pc.LocalAddr().(*net.UDPAddr).Port
		if tl, err = net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(f.dnsPort)); err == nil {
			break
		}
		pc.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, srv := range []*dns.Server{{PacketConn: pc, Handler: f}, {Listener: tl, Handler: f}} {
		go func() { _ = srv.ActivateAndServe() }()
		t.Cleanup(func() { _ = srv.Shutdown() })
	}
	return f
}

func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Linx install suite, DNS company"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	return ca, key
}

// apiURL is the API's address as the containers reach it.
func (f *fakeCompany) apiURL() string {
	return "https://" + fakeCompanyHost + ":" + strconv.Itoa(f.api.Listener.Addr().(*net.TCPAddr).Port) + "/v1"
}

// dnsAddr is the name server's address as the containers reach it.
func (f *fakeCompany) dnsAddr() string { return fakeCompanyHost + ":" + strconv.Itoa(f.dnsPort) }

// values are name's records of type typ, unquoted.
func (f *fakeCompany) values(name, typ string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, v := range f.rrsets[name+" "+typ] {
		if u, err := strconv.Unquote(v); err == nil {
			v = u
		}
		out = append(out, v)
	}
	return out
}

type fakeRRSet struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	TTL     int    `json:"ttl,omitempty"`
	Records []struct {
		Value string `json:"value"`
	} `json:"records"`
}

func (f *fakeCompany) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	notFound := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"not found"}}`))
	}
	set := func(name, typ string, rs fakeRRSet) {
		var vs []string
		for _, r := range rs.Records {
			vs = append(vs, r.Value)
		}
		f.mu.Lock()
		f.rrsets[name+" "+typ] = vs
		f.mu.Unlock()
	}
	ok := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"unable to authenticate"}}`))
			return false
		}
		if r.PathValue("zone") != f.zone {
			notFound(w)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /v1/zones/{zone}", func(w http.ResponseWriter, r *http.Request) {
		if ok(w, r) {
			_ = json.NewEncoder(w).Encode(map[string]any{"zone": map[string]string{"name": f.zone}})
		}
	})
	mux.HandleFunc("GET /v1/zones/{zone}/rrsets/{name}/{type}", func(w http.ResponseWriter, r *http.Request) {
		if !ok(w, r) {
			return
		}
		f.mu.Lock()
		vs, found := f.rrsets[r.PathValue("name")+" "+r.PathValue("type")]
		f.mu.Unlock()
		if !found {
			notFound(w)
			return
		}
		rs := map[string]any{"name": r.PathValue("name"), "type": r.PathValue("type")}
		var recs []map[string]string
		for _, v := range vs {
			recs = append(recs, map[string]string{"value": v})
		}
		rs["records"] = recs
		_ = json.NewEncoder(w).Encode(map[string]any{"rrset": rs})
	})
	mux.HandleFunc("POST /v1/zones/{zone}/rrsets", func(w http.ResponseWriter, r *http.Request) {
		var rs fakeRRSet
		if !ok(w, r) || json.NewDecoder(r.Body).Decode(&rs) != nil {
			return
		}
		set(rs.Name, rs.Type, rs)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST /v1/zones/{zone}/rrsets/{name}/{type}/actions/set_records", func(w http.ResponseWriter, r *http.Request) {
		var rs fakeRRSet
		if !ok(w, r) || json.NewDecoder(r.Body).Decode(&rs) != nil {
			return
		}
		set(r.PathValue("name"), r.PathValue("type"), rs)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("DELETE /v1/zones/{zone}/rrsets/{name}/{type}", func(w http.ResponseWriter, r *http.Request) {
		if !ok(w, r) {
			return
		}
		f.mu.Lock()
		delete(f.rrsets, r.PathValue("name")+" "+r.PathValue("type"))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the DNS company was asked %s %s", r.Method, r.URL.Path)
		notFound(w)
	})
	return mux
}

// ServeDNS is the zone's name server.
func (f *fakeCompany) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	apex := dns.Fqdn(f.zone)
	soa := &dns.SOA{Hdr: dns.RR_Header{Name: apex, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60},
		Ns: "ns." + apex, Mbox: "hostmaster." + apex, Serial: 1, Refresh: 60, Retry: 60, Expire: 60, Minttl: 1}
	if len(req.Question) != 1 {
		m.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(m)
		return
	}
	q := req.Question[0]
	name := strings.ToLower(q.Name)
	if name != apex && !strings.HasSuffix(name, "."+apex) {
		m.Rcode = dns.RcodeRefused
		_ = w.WriteMsg(m)
		return
	}
	rel := "@"
	if name != apex {
		rel = strings.TrimSuffix(name, "."+apex)
	}
	hdr := dns.RR_Header{Name: q.Name, Class: dns.ClassINET, Ttl: 1}
	switch q.Qtype {
	case dns.TypeSOA:
		if name == apex {
			m.Answer = append(m.Answer, soa)
		}
	case dns.TypeTXT:
		for _, v := range f.values(rel, "TXT") {
			h := hdr
			h.Rrtype = dns.TypeTXT
			m.Answer = append(m.Answer, &dns.TXT{Hdr: h, Txt: []string{v}})
		}
	case dns.TypeA:
		for _, v := range f.values(rel, "A") {
			h := hdr
			h.Rrtype = dns.TypeA
			m.Answer = append(m.Answer, &dns.A{Hdr: h, A: net.ParseIP(v)})
		}
	}
	if len(m.Answer) == 0 {
		m.Ns = []dns.RR{soa}
	}
	_ = w.WriteMsg(m)
}
