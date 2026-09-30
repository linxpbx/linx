package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/dnscheck"
	"linxpbx.com/linx/internal/dnsname"
	"linxpbx.com/linx/internal/install"
	"linxpbx.com/linx/internal/installer"
)

// webSettings is the Server settings page on the host
// (install.SettingsApplier, docs/INSTALL.md §7): an installed server's
// size, Portainer, DNS token, domain and front door, changed with setup's
// own plans. It shares the install's host plumbing (webApply) for the
// token check and running plans.
type webSettings struct {
	*webApply
	// repair: the page is also open on port 6464 (the secure address is
	// broken), so every compose command keeps that port published.
	repair bool
	// lookup is a name's addresses at the domain's own name servers.
	lookup func(ctx context.Context, name string) ([]string, error)
}

func newWebSettings(w *webApply, repair bool) webSettings {
	return webSettings{webApply: w, repair: repair, lookup: dnscheck.Resolver{}.LookupA}
}

func (w webSettings) saved() (installer.Config, string, error) {
	b, err := w.env.savedConfig()
	if err != nil {
		return installer.Config{}, "", fmt.Errorf("reading %s: %w", installer.ConfigPath, err)
	}
	c, err := installer.ParseConfig(bytes.NewReader(b))
	if err != nil {
		return c, "", err
	}
	token := ""
	if t, err := w.env.readFile(installer.DNSTokenPath); err == nil && !c.Certificates.NoDNSToken {
		token = strings.TrimSpace(string(t))
	}
	return c, token, nil
}

// where is where the server is: as the install said, or, set up in the
// terminal, as setup finds it.
func (w webSettings) where(c installer.Config) string {
	if c.Install.Where != "" {
		return c.Install.Where
	}
	if w.lan.OK() {
		return install.WhereHome
	}
	return install.WhereRented
}

// portainerAllowed: Portainer is offered only on a server at home, as on
// the install page. A rented server's own network address can be private
// too (a cloud provider's), and Portainer there would be root on this
// server for whoever shares that network. One that setup already turned on
// can be kept (or turned off).
func (w webSettings) portainerAllowed(c installer.Config) bool {
	return w.lan.OK() && (w.where(c) == install.WhereHome || c.ContainerUI == installer.ContainerUIPortainer)
}

// publicAddress is this network's public address ("" when it can't tell).
func (w webSettings) publicAddress(ctx context.Context) string {
	if w.env.web.publicAddress == nil {
		return ""
	}
	a, err := w.env.web.publicAddress(ctx)
	if err != nil {
		return ""
	}
	return a.String()
}

func (w webSettings) View(ctx context.Context) (install.ServerView, error) {
	c, token, err := w.saved()
	if err != nil {
		return install.ServerView{}, err
	}
	opts, pick, reason := w.Profiles(ctx)
	v := install.ServerView{
		Where: w.where(c), FrontDoor: c.FrontDoor.Kind, ProxyAddress: c.FrontDoor.ProxyAddress, TURNUDPPort: c.FrontDoor.TURNUDPPort,
		Domain: c.Domain.Name, Provider: c.Domain.DNSProvider,
		Profile: c.ResourceProfile, Profiles: opts, ProfilePick: pick, ProfileReason: reason,
		Portainer:        c.ContainerUI == installer.ContainerUIPortainer,
		PortainerAllowed: w.portainerAllowed(c),
		PublicAddress:    w.publicAddress(ctx),
	}
	v.FrontDoors = installer.FrontDoorsFor(v.Where)
	if w.lan.OK() {
		v.LANAddress = w.lan.Address.String()
	}
	if v.Profile == installer.ProfileAuto || v.Profile == "" {
		v.Profile = pick
	}
	if token != "" {
		v.Token = "saved"
	}
	return v, nil
}

// change is a ServerChange worked out on setup.yaml: the new settings,
// the token they use, and what they move.
type change struct {
	before, next installer.Config
	token        string
	// domain: the domain changes; door: the front door does.
	domain, door bool
}

// field is a refusal for the page, in plain words.
func field(step, name, msg string) install.FieldError {
	return install.FieldError{Step: step, Field: name, Message: msg}
}

