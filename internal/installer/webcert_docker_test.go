package installer

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/deploy/compose"
	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/dnscheck"
	"linxpbx.com/linx/internal/dnsname"
	"linxpbx.com/linx/internal/install"
	"linxpbx.com/linx/internal/ops"
)

// Pebble, Let's Encrypt's own test ACME server, and its DNS test server
// (test tooling only, never shipped: MPL-2.0).
const (
	pebbleImage       = "ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"
	challtestsrvImage = "ghcr.io/letsencrypt/pebble-challtestsrv@sha256:12ce21884def456bcf9786542113949e1f19dc7738d2c70e156c2d0c38a1405b"
	certTestProject   = "linxcerttest"
	certTestDomain    = "linx.test"
)

// TestWebCertificateInstall gets the web install's first certificate the
// way a rented server where Linx takes 443 does (docs/INSTALL.md §4): the
// real install.yaml with the real control-plane and certd images (make
// image SERVICE=control-plane and SERVICE=certd), the real linx-sni in
// front, the host side (install.Host) driving it over docker exec, and
// Pebble checking the names over acme-tls/1 on port 443. Then the handoff
// moves the session to https://linx.test, verified against Pebble's
// root. Needs Docker and host ports 443 and 6464 free: make test-install
// (not make test-docker, which runs its packages at once: this and
// TestInstallModeDocker both need the container name linx-control-plane).
func TestWebCertificateInstall(t *testing.T) {
	if os.Getenv("LINX_DOCKER_TESTS") != "1" {
		t.Skip("set LINX_DOCKER_TESTS=1 (make test-docker)")
	}
	images := map[string]string{"control-plane": os.Getenv("LINX_CONTROL_PLANE_IMAGE"), "certd": os.Getenv("LINX_CERTD_IMAGE")}
	for svc, img := range images {
		if img == "" {
			images[svc] = "linx-" + svc + ":dev"
		}
		if exec.Command("docker", "image", "inspect", images[svc]).Run() != nil {
			t.Skipf("no %s image: make image SERVICE=%s", images[svc], svc)
		}
	}
	for _, name := range []string{"linx-control-plane", "linx-sni"} {
		if out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name=^"+name+"$").Output(); len(bytes.TrimSpace(out)) > 0 {
			t.Skipf("a %s container already exists (another suite running?)", name)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// The stack's files, as setup writes them next to each other.
	dir := t.TempDir()
	write := func(name, data string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Download first: a pull's messages would mix into create's output.
	docker("pull", "--quiet", pebbleImage)
	docker("pull", "--quiet", challtestsrvImage)
	id := docker("create", pebbleImage)
	docker("cp", id+":/test/certs/pebble.minica.pem", filepath.Join(dir, "pebble.minica.pem"))
	docker("rm", id)
	write("install.yaml", string(compose.InstallFile))
	write("haproxy.cfg", string(HAProxyConfig(certTestDomain)))
	// Tests only: certd asks Pebble instead of Let's Encrypt, and trusts
	// Pebble's own HTTPS certificate for that.
	write("override.yaml", `services:
  certd:
    environment:
      LINX_ACME_TEST_DIRECTORY: https://pebble:14000/dir
      LEGO_CA_CERTIFICATES: /pebble/pebble.minica.pem
    volumes:
      - ./pebble.minica.pem:/pebble/pebble.minica.pem:ro
`)
	c := DefaultConfig()
	c.Domain.Name, c.Domain.DNSProvider = certTestDomain, DNSCloudflare
	c.Certificates.Email = "owner@example.com"
	c.FrontDoor = FrontDoorConfig{Kind: FrontDoorLinx443}
	env := string(InstallCertEnv(c, LAN{}, "dev", netip.MustParseAddr("127.0.0.1")))
	// Linx's own 443 router on this machine's loopback only.
	env = strings.Replace(env, "LINX_SNI_ADDRESS=0.0.0.0", "LINX_SNI_ADDRESS=127.0.0.1", 1)
	tlsDir, firstPool := firstPageTLS(t, "127.0.0.1")
	write("install.env", "LINX_IMAGE_PREFIX="+certTestProject+"\nLINX_INSTALL_TLS_DIR="+tlsDir+"\n"+env)
	for svc, img := range images {
		docker("tag", img, certTestProject+"/linx-"+svc+":dev")
	}
	// "down --volumes" leaves the containers and volumes of services it
	// didn't start (certd's).
	removeVolumes := func() {
		if out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+certTestProject).Output(); len(bytes.TrimSpace(out)) > 0 {
			_ = exec.Command("docker", append([]string{"rm", "--force"}, strings.Fields(string(out))...)...).Run()
		}
		for _, v := range []string{"certd-state", "certs", "acme-challenge"} {
			_ = exec.Command("docker", "volume", "rm", "--force", certTestProject+"_"+v).Run()
		}
	}
	dc := []string{"compose", "--project-name", certTestProject, "--project-directory", dir,
		"--file", filepath.Join(dir, "install.yaml"), "--file", filepath.Join(dir, "override.yaml"), "--env-file", filepath.Join(dir, "install.env")}
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", "--tail", "40", certTestProject+"-pebble").CombinedOutput()
			t.Logf("pebble:\n%s", out)
			out, _ = exec.Command("docker", append(dc, "logs", "--tail", "40")...).CombinedOutput()
			t.Logf("stack:\n%s", out)
		}
		_ = exec.Command("docker", "rm", "--force", certTestProject+"-pebble", certTestProject+"-dns").Run()
		removeVolumes()
		_ = exec.Command("docker", append(dc, "down", "--volumes")...).Run()
		removeVolumes()
		// certd's network, which "down" leaves like its volumes.
		_ = exec.Command("docker", "network", "rm", "linx-egress").Run()
		for _, img := range []string{"control-plane", "certd"} {
			_ = exec.Command("docker", "image", "rm", certTestProject+"/linx-"+img+":dev").Run()
		}
	})
	// Nothing left from an earlier run: its ACME account belongs to another
	// Pebble.
	_ = exec.Command("docker", append(dc, "down", "--volumes")...).Run()
	removeVolumes()
	docker(append(dc, "up", "--detach", "--wait")...)
	// certd's network, which "run" creates in real use, for Pebble to join.
	docker(append(dc, "create", "certd")...)
	sniIP := docker("inspect", "--format", `{{(index .NetworkSettings.Networks "linx-public").IPAddress}}`, "linx-sni")

	// Pebble checks names on port 443 through linx-sni, looking them up in
	// the DNS test server; certd reaches Pebble by name on linx-egress.
	docker("run", "--detach", "--name", certTestProject+"-dns", "--network", "linx-public",
		"--publish", "127.0.0.1::8053/udp", "--publish", "127.0.0.1::8055/tcp",
		challtestsrvImage, "-defaultIPv4", "", "-defaultIPv6", "", "-http01", "", "-https01", "", "-tlsalpn01", "", "-doh", "")
	pebbleConfig := `{"pebble":{"listenAddress":"0.0.0.0:14000","managementListenAddress":"0.0.0.0:15000",` +
		`"certificate":"/test/certs/localhost/cert.pem","privateKey":"/test/certs/localhost/key.pem","httpPort":80,"tlsPort":443}}`
	write("pebble.json", pebbleConfig)
	docker("run", "--detach", "--name", certTestProject+"-pebble", "--network", "linx-egress", "--network-alias", "pebble",
		"--env", "PEBBLE_VA_NOSLEEP=1", "--env", "PEBBLE_WFE_NONCEREJECT=0", "--publish", "127.0.0.1::15000/tcp",
		"--volume", filepath.Join(dir, "pebble.json")+":/pebble.json:ro",
		pebbleImage, "-config", "/pebble.json", "-dnsserver", certTestProject+"-dns:8053")
	docker("network", "connect", "linx-public", certTestProject+"-pebble")
	dnsAddr := docker("port", certTestProject+"-dns", "8053/udp")
	mgmt := "http://" + docker("port", certTestProject+"-dns", "8055/tcp")
	pebbleMgmt := "https://" + docker("port", certTestProject+"-pebble", "15000/tcp")

	var accepted install.Answers
	h := &install.Host{
		Path: filepath.Join(dir, "state.json"),
		Facts: func(context.Context) install.Facts {
			return install.Facts{Where: install.WhereRented, PublicAddress: sniIP}
		},
		Dial: func(ctx context.Context) (io.ReadWriteCloser, error) { return ops.DialBridge(ctx, "install-bridge") },
		Check: func(_ context.Context, a install.Answers) (string, []install.FieldError, error) {
			accepted = a
			return "Domain: " + a.Domain, nil, nil
		},
		Cert:  &dockerCertifier{c: c, dc: dc, dns: dnscheck.Resolver{Server: dnsAddr, Timeout: 2 * time.Second}},
		Retry: 200 * time.Millisecond, Poll: time.Second,
	}
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}
	go h.Run(ctx)

	// The plain page: claim the link, save the answers.
	jar, _ := cookiejar.New(nil)
	plain := &http.Client{Jar: jar, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: firstPool, MinVersion: tls.VersionTLS12}}}
	const base = "https://127.0.0.1:6464"
	var resp *http.Response
	for range 60 {
		r, err := plain.Get(base + "/install/" + h.State().Secret)
		if err == nil && r.StatusCode == http.StatusSeeOther {
			resp = r
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if resp == nil {
		t.Fatal("the link was never claimed")
	}
	post := func(client *http.Client, url, origin, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		r, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", url, err)
		}
		return r
	}
	answers := `{"where":"rented","front_door":"linx-443","domain":"linx.test","name":"Owner","email":"owner@example.com","time_zone":"UTC","agreed_to_terms":true}`
	if r := post(plain, base+"/install/api/check", base, answers); r.StatusCode != http.StatusOK {
		t.Fatalf("check: %d", r.StatusCode)
	}
	if accepted.Domain != certTestDomain {
		t.Fatalf("answers: %+v", accepted)
	}

	// No record yet: the page waits, and nothing is asked of Pebble.
	waitUntil(t, ctx, func() bool { c := h.State().View.Cert; return c != nil && c.DNS.State == install.DNSMissing })
	if c := h.State().View.Cert; c.Prepare.State != install.StageOK || c.Reach.State != "" {
		t.Fatalf("before the record: %+v", c)
	}
	addA := func(host string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"host": dnsname.Host(host, certTestDomain) + ".", "addresses": []string{sniIP}})
		r, err := http.Post(mgmt+"/add-a", "application/json", bytes.NewReader(b))
		if err != nil || r.StatusCode != http.StatusOK {
			t.Fatalf("add-a: %v %v", err, r)
		}
	}
	// The domain alone isn't enough: Let's Encrypt checks turn. too.
	addA(dnsname.Apex)
	waitUntil(t, ctx, func() bool {
		c := h.State().View.Cert
		return len(c.DNS.Names) == 2 && c.DNS.Names[0].State == install.DNSOK
	})
	time.Sleep(2 * time.Second)
	if c := h.State().View.Cert; c.DNS.State != install.DNSMissing || c.Reach.State != "" {
		t.Fatalf("with the domain only: %+v", c)
	}
	// The owner adds the second record: the rest happens by itself.
	addA("turn")
	waitUntil(t, ctx, func() bool {
		c := h.State().View.Cert
		if c != nil && (c.Reach.State == install.StageFailed || c.Certificate.State == install.StageFailed) {
			t.Fatalf("certificate failed: %+v", c)
		}
		return c.Ready()
	})
	var lines []string
	for _, p := range h.State().Progress {
		lines = append(lines, p.Text)
	}
	for _, want := range []string{"linx.test and turn.linx.test point at this server", "Let's Encrypt reached this server on port 443", "Certificate ready: https://linx.test"} {
		if !strings.Contains(strings.Join(lines, "\n"), want) {
			t.Errorf("progress lacks %q: %q", want, lines)
		}
	}

	// Pebble's root, read over its management port (which uses its own
	// test certificate, for "localhost").
	minica, err := os.ReadFile(filepath.Join(dir, "pebble.minica.pem"))
	if err != nil {
		t.Fatal(err)
	}
	mroots := x509.NewCertPool()
	mroots.AppendCertsFromPEM(minica)
	mc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: mroots, ServerName: "localhost"}}}
	r, err := mc.Get(pebbleMgmt + "/roots/0")
	if err != nil {
		t.Fatal(err)
	}
	rootPEM, _ := io.ReadAll(r.Body)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		t.Fatalf("pebble root: %s", rootPEM)
	}

	// The handoff, then the secure page over HTTPS, trusting only Pebble's
	// root: the certificate served is the one just got.
	r = post(plain, base+"/install/api/handoff", base, "{}")
	var ho struct{ Handoff string }
	if r.StatusCode != http.StatusOK || json.NewDecoder(r.Body).Decode(&ho) != nil {
		t.Fatalf("handoff: %d", r.StatusCode)
	}
	sjar, _ := cookiejar.New(nil)
	secure := &http.Client{Jar: sjar, Timeout: 15 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:443")
		},
	}}
	const meet = "https://" + certTestDomain
	if r := post(secure, meet+"/install/api/redeem", meet, `{"handoff":"`+ho.Handoff+`"}`); r.StatusCode != http.StatusNoContent {
		t.Fatalf("redeem: %d", r.StatusCode)
	}
	r, err = secure.Get(meet + "/install/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Secure bool `json:"secure"`
	}
	if r.StatusCode != http.StatusOK || json.NewDecoder(r.Body).Decode(&st) != nil || !st.Secure {
		t.Fatalf("secure state: %d", r.StatusCode)
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || !strings.Contains(fmt.Sprint(r.TLS.PeerCertificates[0].DNSNames), "turn.linx.test") {
		t.Errorf("served certificate: %+v", r.TLS)
	}
	if r, _ := plain.Get(base + "/install/api/state"); r == nil || r.StatusCode != http.StatusNotFound {
		t.Error("the plain page's session still works after the move")
	}
}

