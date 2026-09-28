package installer

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/publicsuffix"

	"linxpbx.com/linx/deploy/compose"
	"linxpbx.com/linx/internal/install"
)

// The web-first install (docs/INSTALL.md, ADR-057): `sudo linx setup` starts
// the control plane in install mode on port 6464 (InstallStackPlan), and the
// plain page's answers come back here, checked with the same code as
// setup.yaml (WebConfig) before anything on the server changes.

// Files the install's first stack uses, next to compose.yaml.
const (
	InstallStackFile = StackDir + "/install.yaml"
	installStackEnv  = StackDir + "/install.env"
)

// InstallConfig is setup.yaml's install: how the web-first install went.
// Empty for a server set up from the terminal or with --config.
type InstallConfig struct {
	// Where is install.WhereHome or install.WhereRented.
	Where string `yaml:"where,omitempty"`
	// TermsAgreedAt is when Let's Encrypt's Subscriber Agreement was
	// ticked on the install page (RFC 3339).
	TermsAgreedAt string `yaml:"terms_agreed_at,omitempty"`
	// FinishedAt is when the install finished (RFC 3339); empty while it's
	// still under way.
	FinishedAt string `yaml:"finished_at,omitempty"`
}

func (i InstallConfig) validate() error {
	if i.Where != "" && i.Where != install.WhereHome && i.Where != install.WhereRented {
		return fmt.Errorf("where: must be %s or %s, got %q", install.WhereHome, install.WhereRented, i.Where)
	}
	for name, v := range map[string]string{"terms_agreed_at": i.TermsAgreedAt, "finished_at": i.FinishedAt} {
		if v == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return fmt.Errorf("%s: not a time like 2026-09-28T12:04:00Z: %q", name, v)
		}
	}
	return nil
}

// Installed reports whether setup has finished on this server: a domain
// set up from the terminal or with --config, or a web install that got to
// the end. A web install still under way isn't installed.
func (c Config) Installed() bool {
	return c.Domain.Name != "" && (c.Install.Where == "" || c.Install.FinishedAt != "")
}

// FrontDoorsFor lists the front doors the install page offers for where
// the server is (docs/ui/INSTALL_SCREENS.md §2.3): a rented server's
// recommended one first. "none" isn't offered: a web install needs a web
// address.
func FrontDoorsFor(where string) []string {
	if where == install.WhereRented {
		return []string{FrontDoorLinx443, FrontDoorNginx, FrontDoorHTTPProxy}
	}
	return []string{FrontDoorPangolin, FrontDoorNginx, FrontDoorHTTPProxy, FrontDoorLinx443, FrontDoorHomeOnly}
}

// FrontDoorShort is each front door as the terminal's progress line says it.
var FrontDoorShort = map[string]string{
	FrontDoorPangolin:  "Pangolin",
	FrontDoorNginx:     "nginx or HAProxy",
	FrontDoorHTTPProxy: "Caddy or Nginx Proxy Manager",
	FrontDoorLinx443:   "Linx takes port 443",
	FrontDoorHomeOnly:  "only at home",
}

const maxNameRunes = 100

