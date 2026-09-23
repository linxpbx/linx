// Package doctor runs the checks behind `linx doctor`. Each result is written
// for a non-technical reader and every problem comes with a fix.
package doctor

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"linxpbx.com/linx/internal/installer"
)

// Result is one check's outcome. Fix is set for warnings and problems.
type Result struct {
	Level   installer.Level
	Message string
	Fix     string
}

// Env is everything the checks touch on the host, so tests can fake it.
type Env struct {
	Runner installer.Runner
	Now    func() time.Time
	// Roots verifies trusted public certificates; nil means the system's.
	Roots *x509.CertPool
	// StagingRoots verifies Let's Encrypt test certificates.
	StagingRoots *x509.CertPool
	Stat         func(string) (fs.FileInfo, error)
}

// Let's Encrypt's staging roots (https://letsencrypt.org/docs/staging-environment/).
// Test certificates chain to one of these, never to a browser-trusted root.
var (
	//go:embed le-staging-root-x1.pem
	stagingRootX1 []byte
	//go:embed le-staging-root-x2.pem
	stagingRootX2 []byte
)

// LEStagingRoots returns Let's Encrypt's staging roots.
func LEStagingRoots() *x509.CertPool {
	p := x509.NewCertPool()
	p.AppendCertsFromPEM(stagingRootX1)
	p.AppendCertsFromPEM(stagingRootX2)
	return p
}

// Hostnames must match internal/certs.Hostnames (a test checks). It's copied
// so the CLI doesn't link the ACME client.
var Hostnames = []string{"admin", "api", "meet", "provision", "sip", "turn"}

// Container names and paths from deploy/compose/compose.yaml.
const (
	certdContainer  = "linx-certd"
	stepCAContainer = "linx-step-ca"
	fullchainPath   = "/var/lib/linx/certs/current/fullchain.pem"
	caRootPath      = "/home/step/certs/root_ca.crt"
	caIntermPath    = "/home/step/certs/intermediate_ca.crt"
)

// Expiry thresholds. Public certificates renew 30 days before expiry, so
// under 21 days means renewal has been failing for over a week (alerts use
// the same 21 and 7 days). The internal CA is renewed by hand, so it warns
// long before.
const (
	day            = 24 * time.Hour
	publicWarn     = 21 * day
	publicFail     = 7 * day
	internalCAWarn = 180 * day
	internalCAFail = 30 * day
)

const (
	composeCmd = "sudo docker compose --file /etc/linx/compose.yaml"
	certdLogs  = "Look at the certificate service's log for the reason: sudo docker logs --tail 50 " + certdContainer +
		". It retries by itself; to retry now: sudo docker restart " + certdContainer
	rerunSetup = "Run: sudo linx setup --config " + installer.ConfigPath
)

// Certificates checks the public certificate and the internal certificate
// authority.
func Certificates(ctx context.Context, env Env, cfg installer.Config) []Result {
	var rs results
	if _, err := env.Runner.Run(ctx, nil, "docker", "version", "--format", "{{.Server.Version}}"); err != nil {
		rs.fail("Docker isn't running, so Linx can't run either.",
			"Start it: sudo systemctl start docker. If it isn't installed, run: sudo linx setup")
		return rs
	}
	rs = append(rs, publicCertificate(ctx, env, cfg)...)
	rs = append(rs, internalCA(ctx, env)...)
	return rs
}

