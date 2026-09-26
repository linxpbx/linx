package trunkconf

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/trunk"
)

func selfSigned(t *testing.T, name string) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func base(name string) trunk.Trunk {
	return trunk.Trunk{ID: uuid.Must(uuid.NewV7()), Name: name, Kind: trunk.KindLANPeer, Host: "ucm.lan", Port: 5061,
		Transport: trunk.TransportTLS, MediaEncryption: trunk.MediaSRTP, CertTrust: trunk.CertPublic,
		DialFormat: trunk.DialLocal, Codecs: []string{"alaw", "ulaw"}, MaxCalls: 4, Enabled: true}
}

func TestRender(t *testing.T) {
	pin := selfSigned(t, "ucm.lan")
	reg := base("Provider")
	reg.Kind, reg.Host, reg.Username = trunk.KindRegistration, "sip.provider.example", "linx01"
	ucm := base("UCM")
	ucm.CertTrust, ucm.PinnedCertificate = trunk.CertPinned, pin+pin // one pin, twice
	ip := base("IP provider")
	ip.Kind, ip.Host = trunk.KindIPAuthenticated, "203.0.113.7"
	plain := base("Plain")
	plain.Host, plain.Port, plain.Transport, plain.MediaEncryption = "old.pbx.lan", 5060, trunk.TransportUDP, trunk.MediaNone
	off := base("Off")
	off.Enabled = false

	files, problems := Render(Input{
		Trunks:    []trunk.Trunk{reg, ucm, ip, plain, off},
		Passwords: map[uuid.UUID]string{reg.ID: "p;ss w0rd"},
		Addresses: map[string][]netip.Addr{
			"sip.provider.example": {netip.MustParseAddr("198.51.100.1"), netip.MustParseAddr("198.51.100.2")},
			"ucm.lan":              {netip.MustParseAddr("192.168.1.30")},
			"old.pbx.lan":          {netip.MustParseAddr("192.168.1.31")},
		},
	})
	if len(problems) > 0 {
		t.Errorf("problems: %v", problems)
	}
	conf := string(files.PJSIP)
	for _, want := range []string{
		// Signing in: the password's ";" escaped, the line identifying the
		// provider's calls.
		"[" + reg.Endpoint() + "]\ntype=auth\nauth_type=userpass\nusername=linx01\npassword=p\\;ss w0rd\n",
		"[" + reg.Endpoint() + "]\ntype=registration\ntransport=transport-tls\noutbound_auth=" + reg.Endpoint() + "\n",
		"server_uri=sip:sip.provider.example:5061;transport=tls\nclient_uri=sip:linx01@sip.provider.example\n",
		"line=yes\nendpoint=" + reg.Endpoint() + "\n",
		// Every trunk: its own context, never a name from a From header.
		"context=linx-from-trunk\n", "identify_by=ip\n", "media_encryption=sdes\n",
		// TLS by name (the certificate is checked against it).
		"[" + ucm.Endpoint() + "]\ntype=aor\ncontact=sip:ucm.lan:5061;transport=tls\n",
		// Calls in from its addresses.
		"[" + ip.Endpoint() + "]\ntype=identify\nendpoint=" + ip.Endpoint() + "\nmatch=203.0.113.7\n",
		"[" + ucm.Endpoint() + "]\ntype=identify\nendpoint=" + ucm.Endpoint() + "\nmatch=192.168.1.30\n",
		// ADR-023: its own transport, by address, no SRTP.
		"[transport-trunk-udp](linx-trunk-transport)\nprotocol=udp\nbind=0.0.0.0:5062\n",
		"contact=sip:192.168.1.31:5060;transport=udp\n", "transport=transport-trunk-udp\n", "media_encryption=no\n",
		"; Unencrypted (ADR-023), confirmed by the admin.\n",
		// One ACL: every trunk's addresses added to the phones'.
		"[phone-networks](+)\npermit=192.168.1.30/32\npermit=192.168.1.31/32\npermit=198.51.100.1/32\npermit=198.51.100.2/32\npermit=203.0.113.7/32\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("pjsip_trunks.conf missing %q:\n%s", want, conf)
		}
	}
	for _, not := range []string{off.Endpoint(), "transport-trunk-tcp", "[" + reg.Endpoint() + "]\ntype=identify",
		"[" + ucm.Endpoint() + "]\ntype=auth"} {
		if strings.Contains(conf, not) {
			t.Errorf("pjsip_trunks.conf has %q:\n%s", not, conf)
		}
	}
	if got := string(files.PinnedCA); got != pin {
		t.Errorf("pinned CA = %q, want the one certificate once", got)
	}
}