// plan checks ch against setup.yaml, the way setup.yaml and the install
// page are checked, and asks the DNS company about a token it will use
// for a new domain (read-only).
func (w webSettings) plan(ctx context.Context, ch install.ServerChange) (change, []install.FieldError, error) {
	c, token, err := w.saved()
	if err != nil {
		return change{}, nil, err
	}
	now, _ := w.View(ctx)
	x := change{before: c, next: c, token: token}
	next := &x.next
	next.ResourceProfile = ch.Profile
	if ch.Profile == now.ProfilePick {
		next.ResourceProfile = installer.ProfileAuto
	}
	next.ContainerUI = installer.ContainerUINone
	if ch.Portainer && w.portainerAllowed(c) {
		next.ContainerUI = installer.ContainerUIPortainer
	}
	newToken := strings.TrimSpace(ch.Token)
	if newToken != "" {
		x.token = newToken
		next.Certificates.NoDNSToken = false
	}

	var errs []install.FieldError
	if ch.Domain != "" {
		d, provider, msg := installer.DomainFor(ch.Domain)
		switch {
		case msg != "":
			errs = append(errs, field(install.StepDomain, "domain", msg))
		case d != c.Domain.Name:
			x.domain = true
			next.Domain.Name, next.Domain.DNSProvider = d, provider
			if provider != c.Domain.DNSProvider && newToken == "" && !next.Certificates.NoDNSToken {
				errs = append(errs, field(install.StepToken, "token", fmt.Sprintf("%s is at %s, not %s: give a %s token below.",
					d, providerName(provider), providerName(c.Domain.DNSProvider), providerName(provider))))
			}
		}
	}
	if ch.FrontDoor != "" {
		fd, fdErrs := installer.FrontDoorChoice(w.where(c), ch.FrontDoor, ch.ProxyAddress, ch.TURNUDPPort, w.lan)
		errs = append(errs, fdErrs...)
		if len(fdErrs) == 0 && fd != c.FrontDoor {
			x.door = true
			next.FrontDoor = fd
			if fd.Kind == installer.FrontDoorLinx443 && c.FrontDoor.Kind != installer.FrontDoorLinx443 {
				if user := installer.PortUser(ctx, w.env.runner, installer.PublicPort); user != "" {
					errs = append(errs, field(install.StepFrontDoor, "front_door", "Port 443 is used by "+user+
						" on this server. Stop it first, or choose that program as what's in front of Linx."))
				}
			}
		}
	}
	if next.Certificates.NoDNSToken && next.FrontDoor.Kind != installer.FrontDoorLinx443 {
		errs = append(errs, field(install.StepToken, "token", "With "+installer.FrontDoorShort[next.FrontDoor.Kind]+
			" in front, Linx needs your DNS company's token to get its certificate: add it below."))
	}
	if len(errs) > 0 {
		return x, errs, nil
	}
	if x.token != "" && !next.Certificates.NoDNSToken && (newToken != "" || x.domain) {
		refusal, err := w.tokenRefusalFor(ctx, next.Domain.DNSProvider, next.Domain.Name, x.token)
		if err != nil {
			return x, nil, err
		}
		if refusal != "" {
			return x, []install.FieldError{field(install.StepToken, "token", refusal)}, nil
		}
	}
	if err := next.Validate(); err != nil {
		return x, nil, err
	}
	return x, nil, nil
}

func providerName(p string) string {
	if p == installer.DNSDuckDNS {
		return "DuckDNS"
	}
	return "Cloudflare"
}

// records are the DNS records to add by hand before a new domain's first
// certificate: without a token, Let's Encrypt checks them on port 443.
func (w webSettings) records(ctx context.Context, x change) []install.Record {
	if !x.domain || !x.next.Certificates.NoDNSToken {
		return nil
	}
	return installer.CertView(x.next, w.lan, install.Facts{PublicAddress: w.publicAddress(ctx)}).AddRecords
}

