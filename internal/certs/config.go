// Package certs issues, renews and deploys Linx's public certificate with ACME
// DNS-01 (lego, ADR-010): Let's Encrypt first (staging on fresh installs),
// ZeroSSL as the fallback CA in production.
package certs

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DNS providers supported for DNS-01. More arrive with the Domain & DNS page.
const (
	ProviderCloudflare = "cloudflare"
	ProviderDuckDNS    = "duckdns"
)

// Providers lists the supported DNS providers.
// deSEC is deferred: lego's deSEC client pulls in MPL-2.0 modules, which the
// licence allowlist rejects (owner decision, 2026-09-23).
var Providers = []string{ProviderCloudflare, ProviderDuckDNS}

// Hostnames are the Linx hostnames under the base domain (ARCHITECTURE §2).
// They're used when the owner chooses a named certificate instead of a wildcard.
var Hostnames = []string{"admin", "api", "meet", "provision", "sip", "turn"}

// RenewBefore is how long before expiry a certificate is renewed.
const RenewBefore = 30 * 24 * time.Hour

// Config is linx-certd's configuration, read from LINX_* environment variables
// that setup writes into the compose file. Secrets are only ever file paths.
type Config struct {
	// Domain is the base domain, e.g. pbx.example.com.
	Domain string
	// Wildcard issues *.Domain; otherwise one certificate names each hostname.
	Wildcard bool
	// Provider is the DNS provider for DNS-01.
	Provider string
	// Email is the ACME contact. Required in production (ZeroSSL needs it).
	Email string
	// Staging uses Let's Encrypt staging (untrusted certs, no rate-limit risk)
	// and disables the ZeroSSL fallback, which has no staging environment.
	Staging bool
	// TokenFile holds the DNS provider API token (a Docker secret).
	TokenFile string
	// ZoneTokenFile optionally holds a separate Cloudflare zone-read token.
	ZoneTokenFile string
	// CertsDir is the shared volume consumers read certificates from.
	CertsDir string
	// StateDir holds ACME account keys. Private to linx-certd.
	StateDir string
}

// ConfigFromEnv reads the configuration from the environment.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		Domain:        strings.ToLower(strings.TrimSpace(getenv("LINX_DOMAIN"))),
		Provider:      strings.TrimSpace(getenv("LINX_DNS_PROVIDER")),
		Email:         strings.TrimSpace(getenv("LINX_ACME_EMAIL")),
		TokenFile:     envOr(getenv, "LINX_DNS_TOKEN_FILE", "/run/secrets/linx_dns_token"),
		ZoneTokenFile: getenv("LINX_DNS_ZONE_TOKEN_FILE"),
		CertsDir:      envOr(getenv, "LINX_CERTS_DIR", "/var/lib/linx/certs"),
		StateDir:      envOr(getenv, "LINX_STATE_DIR", "/var/lib/linx/state"),
	}
	var errs []error
	var err error
	if c.Staging, err = envBool(getenv, "LINX_ACME_STAGING", true); err != nil {
		errs = append(errs, err)
	}
	if c.Wildcard, err = envBool(getenv, "LINX_CERT_WILDCARD", true); err != nil {
		errs = append(errs, err)
	}
	if err := c.Validate(); err != nil {
		errs = append(errs, err)
	}
	return c, errors.Join(errs...)
}

var (
	labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// Validate checks every field.
func (c Config) Validate() error {
	var errs []error
	if err := validDomain(c.Domain); err != nil {
		errs = append(errs, fmt.Errorf("LINX_DOMAIN: %w", err))
	}
	if !slices.Contains(Providers, c.Provider) {
		errs = append(errs, fmt.Errorf("LINX_DNS_PROVIDER: must be one of %v, got %q", Providers, c.Provider))
	}
	if c.Provider == ProviderDuckDNS && !strings.HasSuffix(c.Domain, ".duckdns.org") {
		errs = append(errs, errors.New("LINX_DOMAIN: DuckDNS domains end in .duckdns.org"))
	}
	if c.ZoneTokenFile != "" && c.Provider != ProviderCloudflare {
		errs = append(errs, errors.New("LINX_DNS_ZONE_TOKEN_FILE: only used with Cloudflare"))
	}
	if c.Email != "" && !emailRE.MatchString(c.Email) {
		errs = append(errs, fmt.Errorf("LINX_ACME_EMAIL: %q is not an email address", c.Email))
	}
	if !c.Staging && c.Email == "" {
		errs = append(errs, errors.New("LINX_ACME_EMAIL: required for production certificates (the ZeroSSL fallback needs it)"))
	}
	if c.TokenFile == "" {
		errs = append(errs, errors.New("LINX_DNS_TOKEN_FILE: required"))
	}
	if c.CertsDir == "" || c.StateDir == "" {
		errs = append(errs, errors.New("LINX_CERTS_DIR and LINX_STATE_DIR: required"))
	}
	return errors.Join(errs...)
}

func validDomain(d string) error {
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

// Names are the DNS names the certificate covers.
func (c Config) Names() []string {
	if c.Wildcard {
		return []string{"*." + c.Domain}
	}
	names := make([]string, len(Hostnames))
	for i, h := range Hostnames {
		names[i] = h + "." + c.Domain
	}
	return names
}

func envOr(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(getenv func(string) string, key string, def bool) (bool, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s: must be true or false, got %q", key, v)
	}
	return b, nil
}

// readSecret reads a secret file and trims surrounding whitespace. The error
// names the file but never includes its contents.
func readSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading secret: %w", err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("secret file %s is empty", path)
	}
	return s, nil
}
