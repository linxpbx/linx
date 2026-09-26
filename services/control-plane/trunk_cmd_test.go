package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/trunk"
	"linxpbx.com/linx/internal/trunkprobe"
)

// fakeTrunkStore keeps trunks and DIDs in memory; methods the command
// doesn't use panic (the embedded nil interface).
type fakeTrunkStore struct {
	trunk.Store
	mu       sync.Mutex
	trunks   []trunk.Trunk
	dids     []trunk.DID
	profiles []trunk.WireGuardProfile
}

func (f *fakeTrunkStore) CreateWireGuardProfile(_ context.Context, w trunk.WireGuardProfile, _ auth.AuditEntry) error {
	f.profiles = append(f.profiles, w)
	return nil
}

func (f *fakeTrunkStore) WireGuardProfile(_ context.Context, _, id uuid.UUID) (trunk.WireGuardProfile, error) {
	for _, w := range f.profiles {
		if w.ID == id {
			return w, nil
		}
	}
	return trunk.WireGuardProfile{}, trunk.ErrNotFound
}

func (f *fakeTrunkStore) ListWireGuardProfiles(_ context.Context, _ uuid.UUID, before *uuid.UUID, _ int) ([]trunk.WireGuardProfile, error) {
	if before != nil {
		return nil, nil
	}
	return append([]trunk.WireGuardProfile(nil), f.profiles...), nil
}

func (f *fakeTrunkStore) DeleteWireGuardProfile(_ context.Context, _, id uuid.UUID, _ auth.AuditEntry) error {
	for _, t := range f.trunks {
		if t.WireGuardProfileID != nil && *t.WireGuardProfileID == id {
			return trunk.ErrInUse
		}
	}
	for i, w := range f.profiles {
		if w.ID == id {
			f.profiles = append(f.profiles[:i], f.profiles[i+1:]...)
			return nil
		}
	}
	return trunk.ErrNotFound
}

func (f *fakeTrunkStore) CreateTrunk(_ context.Context, t trunk.Trunk, _ auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trunks = append(f.trunks, t)
	return nil
}

func (f *fakeTrunkStore) Trunk(_ context.Context, _, id uuid.UUID) (trunk.Trunk, error) {
	for _, t := range f.trunks {
		if t.ID == id {
			return t, nil
		}
	}
	return trunk.Trunk{}, trunk.ErrNotFound
}

func (f *fakeTrunkStore) ListTrunks(context.Context, uuid.UUID, *uuid.UUID, int) ([]trunk.Trunk, error) {
	return append([]trunk.Trunk(nil), f.trunks...), nil
}

func (f *fakeTrunkStore) DeleteTrunk(_ context.Context, _, id uuid.UUID, _ time.Time, _ auth.AuditEntry) error {
	for i, t := range f.trunks {
		if t.ID == id {
			f.trunks = append(f.trunks[:i], f.trunks[i+1:]...)
			return nil
		}
	}
	return trunk.ErrNotFound
}

func (f *fakeTrunkStore) SetOutboundOrder(_ context.Context, _ uuid.UUID, order []uuid.UUID, _ time.Time, _ auth.AuditEntry) ([]trunk.Trunk, error) {
	for i := range f.trunks {
		f.trunks[i].OutboundPriority = nil
		for p, id := range order {
			if f.trunks[i].ID == id {
				n := p + 1
				f.trunks[i].OutboundPriority = &n
			}
		}
	}
	return f.trunks, nil
}

func (f *fakeTrunkStore) CreateDID(_ context.Context, d trunk.DID, _ auth.AuditEntry) error {
	f.dids = append(f.dids, d)
	return nil
}

type fakeTrunkAdmin struct {
	tenant uuid.UUID
	ext    pbx.Extension
}

func (f *fakeTrunkAdmin) DefaultTenant(context.Context) (uuid.UUID, error) { return f.tenant, nil }
func (f *fakeTrunkAdmin) ExtensionByNumber(_ context.Context, _ uuid.UUID, n string) (pbx.Extension, error) {
	if n != f.ext.Number {
		return pbx.Extension{}, pbx.ErrNotFound
	}
	return f.ext, nil
}

// fakeProber fails with an untrusted certificate until one is pinned.
type fakeProber struct{ targets []trunkprobe.Target }

var testPEM = func() string {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "UCM6304"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}()

func (p *fakeProber) Run(_ context.Context, t trunkprobe.Target) trunkprobe.Result {
	p.targets = append(p.targets, t)
	if t.WireGuard {
		return trunkprobe.Result{OK: true, Steps: []trunkprobe.Step{{Name: "tunnel", Result: trunkprobe.Warning, Words: "connecting"}}}
	}
	if t.Pinned == "" {
		return trunkprobe.Result{Untrusted: true, Steps: []trunkprobe.Step{
			{Name: "address", Result: trunkprobe.OK, Words: "ok"},
			{Name: "certificate", Result: trunkprobe.Failed, Words: "untrusted"}},
			Certificates: []trunkprobe.Certificate{{Names: []string{"UCM6304"}, Issuer: "UCM6304", SHA256: "AB:CD", SelfSigned: true, PEM: testPEM}}}
	}
	return trunkprobe.Result{OK: true, Steps: []trunkprobe.Step{{Name: "certificate", Result: trunkprobe.OK, Words: "pinned"}}}
}