func (w webSettings) Preview(ctx context.Context, ch install.ServerChange) (install.ServerPreview, error) {
	x, errs, err := w.plan(ctx, ch)
	if err != nil {
		return install.ServerPreview{}, err
	}
	p := install.ServerPreview{Errors: errs, Address: "https://" + x.next.Domain.Name}
	if len(errs) > 0 {
		return p, nil
	}
	p.AddRecords = w.records(ctx, x)
	if s := installer.DoorSetup(x.next, w.lan); s != nil && (x.door || (x.domain && (len(s.Files) > 0 || s.Card != nil))) {
		p.Setup = s
	}
	old, d := x.before.Domain.Name, x.next.Domain.Name
	if x.domain {
		p.Warnings = append(p.Warnings,
			"Linx moves to https://"+d+". https://"+old+" stops working, and everyone signs in again at the new address.",
			"Passkeys only work at the address they were made for. Before you apply, check you can sign in with your password and "+
				"authenticator app (or a recovery code), then add new passkeys at the new address. A system admin with only a passkey "+
				"gets back in with  sudo linx user setup-link EMAIL  on the server.",
			"If you use company sign-in, change its redirect address at Google or Microsoft to https://"+d+"/api/v1/sso/callback.")
		if w.lan.OK() {
			p.Warnings = append(p.Warnings, "Desk phones and phone apps set up with "+dnsname.Host("sip", old)+" need "+dnsname.Host("sip", d)+
				" as their server: change it on each one.")
		}
	}
	if len(p.AddRecords) > 0 {
		p.Warnings = append(p.Warnings, "Add the DNS records below at your DNS company first: Linx checks them before it asks Let's Encrypt.")
	}
	if p.Setup != nil {
		p.Warnings = append(p.Warnings, "Until the steps below are done, Linx can't be reached from outside your network.")
	}
	rows, err := w.rows(ctx, ch, x)
	if err != nil {
		return install.ServerPreview{}, err
	}
	for _, r := range rows {
		p.Steps = append(p.Steps, r.title)
	}
	return p, nil
}

// rows are the change's steps, as the page shows them.
func (w webSettings) rows(ctx context.Context, ch install.ServerChange, x change) ([]applyRow, error) {
	next, token := x.next, x.token
	// An install from before the Alpine database keeps Debian (database.go).
	installer.DecideDatabaseImage(ctx, w.env.runner, &next)
	var rows []applyRow
	if recs := w.records(ctx, x); len(recs) > 0 {
		// A new domain without a token: its first certificate through port
		// 443, before anything restarts (the running Linx answers the check).
		names := make([]string, len(recs))
		for i, r := range recs {
			names[i] = r.Name
		}
		d := next.Domain.Name
		rows = append(rows,
			applyRow{title: "Check the DNS records for " + d, run: func(ctx context.Context) error { return w.checkRecords(ctx, recs) }},
			applyRow{title: "Test certificate for " + strings.Join(names, ", "), run: func(ctx context.Context) error { return w.obtain(ctx, d, true) }},
			applyRow{title: "Certificate for " + strings.Join(names, ", "), run: func(ctx context.Context) error { return w.obtain(ctx, d, false) }},
		)
	}
	rows = append(rows, applyRow{title: "Save your settings", plan: installer.Plan{{Title: "Save your answers to " + installer.ConfigPath,
		File: &installer.File{Path: installer.ConfigPath, Data: next.Marshal(), Mode: 0o600, DirMode: 0o755}}}})
	if ch.Token != "" {
		rows = append(rows, applyRow{title: "Your DNS company's token", plan: installer.SaveDNSTokenPlan(token)})
	}
	if x.door {
		phones, err := installer.PhonesPlan(ctx, w.env.runner, w.lan, installer.FrontDoorFor(next, w.lan), w.env.readFile)
		if err != nil {
			return nil, fmt.Errorf("can't plan the firewall: %w", err)
		}
		if w.repair {
			// Loading the rules closes port 6464: the repair page is still open.
			phones = append(phones, installer.RepairFirewallStep())
		}
		rows = append(rows, applyRow{title: "Firewall for " + installer.FrontDoorShort[next.FrontDoor.Kind], plan: phones})
	}
	now := next.ContainerUI == installer.ContainerUIPortainer
	was := x.before.ContainerUI == installer.ContainerUIPortainer
	switch {
	case now && !was:
		p := installer.PortainerPlan(w.lan.BindAddress())
		rows = append(rows, applyRow{title: "Portainer (home network only)", plan: p.Plan, keep: []install.KeepItem{{
			Title: "Portainer password", Value: p.Password,
			Note: "Open " + p.URL + " from your home network and sign in as admin. Portainer can control everything on this server: never forward its port on your router.",
		}}})
	case was && !now:
		rows = append(rows, applyRow{title: "Stop Portainer", plan: installer.PortainerStopPlan()})
	}
	// Setup's own stack plan, as a terminal setup would run it: the
	// certificate is got again only if its names change (a new token or
	// domain brings the wildcard for it), and only services whose settings
	// changed restart.
	var restart installer.Plan
	files, dns := installer.FrontDoorPlan(next, w.lan)
	if x.domain || x.door {
		restart = append(restart, files...)
	}
	restart = append(restart, installer.StackPlan(next, token, w.imageTag, w.lan).Plan...)
	restart = append(restart, installer.PruneOldImagesStep())
	// Linx's port 443 router reads its settings only when it starts: restart
	// it whenever they change (a new domain or front door, or new defaults).
	restart = append(restart, installer.SNIRestartPlan(next, w.lan, w.env.readFile)...)
	rows = append(rows, applyRow{title: "Restart Linx with the new settings", plan: restart})
	if token != "" && !next.Certificates.NoDNSToken && (ch.Token != "" || x.domain || x.door) && len(dns) > 0 {
		rows = append(rows, applyRow{title: strings.TrimSuffix(strings.TrimPrefix(dns[0].Title, "Point "), " (DNS)"), plan: dns})
	}
	if w.repair {
		for i := range rows {
			rows[i].plan = installer.WithRepair(rows[i].plan)
		}
	}
	return rows, nil
}

