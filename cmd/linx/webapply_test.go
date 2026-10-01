package main

import (
	"context"
	"errors"
	"io/fs"
	"net/netip"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/install"
	"linxpbx.com/linx/internal/installer"
)

// applyRig is a webApply whose plans and commands are recorded, not run.
type applyRig struct {
	w       *webApply
	plans   []installer.Plan
	fail    func(p installer.Plan) error
	stdin   string
	execs   []string
	adminRC int
	saved   map[string]string
}

func newApplyRig(t *testing.T, a install.Answers, lan installer.LAN) *applyRig {
	t.Helper()
	cfg, errs := installer.WebConfig(installer.DefaultConfig(), a, lan, time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC))
	if len(errs) > 0 {
		t.Fatalf("answers: %+v", errs)
	}
	r := &applyRig{saved: map[string]string{installer.DNSTokenPath: testToken}}
	env := testEnv("", nil)
	env.savedConfig = func() ([]byte, error) { return cfg.Marshal(), nil }
	env.readFile = func(p string) ([]byte, error) {
		if s, ok := r.saved[p]; ok {
			return []byte(s), nil
		}
		return nil, fs.ErrNotExist
	}
	env.lan = func() installer.LAN { return lan }
	w := newWebApply(env, lan, "sha-"+testCommit)
	w.now = func() time.Time { return time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC) }
	w.execute = func(_ context.Context, p installer.Plan) error {
		r.plans = append(r.plans, p)
		if r.fail != nil {
			return r.fail(p)
		}
		return nil
	}
	w.exec = func(_ context.Context, stdin []byte, name string, args ...string) ([]byte, int, error) {
		r.execs = append(r.execs, name+" "+strings.Join(args, " "))
		if stdin != nil {
			r.stdin = string(stdin)
		}
		if r.adminRC != 0 {
			return []byte("no"), r.adminRC, &exec.ExitError{}
		}
		return nil, 0, nil
	}
	w.checkToken = func(_ context.Context, provider, domain, token string) error {
		if token == strings.Repeat("n", 40) {
			return errors.Join(certs.ErrTokenRefused, errors.New("x"))
		}
		return nil
	}
	r.w = w
	return r
}

var rentedAnswers = install.Answers{Where: install.WhereRented, FrontDoor: installer.FrontDoorLinx443, Domain: "pbx.example.com",
	Name: "Sam Lee", Email: "certs@example.com", AdminEmail: "sam@example.com", TimeZone: "Asia/Dubai", AgreedToTerms: true}

var homeAnswers = install.Answers{Where: install.WhereHome, FrontDoor: installer.FrontDoorProxy, ProxyAddress: "192.168.1.30",
	Domain: "pbx.example.com", Name: "Sam Lee", Email: "certs@example.com", AdminEmail: "sam@example.com", TimeZone: "Asia/Dubai", AgreedToTerms: true}

var homeLAN = installer.LAN{Address: netip.MustParseAddr("192.168.1.20"), Network: netip.MustParsePrefix("192.168.1.0/24")}

func planText(p installer.Plan) string {
	var b strings.Builder
	for _, s := range p {
		b.WriteString(s.Title + "\n")
		if s.Cmd != nil {
			b.WriteString("$ " + s.Cmd.String() + "\n")
		}
		if s.File != nil {
			b.WriteString("> " + s.File.Path + "\n" + string(s.File.Data) + "\n")
		}
	}
	return b.String()
}