func newTrunkCmd(t *testing.T, input string, interactive bool) (*trunkCmd, *fakeTrunkStore, *fakeProber, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var key [32]byte
	st := &fakeTrunkStore{}
	pr := &fakeProber{}
	svc := &trunk.Service{Store: st, Sealer: dbsecret.NewSealer(key), Now: time.Now, Prober: pr}
	var out, errb bytes.Buffer
	admin := &fakeTrunkAdmin{tenant: uuid.New(), ext: pbx.Extension{ID: uuid.New(), Number: "101"}}
	c := &trunkCmd{st: admin, svc: svc, in: bufio.NewReader(strings.NewReader(input)), out: &out, errw: &errb,
		interactive: interactive, readSecret: func() (string, error) { return "unused", nil }}
	return c, st, pr, &out, &errb
}

func TestTrunkAddGuided(t *testing.T) {
	// Template 4 (Grandstream UCM), default name, address, default port,
	// pin the certificate, one DID ringing 101, then done, primary line.
	input := "4\n\n192.168.1.5\n\nyes\n+97142000100\n101\n\nprimary\n"
	c, st, pr, out, errb := newTrunkCmd(t, input, true)
	if code := c.run(context.Background(), []string{"add"}); code != 0 {
		t.Fatalf("code %d\n%s\n%s", code, out, errb)
	}
	if len(st.trunks) != 1 {
		t.Fatalf("trunks %+v", st.trunks)
	}
	tr := st.trunks[0]
	if tr.Name != "Grandstream UCM (on the LAN)" || tr.Host != "192.168.1.5" || tr.Port != 5061 || tr.Kind != trunk.KindLANPeer ||
		tr.CertTrust != trunk.CertPinned || tr.PinnedCertificate != testPEM || tr.Unencrypted() {
		t.Errorf("saved %+v", tr)
	}
	if len(pr.targets) != 2 || pr.targets[1].Pinned != testPEM {
		t.Errorf("probed %+v", pr.targets)
	}
	if len(st.dids) != 1 || st.dids[0].Number != "+97142000100" || st.dids[0].ExtensionID == nil {
		t.Errorf("dids %+v", st.dids)
	}
	if tr.OutboundPriority == nil || *tr.OutboundPriority != 1 {
		t.Errorf("outgoing priority %v", tr.OutboundPriority)
	}
	for _, want := range []string{"Fingerprint: SHA-256 AB:CD", "Calls to +97142000100 ring extension 101", "primary line"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// list shows it.
	st.trunks[0].Status, st.trunks[0].StatusDetail = "unreachable", "It doesn't answer Linx's keep-alive checks."
	out.Reset()
	if code := c.run(context.Background(), []string{"list"}); code != 0 {
		t.Fatalf("list: %d", code)
	}
	for _, want := range []string{"STATUS", "unreachable", "encrypted", "1st", "keep-alive"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list missing %q:\n%s", want, out)
		}
	}

	// remove.
	c.interactive = false
	if code := c.run(context.Background(), []string{"remove", "grandstream ucm (on the lan)"}); code != 2 {
		t.Errorf("remove without --yes or a terminal: code %d", code)
	}
	if code := c.run(context.Background(), []string{"remove", "--yes", tr.Name}); code != 0 || len(st.trunks) != 0 {
		t.Errorf("remove: code %d, %d left\n%s", code, len(st.trunks), errb)
	}
}

