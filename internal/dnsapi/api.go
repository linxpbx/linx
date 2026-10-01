package dnsapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/challenge/dns01"
)

// Record types Linx writes.
const (
	TypeA   = "A"
	TypeTXT = "TXT"
)

// API is what Linx needs of a DNS company: one name's records of one
// type, read, set and removed. zone is the zone at the company without the
// final dot ("example.com"); name is relative to it, "@" for the zone
// itself. TXT values are the text, without quotes.
type API interface {
	// Get is name's values of type typ: none, and no error, when there
	// are none.
	Get(ctx context.Context, zone, name, typ string) ([]string, error)
	// Set makes values name's only values of type typ.
	Set(ctx context.Context, zone, name, typ string, values []string) error
	// Delete removes name's records of type typ (none is fine).
	Delete(ctx context.Context, zone, name, typ string) error
}

// Options are what New needs besides the key.
type Options struct {
	// HTTP is the client for Linx's own clients (libdns modules use their own).
	HTTP *http.Client
	// PublicAddress is this network's public address: Namecheap wants it
	// with every request.
	PublicAddress func(ctx context.Context) (string, error)
}

// ErrBuiltin: Cloudflare and DuckDNS are changed by internal/certs' own
// clients, not through this package.
var ErrBuiltin = errors.New("changed by Linx's own Cloudflare and DuckDNS clients")

// FindZone is the zone domain is in ("example.com" for
// "pbx.example.com"), as DNS says: the zone the DNS company holds.
var FindZone = func(domain string) (string, error) {
	z, err := dns01.FindZoneByFqdn(dns01.ToFqdn(strings.ToLower(domain)))
	if err != nil {
		return "", fmt.Errorf("finding the zone %s is in: %w", domain, err)
	}
	return strings.TrimSuffix(z, "."), nil
}

// Relative is name under zone as API wants it: "@" for zone itself.
func Relative(name, zone string) string {
	name, zone = strings.TrimSuffix(strings.ToLower(name), "."), strings.TrimSuffix(strings.ToLower(zone), ".")
	if name == zone {
		return "@"
	}
	return strings.TrimSuffix(name, "."+zone)
}

// Absolute is name under zone, fully written out, without the final dot.
func Absolute(name, zone string) string {
	if name == "@" || name == "" {
		return zone
	}
	return name + "." + zone
}

// ErrKeyRefused wraps CheckWith's answer when it's the key (or what it may
// do), not the connection.
var ErrKeyRefused = errors.New("key refused")

// CheckWith makes sure api lets Linx read domain's records at company id,
// changing nothing (docs/ui/SCREENS_PHASE1F.md §3.2); findZone nil is
// FindZone. The error is in plain words, with the company's own reason,
// and wraps ErrKeyRefused when the company said no.
func CheckWith(ctx context.Context, api API, id, domain string, findZone func(string) (string, error)) error {
	c, _ := Find(id)
	if findZone == nil {
		findZone = FindZone
	}
	zone, err := findZone(domain)
	if err != nil {
		return fmt.Errorf("Linx couldn't find %s in DNS to check the key: %w", domain, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := api.Get(ctx, zone, Relative(domain, zone), TypeA); err != nil {
		var ne net.Error
		if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("Linx couldn't reach %s to check the key: %w", c.Name, err)
		}
		return fmt.Errorf("%w: %s didn't let this key read %s's records (%s)", ErrKeyRefused, c.Name, zone, short(err.Error()))
	}
	return nil
}

// short keeps a company's reason readable on the page.
func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
