package browsertest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/deploy/compose"
	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/installer"
)

const (
	project = "linx-browser-test"
	domain  = "linx.test"
	// Names this test gives what compose.yaml names for a real install, so
	// it never touches one's volumes or networks. (Container names are
	// kept: services find each other by them, so this test can't run on a
	// machine where Linx itself is running.)
	caVolume    = "linx-browser-test-ca"
	certsVolume = "linx-browser-test-certs"
	netPrefix   = "linx-browser-test-"
	// linx-public's subnet, pinned so Asterisk's phone networks can name it
	// (the SIPp softphone connects from there).
	publicSubnet = "172.29.201.0/24"
	// Images: built locally (make image SERVICE=...), tagged under this
	// prefix for compose.yaml's ${LINX_IMAGE_PREFIX}/linx-<service>:${LINX_VERSION}.
	imagePrefix = "linx-browser-test"
	sippImage   = "linx-sipp-test:dev"
	// mcr.microsoft.com/playwright:v1.63.0-noble (matches web/package.json's @playwright/test)
	playwrightImage = "mcr.microsoft.com/playwright:v1.63.0-noble@sha256:eff16c30e6f3f4af0a03fa4b706120d5e9b0891c344a27d64559aff5900a4a27"
	// postgres:18, already pinned by compose.yaml: used here only for its
	// shell, to copy the test certificate into the certs volume.
	shellImage = "postgres:18@sha256:86c951e05bf56c93d95d397747fb8820ac76cc3bedb78f43abd83eedbe3666ae"
)

// override is what the test changes in compose.yaml: no certd (the test
// deploys its own certificate), the names browsers use for the control
// plane and coturn, relay addresses on coturn's own ports (the front door
// that maps 443 to them comes in step 6), and the control plane's HTTPS
// port on this machine's loopback, for the test's own API calls.
const override = `services:
  certd:
    profiles: [disabled]
  control-plane:
    environment:
      LINX_TURN_URLS: "turn:turn.linx.test:3478?transport=udp,turns:turn.linx.test:5349?transport=tcp"
    ports: ["127.0.0.1::8443"]
    networks:
      linx-public:
        aliases: [meet.linx.test]
  coturn:
    networks:
      linx-public:
        aliases: [turn.linx.test]
volumes:
  certs:
    name: ` + certsVolume + `
    external: true
networks:
  linx-public:
    ipam:
      config:
        - subnet: ` + publicSubnet + `
`

type harness struct {
	t       *testing.T
	ctx     context.Context
	dir     string
	compose []string
	api     string
	client  *http.Client
	key     string
}

