// Package publicip finds the address this machine reaches the internet
// from, the way linx-certd's DNS records follow it (internal/certs), without
// pulling the certificate code into the linx command.
package publicip

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
)

// TraceURL is Cloudflare's own trace endpoint, over HTTPS to 1.1.1.1 (its
// certificate names that address).
const TraceURL = "https://1.1.1.1/cdn-cgi/trace"

// Lookup asks traceURL (TraceURL) which public IPv4 address the request
// came from.
func Lookup(ctx context.Context, client *http.Client, traceURL string) (netip.Addr, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, traceURL, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("finding this network's public address: %w", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 8<<10))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "ip="); ok {
			a, err := netip.ParseAddr(strings.TrimSpace(v))
			if err != nil || !a.Is4() || !IsPublic(a) {
				return netip.Addr{}, fmt.Errorf("finding this network's public address: got %q", v)
			}
			return a, nil
		}
	}
	return netip.Addr{}, errors.New("finding this network's public address: no answer")
}

// IsPublic reports whether a is an internet address (not private, loopback,
// link-local and so on).
func IsPublic(a netip.Addr) bool {
	return a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback()
}