func waitUntil(t *testing.T, ctx context.Context, cond func() bool) {
	t.Helper()
	for !cond() {
		select {
		case <-ctx.Done():
			t.Fatal("timed out")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// dockerCertifier is linx setup's certifier (cmd/linx webCert) pointed at
// the test's stack instead of /etc/linx.
type dockerCertifier struct {
	c   Config
	dc  []string
	dns dnscheck.Resolver
}

func (d *dockerCertifier) Plan(_ context.Context, _ install.Answers, f install.Facts) (install.CertView, error) {
	return CertView(d.c, LAN{}, f), nil
}
func (d *dockerCertifier) Prepare(context.Context, install.CertView) error { return nil }
func (d *dockerCertifier) Lookup(ctx context.Context, name string) ([]string, error) {
	return d.dns.LookupA(ctx, name)
}
func (d *dockerCertifier) Obtain(ctx context.Context, staging bool) error {
	mode := "real"
	if staging {
		mode = "staging"
	}
	args := append(append([]string{}, d.dc...), "run", "--rm", "--no-TTY", "certd", "-bootstrap", mode)
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if found, res := certs.ParseBootstrapResult(out); found {
		return res
	}
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}
func (d *dockerCertifier) SaveToken(context.Context, install.CertView, string) (string, error) {
	return "", nil
}
func (d *dockerCertifier) Records(context.Context) error         { return nil }
func (d *dockerCertifier) ObtainWithToken(context.Context) error { return nil }

// firstPageTLS makes the first page's certificate as linx setup does, in a
// folder the container's user can read, and a client pool trusting exactly
// it (what the owner does by checking the fingerprint).
func firstPageTLS(t *testing.T, addr string) (dir string, pool *x509.CertPool) {
	t.Helper()
	dir = t.TempDir()
	certPEM, keyPEM, err := install.NewFirstPageCert([]netip.Addr{netip.MustParseAddr(addr)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"cert.pem": certPEM, "key.pem": keyPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return dir, pool
}