func TestWebApplyHome(t *testing.T) {
	r := newApplyRig(t, homeAnswers, homeLAN)
	in := install.ApplyInput{Answers: homeAnswers, Extras: install.Extras{Profile: "standard", Portainer: true}, SetupToken: strings.Repeat("k", 43)}
	titles, err := r.w.Steps(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Save your settings", "Firewall and phone ports on 192.168.1.20", "Internal certificate authority",
		"Portainer (home network only)", "Download Linx's services", "Certificate for pbx.example.com and *.pbx.example.com",
		"Start Linx (this setup page closes)", "Your system admin account (sam@example.com)", "Phone system and call audio",
		"pbx.example.com, turn.pbx.example.com at this network's public address; sip.pbx.example.com at 192.168.1.20 (for desk phones at home)",
		"Helpers: backups, status, firewall sync", "Finish"}
	if !slices.Equal(titles, want) {
		t.Fatalf("rows:\n%s", strings.Join(titles, "\n"))
	}

	var reports []string
	var kept []install.KeepItem
	switchedAt := -1
	err = r.w.Run(context.Background(), in,
		func(i int, state, _ string) { reports = append(reports, state) },
		func(k install.KeepItem) { kept = append(kept, k) },
		func() { switchedAt = len(r.plans) })
	if err != nil {
		t.Fatal(err)
	}
	// Rows run in order; the switch comes just before starting the control plane.
	if switchedAt != 6 || !strings.Contains(planText(r.plans[6]), "up --detach --wait control-plane") {
		t.Errorf("switched after %d plans: %s", switchedAt, planText(r.plans[min(switchedAt, len(r.plans)-1)]))
	}
	if r.stdin != strings.Repeat("k", 43)+"\n" {
		t.Errorf("first admin's token on stdin: %q", r.stdin)
	}
	if len(r.execs) != 2 || !strings.HasSuffix(r.execs[1], "service suggest-site home") {
		t.Errorf("commands: %q", r.execs)
	}
	// The admin signs in with their own email, not the certificate's.
	if len(r.execs) > 0 && (!strings.Contains(r.execs[0], "--email sam@example.com") || strings.Contains(r.execs[0], "certs@")) {
		t.Errorf("first admin made with %q", r.execs[0])
	}
	first := planText(r.plans[0])
	last := planText(r.plans[len(r.plans)-1])
	for _, s := range []string{"resource_profile: standard", "container_ui: portainer"} {
		if !strings.Contains(first, s) {
			t.Errorf("settings missing %q:\n%s", s, first)
		}
	}
	if strings.Contains(first, `finished_at: "2026`) || !strings.Contains(last, `finished_at: "2026-09-28T10:00:00Z"`) {
		t.Errorf("finished_at: first\n%s\nlast\n%s", first, last)
	}
	if !strings.Contains(planText(r.plans[5]), "certd -once") || !strings.Contains(planText(r.plans[4]), "LINX_DNS_RECORDS=@,turn,sip=192.168.1.20") {
		t.Errorf("certificate / .env:\n%s\n%s", planText(r.plans[5]), planText(r.plans[4]))
	}
	if len(kept) != 2 || kept[0].Title != "Certificate authority backup passphrase" || kept[1].Title != "Portainer password" {
		t.Errorf("kept %+v", kept)
	}
	if len(reports) != 2*len(want) {
		t.Errorf("reports %v", reports)
	}
}

