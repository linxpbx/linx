package doctor

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/installer"
)

var now = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func TestHostnamesMatchCerts(t *testing.T) {
	if !slices.Equal(Hostnames, certs.Hostnames) {
		t.Errorf("doctor.Hostnames = %v, certs.Hostnames = %v", Hostnames, certs.Hostnames)
	}
}

func TestStagingRoots(t *testing.T) {
	for _, b := range [][]byte{stagingRootX1, stagingRootX2} {
		cs, err := parseCerts(b)
		if err != nil || !cs[0].IsCA || !strings.Contains(cs[0].Subject.CommonName, "(STAGING)") {
			t.Errorf("bad staging root: %v %v", cs, err)
		}
	}
}

// ca is a test certificate authority.
type ca struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCert(t *testing.T, tmpl *x509.Certificate, parent *ca) *ca {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.SerialNumber = big.NewInt(time.Now().UnixNano())
	if tmpl.NotBefore.IsZero() {
		tmpl.NotBefore = now.Add(-day)
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &ca{c, key}
}

func caTmpl(org string, notAfter time.Time) *x509.Certificate {
	return &x509.Certificate{
		Subject: pkix.Name{Organization: []string{org}, CommonName: org}, NotAfter: notAfter,
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
}

func pemOf(cs ...*ca) []byte {
	var b []byte
	for _, c := range cs {
		b = append(b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw})...)
	}
	return b
}

func tarOf(t *testing.T, data []byte) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "f", Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(data)
	tw.Close()
	return buf.String()
}

// hostRunner fakes a host where only the listed commands succeed.
type hostRunner map[string]string

func (h hostRunner) Run(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	out, ok := h[strings.Join(append([]string{name}, args...), " ")]
	if !ok {
		return nil, &exec.ExitError{}
	}
	return []byte(out), nil
}

const inspect = "docker inspect --type container --format {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}} "

// fixture is a healthy install; tests break one thing each.
type fixture struct {
	runner hostRunner
	env    Env
	cfg    installer.Config
}

func newFixture(t *testing.T, staging bool, leafNotAfter time.Time, dnsNames ...string) *fixture {
	pubRoot := newCert(t, caTmpl("Test Public Root", now.Add(3650*day)), nil)
	org := "Test Public"
	if staging {
		org = "(STAGING) Test Public"
	}
	pubInterm := newCert(t, caTmpl(org, now.Add(1000*day)), pubRoot)
	leaf := newCert(t, &x509.Certificate{
		DNSNames: dnsNames, NotAfter: leafNotAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, pubInterm)
	pool := x509.NewCertPool()
	pool.AddCert(pubRoot.cert)

	caRoot := newCert(t, caTmpl("Linx Internal Root CA", now.Add(3650*day)), nil)
	caInterm := newCert(t, caTmpl("Linx Internal CA", now.Add(3650*day)), caRoot)

	r := hostRunner{
		"docker version --format {{.Server.Version}}":                 "28.4.0",
		inspect + "linx-certd":                                        "running \n",
		inspect + "linx-step-ca":                                      "running healthy\n",
		"docker cp --follow-link linx-certd:" + fullchainPath + " -":  tarOf(t, pemOf(leaf, pubInterm)),
		"docker cp --follow-link linx-step-ca:" + caRootPath + " -":   tarOf(t, pemOf(caRoot)),
		"docker cp --follow-link linx-step-ca:" + caIntermPath + " -": tarOf(t, pemOf(caInterm)),
	}
	cfg := installer.DefaultConfig()
	cfg.Domain.Name = "lab.example.com"
	cfg.Certificates.Staging = staging
	env := Env{
		Runner: r, Now: func() time.Time { return now }, Roots: pool, StagingRoots: x509.NewCertPool(),
		Stat: func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist },
	}
	if staging {
		env.Roots, env.StagingRoots = x509.NewCertPool(), pool
	}
	return &fixture{r, env, cfg}
}

func (f *fixture) run() []Result {
	return Certificates(context.Background(), f.env, f.cfg)
}

func worst(rs []Result) installer.Level {
	l := installer.OK
	for _, r := range rs {
		l = max(l, r.Level)
	}
	return l
}

// want checks that some result has level l and mentions text.
func want(t *testing.T, rs []Result, l installer.Level, text string) {
	t.Helper()
	for _, r := range rs {
		if r.Level == l && strings.Contains(r.Message, text) {
			if l != installer.OK && r.Fix == "" {
				t.Errorf("%s %q has no fix", l, r.Message)
			}
			return
		}
	}
	t.Errorf("no %s mentioning %q in:\n%s", l, text, dump(rs))
}

func dump(rs []Result) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString(r.Level.String() + " " + r.Message + "\n")
	}
	return b.String()
}

