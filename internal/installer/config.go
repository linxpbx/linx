package installer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"linxpbx.com/linx/internal/dnsname"
)

// ConfigPath is where setup saves the owner's answers. Re-running setup reads
// them as defaults; `linx setup --config` uses a file without asking.
const ConfigPath = "/etc/linx/setup.yaml"

// ProfileAuto lets setup pick the resource profile from the hardware.
const ProfileAuto = "auto"

// Config is setup.yaml. Later phases add the deployment profile, etc.
type Config struct {
	Version int          `yaml:"version"`
	Docker  DockerConfig `yaml:"docker"`
	// ContainerUI is "none" or "portainer".
	ContainerUI string `yaml:"container_ui"`
	// ResourceProfile is "auto", "lite", "standard" or "performance".
	ResourceProfile string            `yaml:"resource_profile"`
	Domain          DomainConfig      `yaml:"domain"`
	Certificates    CertificateConfig `yaml:"certificates"`
	FrontDoor       FrontDoorConfig   `yaml:"front_door"`
}

// DomainConfig is the base domain and where its DNS is managed. The DNS
// provider's API token is never in setup.yaml; it's a secret file (DNSTokenPath).
type DomainConfig struct {
	// Name is the base domain, e.g. pbx.example.com.
	Name string `yaml:"name"`
	// DNSProvider is "cloudflare" or "duckdns".
	DNSProvider string `yaml:"dns_provider"`
}

// CertificateConfig controls the public certificate (linx-certd).
type CertificateConfig struct {
	// Staging uses Let's Encrypt's test certificates (not trusted by browsers).
	Staging bool `yaml:"staging"`
	// Wildcard issues one *.domain certificate instead of one naming each host.
	Wildcard bool `yaml:"wildcard"`
	// Email is the certificate authority's contact. Required when not staging.
	Email string `yaml:"email"`
}

// DNS providers setup offers. They must match internal/certs.Providers.
const (
	DNSCloudflare = "cloudflare"
	DNSDuckDNS    = "duckdns"
)

// DNSProviders lists the valid DNS providers, default first.
var DNSProviders = []string{DNSCloudflare, DNSDuckDNS}

// emailRE is deliberately strict: the address is written unquoted into the
// Compose .env file, so quotes, $ and # must never reach it.
var emailRE = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}$`)

// DockerConfig controls the Docker prerequisite.
type DockerConfig struct {
	// Install allows setup to install or upgrade Docker from Docker's official
	// repository when it's missing or too old.
	Install bool `yaml:"install"`
}

// DefaultConfig is used when there are no saved answers.
func DefaultConfig() Config {
	return Config{
		Version: 1, ContainerUI: ContainerUINone, ResourceProfile: ProfileAuto,
		Domain:       DomainConfig{DNSProvider: DNSCloudflare},
		Certificates: CertificateConfig{Staging: true, Wildcard: true},
		FrontDoor:    FrontDoorConfig{Kind: FrontDoorNone},
	}
}

// ParseConfig reads setup.yaml. Unknown keys are errors so typos don't pass
// silently. Omitted keys keep their defaults.
func ParseConfig(r io.Reader) (Config, error) {
	c := DefaultConfig()
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("setup.yaml: %w", err)
	}
	c.Domain.Name = strings.ToLower(strings.TrimSpace(c.Domain.Name))
	return c, c.Validate()
}

// Validate checks every field.
func (c Config) Validate() error {
	var errs []error
	if c.Version != 1 {
		errs = append(errs, fmt.Errorf("version: must be 1, got %d", c.Version))
	}
	if !slices.Contains(ContainerUIs, c.ContainerUI) {
		errs = append(errs, fmt.Errorf("container_ui: must be one of %v, got %q", ContainerUIs, c.ContainerUI))
	}
	if c.ResourceProfile != ProfileAuto && !slices.Contains(Profiles, c.ResourceProfile) {
		errs = append(errs, fmt.Errorf("resource_profile: must be auto or one of %v, got %q", Profiles, c.ResourceProfile))
	}
	// An empty domain is allowed here so answers saved before the domain
	// question still load; setup asks for it, or refuses in --config mode.
	if c.Domain.Name != "" {
		if err := ValidateDomain(c.Domain.Name, c.Domain.DNSProvider); err != nil {
			errs = append(errs, fmt.Errorf("domain.name: %w", err))
		}
	}
	if !slices.Contains(DNSProviders, c.Domain.DNSProvider) {
		errs = append(errs, fmt.Errorf("domain.dns_provider: must be one of %v, got %q", DNSProviders, c.Domain.DNSProvider))
	}
	if err := ValidateEmail(c.Certificates.Email, c.Certificates.Staging); err != nil {
		errs = append(errs, fmt.Errorf("certificates.email: %w", err))
	}
	if err := c.FrontDoor.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("front_door.%w", err))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("setup.yaml: %w", err)
	}
	return nil
}

// ValidateDomain checks a lower-case base domain for the given DNS provider.
func ValidateDomain(name, provider string) error {
	if err := dnsname.ValidDomain(name); err != nil {
		return err
	}
	if provider == DNSDuckDNS && (!strings.HasSuffix(name, ".duckdns.org") || strings.Count(name, ".") != 2) {
		return errors.New("DuckDNS names look like yourname.duckdns.org")
	}
	return nil
}

// ValidateEmail checks the certificate contact email. It may be empty only
// while using test (staging) certificates.
func ValidateEmail(email string, staging bool) error {
	switch {
	case email == "" && !staging:
		return errors.New("required for trusted certificates (set certificates.staging: true to test without one)")
	case email != "" && !emailRE.MatchString(email):
		return fmt.Errorf("%q doesn't look like an email address", email)
	}
	return nil
}

// Marshal renders the config with explanatory comments.
func (c Config) Marshal() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, `# Linx setup answers. Written by linx setup; safe to edit.
# Re-run "sudo linx setup" to change them, or "sudo linx setup --config FILE" to apply a file without questions.
version: %d
docker:
  # Allow setup to install or upgrade Docker from Docker's official repository.
  install: %t
# Optional container management screen: none or portainer.
container_ui: %s
# Resource profile: auto (chosen from the hardware), lite, standard or performance.
resource_profile: %s
domain:
  # Base domain. Linx uses admin., api., meet., provision., sip. and turn. under it.
  name: %s
  # Where the domain's DNS is managed: cloudflare or duckdns. The API token is
  # kept in `+DNSTokenPath+`, not here.
  dns_provider: %s
certificates:
  # true: Let's Encrypt test certificates (browsers warn). Set to false once
  # everything works to get trusted certificates.
  staging: %t
  # true: one certificate for *.<domain>, which keeps the host names private.
  wildcard: %t
  # Contact for certificate expiry notices. Required when staging is false.
  email: %q
# What sits in front of Linx on the internet (docs/WEB.md §3): pangolin,
# nginx (nginx or HAProxy on port 443), http-proxy (Caddy, Nginx Proxy
# Manager, ...), linx-443 (nothing: Linx takes port 443 itself), home-only
# (Linx answers on this home network only) or none.
front_door:
  kind: %s
  # For pangolin, nginx and http-proxy: the home-network address of the
  # machine it runs on (this server's own, if it runs here).
  proxy_address: %q
`, c.Version, c.Docker.Install, c.ContainerUI, c.ResourceProfile,
		c.Domain.Name, c.Domain.DNSProvider, c.Certificates.Staging, c.Certificates.Wildcard, c.Certificates.Email,
		c.FrontDoor.Kind, c.FrontDoor.ProxyAddress)
	return b.Bytes()
}
