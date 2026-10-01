package clients

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libdns/libdns"

	"linxpbx.com/linx/internal/dnsapi"
)

func TestNew(t *testing.T) {
	if _, err := New(context.Background(), dnsapi.Cloudflare, dnsapi.Key{"token": "abcdefghijklmnop"}, dnsapi.Options{}); !errors.Is(err, dnsapi.ErrBuiltin) {
		t.Errorf("Cloudflare through New: %v", err)
	}
	if _, err := New(context.Background(), "gandi", dnsapi.Key{}, dnsapi.Options{}); err == nil {
		t.Error("a company Linx doesn't know: no error")
	}
	if _, err := New(context.Background(), dnsapi.Porkbun, dnsapi.Key{"api_key": "pk1_keykeykey"}, dnsapi.Options{}); err == nil {
		t.Error("half a Porkbun key: no error")
	}
	// Namecheap needs this network's address, never found by the library itself.
	if _, err := New(context.Background(), dnsapi.Namecheap, dnsapi.Key{"api_user": "me", "api_key": "0123456789abcdef"}, dnsapi.Options{}); err == nil {
		t.Error("Namecheap without the public address: no error")
	}
	for _, id := range []string{dnsapi.Route53, dnsapi.GoDaddy, dnsapi.Porkbun, dnsapi.DigitalOcean, dnsapi.Hetzner, dnsapi.DeSEC, dnsapi.OVH, dnsapi.Namecheap} {
		c, _ := dnsapi.Find(id)
		k := dnsapi.Key{}
		for _, f := range c.Fields {
			k[f.Key] = "value-0123456789"
			if len(f.Options) > 0 {
				k[f.Key] = f.Options[0].Value
			}
		}
		api, err := New(context.Background(), id, k, dnsapi.Options{PublicAddress: func(context.Context) (string, error) { return "203.0.113.9", nil }})
		if err != nil || api == nil {
			t.Errorf("%s: %v", id, err)
		}
	}
}

// fake is a DNS company's API: each request is recorded, and answered by
// the first route whose method and path (with query) match.
type fake struct {
	t      *testing.T
	mu     sync.Mutex
	routes []route
	seen   []string
	bodies []string
	auth   []string
}

type route struct {
	method, path string
	status       int
	body         string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	got := r.Method + " " + r.URL.RequestURI()
	f.seen = append(f.seen, got)
	f.bodies = append(f.bodies, string(b))
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	for i, rt := range f.routes {
		if rt.method+" "+rt.path == got {
			f.routes = slices.Delete(f.routes, i, i+1) // each answers once
			w.WriteHeader(rt.status)
			_, _ = io.WriteString(w, rt.body)
			return
		}
	}
	f.t.Errorf("unexpected request %s", got)
	w.WriteHeader(http.StatusTeapot)
}

func serve(t *testing.T, routes ...route) (*fake, string) {
	f := &fake{t: t, routes: routes}
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	t.Cleanup(func() {
		for _, r := range f.routes {
			t.Errorf("never asked: %s %s", r.method, r.path)
		}
	})
	return f, s.URL
}

