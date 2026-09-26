// Package nftset reads and syncs single-address elements of an nftables
// set (docs/TRUNKS.md §12, ADR-047): it only ever runs `nft list set`,
// `nft add element` and `nft delete element` against a table and set it's
// told about, never `nft -f` (which would replace rules) and never a set
// it wasn't asked for. linx-firewall-sync (cmd/linx-firewall-sync) is the
// one thing that calls Sync; linx doctor calls List to check the result.
package nftset

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Run executes a command and returns its combined output: *exec.Cmd in
// production, a fake in tests.
type Run func(ctx context.Context, name string, args ...string) ([]byte, error)

// List returns a set's current elements. The sets this package touches
// never hold a network, only single addresses (an IP-authenticated
// trunk's own address, docs/TRUNKS.md §12): an element that isn't one is
// an error, not silently dropped.
func List(ctx context.Context, run Run, table, set string) ([]netip.Addr, error) {
	out, err := run(ctx, "nft", "-j", "list", "set", "inet", table, set)
	if err != nil {
		return nil, fmt.Errorf("nft list set inet %s %s: %w: %s", table, set, err, bytesTail(out))
	}
	return parse(out)
}

func parse(out []byte) ([]netip.Addr, error) {
	var doc struct {
		Nftables []struct {
			Set *struct {
				Elem []json.RawMessage `json:"elem"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	for _, o := range doc.Nftables {
		if o.Set == nil {
			continue
		}
		addrs := make([]netip.Addr, 0, len(o.Set.Elem))
		for _, e := range o.Set.Elem {
			var s string
			if err := json.Unmarshal(e, &s); err != nil {
				return nil, fmt.Errorf("unexpected set element %s", e)
			}
			a, err := netip.ParseAddr(s)
			if err != nil {
				return nil, fmt.Errorf("unexpected set element %q: %w", s, err)
			}
			addrs = append(addrs, a)
		}
		return addrs, nil
	}
	return nil, nil
}

// Sync makes set's elements exactly want, adding and removing only what's
// needed, and reports what changed. It never touches a rule, and it never
// touches any set but the one named.
func Sync(ctx context.Context, run Run, table, set string, want []netip.Addr) (added, removed []netip.Addr, err error) {
	have, err := List(ctx, run, table, set)
	if err != nil {
		return nil, nil, err
	}
	for _, a := range want {
		if !slices.Contains(have, a) {
			added = append(added, a)
		}
	}
	for _, a := range have {
		if !slices.Contains(want, a) {
			removed = append(removed, a)
		}
	}
	if len(added) > 0 {
		if _, err := run(ctx, "nft", "add", "element", "inet", table, set, elements(added)); err != nil {
			return nil, nil, fmt.Errorf("nft add element inet %s %s: %w", table, set, err)
		}
	}
	if len(removed) > 0 {
		if _, err := run(ctx, "nft", "delete", "element", "inet", table, set, elements(removed)); err != nil {
			return nil, nil, fmt.Errorf("nft delete element inet %s %s: %w", table, set, err)
		}
	}
	return added, removed, nil
}

func elements(addrs []netip.Addr) string {
	s := make([]string, len(addrs))
	for i, a := range addrs {
		s[i] = a.String()
	}
	return "{ " + strings.Join(s, ", ") + " }"
}

func bytesTail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[len(s)-300:]
	}
	return s
}
