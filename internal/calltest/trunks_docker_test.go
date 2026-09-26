package calltest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/ari"
	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/doctor"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/trunk"
	"linxpbx.com/linx/internal/trunkconf"
	"linxpbx.com/linx/internal/trunkstatus"
)

// SDP lines for provider.xml: SRTP (SDES) and unencrypted audio.
var (
	srtpSets  = []string{"-set", "proto", "RTP/SAVP", "-set", "crypto", "a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:y8r4kQ3zYt0Rvq2VJq0yJ3m0z2fX8sA1b5c6d7e8"}
	plainSets = []string{"-set", "proto", "RTP/AVP", "-set", "crypto", "a=ptime:20"}
)

// The login Linx signs in to the registration provider with: ";" must
// survive Asterisk's config (trunkconf escapes it), and so must a space.
const (
	trunkUser = "linxtrunk"
	trunkPass = "p;ss w0rd-" + "x7"
)

// TestTrunksDocker is docs/TRUNKS.md §13 step 3's suite: SIPp stands in for
// phone providers (one Linx signs in to over TLS with SRTP, one that calls
// in from its own address, an unencrypted one per ADR-023, and TLS servers
// whose certificates must be refused), with the trunks rendered exactly as
// the control plane renders them (internal/trunkconf) and reloaded by
// Asterisk's entrypoint. Inbound DIDs ring extensions; outbound calls get
// the number in each trunk's format and the right caller ID; international
// needs permission; emergency numbers always go out; trunk-to-trunk is
// impossible; lines fail over; a reload doesn't touch a call in progress.
func TestTrunksDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	tracker := &pbx.CallTracker{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e := start(t, ctx, func(e *env) ari.App {
		tracker.Store = e.store
		return tracker
	})
	eventually(t, "Asterisk's ARI connection", 30*time.Second, tracker.Connected)
	// LINX_CALLTEST_LOGS=dir keeps Asterisk's whole log there, for debugging.
	if dir := os.Getenv("LINX_CALLTEST_LOGS"); dir != "" {
		t.Cleanup(func() {
			out, _ := exec.Command("docker", "logs", astName).CombinedOutput()
			os.WriteFile(filepath.Join(dir, "asterisk.log"), out, 0o644)
		})
	}

	var key [dbsecret.KeySize]byte
	rand.Read(key[:])
	sealer := dbsecret.NewSealer(key)
	svc := &trunk.Service{Store: e.store, Sealer: sealer, Now: time.Now}
	admin := auth.WithPrincipal(ctx, auth.Principal{Type: auth.TypeSystem, ID: "cli", TenantID: e.tenant, Role: auth.RoleSystemAdmin})
	hosts := map[string][]netip.Addr{}
	renderer := &trunkconf.Renderer{Store: e.store, Sealer: sealer, Dir: filepath.Join(e.dir, "trunks"),
		Log:    slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) { return hosts[host], nil }}
	// render writes the trunks as the control plane does and waits for
	// Asterisk to load them.
	render := func() {
		t.Helper()
		before := strings.Count(e.asteriskLogs(), "trunks reloaded")
		changed, err := renderer.RenderOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// Asterisk runs as another user than this test: the control
		// plane's volume gives its group read access instead.
		for _, name := range []string{asteriskconf.TrunksFile, asteriskconf.PinnedCAFile} {
			os.Chmod(filepath.Join(e.dir, "trunks", name), 0o644)
		}
		if !changed {
			return
		}
		eventually(t, "Asterisk reloading the trunks", 20*time.Second, func() bool {
			return strings.Count(e.asteriskLogs(), "trunks reloaded") > before
		})
	}
	newTrunk := func(in trunk.TrunkInput) trunk.Trunk {
		t.Helper()
		tr, err := svc.CreateTrunk(admin, in)
		if err != nil {
			t.Fatalf("creating trunk %s: %v", in.Name, err)
		}
		return tr
	}
	did := func(tr trunk.Trunk, number string, ext pbx.Extension) {
		t.Helper()
		if _, err := svc.CreateDID(admin, tr.ID, trunk.DIDInput{Number: number, ExtensionID: &ext.ID}); err != nil {
			t.Fatal(err)
		}
	}
	route := func(trunks ...trunk.Trunk) {
		t.Helper()
		var ids []uuid.UUID
		for _, tr := range trunks {
			ids = append(ids, tr.ID)
		}
		if _, err := svc.SetOutboundOrder(admin, ids); err != nil {
			t.Fatal(err)
		}
	}

	// People: Alice may call local, mobile, national and toll-free numbers
	// (not international), Bob answers calls from outside, Dave has no
	// permission level at all.
	staff, err := svc.CreateCallPermissionLevel(admin, trunk.CallPermissionLevelInput{Name: "Staff",
		AllowedCategories: []string{"landline", "mobile", "national", "toll_free"}})
	if err != nil {
		t.Fatal(err)
	}
	alice := e.newPhone("101", "Alice")
	alice.ext.CallPermissionLevelID = &staff.ID
	if _, err := e.store.UpdateExtension(ctx, alice.ext, e.audit("extension.update")); err != nil {
		t.Fatal(err)
	}
	bob := e.newPhone("102", "Bob")
	dave := e.newPhone("104", "Dave")
	bobPhone := e.sipp("bob", "register.xml", bob, "-oocsf", "/scenarios/answer.xml", "-d", "600000")
	t.Cleanup(func() { exec.Command("docker", "rm", "--force", bobPhone).Run() })
	eventually(t, "Bob online", 15*time.Second, func() bool {
		d, _, _ := e.store.DeviceBySIPUsername(ctx, bob.dev.SIPUsername)
		return d.Online
	})

	// Certificates: the registration provider's is from the test CA,
	// which its trunk pins (ADR-045); another CA's is pinned by nobody.
	testCA := string(mustRead(t, filepath.Join(e.dir, "ca", "root_ca.crt")))
	otherCA, otherKey, otherPEM := newCA(t, "Other Test Root")
	e.writeLeaf("provider", e.caCert, e.caKey, 30, "provider.linx.test")
	e.writeLeaf("untrusted", otherCA, otherKey, 31, "untrusted.linx.test")

	var provider, alicePhone string
	t.Run("a provider Linx signs in to, over TLS with SRTP", func(t *testing.T) {
		provider = e.provider("provider", netName, "provider.linx.test", "l1", 5061, "provider",
			append([]string{"-set", "user", trunkUser, "-set", "pass", trunkPass, "-set", "did", "+97142000102"}, srtpSets...)...)
		hosts["provider.linx.test"] = []netip.Addr{e.ipOn(provider, netName)}

		reg := newTrunk(trunk.TrunkInput{Name: "Provider", Kind: trunk.KindRegistration, Host: "provider.linx.test",
			CertTrust: trunk.CertPinned, PinnedCertificate: testCA, Username: trunkUser, Password: trunkPass,
			CallerIDNumber: "+97142000100"})
		did(reg, "+97142000101", alice.ext)
		did(reg, "+97142000102", bob.ext)
		route(reg)
		e.asteriskCLI("pjsip set logger on")
		render()

		// Linx signs in with the right login (the provider checks it) ...
		eventually(t, "the registration", 20*time.Second, func() bool {
			return strings.Contains(e.asteriskCLI("pjsip show registrations"), "Registered")
		})
		// ... and the provider's call on that registration, to Bob's DID
		// in its To header, rings Bob, who answers.
		eventually(t, "the provider's call to Bob answered and ended", 30*time.Second, func() bool {
			return strings.Contains(e.logs(provider), "inbound call ended")
		})
		e.asteriskCLI("pjsip set logger off")
		logs := e.asteriskLogs()
		// The caller's name reaches Bob stripped of quotes and brackets.
		if !strings.Contains(logs, `From: "Evil Caller" <sip:+971501112222@`) || strings.Contains(logs, `Caller>" <sip:+971501112222@sip.linx.test`) {
			t.Errorf("the INVITE to Bob doesn't carry the cleaned-up caller name:\n%s", logs)
		}
		if l := e.logs(provider); strings.Contains(l, "REGISTER refused") {
			t.Errorf("the provider refused Linx's login:\n%s", l)
		}
		// The call's events say where it came from: the trunk, the
		// caller's number, and the DID dialled (LINX_DID).
		in := e.callEnded("+97142000102")
		if in["direction"] != pbx.DirectionInbound || in["trunk_id"] != reg.ID.String() || in["outside_number"] != "+971501112222" {
			t.Errorf("inbound call.ended = %v", in)
		}

		// Asterisk's entrypoint reports the registration, and the control
		// plane's monitor turns it into the trunk's status and an event.
		statusDir := filepath.Join(e.dir, "trunk-status")
		eventually(t, "the trunk status report", 30*time.Second, func() bool {
			f, err := trunkstatus.Read(statusDir)
			return err == nil && f.Trunks[reg.Endpoint()].Registration == trunkstatus.RegRegistered
		})
		mon := &trunk.Monitor{Store: e.store, Alerts: noAlerts{}, Dir: statusDir, Log: slog.New(slog.DiscardHandler)}
		if err := mon.Check(e.ctx); err != nil {
			t.Fatal(err)
		}
		if got, err := e.store.Trunk(e.ctx, reg.TenantID, reg.ID); err != nil || got.Status != trunkstatus.StatusRegistered {
			t.Errorf("trunk status %q (%v), want registered", got.Status, err)
		}
		// doctor's reading of the same console output agrees.
		regs := trunkstatus.ParseRegistrations(e.asteriskCLI("pjsip show registrations"))
		if regs[reg.Endpoint()] != trunkstatus.RegRegistered {
			t.Errorf("doctor's parser: %v", regs)
		}
		// Every doctor check of the phones' transports still passes.
		if bad, ok := doctor.PlainSIPTransports(e.asteriskCLI("pjsip show transports")); !ok || len(bad) > 0 {
			t.Errorf("transports: %v", bad)
		}
	})
	if provider == "" {
		t.Fatal("no provider")
	}

	t.Run("outgoing: a mobile number, with Alice's caller ID, transcoded", func(t *testing.T) {
		call := e.sipp("out-mobile", "call.xml", alice, "-s", "0501234567", "-d", "3000")
		// Alice's phone talks PCMU (ulaw), the provider PCMA (alaw).
		eventually(t, "the call up with both codecs", 15*time.Second, func() bool {
			d := e.channelDetails()
			return strings.Contains(d, "(ulaw)") && strings.Contains(d, "(alaw)")
		})
		e.wait(call)
		if l := e.logs(provider); !strings.Contains(l, "INVITE for +971501234567 from +97142000101 audio RTP/SAVP") {
			t.Errorf("the provider's log:\n%s", l)
		}
		if ended := e.callEnded("0501234567"); ended["outcome"] != pbx.OutcomeAnswered || ended["direction"] != pbx.DirectionOutbound ||
			ended["outside_number"] != "+971501234567" || ended["trunk_id"] == nil {
			t.Errorf("call.ended = %v", ended)
		}
	})

	t.Run("international needs permission", func(t *testing.T) {
		e.run("international", "call-message.xml", alice, "-s", "00442079460958")
		if got := e.callEnded("00442079460958")["outcome"]; got != pbx.OutcomeNotPermitted {
			t.Errorf("outcome %v, want %s", got, pbx.OutcomeNotPermitted)
		}
		if strings.Contains(e.logs(provider), "INVITE for +442079460958") {
			t.Error("the international call went out")
		}
	})

	t.Run("emergency numbers always go out", func(t *testing.T) {
		// Dave has no permission level: only emergency numbers work, with
		// the trunk's main number as caller ID.
		e.run("emergency", "call.xml", dave, "-s", "999", "-d", "1000")
		if l := e.logs(provider); !strings.Contains(l, "INVITE for 999 from +97142000100") {
			t.Errorf("the provider's log:\n%s", l)
		}
		e.run("no-level", "call-message.xml", dave, "-s", "0501234568")
		if got := e.callEnded("0501234568")["outcome"]; got != pbx.OutcomeNotPermitted {
			t.Errorf("outcome %v, want %s", got, pbx.OutcomeNotPermitted)
		}
	})

	t.Run("at most 2 outside calls per extension", func(t *testing.T) {
		// One at a time until answered: every SIPp runs as PID 1 in its
		// container, so calls placed at the same moment carry the same SIP
		// branch and Asterisk takes the second for a repeat of the first.
		first := e.sipp("limit-1", "call.xml", alice, "-s", "0501111111", "-d", "8000")
		eventually(t, "the first call through", 15*time.Second, func() bool {
			return strings.Contains(e.logs(provider), "INVITE for +971501111111")
		})
		time.Sleep(time.Second)
		second := e.sipp("limit-2", "call.xml", alice, "-s", "0501111112", "-d", "8000")
		e.eventuallyOr(t, "both calls through", 15*time.Second, func() bool {
			l := e.logs(provider)
			return strings.Contains(l, "INVITE for +971501111111") && strings.Contains(l, "INVITE for +971501111112")
		}, first, second, provider)
		e.run("limit-3", "call-message.xml", alice, "-s", "0501111113")
		if got := e.callEnded("0501111113")["outcome"]; got != pbx.OutcomeLimitReached {
			t.Errorf("third call: outcome %v, want %s", got, pbx.OutcomeLimitReached)
		}
		e.wait(first)
		e.wait(second)
	})

	t.Run("a provider that calls in from its own address", func(t *testing.T) {
		// The peer sits on a network phones may not connect from: only
		// its trunk's entry in the ACL lets it in.
		peer := e.idle("peer", outsideNet)
		hosts["peer.linx.test"] = []netip.Addr{e.ipOn(peer, outsideNet)}
		pt := newTrunk(trunk.TrunkInput{Name: "Peer", Kind: trunk.KindIPAuthenticated, Host: "peer.linx.test",
			CertTrust: trunk.CertPinned, PinnedCertificate: testCA})
		did(pt, "+97142000103", bob.ext)
		render()

		if out, err := e.execSIPp(peer, "provider-call.xml", "-s", "+97142000103", "-d", "1500"); err != nil {
			t.Fatalf("the peer's call to Bob's DID: %v\n%s\n%s", err, out, e.asteriskLogs())
		}
		// A number that isn't its DID, even one Alice may dial: "not in
		// use", and nothing goes out on the other trunk (no trunk-to-trunk,
		// ADR-048).
		before := strings.Count(e.logs(provider), "INVITE for")
		if out, err := e.execSIPp(peer, "provider-call-message.xml", "-s", "0501234567"); err != nil {
			t.Fatalf("the peer's call to an outside number: %v\n%s\n%s", err, out, e.asteriskLogs())
		}
		// Another trunk's DID isn't this trunk's to call either.
		if out, err := e.execSIPp(peer, "provider-call-message.xml", "-s", "+97142000102"); err != nil {
			t.Fatalf("the peer's call to another trunk's DID: %v\n%s\n%s", err, out, e.asteriskLogs())
		}
		if after := strings.Count(e.logs(provider), "INVITE for"); after != before {
			t.Errorf("a call from one trunk went out on another")
		}
	})

	t.Run("an unencrypted trunk, after a line that's down", func(t *testing.T) {
		plain := e.provider("plain", netName, "plain.linx.test", "t1", 5060, "", plainSets...)
		hosts["plain.linx.test"] = []netip.Addr{e.ipOn(plain, netName)}
		// A LAN phone system whose port is closed: Postgres's address.
		dead := newTrunk(trunk.TrunkInput{Name: "Dead", Kind: trunk.KindLANPeer, Host: e.ipOn(pgName, netName).String(),
			Port: ptr(5099), CertTrust: trunk.CertPinned, PinnedCertificate: testCA})
		if _, err := svc.CreateTrunk(admin, trunk.TrunkInput{Name: "Plain", Kind: trunk.KindLANPeer, Host: "plain.linx.test",
			Port: ptr(5060), Transport: trunk.TransportTCP, MediaEncryption: trunk.MediaNone, DialFormat: trunk.DialLocal}); err == nil {
			t.Fatal("an unencrypted trunk was saved without the ADR-023 confirmation")
		}
		pl := newTrunk(trunk.TrunkInput{Name: "Plain", Kind: trunk.KindLANPeer, Host: "plain.linx.test", Port: ptr(5060),
			Transport: trunk.TransportTCP, MediaEncryption: trunk.MediaNone, DialFormat: trunk.DialLocal, ConfirmUnencrypted: true})
		route(dead, pl)
		render()

		e.asteriskCLI("pjsip set logger on")
		e.run("failover", "call.xml", alice, "-s", "+971501234569", "-d", "1000")
		e.asteriskCLI("pjsip set logger off")
		// The national way for a "local" trunk; unencrypted audio.
		if l := e.logs(plain); !strings.Contains(l, "INVITE for 0501234569 from 101 audio RTP/AVP") {
			t.Errorf("the unencrypted provider's log:\n%s\n--- Asterisk:\n%s\n%s\n%s\n%s", l, e.callLogs("0501234569"),
				e.asteriskCLI("pjsip show transports"), e.asteriskCLI("pjsip show contacts"), e.asteriskCLI("pjsip show endpoint "+pl.Endpoint()))
		}
		out := e.asteriskCLI("pjsip show transports")
		if !strings.Contains(out, "transport-trunk-tcp") {
			t.Errorf("no plain transport for the trunk:\n%s", out)
		}
		if bad, ok := doctor.PlainSIPTransports(out); !ok || len(bad) > 0 {
			t.Errorf("doctor counts the trunk's transport as a phone one: %v", bad)
		}
	})

	t.Run("certificates: unknown CA and wrong name refused, reload during a call", func(t *testing.T) {
		checker := e.provider("tls-check", netName, "untrusted.linx.test", "l1", 5061, "untrusted", srtpSets...)
		docker(t, ctx, "network", "disconnect", netName, checker)
		docker(t, ctx, "network", "connect", "--alias", "untrusted.linx.test", "--alias", "wrongname.linx.test", netName, checker)
		hosts["untrusted.linx.test"] = []netip.Addr{e.ipOn(checker, netName)}
		hosts["wrongname.linx.test"] = hosts["untrusted.linx.test"]

		// A call in progress on the provider while the trunks reload.
		reg, err := e.trunkByName(admin, svc, "Provider")
		if err != nil {
			t.Fatal(err)
		}
		route(reg)
		render()
		alicePhone = e.sipp("during-reload", "call.xml", alice, "-s", "0502222222", "-d", "8000")
		eventually(t, "the call up", 15*time.Second, func() bool { return strings.Contains(e.logs(provider), "INVITE for +971502222222") })

		// A publicly trusted certificate is required without a pin: the
		// other CA isn't one.
		untrusted := newTrunk(trunk.TrunkInput{Name: "Untrusted", Kind: trunk.KindLANPeer, Host: "untrusted.linx.test"})
		render()
		e.wait(alicePhone) // carried on to its normal end through the reload
		route(untrusted)
		e.run("untrusted", "call-message.xml", alice, "-s", "0503333333")
		if got := e.callEnded("0503333333")["outcome"]; got != pbx.OutcomeNoLines {
			t.Errorf("untrusted certificate: outcome %v, want %s\n%s", got, pbx.OutcomeNoLines, e.callLogs("0503333333"))
		}

		// Pinned, but the certificate names another host.
		wrong := newTrunk(trunk.TrunkInput{Name: "Wrong name", Kind: trunk.KindLANPeer, Host: "wrongname.linx.test",
			CertTrust: trunk.CertPinned, PinnedCertificate: otherPEM})
		route(wrong)
		render()
		e.run("wrong-name", "call-message.xml", alice, "-s", "0504444444")
		if got := e.callEnded("0504444444")["outcome"]; got != pbx.OutcomeNoLines {
			t.Errorf("wrong name: outcome %v, want %s", got, pbx.OutcomeNoLines)
		}
		if l := e.logs(checker); strings.Contains(l, "INVITE for") {
			t.Errorf("a call reached a server whose certificate should have been refused:\n%s", l)
		}
		// Refused for the right reasons: no trusted CA, then the name.
		out, _ := exec.Command("docker", "logs", astName).CombinedOutput()
		for _, want := range []string{"'untrusted.linx.test' - " + hosts["untrusted.linx.test"][0].String() + ":5061 - The certificate is untrusted",
			"'wrongname.linx.test' - " + hosts["untrusted.linx.test"][0].String() + ":5061 - The server identity does not match"} {
			if !strings.Contains(string(out), want) {
				t.Errorf("Asterisk didn't log %q", want)
			}
		}
	})

	t.Run("a removed trunk is gone from Asterisk", func(t *testing.T) {
		reg, err := e.trunkByName(admin, svc, "Provider")
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.DeleteTrunk(admin, reg.ID); err != nil {
			t.Fatal(err)
		}
		render()
		if out := e.asteriskCLI("pjsip show endpoints"); strings.Contains(out, reg.Endpoint()) {
			t.Errorf("deleted trunk still loaded:\n%s", out)
		}
		// Phones are untouched by all these reloads.
		e.run("echo-after", "call.xml", alice, "-s", "*43", "-d", "1000")
	})
}