func TestWebApplyRentedSkip(t *testing.T) {
	r := newApplyRig(t, rentedAnswers, installer.LAN{})
	delete(r.saved, installer.DNSTokenPath)
	in := install.ApplyInput{Answers: rentedAnswers, SkipToken: true, Extras: install.Extras{Portainer: true}, SetupToken: "t"}
	titles, err := r.w.Steps(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(titles, "Portainer (home network only)") || !slices.Contains(titles, "Certificate (renews through port 443)") || titles[1] != "Firewall" {
		t.Errorf("rows: %v", titles)
	}
	for _, ti := range titles {
		if strings.Contains(ti, "(DNS)") || strings.Contains(ti, "turn.pbx") {
			t.Errorf("DNS row without a token: %q", ti)
		}
	}
	if err := r.w.Run(context.Background(), in, func(int, string, string) {}, func(install.KeepItem) {}, func() {}); err != nil {
		t.Fatal(err)
	}
	if len(r.execs) != 1 {
		t.Errorf("a rented server got a place suggested: %q", r.execs)
	}
	if s := planText(r.plans[0]); !strings.Contains(s, "no_dns_token: true") || !strings.Contains(s, "container_ui: none") {
		t.Errorf("settings:\n%s", s)
	}
	// No token given and none skipped: refused before anything runs.
	in.SkipToken = false
	if _, err := r.w.Steps(context.Background(), in); err == nil {
		t.Error("steps without a token")
	}
}

func TestWebApplyFailures(t *testing.T) {
	r := newApplyRig(t, rentedAnswers, installer.LAN{})
	in := install.ApplyInput{Answers: rentedAnswers, SkipToken: true, SetupToken: "t"}
	// Before the switch: the first failure stops it.
	r.fail = func(p installer.Plan) error {
		if strings.Contains(planText(p), "certd -once") {
			return &installer.StepError{Step: p[0], Output: "rate limited", Err: errors.New("exit status 1")}
		}
		return nil
	}
	switched := false
	states := map[int]string{}
	err := r.w.Run(context.Background(), in, func(i int, s, _ string) { states[i] = s }, func(install.KeepItem) {}, func() { switched = true })
	if err == nil || switched || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("certificate failure: %v, switched %v", err, switched)
	}

	// After it: the rest still run; the first admin already existing is fine.
	r = newApplyRig(t, rentedAnswers, installer.LAN{})
	r.adminRC = exitFirstAdminExists
	r.fail = func(p installer.Plan) error {
		if strings.Contains(planText(p), "restic") {
			return errors.New("apt is busy")
		}
		return nil
	}
	err = r.w.Run(context.Background(), in, func(int, string, string) {}, func(install.KeepItem) {}, func() {})
	if err == nil || !strings.Contains(err.Error(), "Helpers") || !strings.Contains(planText(r.plans[len(r.plans)-1]), "finished_at") {
		t.Errorf("helpers failure: %v", err)
	}

	// No first admin: stops there.
	r = newApplyRig(t, rentedAnswers, installer.LAN{})
	r.adminRC = 1
	n := 0
	err = r.w.Run(context.Background(), in, func(int, string, string) {}, func(install.KeepItem) {}, func() { n = len(r.plans) })
	if err == nil || len(r.plans) != n+1 {
		t.Errorf("first admin failure: %v, %d plans after the switch", err, len(r.plans)-n)
	}
}

func TestWebApplyToken(t *testing.T) {
	r := newApplyRig(t, homeAnswers, homeLAN)
	ctx := context.Background()
	if msg, err := r.w.SaveToken(ctx, install.DNSKey{Token: "short"}); err != nil || msg == "" {
		t.Errorf("short: %q %v", msg, err)
	}
	if msg, err := r.w.SaveToken(ctx, install.DNSKey{Token: strings.Repeat("n", 40)}); err != nil || msg == "" || strings.Contains(msg, "token refused") {
		t.Errorf("refused: %q %v", msg, err)
	}
	if msg, err := r.w.SaveToken(ctx, install.DNSKey{Token: " " + strings.Repeat("y", 40) + "\n"}); err != nil || msg != "" || len(r.plans) != 1 ||
		!strings.Contains(planText(r.plans[0]), "> "+installer.DNSTokenPath+"\n"+strings.Repeat("y", 40)+"\n") ||
		strings.Contains(planText(r.plans[0]), installer.ConfigPath) {
		t.Errorf("good: %q %v %v", msg, err, r.plans)
	}
	// Another company: its fields kept as one JSON key, and the company in setup.yaml.
	var asked string
	r.w.checkToken = func(_ context.Context, provider, domain, token string) error {
		asked = provider + " " + token
		return nil
	}
	key := install.DNSKey{Provider: "porkbun", Fields: map[string]string{"api_key": "pk1_keykeykey", "secret_api_key": " sk1_secretsecret "}}
	want := `{"api_key":"pk1_keykeykey","secret_api_key":"sk1_secretsecret"}`
	if msg, err := r.w.SaveToken(ctx, key); err != nil || msg != "" || len(r.plans) != 2 || asked != "porkbun "+want ||
		!strings.Contains(planText(r.plans[1]), want) || !strings.Contains(planText(r.plans[1]), "dns_provider: porkbun") {
		t.Errorf("porkbun: %q %v %s", msg, err, planText(r.plans[len(r.plans)-1]))
	}
	for _, bad := range []install.DNSKey{
		{Provider: "porkbun", Fields: map[string]string{"api_key": "pk1_keykeykey"}},
		{Provider: "duckdns", Token: strings.Repeat("y", 40)}, // not a duckdns.org name
		{Provider: "gandi", Token: strings.Repeat("y", 40)},
	} {
		if msg, err := r.w.SaveToken(ctx, bad); err != nil || msg == "" || len(r.plans) != 2 {
			t.Errorf("%+v: %q %v", bad, msg, err)
		}
	}
	opts, pick, reason := r.w.Profiles(ctx)
	if len(opts) != 3 || pick != "lite" || reason == "" || opts[1].Description == "" {
		t.Errorf("profiles %+v %q %q", opts, pick, reason)
	}
}