func (h *harness) docker(args ...string) string {
	h.t.Helper()
	cmd := exec.CommandContext(h.ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("docker %s: %v\n%s", strings.Join(args[:min(4, len(args))], " "), err, tail(string(out), 40))
	}
	return strings.TrimSpace(string(out))
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func imageExists(name string) bool {
	return exec.Command("docker", "image", "inspect", name).Run() == nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// TestBrowserCallsDocker is the browser call suite.
func TestBrowserCallsDocker(t *testing.T) {
	// Only on its own (make test-browser): it runs the whole stack under
	// compose.yaml's container names, which make test-docker's CA test
	// uses too.
	if os.Getenv("LINX_BROWSER_TESTS") != "1" {
		t.Skip("set LINX_BROWSER_TESTS=1 (make test-browser)")
	}
	images := map[string]string{
		"control-plane": envOr("LINX_CONTROL_PLANE_IMAGE", "linx-control-plane:dev"),
		"asterisk":      envOr("LINX_ASTERISK_IMAGE", "linx-asterisk:dev"),
		"coturn":        envOr("LINX_COTURN_IMAGE", "linx-coturn:dev"),
	}
	for svc, img := range images {
		if !imageExists(img) {
			t.Skipf("no %s image %s: run make image SERVICE=%s", svc, img, svc)
		}
	}
	if _, err := os.Stat("../../web/node_modules/@playwright/test"); err != nil {
		t.Skip("web dependencies aren't installed: run make setup-dev")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	h := &harness{t: t, ctx: ctx, dir: t.TempDir()}
	for svc, img := range images {
		h.docker("tag", img, imagePrefix+"/linx-"+svc+":test")
	}
	if !imageExists(sippImage) {
		h.docker("buildx", "build", "-q", "-f", "../../deploy/docker/sipp-test.Dockerfile", "--load", "-t", sippImage, "../..")
	}
	h.start()
	h.provision(t)
}

func (h *harness) start() {
	t := h.t
	secrets := filepath.Join(h.dir, "secrets")
	for _, d := range []string{secrets, filepath.Join(h.dir, "ca-backup")} {
		if err := os.Mkdir(d, 0o777); err != nil {
			t.Fatal(err)
		}
		os.Chmod(d, 0o777) // written by the step-ca container's own user
	}
	cleanup := func() {
		// By the project's compose label, so a stack left by an earlier run
		// (LINX_KEEP_STACK, or an interrupted one) goes too.
		label := "label=com.docker.compose.project=" + project
		for _, kind := range []string{"container", "network", "volume"} {
			ids, _ := exec.Command("docker", kind, "ls", "--all", "--quiet", "--filter", label).Output()
			if kind != "container" {
				ids, _ = exec.Command("docker", kind, "ls", "--quiet", "--filter", label).Output()
			}
			if f := strings.Fields(string(ids)); len(f) > 0 {
				args := append([]string{kind, "rm", "--force"}, f...)
				if kind == "network" {
					args = append([]string{kind, "rm"}, f...)
				}
				exec.Command("docker", args...).Run()
			}
		}
		exec.Command("docker", "rm", "--force", "linx-browser-test-sipp", "linx-browser-test-playwright", "linx-browser-test-tcpdump").Run()
		exec.Command("docker", "volume", "rm", "--force", caVolume, certsVolume).Run()
	}
	h.compose = []string{"compose", "--project-name", project, "--project-directory", h.dir,
		"--file", filepath.Join(h.dir, "compose.yaml"), "--file", filepath.Join(h.dir, "override.yaml")}
	cleanup()
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("LINX_KEEP_STACK") == "1" {
			t.Log("LINX_KEEP_STACK=1: leaving the stack running for inspection")
			return
		}
		if t.Failed() {
			for _, c := range []string{"linx-control-plane", "linx-asterisk", "linx-coturn"} {
				out, _ := exec.Command("docker", "logs", "--tail", envOr("LINX_LOG_LINES", "150"), c).CombinedOutput()
				t.Logf("--- %s:\n%s", c, out)
			}
		}
		cleanup()
	})

	// Secrets, as linx setup makes them, in the test's own directory.
	var files []installer.Step
	pki := installer.PKIPlan(false)
	files = append(files, pki.Plan...)
	cfg := installer.Config{}
	cfg.Domain.Name = domain
	files = append(files, installer.StackPlan(cfg, "unused", "test", installer.LAN{}).Plan...)
	var boot *installer.Cmd
	for _, st := range files {
		if st.File != nil && strings.HasPrefix(st.File.Path, installer.SecretsDir+"/") {
			if err := os.WriteFile(filepath.Join(secrets, filepath.Base(st.File.Path)), st.File.Data, 0o444); err != nil {
				t.Fatal(err)
			}
		}
		if st.Cmd != nil && st.Cmd.Name == "docker" && boot == nil && slicesContainsPrefix(st.Cmd.Args, installer.StepCAVolume+":") {
			boot = st.Cmd
		}
	}
	if boot == nil {
		t.Fatal("no CA bootstrap step in PKIPlan")
	}

	// The internal CA, bootstrapped exactly as setup does, in the test's volume.
	args := make([]string, len(boot.Args))
	for i, a := range boot.Args {
		a = strings.Replace(a, installer.SecretsDir+"/", secrets+"/", 1)
		a = strings.Replace(a, installer.CABackupDir+":", filepath.Join(h.dir, "ca-backup")+":", 1)
		args[i] = strings.Replace(a, installer.StepCAVolume+":", caVolume+":", 1)
	}
	cmd := exec.CommandContext(h.ctx, "docker", args...)
	cmd.Env = append(os.Environ(), boot.Env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("CA bootstrap: %v\n%s", err, tail(string(out), 20))
	}

	// The public certificate certd would get, from a test CA of our own.
	h.deployCertificate()

	// compose.yaml as installed, with the test's volume and network names.
	y := string(compose.File)
	y = strings.ReplaceAll(y, " name: "+installer.StepCAVolume+"\n", " name: "+caVolume+"\n")
	for _, n := range []string{"private", "egress", "public", "sipws", "media"} {
		y = strings.ReplaceAll(y, " name: linx-"+n+"\n", " name: "+netPrefix+n+"\n")
	}
	must(t, os.WriteFile(filepath.Join(h.dir, "compose.yaml"), []byte(y), 0o644))
	must(t, os.WriteFile(filepath.Join(h.dir, "override.yaml"), []byte(override), 0o644))
	env := fmt.Sprintf("LINX_IMAGE_PREFIX=%s\nLINX_VERSION=test\nLINX_DOMAIN=%s\nLINX_DNS_PROVIDER=cloudflare\n"+
		"LINX_SIP_ADDRESS=127.0.0.1\nLINX_SIP_NETWORKS=%s\n", imagePrefix, domain, publicSubnet)
	must(t, os.WriteFile(filepath.Join(h.dir, ".env"), []byte(env), 0o644))

	h.docker(append(h.compose, "up", "--detach", "--wait", "--wait-timeout", "180",
		"postgres", "step-ca", "control-plane", "asterisk", "coturn")...)

	port := h.docker("port", "linx-control-plane", "8443/tcp")
	port = port[strings.LastIndex(port, ":")+1:]
	if i := strings.IndexByte(port, '\n'); i >= 0 {
		port = port[:i]
	}
	h.api = "https://127.0.0.1:" + port
}

func slicesContainsPrefix(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// deployCertificate makes a test CA and a *.linx.test certificate from it,
// deploys it into the certs volume in certd's layout, and keeps the CA and
// the certificate's key hash for the browsers and SIPp.
func (h *harness) deployCertificate() {
	t := h.t
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now()
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Linx browser test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	must(t, err)
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "*." + domain},
		DNSNames: []string{"*." + domain, domain, "asterisk"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &key.PublicKey, caKey)
	must(t, err)
	leaf, _ := x509.ParseCertificate(leafDER)
	kd, _ := x509.MarshalECPrivateKey(key)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), caPEM...)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})

	certsDir := filepath.Join(h.dir, "certs")
	must(t, os.Mkdir(certsDir, 0o755))
	must(t, certs.Store{Dir: certsDir}.Deploy(chain, keyPEM, certs.Meta{Names: leafTmpl.DNSNames, Issuer: "test", NotAfter: leafTmpl.NotAfter, IssuedAt: now}))
	h.docker("volume", "create", certsVolume)
	h.docker("run", "--rm", "--volume", certsVolume+":/certs", "--volume", certsDir+":/src:ro", "--entrypoint", "sh", shellImage,
		"-c", "cp -a /src/. /certs/ && chown -R 65532:65532 /certs && chmod -R g+rX,o-rwx /certs && chmod 0755 /certs")

	// For SIPp (TLS to Asterisk) and the test's own API calls.
	tlsDir := filepath.Join(h.dir, "tls")
	must(t, os.Mkdir(tlsDir, 0o755))
	must(t, os.WriteFile(filepath.Join(tlsDir, "root_ca.crt"), caPEM, 0o644))
	must(t, os.WriteFile(filepath.Join(tlsDir, "client.pem"), chain, 0o644))
	must(t, os.WriteFile(filepath.Join(tlsDir, "client.key"), keyPEM, 0o644))
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	h.client = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "meet." + domain, MinVersion: tls.VersionTLS12}}}
	// Chromium trusts exactly this certificate's key (pinned, not "ignore
	// errors"): the browsers' only way to accept the test CA without
	// installing it system-wide.
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	os.Setenv("LINX_TEST_SPKI", base64.StdEncoding.EncodeToString(sum[:]))
}