func TestAllGreen(t *testing.T) {
	for _, staging := range []bool{true, false} {
		f := newFixture(t, staging, now.Add(80*day), "*.lab.example.com")
		rs := f.run()
		if worst(rs) != installer.OK {
			t.Errorf("staging=%v: want all ok, got:\n%s", staging, dump(rs))
		}
		want(t, rs, installer.OK, "covers admin., api., meet., provision., sip. and turn.lab.example.com")
		want(t, rs, installer.OK, "80 days left")
		if staging {
			want(t, rs, installer.OK, "Let's Encrypt's test authority")
		} else {
			want(t, rs, installer.OK, "up to Test Public Root")
		}
	}
}

func TestNamedCertificate(t *testing.T) {
	var names []string
	for _, h := range Hostnames {
		names = append(names, h+".lab.example.com")
	}
	if rs := newFixture(t, true, now.Add(80*day), names...).run(); worst(rs) != installer.OK {
		t.Errorf("want all ok, got:\n%s", dump(rs))
	}
}

func TestProblems(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fixture)
		// notAfter overrides the certificate's expiry.
		notAfter time.Time
		level    installer.Level
		text     string
	}{
		{name: "docker down", setup: func(f *fixture) { delete(f.runner, "docker version --format {{.Server.Version}}") },
			level: installer.Fail, text: "Docker isn't running"},
		{name: "certd missing", setup: func(f *fixture) { delete(f.runner, inspect+"linx-certd") },
			level: installer.Fail, text: "certificate service (linx-certd) isn't installed"},
		{name: "certd stopped", setup: func(f *fixture) { f.runner[inspect+"linx-certd"] = "exited \n" },
			level: installer.Fail, text: "isn't running (it's exited)"},
		{name: "no certificate", setup: func(f *fixture) { delete(f.runner, "docker cp --follow-link linx-certd:"+fullchainPath+" -") },
			level: installer.Fail, text: "no certificate yet"},
		{name: "other domain", setup: func(f *fixture) { f.cfg.Domain.Name = "new.example.com" },
			level: installer.Fail, text: "doesn't cover admin.new.example.com"},
		{name: "still staging", setup: func(f *fixture) { f.cfg.Certificates.Staging = false },
			level: installer.Fail, text: "still a test certificate"},
		{name: "untrusted chain", setup: func(f *fixture) { f.env.StagingRoots = x509.NewCertPool() },
			level: installer.Fail, text: "doesn't lead to a known authority"},
		{name: "renewal late", notAfter: now.Add(20 * day), level: installer.Warn, text: "should have renewed"},
		{name: "renewal failing", notAfter: now.Add(5 * day), level: installer.Fail, text: "hasn't renewed"},
		{name: "expired", notAfter: now.Add(-day), level: installer.Fail, text: "expired on"},
		{name: "CA starting", setup: func(f *fixture) { f.runner[inspect+"linx-step-ca"] = "running starting\n" },
			level: installer.Warn, text: "still starting"},
		{name: "CA unhealthy", setup: func(f *fixture) { f.runner[inspect+"linx-step-ca"] = "running unhealthy\n" },
			level: installer.Fail, text: "not answering"},
		{name: "CA missing", setup: func(f *fixture) { delete(f.runner, inspect+"linx-step-ca") },
			level: installer.Fail, text: "(linx-step-ca) isn't installed"},
		{name: "CA files missing", setup: func(f *fixture) { delete(f.runner, "docker cp --follow-link linx-step-ca:"+caIntermPath+" -") },
			level: installer.Fail, text: "Can't read the internal"},
		{name: "CA mismatch", setup: func(f *fixture) {
			other := newCert(t, caTmpl("Other", now.Add(3650*day)), nil)
			f.runner["docker cp --follow-link linx-step-ca:"+caRootPath+" -"] = tarOf(t, pemOf(other))
		}, level: installer.Fail, text: "don't belong together"},
		{name: "CA expiring", setup: func(f *fixture) { f.env.Now = func() time.Time { return now.Add(3500 * day) } },
			level: installer.Warn, text: "intermediate certificate expires on"},
		{name: "root key backup left", setup: func(f *fixture) {
			f.env.Stat = func(p string) (fs.FileInfo, error) {
				if p == installer.CABackupDir {
					return nil, nil
				}
				return nil, errors.New("unexpected " + p)
			}
		}, level: installer.Warn, text: "root key backup is still on this server"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notAfter := now.Add(80 * day)
			if !tt.notAfter.IsZero() {
				notAfter = tt.notAfter
			}
			f := newFixture(t, true, notAfter, "*.lab.example.com")
			if tt.setup != nil {
				tt.setup(f)
			}
			rs := f.run()
			want(t, rs, tt.level, tt.text)
		})
	}
}
