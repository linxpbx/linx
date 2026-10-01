// Package dnsapi changes DNS records at the DNS companies Linx can keep
// records right at by itself (ADR-063, docs/SIMPLER.md §3): the domain and
// turn. following a home connection's changing address, sip. at the home
// address, and the certificate's _acme-challenge TXT record.
//
// Seven companies go through libdns (MIT), the library behind Caddy's DNS
// support. Hetzner, DigitalOcean and Route 53 have Linx's own short clients
// instead (owner decision 2026-10-01): their libdns modules add 9 MB, 1 MB
// (with MPL-2.0 code the licence check refuses) and 6 MB to linx-certd.
// Cloudflare and DuckDNS keep the clients Linx had before this package
// (internal/certs and lego's), so they're listed here only for the forms.
package dnsapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Company IDs, as setup.yaml's domain.dns_provider holds them.
const (
	Cloudflare   = "cloudflare"
	DuckDNS      = "duckdns"
	Route53      = "route53"
	GoDaddy      = "godaddy"
	Namecheap    = "namecheap"
	Porkbun      = "porkbun"
	DigitalOcean = "digitalocean"
	Hetzner      = "hetzner"
	DeSEC        = "desec"
	OVH          = "ovh"
)

// Field is one thing a company's form asks for.
type Field struct {
	// Key names it in the saved key (JSON, when there's more than one).
	Key   string `json:"key"`
	Label string `json:"label"`
	// Secret fields are typed like passwords and never shown again.
	Secret bool `json:"secret,omitempty"`
	// Options, if any, are the only values allowed (a list to pick from).
	Options []Option `json:"options,omitempty"`
}

// Option is one of a Field's choices.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Company is a DNS company Linx can keep records right at.
type Company struct {
	ID string `json:"id"`
	// Name is how people (and dnscheck.Company) call it.
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
	// Where says where to make the key, in plain words. {domain} and
	// {address} (this network's public address) are filled in by the page.
	Where string `json:"where"`
	// Note, if set, is a limit worth knowing before choosing it.
	Note string `json:"note,omitempty"`
	// OneAddress: every name under the domain has the same address
	// (DuckDNS), so sip. can't have its own.
	OneAddress bool `json:"one_address,omitempty"`
	// TTL is how long resolvers may keep a record: as short as the company
	// allows, so a new home address is seen soon.
	TTL time.Duration `json:"-"`
}

// Companies are the ten (docs/SIMPLER.md §3.2, owner decision 2026-09-30),
// in the order the form lists them.
var Companies = []Company{
	{ID: Cloudflare, Name: "Cloudflare", TTL: 5 * time.Minute,
		Fields: []Field{{Key: "token", Label: "API token", Secret: true}},
		Where:  "In Cloudflare: My Profile → API Tokens → Create Token → “Edit zone DNS”. Under Zone Resources choose only {domain}. Create it and copy it here."},
	{ID: DuckDNS, Name: "DuckDNS", OneAddress: true, TTL: time.Minute,
		Fields: []Field{{Key: "token", Label: "Token", Secret: true}},
		Where:  "Your token is at the top of duckdns.org once you've signed in."},
	{ID: Route53, Name: "Route 53", TTL: 5 * time.Minute,
		Fields: []Field{{Key: "access_key_id", Label: "Access key ID"}, {Key: "secret_access_key", Label: "Secret access key", Secret: true}},
		Where: "In the AWS console: IAM → Users → Create user, with a policy allowing route53:ListHostedZonesByName, " +
			"route53:ListResourceRecordSets and route53:ChangeResourceRecordSets. Then Security credentials → Create access key."},
	{ID: GoDaddy, Name: "GoDaddy", TTL: 10 * time.Minute,
		Fields: []Field{{Key: "api_key", Label: "API key"}, {Key: "api_secret", Label: "Secret", Secret: true}},
		Where:  "At developer.godaddy.com/keys: Create New API Key, choose Production.",
		Note:   "GoDaddy lets only some accounts use its API (larger accounts and its paid plans). If it refuses the key, add the records by hand."},
	{ID: Namecheap, Name: "Namecheap", TTL: 5 * time.Minute,
		Fields: []Field{{Key: "api_user", Label: "User name"}, {Key: "api_key", Label: "API key", Secret: true}},
		Where:  "In Namecheap: Profile → Tools → Business & Dev Tools → API Access → On. Under Whitelisted IPs add {address}.",
		Note: "Namecheap turns its API on only for accounts with some spending or domains, and accepts Linx only from the addresses on its list: " +
			"when your home address changes, add the new one there. It suits a fixed address best."},
	{ID: Porkbun, Name: "Porkbun", TTL: 10 * time.Minute,
		Fields: []Field{{Key: "api_key", Label: "API key", Secret: true}, {Key: "secret_api_key", Label: "Secret key", Secret: true}},
		Where:  "In Porkbun: Account → API Access → Create API key. Then in Domain Management, turn on “API Access” for {domain}."},
	{ID: DigitalOcean, Name: "DigitalOcean", TTL: 5 * time.Minute,
		Fields: []Field{{Key: "token", Label: "Personal access token", Secret: true}},
		Where:  "In DigitalOcean: API → Generate New Token, with Custom Scopes: domain (read, create, update, delete)."},
	{ID: Hetzner, Name: "Hetzner", TTL: 5 * time.Minute,
		Fields: []Field{{Key: "token", Label: "API token", Secret: true}},
		Where:  "In the Hetzner Console: your project → Security → API tokens → Generate API token, Read & Write.",
		Note:   "Your domain needs to be in the Hetzner Console's DNS. One still in the old DNS Console has to be moved there first."},
	{ID: DeSEC, Name: "deSEC", TTL: time.Hour,
		Fields: []Field{{Key: "token", Label: "Token", Secret: true}},
		Where:  "At desec.io: Token management → +. You can limit it to {domain}.",
		Note:   "deSEC keeps records for at least an hour, so a new home address can take up to an hour to reach everyone."},
	{ID: OVH, Name: "OVH", TTL: 5 * time.Minute,
		Fields: []Field{
			{Key: "endpoint", Label: "Region", Options: []Option{{"ovh-eu", "Europe"}, {"ovh-ca", "Canada"}, {"ovh-us", "United States"}}},
			{Key: "application_key", Label: "Application key"},
			{Key: "application_secret", Label: "Application secret", Secret: true},
			{Key: "consumer_key", Label: "Consumer key", Secret: true},
		},
		Where: "At OVH's “create token” page for your region (eu.api.ovh.com/createToken for Europe), " +
			"allow GET, POST, PUT and DELETE on /domain/zone/*."},
}