func TestRenderRefuses(t *testing.T) {
	ok := base("OK")
	for name, mutate := range map[string]func(*trunk.Trunk, *string){
		"config injection in the host": func(tr *trunk.Trunk, _ *string) { tr.Host = "evil.example\n[x]" },
		"a comma in the login":         func(tr *trunk.Trunk, _ *string) { tr.Username = "a,b" },
		"a line break in the password": func(_ *trunk.Trunk, pw *string) { *pw = "a\ntype=endpoint" },
		"no login for a registration":  func(tr *trunk.Trunk, _ *string) { tr.Kind = trunk.KindRegistration },
		"an unknown codec":             func(tr *trunk.Trunk, _ *string) { tr.Codecs = []string{"alaw", "g729"} },
		"a broken pin": func(tr *trunk.Trunk, _ *string) {
			tr.CertTrust, tr.PinnedCertificate = trunk.CertPinned, "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := base("Bad")
			pw := ""
			mutate(&bad, &pw)
			files, problems := Render(Input{Trunks: []trunk.Trunk{ok, bad}, Passwords: map[uuid.UUID]string{bad.ID: pw},
				Addresses: map[string][]netip.Addr{"ucm.lan": {netip.MustParseAddr("192.168.1.30")}}})
			if len(problems) != 1 || !strings.Contains(problems[0], bad.ID.String()) {
				t.Errorf("problems = %v", problems)
			}
			if strings.Contains(string(files.PJSIP), bad.Endpoint()) || !strings.Contains(string(files.PJSIP), ok.Endpoint()) {
				t.Errorf("the bad trunk was rendered, or the good one wasn't:\n%s", files.PJSIP)
			}
		})
	}
}

// fakeStore holds trunks for the Renderer.
type fakeStore struct {
	tenant uuid.UUID
	trunks []trunk.Trunk
}

func (f *fakeStore) DefaultTenant(context.Context) (uuid.UUID, error) { return f.tenant, nil }
func (f *fakeStore) ListTrunks(_ context.Context, _ uuid.UUID, before *uuid.UUID, limit int) ([]trunk.Trunk, error) {
	var out []trunk.Trunk
	for _, t := range f.trunks {
		if before == nil || t.ID.String() < before.String() {
			out = append(out, t)
		}
	}
	return out[:min(limit, len(out))], nil
}

func TestRenderer(t *testing.T) {
	var key [dbsecret.KeySize]byte
	sealer := dbsecret.NewSealer(key)
	reg := base("Provider")
	reg.Kind, reg.Host, reg.Username = trunk.KindRegistration, "sip.provider.example", "linx01"
	// Sealed as trunk.Service seals it: under the trunk's own row id.
	reg.PasswordEnc, _ = sealer.Seal("trunk:"+reg.ID.String(), []byte("s3cret"))
	broken := base("Broken")
	broken.Kind, broken.Username, broken.PasswordEnc = trunk.KindRegistration, "x", []byte("not sealed")
	st := &fakeStore{trunks: []trunk.Trunk{reg, broken}}

	dir := t.TempDir()
	answers := map[string][]netip.Addr{"sip.provider.example": {netip.MustParseAddr("198.51.100.1")}}
	r := &Renderer{Store: st, Sealer: sealer, Dir: dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			if a, ok := answers[host]; ok {
				return a, nil
			}
			return nil, errors.New("NXDOMAIN")
		}}
	ctx := context.Background()
	changed, err := r.RenderOnce(ctx)
	if err != nil || !changed {
		t.Fatalf("RenderOnce = %v, %v", changed, err)
	}
	conf, _ := os.ReadFile(filepath.Join(dir, "pjsip_trunks.conf"))
	if !strings.Contains(string(conf), "password=s3cret\n") || !strings.Contains(string(conf), "permit=198.51.100.1/32") {
		t.Errorf("rendered:\n%s", conf)
	}
	// A password that doesn't open leaves that trunk out, not the file.
	if strings.Contains(string(conf), broken.Endpoint()) {
		t.Errorf("rendered a trunk whose password didn't open:\n%s", conf)
	}
	if info, _ := os.Stat(filepath.Join(dir, "pjsip_trunks.conf")); info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want 0640 (Asterisk's group only)", info.Mode().Perm())
	}

	// Nothing changed: nothing written.
	if changed, err := r.RenderOnce(ctx); err != nil || changed {
		t.Errorf("second RenderOnce = %v, %v; want no change", changed, err)
	}
	// The provider's DNS fails: its last address is kept.
	delete(answers, "sip.provider.example")
	if changed, err := r.RenderOnce(ctx); err != nil || changed {
		t.Errorf("RenderOnce with DNS down = %v, %v; want the last address kept", changed, err)
	}
	// Turned off: gone from the file.
	st.trunks[0].Enabled = false
	if changed, err := r.RenderOnce(ctx); err != nil || !changed {
		t.Fatalf("RenderOnce = %v, %v", changed, err)
	}
	if conf, _ := os.ReadFile(filepath.Join(dir, "pjsip_trunks.conf")); strings.Contains(string(conf), reg.Endpoint()) {
		t.Errorf("a disabled trunk is still rendered:\n%s", conf)
	}
}

func TestRunRendersOnChange(t *testing.T) {
	st := &fakeStore{}
	dir := t.TempDir()
	r := &Renderer{Store: st, Dir: dir, Interval: time.Hour, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Lookup: func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("NXDOMAIN") }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	path := filepath.Join(dir, "pjsip_trunks.conf")
	waitFor(t, func() bool { _, err := os.Stat(path); return err == nil })

	ip := base("IP provider")
	ip.Kind, ip.Host = trunk.KindIPAuthenticated, "203.0.113.7"
	st.trunks = []trunk.Trunk{ip}
	r.Changed()
	waitFor(t, func() bool {
		b, _ := os.ReadFile(path)
		return strings.Contains(string(b), ip.Endpoint())
	})
	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
