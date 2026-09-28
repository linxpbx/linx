package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"linxpbx.com/linx/internal/install"
	"linxpbx.com/linx/internal/installer"
)

// webSettings is the Server settings page on the host
// (install.SettingsApplier, docs/INSTALL.md §7): an installed server's size,
// Portainer and DNS token, changed with setup's own plans. It shares the
// install's host plumbing (webApply) for the token check and running plans.
type webSettings struct {
	*webApply
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

func (w webSettings) View(ctx context.Context) (install.ServerView, error) {
	c, token, err := w.saved()
	if err != nil {
		return install.ServerView{}, err
	}
	opts, pick, reason := w.Profiles(ctx)
	v := install.ServerView{
		Where: c.Install.Where, FrontDoor: c.FrontDoor.Kind, Domain: c.Domain.Name, Provider: c.Domain.DNSProvider,
		Profile: c.ResourceProfile, Profiles: opts, ProfilePick: pick, ProfileReason: reason,
		Portainer:        c.ContainerUI == installer.ContainerUIPortainer,
		PortainerAllowed: w.lan.OK(),
	}
	if v.Where == "" {
		// Set up in the terminal: where it is, as setup found it.
		v.Where = install.WhereRented
		if w.lan.OK() {
			v.Where = install.WhereHome
		}
	}
	if v.Profile == installer.ProfileAuto || v.Profile == "" {
		v.Profile = pick
	}
	if token != "" {
		v.Token = "saved"
	}
	return v, nil
}

func (w webSettings) CheckToken(ctx context.Context, token string) (string, error) {
	return w.tokenRefusal(ctx, strings.TrimSpace(token))
}

func (w webSettings) rows(ctx context.Context, ch install.ServerChange) ([]applyRow, error) {
	c, token, err := w.saved()
	if err != nil {
		return nil, err
	}
	now, _ := w.View(ctx)
	next := c
	next.ResourceProfile = ch.Profile
	if ch.Profile == now.ProfilePick {
		next.ResourceProfile = installer.ProfileAuto
	}
	next.ContainerUI = installer.ContainerUINone
	if ch.Portainer && w.lan.OK() {
		next.ContainerUI = installer.ContainerUIPortainer
	}
	if newToken := strings.TrimSpace(ch.Token); newToken != "" {
		token = newToken
		next.Certificates.NoDNSToken = false
	}
	if err := next.Validate(); err != nil {
		return nil, err
	}
	rows := []applyRow{{title: "Save your settings", plan: installer.Plan{{Title: "Save your answers to " + installer.ConfigPath,
		File: &installer.File{Path: installer.ConfigPath, Data: next.Marshal(), Mode: 0o600, DirMode: 0o755}}}}}
	if ch.Token != "" {
		rows = append(rows, applyRow{title: "Your DNS company's token", plan: installer.SaveDNSTokenPlan(token)})
	}
	switch {
	case next.ContainerUI == installer.ContainerUIPortainer && !now.Portainer:
		p := installer.PortainerPlan(w.lan.BindAddress())
		rows = append(rows, applyRow{title: "Portainer (home network only)", plan: p.Plan, keep: []install.KeepItem{{
			Title: "Portainer password", Value: p.Password,
			Note: "Open " + p.URL + " from your home network and sign in as admin. Portainer can control everything on this server: never forward its port on your router.",
		}}})
	case next.ContainerUI != installer.ContainerUIPortainer && now.Portainer:
		rows = append(rows, applyRow{title: "Stop Portainer", plan: installer.PortainerStopPlan()})
	}
	// Setup's own stack plan, as a terminal setup would run it: the
	// certificate is got again only if its names change (a new token
	// brings the wildcard), and only services whose settings changed
	// restart.
	rows = append(rows, applyRow{title: "Restart Linx with the new settings", plan: installer.StackPlan(next, token, w.imageTag, w.lan).Plan})
	if ch.Token != "" {
		if _, dns := installer.FrontDoorPlan(next, w.lan); len(dns) > 0 {
			rows = append(rows, applyRow{title: strings.TrimSuffix(strings.TrimPrefix(dns[0].Title, "Point "), " (DNS)"), plan: dns})
		}
	}
	return rows, nil
}

func (w webSettings) Steps(ctx context.Context, ch install.ServerChange) ([]string, error) {
	rows, err := w.rows(ctx, ch)
	if err != nil {
		return nil, err
	}
	titles := make([]string, len(rows))
	for i, r := range rows {
		titles[i] = r.title
	}
	return titles, nil
}

// Run makes the rows in order and stops at the first failure: every step
// is safe to repeat, so the page's Apply just tries again.
func (w webSettings) Run(ctx context.Context, ch install.ServerChange, report func(int, string, string), keep func(install.KeepItem)) error {
	rows, err := w.rows(ctx, ch)
	if err != nil {
		return err
	}
	for i, r := range rows {
		report(i, install.StageRunning, "")
		if err := w.execute(ctx, r.plan); err != nil {
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