func TestTrunkAddNonInteractive(t *testing.T) {
	// --yes never pins a certificate on its own: nothing is saved.
	c, st, _, out, _ := newTrunkCmd(t, "", true)
	code := c.run(context.Background(), []string{"add", "--yes", "--template", "grandstream_ucm", "--host", "192.168.1.5"})
	if code != 1 || len(st.trunks) != 0 || !strings.Contains(out.String(), "Nothing saved") {
		t.Fatalf("code %d, trunks %d\n%s", code, len(st.trunks), out)
	}

	// Missing answers are named.
	c, _, _, _, errb := newTrunkCmd(t, "", false)
	if code := c.run(context.Background(), []string{"add", "--template", "telnyx"}); code != 2 || !strings.Contains(errb.String(), "address") {
		t.Fatalf("code %d: %s", code, errb)
	}

	// Everything given, password from stdin, a pinned certificate passes.
	c, st, pr, out, errb := newTrunkCmd(t, "s3cret\n", false)
	pin := t.TempDir() + "/ca.pem"
	if err := writeFile(pin, testPEM); err != nil {
		t.Fatal(err)
	}
	code = c.run(context.Background(), []string{"add", "--yes", "--template", "generic", "--name", "Provider", "--host", "sip.example.com",
		"--username", "linx", "--password-stdin", "--pin", pin, "--did", "+97142000101", "--outgoing", "backup"})
	if code != 0 || len(st.trunks) != 1 || pr.targets[0].Password != "s3cret" || pr.targets[0].Username != "linx" {
		t.Fatalf("code %d %+v %+v\n%s%s", code, st.trunks, pr.targets, out, errb)
	}
	if len(st.dids) != 1 || st.dids[0].ExtensionID != nil || *st.trunks[0].OutboundPriority != 1 {
		t.Errorf("dids %+v, priority %v", st.dids, st.trunks[0].OutboundPriority)
	}

	// The certificate as linx passes it from the server (--pin-pem).
	c, st, _, out, errb = newTrunkCmd(t, "", false)
	code = c.run(context.Background(), []string{"add", "--yes", "--template", "grandstream_ucm", "--name", "UCM", "--host", "192.168.1.60",
		"--pin-pem", base64.StdEncoding.EncodeToString([]byte(testPEM))})
	if code != 0 || len(st.trunks) != 1 || st.trunks[0].CertTrust != trunk.CertPinned || st.trunks[0].PinnedCertificate != testPEM {
		t.Fatalf("--pin-pem: code %d %+v\n%s%s", code, st.trunks, out, errb)
	}
}

func writeFile(path, s string) error { return os.WriteFile(path, []byte(s), 0o600) }

// WireGuard's documentation's example key pair; not used for anything real.
const wgDocsPrivateKey = "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=" // gitleaks:allow (documentation example)

func TestTrunkWireGuard(t *testing.T) {
	conf := `[Interface]
PrivateKey = ` + wgDocsPrivateKey + `
Address = 10.6.0.2/24
DNS = 1.1.1.1

[Peer]
PublicKey = xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=
Endpoint = vpn.provider.test:51820
AllowedIPs = 0.0.0.0/0, ::/0
`
	c, st, pr, out, errb := newTrunkCmd(t, conf, false)
	if code := c.run(context.Background(), []string{"wireguard", "add", "Provider VPN"}); code != 0 {
		t.Fatalf("add: code %d\n%s\n%s", code, out, errb)
	}
	if len(st.profiles) != 1 || !strings.Contains(out.String(), "sent all traffic through the tunnel") ||
		!strings.Contains(out.String(), "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=") {
		t.Fatalf("profiles %v, output:\n%s", st.profiles, out)
	}

	// A line through it: plain UDP inside the tunnel, no ADR-023 warning,
	// and the test reports the tunnel.
	out.Reset()
	c.in = bufio.NewReader(strings.NewReader(""))
	if code := c.run(context.Background(), []string{"add", "--template", "grandstream_ucm", "--name", "VPN line", "--wireguard", "provider vpn",
		"--host", "10.6.0.1", "--outgoing", "no", "--yes"}); code != 0 {
		t.Fatalf("trunk add: code %d\n%s\n%s", code, out, errb)
	}
	tr := st.trunks[0]
	if tr.WireGuardProfileID == nil || *tr.WireGuardProfileID != st.profiles[0].ID || tr.Transport != trunk.TransportUDP ||
		tr.Port != 5060 || tr.MediaEncryption != trunk.MediaNone || tr.Unencrypted() || tr.UnencryptedConfirmedAt != nil {
		t.Fatalf("trunk %+v", tr)
	}
	if last := pr.targets[len(pr.targets)-1]; !last.WireGuard || last.TunnelName != "Provider VPN" {
		t.Errorf("probe target %+v", last)
	}

	// A name isn't allowed through a tunnel.
	errb.Reset()
	if code := c.run(context.Background(), []string{"add", "--template", "grandstream_ucm", "--name", "By name", "--wireguard", "Provider VPN",
		"--host", "sip.provider.test", "--outgoing", "no", "--yes"}); code == 0 || !strings.Contains(errb.String(), "IPv4 address") {
		t.Errorf("a name through a tunnel: code %d\n%s", code, errb)
	}

	out.Reset()
	if code := c.run(context.Background(), []string{"wireguard", "list"}); code != 0 || !strings.Contains(out.String(), "VPN line") {
		t.Errorf("list: code %d\n%s", code, out)
	}
	errb.Reset()
	if code := c.run(context.Background(), []string{"wireguard", "remove", "Provider VPN", "--yes"}); code != 1 ||
		!strings.Contains(errb.String(), "A trunk still connects through this profile") {
		t.Errorf("remove in use: code %d\n%s", code, errb)
	}
}
