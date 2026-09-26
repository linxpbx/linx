package browsertest

import (
	"bufio"
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
	"net/netip"
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

// Front doors the suite runs behind (docs/WEB.md §3), each with its own
// stack: Pangolin's Traefik with the file linx setup generates, and Linx's
// own port 443 router (linx-sni). LINX_FRONT_DOORS picks some.
var frontDoors = []string{installer.FrontDoorPangolin, installer.FrontDoorNginx, installer.FrontDoorHTTPProxy, installer.FrontDoorLinx443}

// traefikImage is the Traefik Pangolin runs (its installer's traefik:v3.7).
const traefikImage = "traefik:v3.7.13@sha256:24841fe2de7304c149343d877d2923b4c8800a38ba015dea9174c23b20e344a0"

// The stand-in proxies' fixed addresses (LINX_TRUSTED_PROXIES).
const (
	pangolinAddress = "172.29.201.30"
	nginxAddress    = "172.29.201.31"
	caddyAddress    = "172.29.201.32"
)

// nginx:1.28.3-alpine (with the stream and ssl_preread modules) and
// caddy:2.11.4-alpine: the owner's own proxies, stood in for.
const (
	nginxImage = "nginx:1.28-alpine@sha256:a8b39bd9cf0f83869a2162827a0caf6137ddf759d50a171451b335cecc87d236"
	caddyImage = "caddy:2.11.4-alpine@sha256:6aeddd44c3078b0f9a35206472a11420648a79c184603ef95957d0a20044cb2b"
)

// relayURLs: TURN over TLS on 443 through the front door, by name (on
// coturn's own 5349 behind an HTTP-only proxy); UDP straight to coturn (at
// home, the router's UDP 443 forward).
func relayURLs(door string) string {
	if door == installer.FrontDoorHTTPProxy {
		return "turn:coturn:3478?transport=udp,turns:turn.linx.test:5349?transport=tcp"
	}
	return "turn:coturn:3478?transport=udp,turns:turn.linx.test:443?transport=tcp"
}

// override is what the test changes in compose.yaml: no certd (the test
// deploys its own certificate), relay addresses, the control plane's HTTPS
// port on this machine's loopback for the test's own API calls, and the
// front door answering for the public names.
func override(door string) string {
	s := `services:
  certd:
    profiles: [disabled]
  control-plane:
    environment:
      LINX_TURN_URLS: "` + relayURLs(door) + `"
    ports: ["127.0.0.1::8443"]
`
	switch door {
	case installer.FrontDoorPangolin:
		s += `  pangolin-traefik:
    image: ` + traefikImage + `
    command: [--configFile=/etc/traefik/traefik_config.yml]
    volumes: ["./traefik:/etc/traefik:ro"]
    networks:
      linx-public:
        ipv4_address: ` + pangolinAddress + `
        aliases: [meet.linx.test, api.linx.test, turn.linx.test]
`
	case installer.FrontDoorNginx:
		s += `  owners-nginx:
    image: ` + nginxImage + `
    volumes: ["./nginx.conf:/etc/nginx/nginx.conf:ro"]
    # It looks the containers up when it starts (a real install names IPs).
    depends_on: [control-plane, coturn]
    networks:
      linx-public:
        ipv4_address: ` + nginxAddress + `
        aliases: [meet.linx.test, api.linx.test, turn.linx.test]
`
	case installer.FrontDoorHTTPProxy:
		s += `  owners-caddy:
    image: ` + caddyImage + `
    volumes:
      - "./Caddyfile:/etc/caddy/Caddyfile:ro"
      - "./tls:/tls:ro"
    networks:
      linx-public:
        ipv4_address: ` + caddyAddress + `
        aliases: [meet.linx.test, api.linx.test]
  coturn:
    networks:
      linx-public:
        aliases: [turn.linx.test]
`
	case installer.FrontDoorLinx443:
		s += `  sni:
    networks:
      linx-public:
        aliases: [linx-sni, meet.linx.test, api.linx.test, turn.linx.test]
`
	}
	return s + `volumes:
  certs:
    name: ` + certsVolume + `
    external: true
networks:
  linx-public:
    ipam:
      config:
        - subnet: ` + publicSubnet + `
`
}

// pangolinStatic is Pangolin's installer traefik_config.yml, minus what a
// test can't have (ACME, its dashboard, the badger plugin) and minus HTTP/3
// (the Pangolin steps say to turn it off). Its global insecureSkipVerify is
// kept on purpose: Linx's passthrough mustn't depend on it.
const pangolinStatic = `providers:
  file:
    filename: "/etc/traefik/dynamic_config.yml"
entryPoints:
  web:
    address: ":80"
  websecure:
    address: ":443"
    transport:
      respondingTimeouts:
        readTimeout: "30m"
serversTransport:
  insecureSkipVerify: true
log:
  level: "INFO"
`

// pangolinDynamic stands in for Pangolin's own dynamic_config.yml (an HTTP
// router of its own on websecure), with Linx's generated block appended as
// the owner does.
func pangolinDynamic(linxBlock string) string {
	return `http:
  routers:
    pangolin-dashboard:
      rule: "Host(` + "`pangolin.linx.test`" + `)"
      service: pangolin-dashboard
      entryPoints: [websecure]
      tls: {}
  services:
    pangolin-dashboard:
      loadBalancer:
        servers:
          - url: "http://127.0.0.1:3002"
` + linxBlock
}

type harness struct {
	door    string
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
	if v := os.Getenv("LINX_FRONT_DOORS"); v != "" {
		frontDoors = strings.Split(v, ",")
	}
	if !imageExists(sippImage) {
		out, err := exec.Command("docker", "buildx", "build", "-q", "-f", "../../deploy/docker/sipp-test.Dockerfile", "--load", "-t", sippImage, "../..").CombinedOutput()
		if err != nil {
			t.Fatalf("building SIPp: %v\n%s", err, tail(string(out), 20))
		}
	}
	for _, door := range frontDoors {
		t.Run(door, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			h := &harness{door: door, t: t, ctx: ctx, dir: t.TempDir()}
			for svc, img := range images {
				h.docker("tag", img, imagePrefix+"/linx-"+svc+":test")
			}
			h.start()
			h.provision(t)
		})
	}
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
		exec.Command("docker", "rm", "--force", "linx-browser-test-sipp", "linx-browser-test-sipp-call", "linx-browser-test-playwright", "linx-browser-test-tcpdump").Run()
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
	must(t, os.WriteFile(filepath.Join(h.dir, "override.yaml"), []byte(override(h.door)), 0o644))
	env := fmt.Sprintf("LINX_IMAGE_PREFIX=%s\nLINX_VERSION=test\nLINX_DOMAIN=%s\nLINX_DNS_PROVIDER=cloudflare\n"+
		"LINX_SIP_ADDRESS=127.0.0.1\nLINX_SIP_NETWORKS=%s\n", imagePrefix, domain, publicSubnet)
	services := []string{"postgres", "step-ca", "control-plane", "asterisk", "coturn"}
	linx := netip.MustParseAddr("192.0.2.1") // replaced by container names below
	switch h.door {
	case installer.FrontDoorPangolin:
		// Exactly what setup generates for the owner, pointed at the
		// containers instead of a LAN address (at home both sit behind
		// that one address).
		block := string(installer.PangolinTraefik(domain, linx))
		block = strings.NewReplacer("192.0.2.1:8443", "control-plane:8443", "192.0.2.1:5349", "coturn:5349").Replace(block)
		tdir := filepath.Join(h.dir, "traefik")
		must(t, os.Mkdir(tdir, 0o755))
		must(t, os.WriteFile(filepath.Join(tdir, "traefik_config.yml"), []byte(pangolinStatic), 0o644))
		must(t, os.WriteFile(filepath.Join(tdir, "dynamic_config.yml"), []byte(pangolinDynamic(block)), 0o644))
		env += "LINX_TRUSTED_PROXIES=" + pangolinAddress + "\n"
		services = append(services, "pangolin-traefik")
	case installer.FrontDoorNginx:
		// The generated stream block, pointed at the containers (so nginx
		// needs Docker's DNS for its map), in a minimal nginx.conf.
		block := string(installer.NginxStream(domain, linx))
		block = strings.NewReplacer("192.0.2.1:8443", "control-plane:8443", "192.0.2.1:5349", "coturn:5349",
			"stream {\n", "stream {\n    resolver 127.0.0.11;\n").Replace(block)
		must(t, os.WriteFile(filepath.Join(h.dir, "nginx.conf"), []byte("events {}\n"+block), 0o644))
		env += "LINX_TRUSTED_PROXIES=" + nginxAddress + "\n"
		services = append(services, "owners-nginx")
	case installer.FrontDoorHTTPProxy:
		// The generated site block, pointed at the container, trusting the
		// test's CA for Linx's certificate (a real install has a trusted
		// one), and serving the browsers the test certificate.
		block := string(installer.CaddyConfig(domain, linx))
		block = strings.NewReplacer("https://192.0.2.1:8443", "https://control-plane:8443",
			"tls_server_name meet.linx.test\n", "tls_server_name meet.linx.test\n\t\t\ttls_trust_pool file /tls/root_ca.crt\n",
			"meet.linx.test, api.linx.test {\n", "meet.linx.test, api.linx.test {\n\ttls /tls/client.pem /tls/client.key\n").Replace(block)
		must(t, os.WriteFile(filepath.Join(h.dir, "Caddyfile"), []byte("{\n\tauto_https disable_redirects\n}\n"+block), 0o644))
		env += "LINX_TRUSTED_PROXIES=" + caddyAddress + "\nLINX_PROXY_PROTOCOL=false\n"
		services = append(services, "owners-caddy")
	case installer.FrontDoorLinx443:
		must(t, os.WriteFile(filepath.Join(h.dir, "haproxy.cfg"), installer.HAProxyConfig(domain), 0o644))
		env += "LINX_TRUSTED_PROXIES=" + installer.SNIContainerName + "\nCOMPOSE_PROFILES=" + installer.FrontDoorLinx443 + "\n"
		services = append(services, "sni")
	}
	must(t, os.WriteFile(filepath.Join(h.dir, ".env"), []byte(env), 0o644))

	h.docker(append(append(h.compose, "up", "--detach", "--wait", "--wait-timeout", "180"), services...)...)

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
		"--name", "browser test", "--role", "admin", "--scopes", "all,devices:write,routing:write,trunks:write")
	h.key = keyRe.FindString(out)
	if h.key == "" {
		t.Fatalf("no API key in:\n%s", out)
	}
	// A permission level letting 101 and 102 call mobile numbers: the
	// Phase 1 exit test dials one, out through the provider trunk below.
	var level struct {
		ID string `json:"id"`
	}
	h.call("POST", "/api/v1/call-permission-levels", map[string]any{"name": "Browser test", "allowed_categories": []string{"mobile"}}, &level)

	var ext struct {
		ID string `json:"id"`
	}
	for _, n := range []string{"101", "102"} {
		h.call("POST", "/api/v1/extensions", map[string]any{"number": n, "display_name": "Browser " + n, "call_permission_level_id": level.ID}, nil)
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
	// docker run arguments for a SIPp container signed in as 103's softphone.
	sipp := func(name string, args ...string) []string {
		base := []string{"run", "--detach", "--name", name, "--network", netPrefix + "public",
			"--volume", testdata + ":/scenarios:ro", "--volume", filepath.Join(h.dir, "tls") + ":/tls:ro",
			sippImage, "asterisk:5061", "-t", "l1",
			"-tls_cert", "/tls/client.pem", "-tls_key", "/tls/client.key", "-tls_ca", "/tls/root_ca.crt",
			"-m", "1", "-nostdin",
			"-set", "user", dev.Device.SIPUsername, "-au", dev.Device.SIPUsername, "-ap", dev.Password}
		return append(base, args...)
	}
	h.docker(sipp("linx-browser-test-sipp", "-sf", "/scenarios/register.xml", "-oocsf", "/scenarios/answer.xml", "-d", "600000")...)

	h.providerTrunk(t, testdata)

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
	// web/e2e/calls.spec.ts against https://meet.linx.test, through the
	// front door on 443.
	web, _ := filepath.Abs("../../web")
	cmd := exec.CommandContext(h.ctx, "docker", "run", "--rm", "--name", "linx-browser-test-playwright",
		"--network", netPrefix+"public", "--ipc", "host", "--init",
		"--volume", web+":/web", "--workdir", "/web",
		"--env", "LINX_BASE_URL=https://meet."+domain,
		"--env", "LINX_TEST_SPKI="+os.Getenv("LINX_TEST_SPKI"),
		"--env", "LINX_SETUP_A="+aisha, "--env", "LINX_SETUP_B="+omar,
		"--env", "CI=1",
		playwrightImage, "npx", "--no-install", "playwright", "test", "--config", "e2e/calls.config.ts")
	// The softphone calls a browser when the suite says it's ready for it
	// (it prints softphoneCallMarker): it dials 101, talks for 3 seconds
	// once answered and hangs up.
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the browsers: %v", err)
	}
	var outB strings.Builder
	calling := false
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			line := sc.Text()
			outB.WriteString(line + "\n")
			if !calling && strings.Contains(line, softphoneCallMarker) {
				calling = true
				// (Not h.docker: it can't fail the test from this goroutine.)
				out, err := exec.CommandContext(h.ctx, "docker",
					sipp("linx-browser-test-sipp-call", "-sf", "/scenarios/call.xml", "-s", "101", "-d", "3000")...).CombinedOutput()
				if err != nil {
					outB.WriteString(fmt.Sprintf("starting the softphone's call: %v\n%s\n", err, out))
				}
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	err := cmd.Wait()
	pw.Close()
	<-scanned
	t.Logf("browsers:\n%s", tail(outB.String(), 60))
	if err != nil {
		t.Fatalf("browser call suite failed: %v", err)
	}
	if !calling {
		t.Fatal("the browser suite never asked for the softphone's call")
	}
	// The softphone's side of that call: answered, then hung up cleanly.
	code := h.docker("wait", "linx-browser-test-sipp-call")
	if code != "0" {
		out, _ := exec.Command("docker", "logs", "--tail", "40", "linx-browser-test-sipp-call").CombinedOutput()
		t.Fatalf("the softphone's call to a browser failed (SIPp exit %s):\n%s", code, out)
	}
}