// WebConfig checks the install page's answers and, if they're right,
// returns base (the saved setup.yaml, or DefaultConfig) with them applied.
// Every refusal is in plain words, tied to the step it belongs to.
func WebConfig(base Config, a install.Answers, lan LAN, now time.Time) (Config, []install.FieldError) {
	var errs []install.FieldError
	add := func(step, field, msg string) {
		errs = append(errs, install.FieldError{Step: step, Field: field, Message: msg})
	}

	if a.Where != install.WhereHome && a.Where != install.WhereRented {
		add(install.StepWhere, "where", "Choose where this server is.")
	}

	fd := FrontDoorConfig{Kind: a.FrontDoor}
	switch {
	case !slices.Contains(FrontDoorsFor(a.Where), a.FrontDoor):
		add(install.StepFrontDoor, "front_door", "Choose what's in front of this server.")
	case (NeedsProxyAddress(a.FrontDoor) || a.FrontDoor == FrontDoorHomeOnly) && !lan.OK():
		add(install.StepFrontDoor, "front_door", "That needs this server on a home network, and it isn't on one. "+
			"Choose “Directly — Linx answers on port 443 itself”, or run setup on the home server.")
	case NeedsProxyAddress(a.FrontDoor):
		fd.ProxyAddress = strings.TrimSpace(a.ProxyAddress)
		if err := ValidateProxyAddress(fd.ProxyAddress); err != nil {
			add(install.StepFrontDoor, "proxy_address", upperFirst(err.Error())+".")
		}
		if a.TURNUDPPort != 0 && a.TURNUDPPort != PublicPort {
			if err := ValidateTURNUDPPort(a.TURNUDPPort); err != nil {
				add(install.StepFrontDoor, "turn_udp_port", upperFirst(err.Error())+".")
			}
			fd.TURNUDPPort = a.TURNUDPPort
		}
	}

	domain := strings.ToLower(strings.TrimSpace(a.Domain))
	provider := DNSCloudflare
	if strings.HasSuffix(domain, ".duckdns.org") {
		provider = DNSDuckDNS
	}
	if msg := domainProblem(domain, provider); msg != "" {
		add(install.StepDomain, "domain", msg)
	}

	name := strings.TrimSpace(a.Name)
	switch {
	case name == "":
		add(install.StepYou, "name", "Give your name.")
	case utf8.RuneCountInString(name) > maxNameRunes:
		add(install.StepYou, "name", "That name is too long.")
	case strings.ContainsFunc(name, unicode.IsControl):
		add(install.StepYou, "name", "A name can't contain control characters.")
	}
	email := strings.TrimSpace(a.Email)
	switch {
	case email == "":
		add(install.StepYou, "email", "Give your email address.")
	case !emailRE.MatchString(email):
		add(install.StepYou, "email", "That doesn't look like an email address.")
	}
	if !a.AgreedToTerms {
		add(install.StepYou, "agreed_to_terms", "Tick the box to agree to Let's Encrypt's Subscriber Agreement. Linx can't get a certificate without it.")
	}
	if len(errs) > 0 {
		return base, errs
	}

	c := base
	c.Docker.Install = true
	c.Domain.Name, c.Domain.DNSProvider = domain, provider
	// Staging first, then the real certificate, is automatic now
	// (docs/INSTALL.md §4.2): the setting is the real one.
	c.Certificates = CertificateConfig{Staging: false, Wildcard: true, Email: email}
	c.FrontDoor = fd
	c.Install = InstallConfig{Where: a.Where, TermsAgreedAt: now.UTC().Format(time.RFC3339)}
	if err := c.Validate(); err != nil {
		// WebConfig's own checks should have caught everything; say what's
		// left rather than save a file setup can't read back.
		return base, []install.FieldError{{Step: install.StepDomain, Field: "", Message: err.Error()}}
	}
	return c, nil
}

// WebProgress is the terminal's line once the answers are saved.
func WebProgress(c Config) string {
	return fmt.Sprintf("Domain: %s, front door: %s", c.Domain.Name, FrontDoorShort[c.FrontDoor.Kind])
}

var digitsRE = regexp.MustCompile(`^[0-9]+$`)

// domainProblem is what's wrong with a domain, in plain words ("" if
// nothing).
func domainProblem(d, provider string) string {
	if d == "" {
		return "Give your domain, like example.com."
	}
	if _, err := netip.ParseAddr(d); err == nil || digitsRE.MatchString(d[strings.LastIndexByte(d, '.')+1:]) {
		return "That's an address, not a domain. It should look like example.com."
	}
	if ValidateDomain(d, DNSCloudflare) != nil {
		return "That isn't a domain. It should look like example.com."
	}
	if _, err := publicsuffix.EffectiveTLDPlusOne(d); err != nil {
		return d + " is shared by everyone. Use your own domain."
	}
	if err := ValidateDomain(d, provider); err != nil {
		return upperFirst(err.Error()) + "."
	}
	return ""
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// InstallStackPlan writes the install's first stack (deploy/compose/
// install.yaml: the control plane in install mode, publishing port 6464
// on address) and starts it.
func InstallStackPlan(imageTag string, address netip.Addr) Plan {
	dc := installCompose()
	return Plan{
		fileStep("Write the installer's services configuration", InstallStackFile, compose.InstallFile, 0o644, 0o755),
		fileStep("Write the installer's settings", installStackEnv, fmt.Appendf(nil,
			"# Generated by linx setup for the web install's first page (docs/INSTALL.md).\n"+
				"LINX_VERSION=%s\nLINX_INSTALL_ADDRESS=%s\n", imageTag, address), 0o644, 0o755),
		cmdStep("Download Linx", "docker", append(dc, "pull", "--quiet")...),
		cmdStep("Start the installer", "docker", append(dc, "up", "--detach", "--wait")...),
	}
}

// InstallStackDown stops the install's first stack.
func InstallStackDown(ctx context.Context, r Runner) error {
	out, err := r.Run(ctx, nil, "docker", append(installCompose(), "down")...)
	if err != nil {
		return fmt.Errorf("%w: %s", err, tail(string(out), 5))
	}
	return nil
}

func installCompose() []string {
	return []string{"compose", "--file", InstallStackFile, "--env-file", installStackEnv}
}

// PortUser names the program listening on TCP port here ("" if none), from
// ss. A port Docker publishes without its per-port helper doesn't show.
func PortUser(ctx context.Context, r Runner, port int) string {
	out, err := output(ctx, r, "ss", "-Hltnp", fmt.Sprintf("sport = :%d", port))
	if err != nil || out == "" {
		return ""
	}
	_, rest, ok := strings.Cut(out, `users:(("`)
	if !ok {
		return "another program"
	}
	name, _, _ := strings.Cut(rest, `"`)
	switch {
	case name == "docker-proxy":
		return "Docker (a container)"
	case name == "":
		return "another program"
	}
	return name
}
