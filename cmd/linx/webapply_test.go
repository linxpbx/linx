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
	Name: "Sam Lee", Email: "sam@example.com", TimeZone: "Asia/Dubai", AgreedToTerms: true}

var homeAnswers = install.Answers{Where: install.WhereHome, FrontDoor: installer.FrontDoorPangolin, ProxyAddress: "192.168.1.30",
	Domain: "pbx.example.com", Name: "Sam Lee", Email: "sam@example.com", TimeZone: "Asia/Dubai", AgreedToTerms: true}

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
		"Start Linx (this setup page closes)", "Your account (sam@example.com)", "Phone system and call audio",
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
	if msg, err := r.w.SaveToken(ctx, "short"); err != nil || msg == "" {
		t.Errorf("short: %q %v", msg, err)
	}
	if msg, err := r.w.SaveToken(ctx, strings.Repeat("n", 40)); err != nil || msg == "" || strings.Contains(msg, "token refused") {
		t.Errorf("refused: %q %v", msg, err)
	}
	if msg, err := r.w.SaveToken(ctx, " "+strings.Repeat("y", 40)+"\n"); err != nil || msg != "" || len(r.plans) != 1 ||
		!strings.Contains(planText(r.plans[0]), "> "+installer.DNSTokenPath+"\n"+strings.Repeat("y", 40)+"\n") {
		t.Errorf("good: %q %v %v", msg, err, r.plans)
	}
	opts, pick, reason := r.w.Profiles(ctx)
	if len(opts) != 3 || pick != "lite" || reason == "" || opts[1].Description == "" {
		t.Errorf("profiles %+v %q %q", opts, pick, reason)
	}
}

func TestWebSettings(t *testing.T) {
	ctx := context.Background()
	r := newApplyRig(t, homeAnswers, homeLAN)
	s := webSettings{r.w}
	v, err := s.View(ctx)
	if err != nil || v.Where != install.WhereHome || v.Token != "saved" || v.Profile != "lite" || v.Portainer || !v.PortainerAllowed {
		t.Fatalf("view %+v %v", v, err)
	}
	titles, err := s.Steps(ctx, install.ServerChange{Profile: "standard", Portainer: true})
	if err != nil || !slices.Equal(titles, []string{"Save your settings", "Portainer (home network only)", "Restart Linx with the new settings"}) {
		t.Fatalf("rows %v %v", titles, err)
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
	s = webSettings{r.w}
	if v, _ := s.View(ctx); v.Token != "" || v.PortainerAllowed {
		t.Errorf("rented view %+v", v)
	}
	token := strings.Repeat("y", 40)
	titles, _ = s.Steps(ctx, install.ServerChange{Profile: "lite", Token: token})
	if !slices.Contains(titles, "Your DNS company's token") || !strings.HasPrefix(titles[len(titles)-1], "pbx.example.com, turn.pbx.example.com at") {
		t.Errorf("token rows %v", titles)
	}
	if refusal, _ := s.CheckToken(ctx, strings.Repeat("n", 40)); refusal == "" {
		t.Error("refused token accepted")
	}
}
