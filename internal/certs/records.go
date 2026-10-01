package certs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"linxpbx.com/linx/internal/dnsapi"
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
	// State remembers which records Linx made at companies without
	// Cloudflare's comments, and when the followed address changed.
	State *DNSState
	// NewAPI and Zone are overridable for tests (dnsapi.New, dnsapi.FindZone).
	NewAPI func(ctx context.Context, provider string, k dnsapi.Key) (dnsapi.API, error)
	Zone   func(domain string) (string, error)
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
	return c.company(ctx, cfg.Provider, token, cfg.Domain, hosts, ip)
}

// Connect makes a client for a company internal/dnsapi lists
// (clients.New). linx-certd and the linx command set it; the control plane,
// which only reads certificates, doesn't link the companies' libraries.
var Connect func(ctx context.Context, provider string, k dnsapi.Key, o dnsapi.Options) (dnsapi.API, error)

// companyAPI is the dnsapi client for provider with the key saved as
// secret (dnsapi.Company.Encode).
func companyAPI(ctx context.Context, provider, secret string, c *RecordsClient) (dnsapi.API, error) {
	co, ok := dnsapi.Find(provider)
	if !ok {
		return nil, fmt.Errorf("unsupported DNS provider %q", provider)
	}
	k, err := co.Decode(secret)
	if err != nil {
		return nil, fmt.Errorf("the saved %s key: %w", co.Name, err)
	}
	if c.NewAPI != nil {
		return c.NewAPI(ctx, provider, k)
	}
	if Connect == nil {
		return nil, errors.New("this program can't change DNS records")
	}
	return Connect(ctx, provider, k, dnsapi.Options{HTTP: c.HTTP, PublicAddress: func(ctx context.Context) (string, error) {
		a, err := c.PublicIPv4(ctx)
		if err != nil {
			return "", err
		}
		return a.String(), nil
	}})
}

// company points hosts at ip at a company internal/dnsapi changes records
// at, with the same rules as Cloudflare: an alias (CNAME) is left alone,
// and so is an address someone else set, at the base domain always and
// elsewhere for the follower (OwnOnly). Records can't carry a note there,
// so which are Linx's is kept in State; one that already points at ip is
// taken as Linx's (it's the one the records card asked for).
func (c *RecordsClient) company(ctx context.Context, provider, secret, domain string, hosts []string, ip netip.Addr) ([]RecordResult, error) {
	api, err := companyAPI(ctx, provider, secret, c)
	if err != nil {
		return nil, err
	}
	findZone := c.Zone
	if findZone == nil {
		findZone = dnsapi.FindZone
	}
	zone, err := findZone(domain)
	if err != nil {
		return nil, err
	}
	at := " (" + dnsapi.Name(provider) + ")"
	var out []RecordResult
	for _, h := range hosts {
		name := dnsname.Host(h, domain)
		rel := dnsapi.Relative(name, zone)
		have, err := api.Get(ctx, zone, rel, dnsapi.TypeA)
		if err != nil {
			return out, err
		}
		ownOnly := c.OwnOnly || h == dnsname.Apex
		mine := len(have) == 1 && c.State.Owner(name) == have[0]
		switch {
		case len(have) == 1 && have[0] == ip.String():
			out = append(out, RecordResult{name, "already points at " + ip.String() + at})
		case len(have) == 0:
			alias, err := api.Get(ctx, zone, rel, "CNAME")
			if err != nil {
				return out, err
			}
			if len(alias) > 0 {
				out = append(out, RecordResult{name, "left as is: it's an alias (CNAME) for " + alias[0]})
				continue
			}
			if err := api.Set(ctx, zone, rel, dnsapi.TypeA, []string{ip.String()}); err != nil {
				return out, err
			}
			out = append(out, RecordResult{name, "created, pointing at " + ip.String() + at})
		case ownOnly && !mine:
			out = append(out, RecordResult{name, "left as is: someone else made it (it points at " + strings.Join(have, ", ") + ")"})
			continue
		default:
			if err := api.Set(ctx, zone, rel, dnsapi.TypeA, []string{ip.String()}); err != nil {
				return out, err
			}
			out = append(out, RecordResult{name, "changed to " + ip.String() + at})
		}
		if err := c.State.Own(name, ip.String()); err != nil {
			return out, fmt.Errorf("remembering %s is Linx's: %w", name, err)
		}
	}
	return out, nil
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
		case a != nil && a.Content == ip.String() && !a.Proxied && strings.HasPrefix(a.Comment, recordComment):
			out = append(out, RecordResult{name, "already points at " + ip.String()})
		case a != nil && a.Content == ip.String() && !a.Proxied:
			// Added by hand from the records card: Linx's from now on, so
			// it follows a new address.
			if err := c.cf(ctx, token, http.MethodPatch, "/zones/"+zone+"/dns_records/"+a.ID, map[string]any{"comment": body["comment"]}, nil); err != nil {
				return out, err
			}
			out = append(out, RecordResult{name, "already points at " + ip.String() + "; Linx keeps it right from now on"})
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

// ErrTokenRefused is CheckToken's answer when the DNS company says no.
var ErrTokenRefused = errors.New("token refused")

// CheckToken makes sure a key can see domain's zone and its records,
// changing nothing (docs/ui/INSTALL_SCREENS.md §3.2, SCREENS_PHASE1F.md
// §3.2); the error says why in plain words and wraps ErrTokenRefused when
// it's the key. DuckDNS has no way to check without changing the address:
// nil.
func (c *RecordsClient) CheckToken(ctx context.Context, provider, domain, token string) error {
	if provider == ProviderDuckDNS {
		return nil
	}
	if provider != ProviderCloudflare {
		api, err := companyAPI(ctx, provider, token, c)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrTokenRefused, err)
		}
		err = dnsapi.CheckWith(ctx, api, provider, domain, c.Zone)
		if errors.Is(err, dnsapi.ErrKeyRefused) {
			return fmt.Errorf("%w: %v", ErrTokenRefused, strings.TrimPrefix(err.Error(), dnsapi.ErrKeyRefused.Error()+": "))
		}
		return err
	}
	zone, err := c.cfZone(ctx, token, domain)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("Linx couldn't reach Cloudflare to check the token: %w", err)
		}
		return fmt.Errorf("%w: that token can't see %s at Cloudflare. It needs Zone → Zone → Read and Zone → DNS → Edit on that zone", ErrTokenRefused, domain)
	}
	var recs []cfRecord
	if err := c.cf(ctx, token, http.MethodGet, "/zones/"+zone+"/dns_records?"+url.Values{"per_page": {"5"}}.Encode(), nil, &recs); err != nil {
		return fmt.Errorf("%w: that token can't read %s's DNS records at Cloudflare. It needs Zone → DNS → Edit on that zone", ErrTokenRefused, domain)
	}
	return nil
}
