// Package weburl builds Linx's public web address, https://<domain>, or
// https://<domain>:<port> when the router or server forwards another
// public port to Linx (docs/SIMPLER.md §2.5, ADR-064). Everything that
// gives out or checks Linx's address uses it: setup's links, passkeys,
// company sign-in's return address, redirects, the phone link of Check it.
package weburl

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// DefaultPort is the standard port for secure websites, and the one Linx
// is tuned for.
const DefaultPort = 443

// Env names the public port in the services' environment ("": 443).
const Env = "LINX_PUBLIC_PORT"

// Origin is the address people open: https://<domain>, with :<port>
// unless it's 443 (0 means 443).
func Origin(domain string, port int) string {
	return "https://" + Host(domain, port)
}

// Host is the address's host part: domain, or domain:port.
func Host(domain string, port int) string {
	if port == 0 || port == DefaultPort {
		return domain
	}
	return net.JoinHostPort(domain, strconv.Itoa(port))
}

// SSOCallbackPath is where company sign-in's providers send the browser
// back (internal/sso); here so the linx command can say the address
// without company sign-in's code.
const SSOCallbackPath = "/api/v1/sso/callback"

// SSOCallback is the return address to register at every company sign-in
// provider, for Linx's address origin.
func SSOCallback(origin string) string { return origin + SSOCallbackPath }

// Port reads the public port from the environment: 443 when unset or not
// a port.
func Port(getenv func(string) string) int {
	p, err := strconv.Atoi(strings.TrimSpace(getenv(Env)))
	if err != nil || p < 1 || p > 65535 {
		return DefaultPort
	}
	return p
}

// FromEnv is Origin for LINX_DOMAIN and the public port ("" without a
// domain).
func FromEnv(getenv func(string) string) string {
	d := strings.TrimSpace(getenv("LINX_DOMAIN"))
	if d == "" {
		return ""
	}
	return Origin(d, Port(getenv))
}

// Problem says, in plain words, why p can't be Linx's public port ("" if
// it can): 443, or one from 1024 to 65535 that browsers open and Linx
// doesn't use itself.
func Problem(p int) string {
	switch {
	case p == DefaultPort:
		return ""
	case p < 1024 || p > 65535:
		return fmt.Sprintf("%d isn't a port Linx can use: choose one from 1024 to 65535, like 8443.", p)
	case linxPort(p):
		return fmt.Sprintf("%d is used by Linx itself: choose another, like 8443.", p)
	case browserBlocked(p):
		return fmt.Sprintf("%d is blocked by browsers: choose another, like 8443.", p)
	}
	return ""
}

// linxPort: phones' SIP ports (5060–5064), the relay's TLS port, the
// install's first page.
func linxPort(p int) bool {
	return p >= 5060 && p <= 5064 || p == 5349 || p == 6464
}

// browserBlocked are the ports from 1024 up that browsers refuse to open
// (the WHATWG Fetch standard's "bad ports").
func browserBlocked(p int) bool {
	switch p {
	case 1719, 1720, 1723, 2049, 3659, 4045, 4190, 5060, 5061, 6000, 6566, 6665, 6666, 6667, 6668, 6669, 6679, 6697, 10080:
		return true
	}
	return false
}
