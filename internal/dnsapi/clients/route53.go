package clients

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"linxpbx.com/linx/internal/dnsapi"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// route53API is Route 53's one global endpoint (it signs as us-east-1).
const route53API = "https://route53.amazonaws.com/2013-04-01"

const (
	route53Region  = "us-east-1"
	route53Service = "route53"
)

// route53 is Linx's own Route 53 client: Amazon's SDK adds 6 MB to
// linx-certd for three calls. Requests are signed with Signature Version 4.
type route53 struct {
	http          *http.Client
	base          string
	keyID, secret string
	ttl           int // seconds
	now           func() time.Time
	zones         map[string]string // zone name → hosted zone path ("/hostedzone/Z123")
}

type r53RRSet struct {
	Name   string `xml:"Name"`
	Type   string `xml:"Type"`
	TTL    int    `xml:"TTL"`
	Values []struct {
		Value string `xml:"Value"`
	} `xml:"ResourceRecords>ResourceRecord"`
}

func (r *route53) zoneID(ctx context.Context, zone string) (string, error) {
	if id, ok := r.zones[zone]; ok {
		return id, nil
	}
	var res struct {
		Zones []struct {
			ID      string `xml:"Id"`
			Name    string `xml:"Name"`
			Private bool   `xml:"Config>PrivateZone"`
		} `xml:"HostedZones>HostedZone"`
	}
	q := url.Values{"dnsname": {zone}, "maxitems": {"10"}}
	if err := r.do(ctx, http.MethodGet, "/hostedzonesbyname", q, nil, &res); err != nil {
		return "", err
	}
	for _, z := range res.Zones {
		if strings.EqualFold(z.Name, zone+".") && !z.Private {
			if r.zones == nil {
				r.zones = map[string]string{}
			}
			r.zones[zone] = z.ID
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("Route 53: no public hosted zone %s for this key", zone)
}

func (r *route53) rrset(ctx context.Context, zone, name, typ string) (string, *r53RRSet, error) {
	id, err := r.zoneID(ctx, zone)
	if err != nil {
		return "", nil, err
	}
	fq := dnsapi.Absolute(name, zone) + "."
	var res struct {
		Sets []r53RRSet `xml:"ResourceRecordSets>ResourceRecordSet"`
	}
	q := url.Values{"name": {fq}, "type": {typ}, "maxitems": {"1"}}
	if err := r.do(ctx, http.MethodGet, id+"/rrset", q, nil, &res); err != nil {
		return id, nil, err
	}
	// The list starts at name and type, but goes on past them when they
	// aren't there.
	if len(res.Sets) == 0 || !strings.EqualFold(res.Sets[0].Name, fq) || res.Sets[0].Type != typ {
		return id, nil, nil
	}
	return id, &res.Sets[0], nil
}

func (r *route53) Get(ctx context.Context, zone, name, typ string) ([]string, error) {
	_, set, err := r.rrset(ctx, zone, name, typ)
	if err != nil || set == nil {
		return nil, err
	}
	out := make([]string, len(set.Values))
	for i, v := range set.Values {
		out[i] = unquoteTXT(typ, v.Value)
	}
	return out, nil
}

func (r *route53) Set(ctx context.Context, zone, name, typ string, values []string) error {
	if len(values) == 0 {
		return r.Delete(ctx, zone, name, typ)
	}
	id, err := r.zoneID(ctx, zone)
	if err != nil {
		return err
	}
	set := r53RRSet{Name: dnsapi.Absolute(name, zone) + ".", Type: typ, TTL: r.ttl}
	for _, v := range values {
		set.Values = append(set.Values, struct {
			Value string `xml:"Value"`
		}{quoteTXT(typ, v)})
	}
	return r.change(ctx, id, "UPSERT", set)
}

func (r *route53) Delete(ctx context.Context, zone, name, typ string) error {
	// Route 53 deletes a record set only as it is, TTL and all.
	id, set, err := r.rrset(ctx, zone, name, typ)
	if err != nil || set == nil {
		return err
	}
	return r.change(ctx, id, "DELETE", *set)
}

func (r *route53) change(ctx context.Context, id, action string, set r53RRSet) error {
	type change struct {
		Action string   `xml:"Action"`
		Set    r53RRSet `xml:"ResourceRecordSet"`
	}
	body := struct {
		XMLName xml.Name `xml:"https://route53.amazonaws.com/doc/2013-04-01/ ChangeResourceRecordSetsRequest"`
		Changes []change `xml:"ChangeBatch>Changes>Change"`
	}{Changes: []change{{action, set}}}
	return r.do(ctx, http.MethodPost, id+"/rrset", nil, body, nil)
}

func (r *route53) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		b, err := xml.Marshal(body)
		if err != nil {
			return err
		}
		payload = append([]byte(xml.Header), b...)
	}
	u := r.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/xml")
	}
	signV4(req, payload, r.keyID, r.secret, route53Region, route53Service, r.now())
	resp, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("Route 53: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `xml:"Error>Message"`
			Other   string `xml:"Message"` // InvalidChangeBatch's form
		}
		if xml.Unmarshal(b, &e) == nil && (e.Message != "" || e.Other != "") {
			return fmt.Errorf("Route 53: %s", strings.TrimSpace(e.Message+" "+e.Other))
		}
		return fmt.Errorf("Route 53: %s", resp.Status)
	}
	if out != nil {
		if err := xml.Unmarshal(b, out); err != nil {
			return errors.New("Route 53: unexpected answer")
		}
	}
	return nil
}

// signV4 signs req with AWS Signature Version 4 (docs.aws.amazon.com,
// "Create a signed AWS API request"): the host and date headers signed,
// the payload's hash included.
func signV4(req *http.Request, payload []byte, keyID, secret, region, service string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	hash := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(hash[:])

	headers := map[string]string{"host": req.URL.Host, "x-amz-date": amzDate}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		headers["content-type"] = ct
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	slices.Sort(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
	}
	signed := strings.Join(names, ";")

	canonical := strings.Join([]string{
		req.Method,
		canonicalPath(req.URL),
		canonicalQuery(req.URL.Query()),
		canonHeaders.String(),
		signed,
		payloadHash,
	}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	ch := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(ch[:])

	key := hmacSHA256([]byte("AWS4"+secret), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+keyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// awsEscape is RFC 3986 escaping, as SigV4 wants it: everything but
// letters, digits and -_.~ is %XX.
func awsEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func canonicalPath(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	return p
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var parts []string
	for _, k := range keys {
		vs := slices.Clone(q[k])
		slices.Sort(vs)
		for _, v := range vs {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	return strings.Join(parts, "&")
}