func ptr[T any](v T) *T { return &v }

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// newCA makes a CA nobody else trusts.
func newCA(t *testing.T, name string) (*x509.Certificate, *ecdsa.PrivateKey, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// writeLeaf writes a server certificate for dns into e.dir/tls-<name>.
func (e *env) writeLeaf(name string, ca *x509.Certificate, key *ecdsa.PrivateKey, serial int64, dns string) {
	e.t.Helper()
	cert, k := e.leafFrom(ca, key, serial, dns)
	dir := filepath.Join(e.dir, "tls-"+name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	os.Chmod(dir, 0o755)
	for f, b := range map[string][]byte{"server.pem": cert, "server.key": k} {
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
}

// provider starts provider.xml as a SIPp server (transport l1 = TLS with
// the certificate writeLeaf wrote for tlsName, t1 = TCP) on network with
// alias, and waits until it listens.
func (e *env) provider(name, network, alias, transport string, port int, tlsName string, extra ...string) string {
	e.t.Helper()
	cname := prefix + "-prov-" + name
	exec.Command("docker", "rm", "--force", cname).Run()
	testdata, err := filepath.Abs("testdata")
	if err != nil {
		e.t.Fatal(err)
	}
	args := []string{"run", "--detach", "--name", cname, "--network", network, "--network-alias", alias,
		"--volume", testdata + ":/scenarios:ro"}
	if tlsName != "" {
		args = append(args, "--volume", filepath.Join(e.dir, "tls-"+tlsName)+":/tls:ro")
	}
	args = append(args, sippImage, "-t", transport, "-p", strconv.Itoa(port), "-sf", "/scenarios/provider.xml",
		"-nostdin", "-trace_logs", "-log_file", "/dev/stdout", "-trace_err", "-error_file", "/dev/stderr")
	if tlsName != "" {
		args = append(args, "-tls_cert", "/tls/server.pem", "-tls_key", "/tls/server.key")
	}
	docker(e.t, e.ctx, append(args, extra...)...)
	e.t.Cleanup(func() { exec.Command("docker", "rm", "--force", cname).Run() })
	eventually(e.t, name+" listening", 15*time.Second, func() bool {
		out, _ := exec.Command("docker", "exec", cname, "cat", "/proc/net/tcp").CombinedOutput()
		return slices.ContainsFunc(doctor.ListeningTCP(string(out)), func(a netip.AddrPort) bool { return a.Port() == uint16(port) })
	})
	return cname
}

// idle starts a SIPp container that does nothing, so scenarios run in it
// (execSIPp) always come from the same address.
func (e *env) idle(name, network string) string {
	e.t.Helper()
	cname := prefix + "-idle-" + name
	exec.Command("docker", "rm", "--force", cname).Run()
	testdata, err := filepath.Abs("testdata")
	if err != nil {
		e.t.Fatal(err)
	}
	docker(e.t, e.ctx, "run", "--detach", "--name", cname, "--network", network,
		"--volume", testdata+":/scenarios:ro", "--volume", filepath.Join(e.dir, "sipp")+":/tls:ro",
		"--entrypoint", "sleep", sippImage, "infinity")
	e.t.Cleanup(func() { exec.Command("docker", "rm", "--force", cname).Run() })
	return cname
}

// execSIPp runs a scenario to Asterisk over TLS in container cname.
func (e *env) execSIPp(cname, scenario string, extra ...string) (string, error) {
	args := []string{"exec", cname, "sipp", "asterisk:5061", "-t", "l1",
		"-tls_cert", "/tls/client.pem", "-tls_key", "/tls/client.key", "-tls_ca", "/tls/root_ca.crt",
		"-sf", "/scenarios/" + scenario, "-m", "1", "-nostdin", "-timeout", sipTimeout, "-timeout_error",
		"-trace_err", "-error_file", "/dev/stderr"}
	out, err := exec.CommandContext(e.ctx, "docker", append(args, extra...)...).CombinedOutput()
	return string(out), err
}

// ipOn is a container's address on a network.
func (e *env) ipOn(cname, network string) netip.Addr {
	e.t.Helper()
	s := docker(e.t, e.ctx, "inspect", "--format", "{{(index .NetworkSettings.Networks \""+network+"\").IPAddress}}", cname)
	a, err := netip.ParseAddr(s)
	if err != nil {
		e.t.Fatalf("%s's address on %s: %q", cname, network, s)
	}
	return a
}

// eventuallyOr is eventually, failing with the logs of the containers
// named and Asterisk's.
func (e *env) eventuallyOr(t *testing.T, what string, timeout time.Duration, cond func() bool, containers ...string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			var b strings.Builder
			for _, c := range containers {
				b.WriteString("--- " + c + ":\n" + e.logs(c))
			}
			t.Fatalf("timed out waiting for %s\n%s--- Asterisk:\n%s", what, b.String(), e.callLogs("linx-outbound"))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// callLogs is every Asterisk log line of the calls whose log mentions
// sub (by their "[C-…]" call id), plus every warning and error.
func (e *env) callLogs(sub string) string {
	out, _ := exec.Command("docker", "logs", astName).CombinedOutput()
	lines := strings.Split(string(out), "\n")
	calls := map[string]bool{}
	id := regexp.MustCompile(`\[C-[0-9a-f]+\]`)
	for _, l := range lines {
		if strings.Contains(l, sub) {
			if m := id.FindString(l); m != "" {
				calls[m] = true
			}
		}
	}
	var b strings.Builder
	for _, l := range lines {
		if m := id.FindString(l); calls[m] && m != "" || strings.Contains(l, "WARNING[") || strings.Contains(l, "ERROR[") || strings.Contains(l, "NOTICE[") {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

// logs is a container's whole log.
func (e *env) logs(cname string) string {
	out, _ := exec.Command("docker", "logs", cname).CombinedOutput()
	return string(out)
}

func (e *env) trunkByName(ctx context.Context, svc *trunk.Service, name string) (trunk.Trunk, error) {
	all, err := svc.ListTrunks(ctx, nil, 100)
	if err != nil {
		return trunk.Trunk{}, err
	}
	for _, tr := range all {
		if tr.Name == name {
			return tr, nil
		}
	}
	return trunk.Trunk{}, trunk.ErrNotFound
}

// noAlerts is an alert engine that drops everything.
type noAlerts struct{}

func (noAlerts) FireAfter(context.Context, uuid.UUID, string, string, string, string, string, time.Duration) error {
	return nil
}
func (noAlerts) Resolve(context.Context, uuid.UUID, string) error { return nil }