func TestHetzner(t *testing.T) {
	ctx := context.Background()
	f, base := serve(t,
		route{"GET", "/zones/example.com/rrsets/pbx/A", 200, `{"rrset":{"name":"pbx","type":"A","records":[{"value":"1.2.3.4"}]}}`},
		// Set on an existing set: set_records.
		route{"GET", "/zones/example.com/rrsets/pbx/A", 200, `{"rrset":{"name":"pbx","type":"A","records":[{"value":"1.2.3.4"}]}}`},
		route{"POST", "/zones/example.com/rrsets/pbx/A/actions/set_records", 201, `{}`},
		// Set on a new one: created, TXT quoted.
		route{"GET", "/zones/example.com/rrsets/_acme-challenge.pbx/TXT", 404, `{"error":{"code":"not_found","message":"rrset not found"}}`},
		route{"GET", "/zones/example.com", 200, `{"zone":{"name":"example.com"}}`},
		route{"POST", "/zones/example.com/rrsets", 201, `{}`},
		// A zone this token can't see.
		route{"GET", "/zones/other.com/rrsets/@/A", 404, `{"error":{"code":"not_found","message":"not found"}}`},
		route{"GET", "/zones/other.com", 404, `{"error":{"code":"not_found","message":"zone not found"}}`},
		route{"DELETE", "/zones/example.com/rrsets/_acme-challenge.pbx/TXT", 404, `{}`},
		route{"GET", "/zones/example.com/rrsets/@/A", 401, `{"error":{"code":"unauthorized","message":"unable to authenticate"}}`},
	)
	h := &hetzner{http: http.DefaultClient, base: base, token: "tok", ttl: 300}
	if got, err := h.Get(ctx, "example.com", "pbx", dnsapi.TypeA); err != nil || !slices.Equal(got, []string{"1.2.3.4"}) {
		t.Fatalf("Get = %v, %v", got, err)
	}
	if err := h.Set(ctx, "example.com", "pbx", dnsapi.TypeA, []string{"5.6.7.8"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Set(ctx, "example.com", "_acme-challenge.pbx", dnsapi.TypeTXT, []string{"abc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Get(ctx, "other.com", "@", dnsapi.TypeA); err == nil || !strings.Contains(err.Error(), "can't see a zone other.com") {
		t.Errorf("unknown zone: %v", err)
	}
	if err := h.Delete(ctx, "example.com", "_acme-challenge.pbx", dnsapi.TypeTXT); err != nil {
		t.Errorf("deleting what isn't there: %v", err)
	}
	if _, err := h.Get(ctx, "example.com", "@", dnsapi.TypeA); err == nil || err.Error() != "Hetzner: unable to authenticate" {
		t.Errorf("refused: %v", err)
	}
	if f.bodies[2] != `{"records":[{"value":"5.6.7.8"}]}` {
		t.Errorf("set_records body %s", f.bodies[2])
	}
	if f.bodies[5] != `{"name":"_acme-challenge.pbx","records":[{"value":"\"abc\""}],"ttl":300,"type":"TXT"}` {
		t.Errorf("create body %s", f.bodies[5])
	}
	if f.auth[0] != "Bearer tok" {
		t.Errorf("auth %q", f.auth[0])
	}
}

func TestDigitalOcean(t *testing.T) {
	ctx := context.Background()
	list := "/domains/example.com/records?name=pbx.example.com&per_page=200&type=A"
	f, base := serve(t,
		route{"GET", list, 200, `{"domain_records":[{"id":1,"type":"A","name":"pbx","data":"1.2.3.4"},{"id":2,"type":"A","name":"pbx","data":"9.9.9.9"}]}`},
		route{"PUT", "/domains/example.com/records/1", 200, `{}`},
		route{"DELETE", "/domains/example.com/records/2", 204, ``},
		route{"GET", "/domains/example.com/records?name=example.com&per_page=200&type=A", 200, `{"domain_records":[]}`},
		route{"POST", "/domains/example.com/records", 201, `{}`},
		route{"GET", "/domains/nothere.com/records?name=nothere.com&per_page=200&type=A", 404, `{"id":"not_found","message":"The resource you were accessing could not be found."}`},
	)
	d := &digitalOcean{http: http.DefaultClient, base: base, token: "tok", ttl: 300}
	if err := d.Set(ctx, "example.com", "pbx", dnsapi.TypeA, []string{"5.6.7.8"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Set(ctx, "example.com", "@", dnsapi.TypeA, []string{"5.6.7.8"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Get(ctx, "nothere.com", "@", dnsapi.TypeA); err == nil || !strings.Contains(err.Error(), "could not be found") {
		t.Errorf("unknown domain: %v", err)
	}
	if f.bodies[1] != `{"data":"5.6.7.8","name":"pbx","ttl":300,"type":"A"}` || f.bodies[4] != `{"data":"5.6.7.8","name":"@","ttl":300,"type":"A"}` {
		t.Errorf("bodies %q", f.bodies)
	}
}

func TestRoute53(t *testing.T) {
	ctx := context.Background()
	zones := `<ListHostedZonesByNameResponse><HostedZones>
	  <HostedZone><Id>/hostedzone/ZPRIV</Id><Name>example.com.</Name><Config><PrivateZone>true</PrivateZone></Config></HostedZone>
	  <HostedZone><Id>/hostedzone/Z123</Id><Name>example.com.</Name><Config><PrivateZone>false</PrivateZone></Config></HostedZone>
	</HostedZones></ListHostedZonesByNameResponse>`
	f, base := serve(t,
		route{"GET", "/hostedzonesbyname?dnsname=example.com&maxitems=10", 200, zones},
		// Get: the list goes on past a name that isn't there.
		route{"GET", "/hostedzone/Z123/rrset?maxitems=1&name=_acme-challenge.example.com.&type=TXT", 200,
			`<ListResourceRecordSetsResponse><ResourceRecordSets><ResourceRecordSet><Name>www.example.com.</Name><Type>A</Type><TTL>300</TTL>
			<ResourceRecords><ResourceRecord><Value>1.1.1.1</Value></ResourceRecord></ResourceRecords></ResourceRecordSet></ResourceRecordSets></ListResourceRecordSetsResponse>`},
		route{"POST", "/hostedzone/Z123/rrset", 200, `<ChangeResourceRecordSetsResponse/>`},
		// Delete sends the set as it is.
		route{"GET", "/hostedzone/Z123/rrset?maxitems=1&name=_acme-challenge.example.com.&type=TXT", 200,
			`<ListResourceRecordSetsResponse><ResourceRecordSets><ResourceRecordSet><Name>_acme-challenge.example.com.</Name><Type>TXT</Type><TTL>60</TTL>
			<ResourceRecords><ResourceRecord><Value>"abc"</Value></ResourceRecord></ResourceRecords></ResourceRecordSet></ResourceRecordSets></ListResourceRecordSetsResponse>`},
		route{"POST", "/hostedzone/Z123/rrset", 200, `<ChangeResourceRecordSetsResponse/>`},
		route{"GET", "/hostedzonesbyname?dnsname=other.com&maxitems=10", 403,
			`<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>User is not authorized</Message></Error></ErrorResponse>`},
	)
	r := &route53{http: http.DefaultClient, base: base, keyID: "AKID", secret: "secretsecret", ttl: 300,
		now: func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }}
	if got, err := r.Get(ctx, "example.com", "_acme-challenge", dnsapi.TypeTXT); err != nil || got != nil {
		t.Fatalf("Get of a name that isn't there = %v, %v", got, err)
	}
	if err := r.Set(ctx, "example.com", "_acme-challenge", dnsapi.TypeTXT, []string{"abc"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "example.com", "_acme-challenge", dnsapi.TypeTXT); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "other.com", "@", dnsapi.TypeA); err == nil || err.Error() != "Route 53: User is not authorized" {
		t.Errorf("refused: %v", err)
	}
	for _, want := range []string{
		`<Action>UPSERT</Action><ResourceRecordSet><Name>_acme-challenge.example.com.</Name><Type>TXT</Type><TTL>300</TTL><ResourceRecords><ResourceRecord><Value>&#34;abc&#34;</Value>`,
	} {
		if !strings.Contains(f.bodies[2], want) || !strings.HasPrefix(f.bodies[2], `<?xml`) ||
			!strings.Contains(f.bodies[2], `<ChangeResourceRecordSetsRequest xmlns="https://route53.amazonaws.com/doc/2013-04-01/">`) {
			t.Errorf("UPSERT body %s", f.bodies[2])
		}
	}
	if !strings.Contains(f.bodies[4], `<Action>DELETE</Action><ResourceRecordSet><Name>_acme-challenge.example.com.</Name><Type>TXT</Type><TTL>60</TTL>`) {
		t.Errorf("DELETE body %s", f.bodies[4])
	}
	if !strings.HasPrefix(f.auth[0], "AWS4-HMAC-SHA256 Credential=AKID/20261001/us-east-1/route53/aws4_request, SignedHeaders=host;x-amz-date, Signature=") {
		t.Errorf("auth %q", f.auth[0])
	}
}

// The signatures are AWS's own signer's (aws-sdk-go-v2 v1.39.1, checked
// 2026-10-01) for the same requests.
func TestSignV4(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 34, 56, 0, time.UTC)
	for u, sig := range map[string]string{
		"https://route53.amazonaws.com/2013-04-01/hostedzonesbyname?maxitems=10&dnsname=example.com":                                  "bd0c91ca75aac236501138908f3a4bbd0e47cefdbb1062eef9adae2a33f0a1ef",
		"https://route53.amazonaws.com/2013-04-01/hostedzone/Z123ABC/rrset?name=_acme-challenge.pbx.example.com.&type=TXT&maxitems=1": "4a001fda992639fe65480584124a3740ae01115f51fed9a04759f7305badbe3d",
	} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		signV4(req, nil, "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", "route53", now)
		want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261001/us-east-1/route53/aws4_request, SignedHeaders=host;x-amz-date, Signature=" + sig
		if got := req.Header.Get("Authorization"); got != want {
			t.Errorf("%s:\n got %s\nwant %s", u, got, want)
		}
	}
}

// errProvider fails every call with err.
type errProvider struct{ err error }

func (e errProvider) GetRecords(context.Context, string) ([]libdns.Record, error) { return nil, e.err }
func (e errProvider) SetRecords(context.Context, string, []libdns.Record) ([]libdns.Record, error) {
	return nil, e.err
}
func (e errProvider) DeleteRecords(context.Context, string, []libdns.Record) ([]libdns.Record, error) {
	return nil, e.err
}

// Namecheap's API takes the key in the web address: never in an error.
func TestLibdnsErrorsHideTheKey(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	ue := &url.Error{Op: "Get", URL: "https://api.namecheap.com/xml.response?ApiKey=" + secret, Err: errors.New("connection refused")}
	a := &libdnsAPI{p: errProvider{ue}, name: "Namecheap", key: dnsapi.Key{"api_user": "me", "api_key": secret}}
	_, err := a.Get(context.Background(), "example.com", "@", dnsapi.TypeA)
	if err == nil || strings.Contains(err.Error(), secret) || err.Error() != "Namecheap: Get: connection refused" {
		t.Errorf("url error: %v", err)
	}
	a.p = errProvider{errors.New("API error: bad key " + secret)}
	if err := a.Set(context.Background(), "example.com", "@", dnsapi.TypeA, []string{"192.0.2.1"}); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("echoed key: %v", err)
	}
}
