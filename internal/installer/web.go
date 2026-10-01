package installer

import (
	"context"
	"fmt"
	"linxpbx.com/linx/internal/dnsname"
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
	"linxpbx.com/linx/internal/weburl"
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

// FrontDoorsFor lists the front doors the install page and Server
// settings offer for where the server is (docs/ui/SCREENS_PHASE1F.md
// §1.1): a rented server's recommended one first. "none" isn't offered: a
// web install needs a web address. A proxy that decrypts is advanced and
// home only (owner decision 2026-09-30); another public port is advanced,
// offered on rented servers too (owner decision 2026-09-29, ADR-064).
func FrontDoorsFor(where string) []string {
	if where == install.WhereRented {
		return []string{FrontDoorLinx443, FrontDoorProxy, FrontDoorPublicPort}
	}
	return []string{FrontDoorLinx443, FrontDoorProxy, FrontDoorHomeOnly, FrontDoorPublicPort, FrontDoorHTTPProxy}
}

// FrontDoorShort is each front door as the terminal's progress line says it.
var FrontDoorShort = map[string]string{
	FrontDoorProxy:      "another program passes Linx through",
	FrontDoorPangolin:   "Pangolin",
	FrontDoorNginx:      "nginx or HAProxy",
	FrontDoorHTTPProxy:  "a proxy that unlocks the traffic",
	FrontDoorLinx443:    "Linx takes port 443",
	FrontDoorHomeOnly:   "only at home",
	FrontDoorPublicPort: "Linx takes another public port",
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

	fd, fdErrs := FrontDoorChoice(a.Where, a.FrontDoor, a.ProxyAddress, a.TURNUDPPort, a.PublicPort, lan)
	errs = append(errs, fdErrs...)

	domain, provider, msg := DomainFor(a.Domain)
	if msg != "" {
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
		add(install.StepYou, "email", "Give an email for certificate notices.")
	case !emailRE.MatchString(email):
		add(install.StepYou, "email", "That doesn't look like an email address.")
	}
	switch adminEmail := strings.TrimSpace(a.AdminEmail); {
	case adminEmail == "":
		add(install.StepYou, "admin_email", "Give the email you'll sign in with.")
	case !emailRE.MatchString(adminEmail):
		add(install.StepYou, "admin_email", "That doesn't look like an email address.")
	}
	zone := strings.TrimSpace(a.TimeZone)
	if zone == "" {
		add(install.StepYou, "time_zone", "Choose your time zone.")
	} else if ValidateTimeZone(zone) != nil {
		add(install.StepYou, "time_zone", "That isn't a time zone this server knows. Choose one from the list.")
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
	c.TimeZone = zone
	c.Install = InstallConfig{Where: a.Where, TermsAgreedAt: now.UTC().Format(time.RFC3339)}
	if err := c.Validate(); err != nil {
		// WebConfig's own checks should have caught everything; say what's
		// left rather than save a file setup can't read back.
		return base, []install.FieldError{{Step: install.StepDomain, Field: "", Message: err.Error()}}
	}
	return c, nil
}

// FrontDoorChoice checks a front door chosen on a page (the install's, or
// Server settings') for where the server is. Refusals are plain words,
// tied to the front-door step.
func FrontDoorChoice(where, kind, proxyAddress string, turnUDPPort, publicPort int, lan LAN) (FrontDoorConfig, []install.FieldError) {
	var errs []install.FieldError
	add := func(field, msg string) {
		errs = append(errs, install.FieldError{Step: install.StepFrontDoor, Field: field, Message: msg})
	}
	fd := FrontDoorConfig{Kind: kind}
	switch {
	case !slices.Contains(FrontDoorsFor(where), kind):
		add("front_door", "Choose what's in front of this server.")
	case (NeedsProxyAddress(kind) || kind == FrontDoorHomeOnly) && !lan.OK():
		add("front_door", "That needs this server on a home network, and it isn't on one. "+
			"Choose “Nothing else uses port 443 — Linx takes it”, or run setup on the home server.")
	case NeedsProxyAddress(kind):
		fd.ProxyAddress = strings.TrimSpace(proxyAddress)
		if err := ValidateProxyAddress(fd.ProxyAddress); err != nil {
			add("proxy_address", upperFirst(err.Error())+".")
		}
		if turnUDPPort != 0 && turnUDPPort != PublicPort {
			if err := ValidateTURNUDPPort(turnUDPPort); err != nil {
				add("turn_udp_port", upperFirst(err.Error())+".")
			}
			fd.TURNUDPPort = turnUDPPort
		}
	case kind == FrontDoorPublicPort:
		switch {
		case publicPort == 0:
			add("public_port", "Give the public port, like 8443.")
		case publicPort == PublicPort:
			add("public_port", "443 is the standard port: choose “Nothing else uses port 443 — Linx takes it” instead.")
		default:
			if msg := weburl.Problem(publicPort); msg != "" {
				add("public_port", msg)
			}
		}
		fd.PublicPort = publicPort
		if turnUDPPort != 0 && turnUDPPort != PublicPort {
			if err := ValidateTURNUDPPort(turnUDPPort); err != nil {
				add("turn_udp_port", upperFirst(err.Error())+".")
			}
			fd.TURNUDPPort = turnUDPPort
		}
	}
	return fd, errs
}

// DomainFor tidies a domain typed on a page and says which DNS company it
// belongs to, or what's wrong with it in plain words.
func DomainFor(raw string) (domain, provider, problem string) {
	domain = strings.ToLower(strings.TrimSpace(raw))
	provider = DNSCloudflare
	if strings.HasSuffix(domain, ".duckdns.org") {
		provider = DNSDuckDNS
	}
	return domain, provider, domainProblem(domain, provider)
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
	if dnsname.ValidDomain(d) != nil {
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
		fileStep("Write the installer's settings", installStackEnv, []byte(installEnv(imageTag, address)), 0o644, 0o755),
		cmdStep("Download Linx", "docker", append(dc, "pull", "--quiet")...),
		cmdStep("Start the installer", "docker", append(dc, "up", "--detach", "--wait")...),
	}
}

// FirstPageTLSPlan keeps the first page's certificate (docs/INSTALL.md §14
// item 1) in install.FirstPageTLSDir: the one already there while it still
// does for addrs, so the browser's exception keeps working, else a new one.
func FirstPageTLSPlan(addrs []netip.Addr, readFile func(string) ([]byte, error), now time.Time) (Plan, error) {
	certPath := install.FirstPageTLSDir + "/" + install.FirstPageCertFile
	keyPath := install.FirstPageTLSDir + "/" + install.FirstPageKeyFile
	c, cerr := readFile(certPath)
	k, kerr := readFile(keyPath)
	if cerr == nil && kerr == nil && install.FirstPageCertUsable(c, k, addrs, now) {
		return nil, nil
	}
	certPEM, keyPEM, err := install.NewFirstPageCert(addrs, now)
	if err != nil {
		return nil, err
	}
	key := fileStep("Save the installer's temporary certificate key (readable by root and the Linx services only)", keyPath, keyPEM, 0o440, 0o755)
	key.File.Gid = nonrootGID
	return Plan{
		key,
		fileStep("Save the installer's temporary certificate", certPath, certPEM, 0o644, 0o755),
	}, nil
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