func previewSteps(t *testing.T, s webSettings, ch install.ServerChange) []string {
	t.Helper()
	p, err := s.Preview(context.Background(), ch)
	if err != nil || len(p.Errors) > 0 {
		t.Fatalf("preview %+v: %v %v", ch, p.Errors, err)
	}
	return p.Steps
}

func TestWebSettings(t *testing.T) {
	ctx := context.Background()
	r := newApplyRig(t, homeAnswers, homeLAN)
	s := newWebSettings(r.w, false)
	v, err := s.View(ctx)
	if err != nil || v.Where != install.WhereHome || v.Token != "saved" || v.Profile != "lite" || v.Portainer || !v.PortainerAllowed ||
		v.ProxyAddress != "192.168.1.30" || !slices.Contains(v.FrontDoors, installer.FrontDoorLinx443) {
		t.Fatalf("view %+v %v", v, err)
	}
	titles := previewSteps(t, s, install.ServerChange{Profile: "standard", Portainer: true})
	if !slices.Equal(titles, []string{"Save your settings", "Portainer (home network only)", "Restart Linx with the new settings"}) {
		t.Fatalf("rows %v", titles)
	}
	var kept []install.KeepItem
	if err := s.Run(ctx, install.ServerChange{Profile: "standard", Portainer: true}, func(int, string, string) {}, func(k install.KeepItem) { kept = append(kept, k) }); err != nil {
		t.Fatal(err)
	}
	first := planText(r.plans[0])
	if !strings.Contains(first, "resource_profile: standard") || !strings.Contains(first, "container_ui: portainer") || len(kept) != 1 ||
		!strings.Contains(planText(r.plans[2]), "up --detach --wait") || !strings.Contains(planText(r.plans[2]), testToken) {
		t.Errorf("plans:\n%s\n%s, kept %v", first, planText(r.plans[2]), kept)
	}

	// Rented, set up without a token: adding one brings the DNS records.
	r = newApplyRig(t, rentedAnswers, installer.LAN{})
	delete(r.saved, installer.DNSTokenPath)
	s = newWebSettings(r.w, false)
	if v, _ := s.View(ctx); v.Token != "" || v.PortainerAllowed || slices.Contains(v.FrontDoors, installer.FrontDoorHTTPProxy) {
		t.Errorf("rented view %+v", v)
	}
	token := strings.Repeat("y", 40)
	titles = previewSteps(t, s, install.ServerChange{Profile: "lite", Token: token})
	if !slices.Contains(titles, "Your Cloudflare key") || !strings.HasPrefix(titles[len(titles)-1], "pbx.example.com, turn.pbx.example.com at") {
		t.Errorf("token rows %v", titles)
	}
	if p, _ := s.Preview(ctx, install.ServerChange{Profile: "lite", Token: strings.Repeat("n", 40)}); len(p.Errors) != 1 || p.Errors[0].Field != "token" {
		t.Errorf("refused token accepted: %+v", p)
	}

	// Set it up automatically at another company, then Stop, then start again.
	r = newApplyRig(t, homeAnswers, homeLAN)
	s = newWebSettings(r.w, false)
	pork := &install.DNSKey{Provider: "porkbun", Fields: map[string]string{"api_key": "pk1_keykeykey", "secret_api_key": "sk1_secretsecret"}}
	titles = previewSteps(t, s, install.ServerChange{Profile: "lite", Key: pork})
	if !slices.Contains(titles, "Your Porkbun key") || !strings.HasPrefix(titles[len(titles)-1], "pbx.example.com, turn.pbx.example.com") {
		t.Errorf("porkbun rows %v", titles)
	}
	if err := s.Run(ctx, install.ServerChange{Profile: "lite", Key: pork}, func(int, string, string) {}, func(install.KeepItem) {}); err != nil {
		t.Fatal(err)
	}
	if all := plansText(r.plans); !strings.Contains(all, "dns_provider: porkbun") || !strings.Contains(all, "LINX_DNS_PROVIDER=porkbun") ||
		!strings.Contains(all, "LINX_DNS_RECORDS=@,turn,sip=192.168.1.20") {
		t.Errorf("porkbun plans:\n%s", all)
	}
	stop := true
	r.plans = nil
	titles = previewSteps(t, s, install.ServerChange{Profile: "lite", DNSByHand: &stop})
	if slices.ContainsFunc(titles, func(s string) bool { return strings.Contains(s, " at ") }) {
		t.Errorf("stop points records: %v", titles)
	}
	if err := s.Run(ctx, install.ServerChange{Profile: "lite", DNSByHand: &stop}, func(int, string, string) {}, func(install.KeepItem) {}); err != nil {
		t.Fatal(err)
	}
	if all := plansText(r.plans); !strings.Contains(all, "dns_by_hand: true") || !strings.Contains(all, "LINX_DNS_RECORDS=\n") {
		t.Errorf("stop plans:\n%s", all)
	}
	// Stopped (as setup.yaml now says): the view says so, and Start again
	// points the records once more.
	byHand := []byte(strings.Replace(string(r.w.env.mustConfig(t)), "dns_provider: cloudflare\n", "dns_provider: porkbun\n  dns_by_hand: true\n", 1))
	r.w.env.savedConfig = func() ([]byte, error) { return byHand, nil }
	r.saved[installer.DNSTokenPath] = `{"api_key":"pk1_keykeykey","secret_api_key":"sk1_secretsecret"}`
	if v, _ := s.View(ctx); !v.DNSByHand || v.Provider != "porkbun" {
		t.Errorf("view after stop %+v", v)
	}
	again := false
	titles = previewSteps(t, s, install.ServerChange{Profile: "lite", DNSByHand: &again})
	if !strings.HasPrefix(titles[len(titles)-1], "pbx.example.com, turn.pbx.example.com") {
		t.Errorf("start again rows %v", titles)
	}

	// Rented, but its own address is private (a cloud provider's network):
	// Portainer isn't offered, as on the install page (security review).
	r = newApplyRig(t, rentedAnswers, homeLAN)
	s = newWebSettings(r.w, false)
	if v, _ := s.View(ctx); v.Where != install.WhereRented || v.PortainerAllowed {
		t.Errorf("rented on a private network: %+v", v)
	}
	if _, errs, err := s.plan(ctx, install.ServerChange{Profile: "lite", Portainer: true}); err != nil || len(errs) > 0 {
		t.Fatalf("plan: %v %v", errs, err)
	} else if x, _, _ := s.plan(ctx, install.ServerChange{Profile: "lite", Portainer: true}); x.next.ContainerUI != installer.ContainerUINone {
		t.Errorf("Portainer turned on on a rented server: %q", x.next.ContainerUI)
	}
}