func publicCertificate(ctx context.Context, env Env, cfg installer.Config) []Result {
	var rs results
	status, _, err := containerState(ctx, env.Runner, certdContainer)
	switch {
	case err != nil:
		rs.fail("The certificate service (linx-certd) isn't installed.", rerunSetup)
		return rs
	case status != "running":
		rs.fail(fmt.Sprintf("The certificate service (linx-certd) isn't running (it's %s), so the certificate won't renew.", status),
			"Start it: "+composeCmd+" up --detach")
	default:
		rs.ok("The certificate service is running.")
	}

	pemData, err := copyFromContainer(ctx, env.Runner, certdContainer, fullchainPath)
	if err != nil {
		rs.fail("There's no certificate yet.", certdLogs+". Or get one now: "+rerunSetup)
		return rs
	}
	chain, err := parseCerts(pemData)
	if err != nil {
		rs.fail("The certificate file is damaged: "+err.Error(), "Get a new one: "+rerunSetup)
		return rs
	}
	leaf := chain[0]
	staging := isStaging(leaf)
	kind := "Trusted certificate"
	if staging {
		kind = "Test certificate"
	}

	var missing []string
	for _, h := range Hostnames {
		if leaf.VerifyHostname(h+"."+cfg.Domain.Name) != nil {
			missing = append(missing, h+"."+cfg.Domain.Name)
		}
	}
	if len(missing) > 0 {
		rs.fail(fmt.Sprintf("The certificate doesn't cover %s. It may be from before the domain was changed.", strings.Join(missing, ", ")),
			"Get one for the current domain: "+rerunSetup)
	} else {
		rs.ok(fmt.Sprintf("%s covers %s.", kind, coverage(cfg.Domain.Name)))
	}

	if staging && !cfg.Certificates.Staging {
		rs.fail("It's still a test certificate, but setup asked for a trusted one. Browsers will warn.", certdLogs)
	}

	now := env.Now()
	left := leaf.NotAfter.Sub(now)
	until := fmt.Sprintf("%s (%s)", leaf.NotAfter.Local().Format("2 Jan 2006"), days(left))
	switch {
	case left <= 0:
		rs.fail("The certificate expired on "+leaf.NotAfter.Local().Format("2 Jan 2006")+". Apps and browsers can't connect securely.", certdLogs)
		return rs // chain checks are meaningless on an expired certificate
	case left <= publicFail:
		rs.fail("The certificate expires on "+until+" and hasn't renewed.", certdLogs)
	case left <= publicWarn:
		rs.warn("The certificate expires on "+until+". It should have renewed by now.", certdLogs)
	default:
		rs.ok("Valid until " + until + ". It renews automatically 30 days before.")
	}

	opts := x509.VerifyOptions{
		Roots: env.Roots, Intermediates: x509.NewCertPool(), CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if staging {
		opts.Roots = env.StagingRoots
	}
	for _, c := range chain[1:] {
		opts.Intermediates.AddCert(c)
	}
	chains, err := leaf.Verify(opts)
	if err != nil {
		fix := "Update this server's list of trusted authorities: sudo apt-get update && sudo apt-get install --only-upgrade ca-certificates"
		if staging {
			fix = "Let's Encrypt may have changed its test authority. Update linx to the latest version and run doctor again."
		}
		rs.fail("The certificate's chain doesn't lead to a known authority: "+err.Error(), fix)
	} else if staging {
		rs.ok("Full chain checked up to Let's Encrypt's test authority. Browsers warn about test certificates; that's expected while testing.")
	} else {
		rs.ok("Full chain checked up to " + orgName(chains[0][len(chains[0])-1]) + ", which browsers and apps trust.")
	}
	return rs
}

func internalCA(ctx context.Context, env Env) []Result {
	var rs results
	status, health, err := containerState(ctx, env.Runner, stepCAContainer)
	switch {
	case err != nil:
		rs.fail("The internal certificate authority (linx-step-ca) isn't installed.", rerunSetup)
		return rs
	case status != "running":
		rs.fail(fmt.Sprintf("The internal certificate authority (linx-step-ca) isn't running (it's %s).", status),
			"Start it: "+composeCmd+" up --detach")
	case health == "starting":
		rs.warn("The internal certificate authority is still starting.", "Run linx doctor again in a minute.")
	case health != "healthy":
		rs.fail("The internal certificate authority is running but not answering.",
			"Look at its log for the reason: sudo docker logs --tail 50 "+stepCAContainer)
	default:
		rs.ok("The internal certificate authority is running and answering.")
	}

	var certs [2]*x509.Certificate
	for i, p := range []string{caRootPath, caIntermPath} {
		b, err := copyFromContainer(ctx, env.Runner, stepCAContainer, p)
		if err == nil {
			var cs []*x509.Certificate
			if cs, err = parseCerts(b); err == nil {
				certs[i] = cs[0]
			}
		}
		if err != nil {
			rs.fail("Can't read the internal certificate authority's certificates.",
				"See \"Rebuilding the intermediate\" in docs/ops/INTERNAL_CA.md.")
			return rs
		}
	}
	root, interm := certs[0], certs[1]
	roots := x509.NewCertPool()
	roots.AddCert(root)
	now := env.Now()
	_, err = interm.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	if err != nil && now.Before(interm.NotAfter) && now.Before(root.NotAfter) {
		rs.fail("The internal certificate authority's certificates don't belong together: "+err.Error(),
			"See \"Rebuilding the intermediate\" in docs/ops/INTERNAL_CA.md.")
	}

	for _, c := range []struct {
		name string
		cert *x509.Certificate
		fix  string
	}{
		{"root certificate", root, "Renew the internal certificate authority with your root key backup: see docs/ops/INTERNAL_CA.md."},
		{"intermediate certificate", interm, "Follow \"Rebuilding the intermediate\" in docs/ops/INTERNAL_CA.md with your root key backup."},
	} {
		left := c.cert.NotAfter.Sub(now)
		until := fmt.Sprintf("%s (%s)", c.cert.NotAfter.Local().Format("2 Jan 2006"), days(left))
		switch {
		case left <= 0:
			rs.fail("The internal certificate authority's "+c.name+" has expired. Linx's parts and apps can't trust each other.", c.fix)
		case left <= internalCAFail:
			rs.fail("The internal certificate authority's "+c.name+" expires on "+until+".", c.fix)
		case left <= internalCAWarn:
			rs.warn("The internal certificate authority's "+c.name+" expires on "+until+".", c.fix)
		default:
			rs.ok("Internal " + c.name + " valid until " + until + ".")
		}
	}

	if _, err := env.Stat(installer.CABackupDir); err == nil {
		rs.warn("The root key backup is still on this server ("+installer.CABackupDir+"). Anyone who breaks into the server gets a copy.",
			"Copy it somewhere safe, then delete it: see \"After setup: take the root key off the server\" in docs/ops/INTERNAL_CA.md.")
	}
	return rs
}

type results []Result

func (rs *results) ok(msg string)        { *rs = append(*rs, Result{installer.OK, msg, ""}) }
func (rs *results) warn(msg, fix string) { *rs = append(*rs, Result{installer.Warn, msg, fix}) }
func (rs *results) fail(msg, fix string) { *rs = append(*rs, Result{installer.Fail, msg, fix}) }

// containerState returns a container's status (e.g. "running", "exited") and
// its health ("" when it has no health check).
func containerState(ctx context.Context, r installer.Runner, name string) (status, health string, err error) {
	out, err := r.Run(ctx, nil, "docker", "inspect", "--type", "container", "--format",
		"{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}", name)
	if err != nil {
		return "", "", err
	}
	status, health, _ = strings.Cut(strings.TrimSpace(string(out)), " ")
	return status, health, nil
}

// copyFromContainer reads one file from a container, running or stopped.
// Symlinks (like certd's "current") are followed.
func copyFromContainer(ctx context.Context, r installer.Runner, container, path string) ([]byte, error) {
	out, err := r.Run(ctx, nil, "docker", "cp", "--follow-link", container+":"+path, "-")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	tr := tar.NewReader(bytes.NewReader(out))
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 1<<20))
		}
	}
}

func parseCerts(b []byte) ([]*x509.Certificate, error) {
	var cs []*x509.Certificate
	for {
		var block *pem.Block
		block, b = pem.Decode(b)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	if len(cs) == 0 {
		return nil, errors.New("no certificate found")
	}
	return cs, nil
}

// isStaging reports whether Let's Encrypt's test service issued c.
func isStaging(c *x509.Certificate) bool {
	for _, o := range c.Issuer.Organization {
		if strings.Contains(o, "(STAGING)") {
			return true
		}
	}
	return strings.Contains(c.Issuer.CommonName, "(STAGING)")
}

// orgName names a root authority, e.g. "Internet Security Research Group".
func orgName(c *x509.Certificate) string {
	if len(c.Subject.Organization) > 0 {
		return c.Subject.Organization[0]
	}
	return c.Subject.CommonName
}

func coverage(domain string) string {
	n := len(Hostnames)
	return strings.Join(Hostnames[:n-1], "., ") + ". and " + Hostnames[n-1] + "." + domain
}

func days(d time.Duration) string {
	n := int(d / day)
	if n == 1 {
		return "1 day left"
	}
	return fmt.Sprintf("%d days left", n)
}
