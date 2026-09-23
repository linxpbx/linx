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