// A new domain at home, with the saved token: the token must see it, the
// front door's block is shown again, and the restart brings everything to
// the new name.
func TestWebSettingsNewDomainHome(t *testing.T) {
	ctx := context.Background()
	r := newApplyRig(t, homeAnswers, homeLAN)
	s := newWebSettings(r.w, false)
	var asked []string
	r.w.checkToken = func(_ context.Context, provider, domain, token string) error {
		asked = append(asked, provider+" "+domain)
		return nil
	}
	ch := install.ServerChange{Profile: "lite", Domain: " PBX.Example.ORG "}
	p, err := s.Preview(ctx, ch)
	if err != nil || len(p.Errors) > 0 || p.Address != "https://pbx.example.org" || p.Setup == nil || p.Setup.Card == nil ||
		p.Setup.Card.Routes[0].Name != "pbx.example.org" || !strings.Contains(p.Setup.Card.Guides[0].Files[0].Text, "pbx.example.org") || len(p.AddRecords) != 0 || !slices.Equal(asked, []string{"cloudflare pbx.example.org"}) {
		t.Fatalf("preview %+v %v, asked %v", p, err, asked)
	}
	for _, want := range []string{"Passkeys only work", "sip.pbx.example.org", "/api/v1/sso/callback"} {
		if !strings.Contains(strings.Join(p.Warnings, "\n"), want) {
			t.Errorf("warnings miss %q: %v", want, p.Warnings)
		}
	}
	if err := s.Run(ctx, ch, func(int, string, string) {}, func(install.KeepItem) {}); err != nil {
		t.Fatal(err)
	}
	all := ""
	for _, pl := range r.plans {
		all += planText(pl)
	}
	for _, want := range []string{"name: pbx.example.org", "LINX_DOMAIN=pbx.example.org", "certd -once", "up --detach --wait", "pbx.example.org"} {
		if !strings.Contains(all, want) {
			t.Errorf("plans miss %q", want)
		}
	}

	// A DuckDNS name needs a DuckDNS token.
	p, _ = s.Preview(ctx, install.ServerChange{Profile: "lite", Domain: "mypbx.duckdns.org"})
	if len(p.Errors) != 1 || p.Errors[0].Field != "token" || !strings.Contains(p.Errors[0].Message, "DuckDNS") {
		t.Errorf("duckdns: %+v", p.Errors)
	}
	// Not a domain.
	p, _ = s.Preview(ctx, install.ServerChange{Profile: "lite", Domain: "203.0.113.9"})
	if len(p.Errors) != 1 || p.Errors[0].Field != "domain" {
		t.Errorf("address: %+v", p.Errors)
	}
}

