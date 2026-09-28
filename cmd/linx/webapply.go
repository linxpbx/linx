package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/install"
	"linxpbx.com/linx/internal/installer"
)

// webApply is the secure page's Install on the host (install.Applier,
// docs/INSTALL.md §5 and §14): the terminal setup's own plans, from the
// saved answers and the page's token and extras, grouped into the rows the
// page shows. Its "Start Linx" row replaces the installer's stack with the
// full one (same project and names), which closes port 6464; the first
// admin is made right after, with the set-password token the page already
// holds, while the phone system starts.
type webApply struct {
	env      setupEnv
	lan      installer.LAN
	imageTag string
	now      func() time.Time
	// exec runs a command with stdin, for the first admin's token (a
	// process argument would show in ps). It returns the exit code.
	exec func(ctx context.Context, stdin []byte, name string, args ...string) (out []byte, code int, err error)
	// checkToken asks the DNS company whether the token can see the
	// domain (certs.RecordsClient.CheckToken).
	checkToken func(ctx context.Context, provider, domain, token string) error
	// execute runs a plan (Plan.Execute; tests record instead).
	execute func(ctx context.Context, p installer.Plan) error
}

func newWebApply(env setupEnv, lan installer.LAN, imageTag string) *webApply {
	return &webApply{
		env: env, lan: lan, imageTag: imageTag, now: time.Now,
		exec: func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, int, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdin = bytes.NewReader(stdin)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return out.Bytes(), ee.ExitCode(), err
			}
			return out.Bytes(), 0, err
		},
		execute: func(ctx context.Context, p installer.Plan) error {
			return p.Execute(ctx, env.runner, func(installer.Step) {})
		},
		checkToken: func(ctx context.Context, provider, domain, token string) error {
			ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			c := certs.NewRecordsClient()
			c.HTTP = &http.Client{Timeout: 20 * time.Second}
			return c.CheckToken(ctx, provider, domain, token)
		},
	}
}

// applyRow is one row of the page's progress list.
type applyRow struct {
	title string
	plan  installer.Plan
	// run does the row instead of plan.
	run func(ctx context.Context) error
	// switching: this row replaces the installer's stack with the full one.
	switching bool
	// keep is what the owner writes down once the row is done.
	keep []install.KeepItem
}

// exitFirstAdminExists is `service user create --first-admin`'s exit code
// when Linx already has a system admin.
const exitFirstAdminExists = 3

// config is setup.yaml with the secure page's choices.
func (w *webApply) config(in install.ApplyInput) (installer.Config, error) {
	b, err := w.env.savedConfig()
	if err != nil {
		return installer.Config{}, fmt.Errorf("reading %s: %w", installer.ConfigPath, err)
	}
	c, err := installer.ParseConfig(bytes.NewReader(b))
	if err != nil {
		return c, err
	}
	pick, _ := installer.SuggestProfile(w.env.detect())
	c.ResourceProfile = installer.ProfileAuto
	if p := in.Extras.Profile; p != "" && p != pick {
		c.ResourceProfile = p
	}
	c.ContainerUI = installer.ContainerUINone
	if in.Extras.Portainer && w.lan.OK() {
		c.ContainerUI = installer.ContainerUIPortainer
	}
	c.Certificates.NoDNSToken = in.SkipToken
	c.Install.FinishedAt = ""
	return c, c.Validate()
}

// token is the saved DNS token ("" when skipped).
func (w *webApply) token(in install.ApplyInput) (string, error) {
	if in.SkipToken {
		return "", nil
	}
	b, err := w.env.readFile(installer.DNSTokenPath)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return "", errors.New("the DNS token isn't saved on the server: add it again")
	}
	return strings.TrimSpace(string(b)), nil
}

