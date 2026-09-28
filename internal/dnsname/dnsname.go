// Package dnsname validates Linx base domains. It's shared by linx setup and
// linx-certd without pulling lego into the CLI.
package dnsname

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidDomain checks a lower-case base domain such as pbx.example.com. It
// leaves room for the longest Linx hostname prefix ("provision.").
func ValidDomain(d string) error {
	if len(d) > 253-len("provision.") {
		return errors.New("too long")
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return fmt.Errorf("%q is not a domain name", d)
	}
	for _, l := range labels {
		if !labelRE.MatchString(l) {
			return fmt.Errorf("%q is not a valid domain name (letters, digits and hyphens only; no wildcard)", d)
		}
	}
	return nil
}

// Apex stands for the base domain itself in Linx's lists of host names:
// the web app and the API answer there (https://pbx.example.com), while
// turn. and sip. are names under it.
const Apex = "@"

// Host is h under domain: the domain itself for Apex.
func Host(h, domain string) string {
	if h == Apex {
		return domain
	}
	return h + "." + domain
}

// Hosts is each of hosts under domain.
func Hosts(hosts []string, domain string) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = Host(h, domain)
	}
	return out
}

// And joins names for plain words: "a", "a and b", "a, b and c".
func And(names []string) string {
	switch n := len(names); n {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return strings.Join(names[:n-1], ", ") + " and " + names[n-1]
	}
}