// A rented server without a token: a new domain's records are checked,
// then its certificate comes through port 443 before anything restarts.
func TestWebSettingsNewDomainNoToken(t *testing.T) {
	ctx := context.Background()
	r := newApplyRig(t, rentedAnswers, installer.LAN{})
	delete(r.saved, installer.DNSTokenPath)
	cfg, _ := installer.ParseConfig(strings.NewReader(mustSaved(t, r)))
	cfg.Certificates.NoDNSToken = true
	r.w.env.savedConfig = func() ([]byte, error) { return cfg.Marshal(), nil }
	var ran []string
	r.w.env.runner = recordRunner{&ran}
	r.w.env.web.publicAddress = func(context.Context) (netip.Addr, error) { return netip.MustParseAddr("203.0.113.5"), nil }
	s := newWebSettings(r.w, true)
	lookups := map[string][]string{"pbx.example.org": {"203.0.113.5"}}
	s.lookup = func(_ context.Context, name string) ([]string, error) { return lookups[name], nil }

	ch := install.ServerChange{Profile: "lite", Domain: "pbx.example.org"}
	p, err := s.Preview(ctx, ch)
	if err != nil || len(p.AddRecords) != 2 || p.AddRecords[1].Name != "turn.pbx.example.org" || p.AddRecords[1].Value != "203.0.113.5" ||
		p.Steps[0] != "Check the DNS records for pbx.example.org" {
		t.Fatalf("preview %+v %v", p, err)
	}
	err = s.Run(ctx, ch, func(int, string, string) {}, func(install.KeepItem) {})
	if err == nil || !strings.Contains(err.Error(), "turn.pbx.example.org has no record yet") || len(r.plans) != 0 {
		t.Fatalf("records missing: %v, plans %d", err, len(r.plans))
	}
	lookups["turn.pbx.example.org"] = []string{"203.0.113.5"}
	if err := s.Run(ctx, ch, func(int, string, string) {}, func(install.KeepItem) {}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"docker compose --file /etc/linx/compose.yaml run --rm --no-TTY --env LINX_DOMAIN=pbx.example.org certd -bootstrap staging",
		"docker compose --file /etc/linx/compose.yaml run --rm --no-TTY --env LINX_DOMAIN=pbx.example.org certd -bootstrap real",
	}
	// Only the certificate commands matter here, not setup's read-only
	// look for an existing database (installer.DecideDatabaseImage).
	certRuns := slices.DeleteFunc(slices.Clone(ran), func(c string) bool { return strings.HasPrefix(c, "docker volume inspect ") })
	if !slices.Equal(certRuns, want) {
		t.Errorf("ran %q", ran)
	}
	// The repair page stays open through the restart.
	restart := planText(r.plans[len(r.plans)-1])
	if !strings.Contains(restart, "--file /etc/linx/compose.yaml --file /etc/linx/repair.yaml up --detach --wait") ||
		!strings.Contains(restart, "--force-recreate sni") {
		t.Errorf("restart:\n%s", restart)
	}

	// Without a token, only Linx's own port 443 works.
	p, _ = s.Preview(ctx, install.ServerChange{Profile: "lite", FrontDoor: installer.FrontDoorProxy, ProxyAddress: "203.0.113.7"})
	if len(p.Errors) == 0 {
		t.Errorf("nginx without a token: %+v", p)
	}
}

