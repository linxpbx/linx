package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/dnscheck"
	"linxpbx.com/linx/internal/install"
	"linxpbx.com/linx/internal/installer"
)

// webCert is the web install's certificate page on the host
// (install.Certifier, docs/INSTALL.md §4): the saved answers decide
// everything, and every step is a docker compose command on the install's
// own stack.
type webCert struct {
	env      setupEnv
	lan      installer.LAN
	imageTag string
	address  netip.Addr
	dns      dnscheck.Resolver
}

func (w *webCert) config() (installer.Config, error) {
	b, err := w.env.savedConfig()
	if err != nil {
		return installer.Config{}, fmt.Errorf("reading %s: %w", installer.ConfigPath, err)
	}
	return installer.ParseConfig(bytes.NewReader(b))
}

func (w *webCert) Plan(_ context.Context, _ install.Answers, f install.Facts) (install.CertView, error) {
	c, err := w.config()
	if err != nil {
		return install.CertView{}, err
	}
	return installer.CertView(c, w.lan, f), nil
}

func (w *webCert) Prepare(ctx context.Context, _ install.CertView) error {
	c, err := w.config()
	if err != nil {
		return err
	}
	return installer.InstallCertPlan(c, w.lan, w.imageTag, w.address).Execute(ctx, w.env.runner, func(installer.Step) {})
}

func (w *webCert) Lookup(ctx context.Context, name string) ([]string, error) {
	return w.dns.LookupA(ctx, name)
}

func (w *webCert) NameServers(ctx context.Context, name string) (string, []string, error) {
	return w.dns.NameServers(ctx, name)
}

func (w *webCert) Obtain(ctx context.Context, staging bool) error {
	mode := "real"
	if staging {
		mode = "staging"
	}
	out, err := w.env.runner.Run(ctx, nil, "docker", installer.CertdRun(false, "-bootstrap", mode)...)
	found, res := certs.ParseBootstrapResult(out)
	switch {
	case found:
		return res
	case err != nil:
		return &certs.BootstrapError{Kind: certs.ProblemOther, Detail: lastLines(string(out), err)}
	}
	return nil
}

func (w *webCert) SaveToken(ctx context.Context, cv install.CertView, key install.DNSKey) (string, error) {
	c, err := w.config()
	if err != nil {
		return "", err
	}
	provider, secret, refusal := key.Secret(c.Domain.DNSProvider)
	if refusal != "" {
		return refusal, nil
	}
	if refusal := keyFormRefusal(c.Domain.Name, provider, secret); refusal != "" {
		return refusal, nil
	}
	return "", installer.SaveDNSKeyPlan(c, provider, secret).Execute(ctx, w.env.runner, func(installer.Step) {})
}

// keyFormRefusal is what's wrong with a key's form for domain at provider,
// in plain words ("" if nothing): the company itself is asked later.
func keyFormRefusal(domain, provider, secret string) string {
	if err := installer.ValidateDomain(domain, provider); err != nil {
		return upper(err.Error()) + "."
	}
	if err := installer.ValidateDNSKey(provider, secret); err != nil {
		return strings.TrimSuffix(upper(err.Error()), ".") + "."
	}
	return ""
}

func (w *webCert) Records(ctx context.Context) error {
	c, err := w.config()
	if err != nil {
		return err
	}
	out, err := w.env.runner.Run(ctx, nil, "docker", installer.CertdRun(true, installer.RecordsArgs(c, w.lan)...)...)
	if err != nil {
		return errors.New(lastLines(string(out), err))
	}
	return nil
}

func (w *webCert) ObtainWithToken(ctx context.Context) error {
	out, err := w.env.runner.Run(ctx, nil, "docker", installer.CertdRun(true, "-once")...)
	if err != nil {
		return errors.New(lastLines(string(out), err))
	}
	return nil
}

// lastLines is the end of a command's output, for the page's "Details".
func lastLines(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	s := strings.TrimSpace(strings.Join(lines, "\n"))
	if s == "" {
		return err.Error()
	}
	return s
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