// softphoneCallMarker is the line web/e2e/calls.spec.ts prints when a
// browser is waiting for the softphone to call it.
const softphoneCallMarker = "LINX-TEST: softphone, call 101 now"

// providerTrunkUser and providerTrunkPass: the login Linx registers to the
// provider trunk with.
const (
	providerTrunkUser = "browsertrunk"
	providerTrunkPass = "p;ss w0rd-x7"
	providerHost      = "provider." + domain
)

// providerTrunk sets up a phone-line provider (docs/TRUNKS.md), the same
// SIPp scenario the trunk call suite uses as a registration provider over
// TLS with SRTP: it reuses the *.linx.test certificate deployCertificate
// already made (its wildcard covers provider.linx.test too), so nothing
// new needs pinning by hand. This is the Phase 1 exit test's other leg: a
// browser with UDP blocked calls out through it and back.
func (h *harness) providerTrunk(t *testing.T, testdata string) {
	h.docker("run", "--detach", "--name", "linx-browser-test-provider", "--network", netPrefix+"public",
		"--network-alias", providerHost,
		"--volume", testdata+":/scenarios:ro", "--volume", filepath.Join(h.dir, "tls")+":/tls:ro",
		sippImage, "-t", "l1", "-p", "5061", "-sf", "/scenarios/provider.xml",
		"-tls_cert", "/tls/client.pem", "-tls_key", "/tls/client.key",
		"-nostdin", "-trace_logs", "-log_file", "/dev/stdout", "-trace_err", "-error_file", "/dev/stderr",
		"-set", "user", providerTrunkUser, "-set", "pass", providerTrunkPass, "-set", "did", "+97142000199",
		"-set", "proto", "RTP/SAVP", "-set", "crypto", "a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:y8r4kQ3zYt0Rvq2VJq0yJ3m0z2fX8sA1b5c6d7e8")
	t.Cleanup(func() { exec.Command("docker", "rm", "--force", "linx-browser-test-provider").Run() })

	rootCA, err := os.ReadFile(filepath.Join(h.dir, "tls", "root_ca.crt"))
	must(t, err)
	var tr struct {
		ID string `json:"id"`
	}
	h.call("POST", "/api/v1/trunks", map[string]any{
		"name": "Browser test provider", "kind": "registration", "host": providerHost,
		"cert_trust": "pinned", "pinned_certificate": string(rootCA),
		"username": providerTrunkUser, "password": providerTrunkPass, "caller_id_number": "+97142000100",
	}, &tr)
	h.call("PUT", "/api/v1/outbound-routing", map[string]any{"order": []string{tr.ID}}, nil)

	waitFor(t, h.ctx, 30*time.Second, func() bool {
		out, err := exec.CommandContext(h.ctx, "docker", "exec", "linx-asterisk", "asterisk", "-rx", "pjsip show registrations").CombinedOutput()
		return err == nil && strings.Contains(string(out), "Registered")
	}, func() string {
		out, _ := exec.CommandContext(h.ctx, "docker", "exec", "linx-asterisk", "asterisk", "-rx", "pjsip show registrations").CombinedOutput()
		logs, _ := exec.Command("docker", "logs", "--tail", "40", "linx-browser-test-provider").CombinedOutput()
		return fmt.Sprintf("registrations:\n%s\nprovider's log:\n%s", out, logs)
	})
}

// waitFor polls until check reports true or timeout passes, failing t with
// detail's output (evaluated only on failure) if it never does.
func waitFor(t *testing.T, ctx context.Context, timeout time.Duration, check func() bool, detail func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if check() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s:\n%s", timeout, detail())
		}
		select {
		case <-ctx.Done():
			t.Fatalf("cancelled:\n%s", detail())
		case <-time.After(200 * time.Millisecond):
		}
	}
}