// A new front door from the repair page: the firewall is written again,
// and port 6464 opened again right after, so the page stays.
func TestWebSettingsNewDoorRepair(t *testing.T) {
	ctx := context.Background()
	r := newApplyRig(t, homeAnswers, homeLAN)
	s := newWebSettings(r.w, true)
	ch := install.ServerChange{Profile: "lite", FrontDoor: installer.FrontDoorLinx443}
	p, err := s.Preview(ctx, ch)
	if err != nil || len(p.Errors) > 0 || p.Setup == nil || !strings.Contains(strings.Join(p.Setup.Steps, " "), "send TCP and UDP port 443") {
		t.Fatalf("preview %+v %v", p, err)
	}
	if err := s.Run(ctx, ch, func(int, string, string) {}, func(install.KeepItem) {}); err != nil {
		t.Fatal(err)
	}
	var firewall string
	for _, pl := range r.plans {
		if txt := planText(pl); strings.Contains(txt, "Apply the firewall rules now") {
			firewall = txt
		}
	}
	load := strings.Index(firewall, "reload-or-restart linx-firewall.service")
	open := strings.Index(firewall, "nft add element inet linx install_page { 0.0.0.0/0 }")
	if load < 0 || open < load {
		t.Errorf("firewall row:\n%s", firewall)
	}
	if !strings.Contains(planText(r.plans[len(r.plans)-2]), "--file /etc/linx/repair.yaml up --detach --wait --force-recreate sni") {
		t.Errorf("restart:\n%s", planText(r.plans[len(r.plans)-2]))
	}
}

func mustSaved(t *testing.T, r *applyRig) string {
	t.Helper()
	b, err := r.w.env.savedConfig()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// recordRunner records commands and answers nothing.
type recordRunner struct{ ran *[]string }

func (r recordRunner) Run(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	*r.ran = append(*r.ran, name+" "+strings.Join(args, " "))
	return nil, nil
}

func plansText(plans []installer.Plan) string {
	var b strings.Builder
	for _, p := range plans {
		b.WriteString(planText(p))
	}
	return b.String()
}

func (e setupEnv) mustConfig(t *testing.T) []byte {
	t.Helper()
	b, err := e.savedConfig()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