// checkRecords checks each record points at its address, and only there,
// at the domain's own name servers.
func (w webSettings) checkRecords(ctx context.Context, recs []install.Record) error {
	var wrong []string
	for _, r := range recs {
		got, err := w.lookup(ctx, r.Name)
		switch {
		case err != nil:
			wrong = append(wrong, r.Name+": can't look it up ("+err.Error()+")")
		case len(got) == 0:
			wrong = append(wrong, r.Name+" has no record yet")
		case !slices.Equal(got, []string{r.Value}):
			wrong = append(wrong, r.Name+" points at "+strings.Join(got, ", ")+", not "+r.Value)
		}
	}
	if len(wrong) > 0 {
		return errors.New(strings.Join(wrong, "; ") + ". DNS changes can take a few minutes: apply again once they're right")
	}
	return nil
}

// obtain gets a new domain's certificate through port 443: the test one
// only proves it works, the real one replaces the one Linx serves.
func (w webSettings) obtain(ctx context.Context, domain string, staging bool) error {
	mode := "real"
	if staging {
		mode = "staging"
	}
	out, err := w.env.runner.Run(ctx, nil, "docker", installer.StackCertdRun(domain, "-bootstrap", mode)...)
	found, res := certs.ParseBootstrapResult(out)
	switch {
	case found:
		return res
	case err != nil:
		return errors.New(lastLines(string(out), err))
	}
	return nil
}

// Run makes the rows in order and stops at the first failure: every step
// is safe to repeat, so the page's Apply just tries again.
func (w webSettings) Run(ctx context.Context, ch install.ServerChange, report func(int, string, string), keep func(install.KeepItem)) error {
	x, errs, err := w.plan(ctx, ch)
	if err != nil {
		return err
	}
	if len(errs) > 0 {
		return errors.New(errs[0].Message)
	}
	rows, err := w.rows(ctx, ch, x)
	if err != nil {
		return err
	}
	for i, r := range rows {
		report(i, install.StageRunning, "")
		var err error
		if r.run != nil {
			err = r.run(ctx)
		} else {
			err = w.execute(ctx, r.plan)
		}
		if err != nil {
			detail := stepProblem(err)
			report(i, install.StageFailed, detail)
			return errors.New(r.title + ": " + detail)
		}
		report(i, install.StageOK, "")
		for _, k := range r.keep {
			keep(k)
		}
	}
	return nil
}

var _ install.SettingsApplier = webSettings{}
