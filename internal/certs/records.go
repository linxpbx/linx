package certs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"linxpbx.com/linx/internal/dnsname"
	"linxpbx.com/linx/internal/publicip"
)

// Public DNS records for the front door (docs/WEB.md §3): the base domain
// itself (the web app and the API) and turn.<domain> point at the address the internet reaches Linx on (the home
// router, or a VPS). linx-certd does it because it already holds the DNS
// token; `linx setup` runs it once (`certd -records ...`), and step 7's
// updater will follow a changing home address the same way.

// RecordsClient talks to the DNS provider and finds the public address.
type RecordsClient struct {
	HTTP *http.Client
	// CloudflareAPI, DuckDNSAPI and TraceURL are overridable for tests.
	CloudflareAPI string
	DuckDNSAPI    string
	TraceURL      string
	// OwnOnly changes an existing Cloudflare record only if Linx made it
	// (its comment starts with recordComment). The background IP follower
	// sets it, so an address the owner typed in by hand is never
	// overwritten behind their back; `linx setup` doesn't, since the owner
	// just asked for these names.
	OwnOnly bool
}

// recordComment marks the Cloudflare records Linx made.
const recordComment = "Linx"

// NewRecordsClient uses the real endpoints.
func NewRecordsClient() *RecordsClient {
	return &RecordsClient{
		HTTP:          &http.Client{Timeout: 20 * time.Second},
		CloudflareAPI: "https://api.cloudflare.com/client/v4",
		DuckDNSAPI:    "https://www.duckdns.org/update",
		// Cloudflare's own trace endpoint, over HTTPS to 1.1.1.1 (its
		// certificate names that address): the same company as the DNS.
		TraceURL: "https://1.1.1.1/cdn-cgi/trace",
	}
}

// RecordResult says what happened to one name, in plain words.
type RecordResult struct {
	Name, Outcome string
}

// PublicIPv4 is the address this machine reaches the internet from.
func (c *RecordsClient) PublicIPv4(ctx context.Context) (netip.Addr, error) {
	return publicip.Lookup(ctx, c.HTTP, c.TraceURL)
}

func isPublic(a netip.Addr) bool { return publicip.IsPublic(a) }

// PointRecords points each of hosts (e.g. dnsname.Apex, "turn") under cfg.Domain at ip:
// an A record on Cloudflare (created, or changed if it points elsewhere;
// never proxied, since calls can't go through Cloudflare's proxy; a name
// that's a CNAME is left alone), or the DuckDNS name's address (DuckDNS
// answers every name under yours with it).
func (c *RecordsClient) PointRecords(ctx context.Context, cfg Config, hosts []string, ip netip.Addr) ([]RecordResult, error) {
	token, err := readSecret(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	switch cfg.Provider {
	case ProviderCloudflare:
		return c.cloudflare(ctx, token, cfg.Domain, hosts, ip)
	case ProviderDuckDNS:
		return c.duckdns(ctx, token, cfg.Domain, hosts, ip)
	}
	return nil, fmt.Errorf("unsupported DNS provider %q", cfg.Provider)
}

func (c *RecordsClient) duckdns(ctx context.Context, token, domain string, hosts []string, ip netip.Addr) ([]RecordResult, error) {
	sub := strings.TrimSuffix(domain, ".duckdns.org")
	if i := strings.LastIndexByte(sub, '.'); i >= 0 {
		sub = sub[i+1:] // DuckDNS knows only the name directly under duckdns.org
	}
	q := url.Values{"domains": {sub}, "token": {token}, "ip": {ip.String()}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.DuckDNSAPI+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DuckDNS: %w", errors.Unwrap(err)) // never the URL: it holds the token
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if strings.TrimSpace(string(b)) != "OK" {
		return nil, errors.New("DuckDNS refused the update (check the token)")
	}
	out := make([]RecordResult, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, RecordResult{dnsname.Host(h, domain), "points at " + ip.String() + " (DuckDNS)"})
	}
	return out, nil
}

type cfRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

func (c *RecordsClient) cloudflare(ctx context.Context, token, domain string, hosts []string, ip netip.Addr) ([]RecordResult, error) {
	zone, err := c.cfZone(ctx, token, domain)
	if err != nil {
		return nil, err
	}
	var out []RecordResult
	for _, h := range hosts {
		name := dnsname.Host(h, domain)
		// The base domain may already be someone's website: an address
		// Linx didn't set there is never replaced, not even by setup.
		ownOnly := c.OwnOnly || h == dnsname.Apex
		var found []cfRecord
		if err := c.cf(ctx, token, http.MethodGet, "/zones/"+zone+"/dns_records?"+url.Values{"name": {name}}.Encode(), nil, &found); err != nil {
			return out, err
		}
		body := map[string]any{"type": "A", "name": name, "content": ip.String(), "ttl": 300, "proxied": false,
			"comment": recordComment + " (linx setup)"}
		var a *cfRecord
		cname := ""
		for i, r := range found {
			switch r.Type {
			case "A":
				a = &found[i]
			case "CNAME":
				cname = r.Content
			}
		}
		switch {
		case cname != "":
			out = append(out, RecordResult{name, "left as is: it's an alias (CNAME) for " + cname})
		case a != nil && a.Content == ip.String() && !a.Proxied:
			out = append(out, RecordResult{name, "already points at " + ip.String()})
		case a != nil && ownOnly && !strings.HasPrefix(a.Comment, recordComment):
			out = append(out, RecordResult{name, "left as is: someone else made it (it points at " + a.Content + ")"})
		case a != nil:
			if err := c.cf(ctx, token, http.MethodPut, "/zones/"+zone+"/dns_records/"+a.ID, body, nil); err != nil {
				return out, err
			}
			out = append(out, RecordResult{name, "changed to " + ip.String()})
		default:
			if err := c.cf(ctx, token, http.MethodPost, "/zones/"+zone+"/dns_records", body, nil); err != nil {
				return out, err
			}
			out = append(out, RecordResult{name, "created, pointing at " + ip.String()})
		}
	}
	return out, nil
}

// cfZone finds the zone holding domain: the longest suffix Cloudflare has.
func (c *RecordsClient) cfZone(ctx context.Context, token, domain string) (string, error) {
	labels := strings.Split(domain, ".")
	for i := 0; i < len(labels)-1; i++ {
		var zones []struct {
			ID string `json:"id"`
		}
		name := strings.Join(labels[i:], ".")
		if err := c.cf(ctx, token, http.MethodGet, "/zones?"+url.Values{"name": {name}}.Encode(), nil, &zones); err != nil {
			return "", err
		}
		if len(zones) > 0 {
			return zones[0].ID, nil
		}
	}
	return "", fmt.Errorf("Cloudflare: no zone for %s is visible to this token", domain)
}

func (c *RecordsClient) cf(ctx context.Context, token, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.CloudflareAPI+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Cloudflare: %w", err)
	}
	defer resp.Body.Close()
	var env struct {
		Success bool                       `json:"success"`
		Errors  []struct{ Message string } `json:"errors"`
		Result  json.RawMessage            `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil {
		return fmt.Errorf("Cloudflare: unexpected answer (%s)", resp.Status)
	}
	if !env.Success {
		msg := resp.Status
		if len(env.Errors) > 0 {
			msg = env.Errors[0].Message
		}
		return fmt.Errorf("Cloudflare: %s", msg)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}