// IDs are the companies' IDs, in order.
func IDs() []string {
	out := make([]string, len(Companies))
	for i, c := range Companies {
		out[i] = c.ID
	}
	return out
}

// Find is the company with id.
func Find(id string) (Company, bool) {
	i := slices.IndexFunc(Companies, func(c Company) bool { return c.ID == id })
	if i < 0 {
		return Company{}, false
	}
	return Companies[i], true
}

// ByName is the company dnscheck.Company names ("Porkbun"), if one of ours.
func ByName(name string) (Company, bool) {
	i := slices.IndexFunc(Companies, func(c Company) bool { return c.Name == name })
	if i < 0 {
		return Company{}, false
	}
	return Companies[i], true
}

// Name is id's company name, or id itself.
func Name(id string) string {
	if c, ok := Find(id); ok {
		return c.Name
	}
	return id
}

// Key is what a company's form was filled in with, by Field.Key.
type Key map[string]string

// maxValue bounds one field: real keys are well under it.
const maxValue = 300

// Check says what's wrong with k for c, in plain words, field by field
// (nil if nothing). Values are trimmed first.
func (c Company) Check(k Key) map[string]string {
	bad := map[string]string{}
	for _, f := range c.Fields {
		v := strings.TrimSpace(k[f.Key])
		l := c.Name + " " + lower(f.Label)
		switch {
		case v == "":
			bad[f.Key] = "Fill in the " + l + "."
		case len(f.Options) > 0 && !slices.ContainsFunc(f.Options, func(o Option) bool { return o.Value == v }):
			bad[f.Key] = "Choose the " + l + " from the list."
		case strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }):
			bad[f.Key] = "The " + l + " can't contain spaces: copy just the " + lower(f.Label) + "."
		case len(v) > maxValue:
			bad[f.Key] = "That's too long to be the " + l + "."
		case f.Secret && len(v) < 8:
			bad[f.Key] = "That's too short to be the " + l + "."
		}
	}
	for key := range k {
		if !slices.ContainsFunc(c.Fields, func(f Field) bool { return f.Key == key }) {
			bad[key] = c.Name + " doesn't need this."
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return bad
}

// lower is a label inside a sentence: "Secret key" → "secret key", but
// "API key" stays.
func lower(label string) string {
	first, _, _ := strings.Cut(label, " ")
	if len(first) > 1 && strings.ToUpper(first) == first {
		return label
	}
	return strings.ToLower(label[:1]) + label[1:]
}

// Encode is k as the secret file holds it: the value itself when the
// company asks for one thing (as Cloudflare and DuckDNS tokens always
// were), a JSON object otherwise. It's checked first.
func (c Company) Encode(k Key) (string, error) {
	if bad := c.Check(k); bad != nil {
		return "", errors.New(firstProblem(c, bad))
	}
	if len(c.Fields) == 1 {
		return strings.TrimSpace(k[c.Fields[0].Key]), nil
	}
	clean := Key{}
	for _, f := range c.Fields {
		clean[f.Key] = strings.TrimSpace(k[f.Key])
	}
	b, err := json.Marshal(clean) // map keys sorted: the same key, the same file
	return string(b), err
}

// Decode reads a secret file's contents back. The error never includes
// any of it.
func (c Company) Decode(secret string) (Key, error) {
	secret = strings.TrimSpace(secret)
	var k Key
	if len(c.Fields) == 1 {
		k = Key{c.Fields[0].Key: secret}
	} else if err := json.Unmarshal([]byte(secret), &k); err != nil {
		return nil, fmt.Errorf("that isn't a %s key as Linx keeps it (a JSON object with %s)", c.Name, c.fieldKeys())
	}
	if bad := c.Check(k); bad != nil {
		return nil, errors.New(firstProblem(c, bad))
	}
	return k, nil
}

func (c Company) fieldKeys() string {
	keys := make([]string, len(c.Fields))
	for i, f := range c.Fields {
		keys[i] = f.Key
	}
	return strings.Join(keys, ", ")
}

// firstProblem is the first field's problem, in the form's order.
func firstProblem(c Company, bad map[string]string) string {
	for _, f := range c.Fields {
		if m, ok := bad[f.Key]; ok {
			return m
		}
	}
	keys := make([]string, 0, len(bad))
	for k := range bad {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys[0] + ": " + bad[keys[0]]
}

// FirstProblem is Check's first problem in the form's order ("" if none).
func (c Company) FirstProblem(k Key) string {
	if bad := c.Check(k); bad != nil {
		return firstProblem(c, bad)
	}
	return ""
}