// call makes an API request as the test's admin key.
func (h *harness) call(method, path string, body any, out any) {
	h.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequestWithContext(h.ctx, method, h.api+path, r)
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		h.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		must(h.t, json.Unmarshal(b, out))
	}
}

var (
	keyRe  = regexp.MustCompile(`linx_[A-Za-z0-9_-]{20,}`)
	linkRe = regexp.MustCompile(`https://meet\.` + regexp.QuoteMeta(domain) + `/setup/([A-Za-z0-9_-]+)`)
)

func (h *harness) provision(t *testing.T) {
	out := h.docker("exec", "linx-control-plane", "/usr/local/bin/service", "api-key", "create",
		"--name", "browser test", "--role", "admin", "--scopes", "all,devices:write")
	h.key = keyRe.FindString(out)
	if h.key == "" {
		t.Fatalf("no API key in:\n%s", out)
	}
	var ext struct {
		ID string `json:"id"`
	}
	for _, n := range []string{"101", "102"} {
		h.call("POST", "/api/v1/extensions", map[string]string{"number": n, "display_name": "Browser " + n}, nil)
	}
	h.call("POST", "/api/v1/extensions", map[string]string{"number": "103", "display_name": "Desk softphone"}, &ext)
	var dev struct {
		Device struct {
			SIPUsername string `json:"sip_username"`
		} `json:"device"`
		Password string `json:"password"`
	}
	h.call("POST", "/api/v1/extensions/"+ext.ID+"/devices", map[string]string{"name": "SIPp"}, &dev)

	person := func(email, name, ext string) string {
		out := h.docker("exec", "linx-control-plane", "/usr/local/bin/service", "user", "create",
			"--email", email, "--name", name, "--role", "user", "--extension", ext)
		m := linkRe.FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no set-password link in:\n%s", out)
		}
		return m[1]
	}
	aisha := person("aisha@linx.test", "Aisha Rahman", "101")
	omar := person("omar@linx.test", "Omar Khalil", "102")

	// The softphone on 103 signs in over TLS from linx-public and answers
	// whatever rings it.
	testdata, _ := filepath.Abs("../calltest/testdata")
	h.docker("run", "--detach", "--name", "linx-browser-test-sipp", "--network", netPrefix+"public",
		"--volume", testdata+":/scenarios:ro", "--volume", filepath.Join(h.dir, "tls")+":/tls:ro",
		sippImage, "asterisk:5061", "-t", "l1",
		"-tls_cert", "/tls/client.pem", "-tls_key", "/tls/client.key", "-tls_ca", "/tls/root_ca.crt",
		"-sf", "/scenarios/register.xml", "-oocsf", "/scenarios/answer.xml", "-m", "1", "-nostdin", "-d", "600000",
		"-set", "user", dev.Device.SIPUsername, "-au", dev.Device.SIPUsername, "-ap", dev.Password)

	// LINX_SIP_DEBUG=1 logs every SIP message Asterisk sees (shown on failure).
	if os.Getenv("LINX_SIP_DEBUG") == "1" {
		h.docker("exec", "linx-asterisk", "asterisk", "-rx", "pjsip set logger on")
		// And the UDP packets Asterisk sends and receives (a debugging aid:
		// needs the netshoot image, pulled on first use).
		h.docker("run", "--detach", "--name", "linx-browser-test-tcpdump", "--network", "container:linx-asterisk",
			"--cap-add", "NET_RAW", "--cap-add", "NET_ADMIN", "nicolaka/netshoot", "tcpdump", "-n", "-l", "-i", "any", "udp and not port 53")
		t.Cleanup(func() {
			if t.Failed() {
				out, _ := exec.Command("docker", "logs", "--tail", "80", "linx-browser-test-tcpdump").CombinedOutput()
				t.Logf("--- Asterisk's UDP:\n%s", out)
			}
			exec.Command("docker", "rm", "--force", "linx-browser-test-tcpdump").Run()
		})
	}

	// The browsers: Playwright's own image on linx-public, running
	// web/e2e/calls.spec.ts against https://meet.linx.test:8443.
	web, _ := filepath.Abs("../../web")
	cmd := exec.CommandContext(h.ctx, "docker", "run", "--rm", "--name", "linx-browser-test-playwright",
		"--network", netPrefix+"public", "--ipc", "host", "--init",
		"--volume", web+":/web", "--workdir", "/web",
		"--env", "LINX_BASE_URL=https://meet."+domain+":8443",
		"--env", "LINX_TEST_SPKI="+os.Getenv("LINX_TEST_SPKI"),
		"--env", "LINX_SETUP_A="+aisha, "--env", "LINX_SETUP_B="+omar,
		"--env", "CI=1",
		playwrightImage, "npx", "--no-install", "playwright", "test", "--config", "e2e/calls.config.ts")
	outB, err := cmd.CombinedOutput()
	t.Logf("browsers:\n%s", tail(string(outB), 60))
	if err != nil {
		t.Fatalf("browser call suite failed: %v", err)
	}
}