func (w *webApply) rows(ctx context.Context, in install.ApplyInput) ([]applyRow, error) {
	cfg, err := w.config(in)
	if err != nil {
		return nil, err
	}
	token, err := w.token(in)
	if err != nil {
		return nil, err
	}
	a := in.Answers
	save := func(c installer.Config) installer.Plan {
		return installer.Plan{{Title: "Save your answers to " + installer.ConfigPath, File: &installer.File{
			Path: installer.ConfigPath, Data: c.Marshal(), Mode: 0o600, DirMode: 0o755,
		}}}
	}
	rows := []applyRow{{title: "Save your settings", plan: save(cfg)}}

	phones, err := installer.PhonesPlan(ctx, w.env.runner, w.lan, installer.FrontDoorFor(cfg, w.lan), w.env.readFile)
	if err != nil {
		return nil, fmt.Errorf("can't plan the phone connections: %w", err)
	}
	title := "Firewall"
	if w.lan.OK() {
		title = "Firewall and phone ports on " + w.lan.Address.String()
	}
	rows = append(rows, applyRow{title: title, plan: append(phones, installer.WireGuardPlan()...)})

	pki := installer.PKIPlan(installer.CAExists(ctx, w.env.runner))
	ca := applyRow{title: "Internal certificate authority", plan: pki.Plan}
	if pki.Passphrase != "" {
		ca.keep = []install.KeepItem{{
			Title: "Certificate authority backup passphrase",
			Value: pki.Passphrase,
			Note: "Linx's internal certificate authority keeps its master key only as a backup locked with this passphrase, in " +
				installer.CABackupDir + " on the server. Write the passphrase down, copy that folder somewhere safe (a USB stick or your " +
				"password manager), then delete it from the server. You'd only need them to renew or rebuild it, in about 10 years.",
		}}
	}
	rows = append(rows, ca)

	if cfg.ContainerUI == installer.ContainerUIPortainer {
		p := installer.PortainerPlan(w.lan.BindAddress())
		rows = append(rows, applyRow{title: "Portainer (home network only)", plan: p.Plan, keep: []install.KeepItem{{
			Title: "Portainer password",
			Value: p.Password,
			Note:  "Open " + p.URL + " from your home network and sign in as admin. Portainer can control everything on this server: never forward its port on your router.",
		}}})
	}

	files, dns := installer.FrontDoorPlan(cfg, w.lan)
	before, up := installer.StackPlan(cfg, token, w.imageTag, w.lan).Split()
	getCert := before[len(before)-1]
	certTitle := "Certificate for " + a.Domain + " and *." + a.Domain
	if cfg.Certificates.NoDNSToken {
		certTitle = "Certificate (renews through port 443)"
	}
	rows = append(rows,
		applyRow{title: "Download Linx's services", plan: append(files, before[:len(before)-1]...)},
		applyRow{title: certTitle, plan: installer.Plan{getCert}},
		applyRow{title: "Start Linx (this setup page closes)", switching: true,
			plan: installer.Plan{installer.StartServicesStep("Start the control plane", "control-plane")}},
		applyRow{title: "Your account (" + a.Email + ")", run: func(ctx context.Context) error {
			return w.firstAdmin(ctx, a, in.SetupToken)
		}},
		applyRow{title: "Phone system and call audio", plan: installer.Plan{up}},
	)
	if len(dns) > 0 {
		rows = append(rows, applyRow{title: strings.TrimSuffix(strings.TrimPrefix(dns[0].Title, "Point "), " (DNS)"), plan: dns})
	}
	var helpers installer.Plan
	helpers = append(helpers, installer.CLIPlan(w.env.executable, w.env.resolve)...)
	helpers = append(helpers, installer.FirewallSyncPlan(w.env.executable, w.env.resolve, w.env.stat)...)
	helpers = append(helpers, installer.ResticPlan(ctx, w.env.runner)...)
	helpers = append(helpers, installer.BackupAgentPlan(w.env.executable, w.env.resolve, w.env.stat)...)
	helpers = append(helpers, installer.OpsAgentPlan(w.env.executable, w.env.resolve, w.env.stat)...)
	helpers = append(helpers, installer.PruneOldImagesStep())
	rows = append(rows, applyRow{title: "Helpers: backups, status, firewall sync", plan: helpers})

	done := cfg
	done.Install.FinishedAt = w.now().UTC().Format(time.RFC3339)
	rows = append(rows, applyRow{title: "Finish", plan: save(done)})
	return rows, nil
}

// firstAdmin makes the first system admin with the page's set-password
// token (docs/INSTALL.md §14 item 2). One that already exists is left
// alone: the page then goes to the ordinary sign-in.
func (w *webApply) firstAdmin(ctx context.Context, a install.Answers, token string) error {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = a.Email
	}
	out, code, err := w.exec(ctx, []byte(token+"\n"), "docker", "exec", "-i", controlPlaneContainer, controlPlaneBinary,
		"user", "create", "--first-admin", "--setup-token-stdin", "--role", "system_admin", "--email", a.Email, "--name", name)
	if err != nil && code != exitFirstAdminExists {
		return errors.New(lastLines(string(out), err))
	}
	// The setup wizard's Place step starts from "at home" (a rented server
	// gets no pre-pick: home or business is the owner's call). Only a hint:
	// if it doesn't take, the wizard just asks.
	if a.Where == install.WhereHome {
		_, _, _ = w.exec(ctx, nil, "docker", "exec", controlPlaneContainer, controlPlaneBinary, "suggest-site", "home")
	}
	return nil
}

func (w *webApply) Steps(ctx context.Context, in install.ApplyInput) ([]string, error) {
	rows, err := w.rows(ctx, in)
	if err != nil {
		return nil, err
	}
	titles := make([]string, len(rows))
	for i, r := range rows {
		titles[i] = r.title
	}
	return titles, nil
}

// Run does the rows in order. Before the switch the first failure stops
// it (the page offers Install again: every step is safe to repeat). After
// it the page is gone, so every row still runs, and the first admin's row
// decides whether the install counts as finished.
func (w *webApply) Run(ctx context.Context, in install.ApplyInput, report func(int, string, string), keep func(install.KeepItem), switching func()) error {
	rows, err := w.rows(ctx, in)
	if err != nil {
		return err
	}
	switched := false
	var failed []string
	for i, r := range rows {
		if r.switching {
			switching()
			switched = true
		}
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
			if !switched || r.run != nil {
				// Before the switch, or no first admin: stop here.
				return fmt.Errorf("%s: %s", r.title, detail)
			}
			failed = append(failed, r.title)
			continue
		}
		report(i, install.StageOK, "")
		for _, k := range r.keep {
			keep(k)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("Linx is running, but these didn't finish: %s. Run sudo linx setup to try them again", strings.Join(failed, "; "))
	}
	return nil
}

// stepProblem is a failed step in plain words, with the command's last
// lines.
func stepProblem(err error) string {
	var se *installer.StepError
	if errors.As(err, &se) && se.Output != "" {
		return se.Step.Title + ": " + lastLines(se.Output, err)
	}
	return err.Error()
}

func (w *webApply) SaveToken(ctx context.Context, token string) (string, error) {
	token = strings.TrimSpace(token)
	if refusal, err := w.tokenRefusal(ctx, token); refusal != "" || err != nil {
		return refusal, err
	}
	return "", w.execute(ctx, installer.SaveDNSTokenPlan(token))
}

// tokenRefusal checks a DNS token, changing nothing: its form, then
// whether the DNS company lets it see the domain. A refusal is plain words.
func (w *webApply) tokenRefusal(ctx context.Context, token string) (string, error) {
	b, err := w.env.savedConfig()
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", installer.ConfigPath, err)
	}
	c, err := installer.ParseConfig(bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	return w.tokenRefusalFor(ctx, c.Domain.DNSProvider, c.Domain.Name, token)
}

// tokenRefusalFor is tokenRefusal for a domain at a DNS company.
func (w *webApply) tokenRefusalFor(ctx context.Context, provider, domain, token string) (string, error) {
	if err := installer.ValidateDNSToken(token); err != nil {
		return upper(err.Error()) + ".", nil
	}
	if err := w.checkToken(ctx, provider, domain, token); err != nil {
		if errors.Is(err, certs.ErrTokenRefused) {
			return upper(strings.TrimPrefix(err.Error(), certs.ErrTokenRefused.Error()+": ")) + ".", nil
		}
		return upper(err.Error()) + ".", nil
	}
	return "", nil
}

func (w *webApply) Profiles(context.Context) ([]install.ProfileOption, string, string) {
	pick, reason := installer.SuggestProfile(w.env.detect())
	opts := make([]install.ProfileOption, 0, len(installer.Profiles))
	for _, p := range installer.Profiles {
		opts = append(opts, install.ProfileOption{Name: p, Description: installer.ProfileDescription[p]})
	}
	return opts, pick, reason
}

var _ install.Applier = (*webApply)(nil)
