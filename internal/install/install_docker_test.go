package install

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/ops"
)

// TestInstallModeDocker runs the real control-plane image in install mode
// from the real deploy/compose/install.yaml (make image
// SERVICE=control-plane first), with this package's Host on the other end
// of docker exec install-bridge, and goes through the plain page as a
// browser would: only the link answers, it's claimed once, the page and its
// files are served, and the answers reach the host. Needs Docker: make
// test-docker.
func TestInstallModeDocker(t *testing.T) {
	if os.Getenv("LINX_DOCKER_TESTS") != "1" {
		t.Skip("set LINX_DOCKER_TESTS=1 (make test-docker)")
	}
	image := os.Getenv("LINX_CONTROL_PLANE_IMAGE")
	if image == "" {
		image = "linx-control-plane:dev"
	}
	if exec.Command("docker", "image", "inspect", image).Run() != nil {
		t.Skipf("no %s image: make image SERVICE=control-plane", image)
	}
	if out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name=^linx-control-plane$").Output(); len(strings.TrimSpace(string(out))) > 0 {
		t.Skip("a linx-control-plane container already exists (another suite running?)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	// install.yaml names images <prefix>/linx-control-plane:<version>.
	run("tag", image, "linxinstalltest/linx-control-plane:dev")
	compose := []string{"compose", "--project-name", "linxinstalltest", "--file", "../../deploy/compose/install.yaml"}
	tlsDir, pool := firstPageTLS(t, "127.0.0.1")
	env := append(os.Environ(), "LINX_IMAGE_PREFIX=linxinstalltest", "LINX_VERSION=dev", "LINX_INSTALL_ADDRESS=127.0.0.1", "LINX_INSTALL_TLS_DIR="+tlsDir)
	up := exec.CommandContext(ctx, "docker", append(compose, "up", "--detach", "--wait")...)
	up.Env = env
	t.Cleanup(func() {
		down := exec.Command("docker", append(compose, "down")...)
		down.Env = env
		_ = down.Run()
		_ = exec.Command("docker", "image", "rm", "linxinstalltest/linx-control-plane:dev").Run()
	})
	if out, err := up.CombinedOutput(); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}

	var accepted Answers
	h := &Host{
		Path:  filepath.Join(t.TempDir(), "state.json"),
		Facts: func(context.Context) Facts { return Facts{Where: WhereRented, PublicAddress: "203.0.113.5"} },
		Dial:  func(ctx context.Context) (io.ReadWriteCloser, error) { return ops.DialBridge(ctx, "install-bridge") },
		Check: func(_ context.Context, a Answers) (string, []FieldError, error) {
			accepted = a
			return "Domain: " + a.Domain, nil, nil
		},
		Retry: 200 * time.Millisecond,
	}
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}
	go h.Run(ctx)

	const base = "https://127.0.0.1:6464"
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	get := func(path, cookie string) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, "GET", base+path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp
	}
	if r := get("/", ""); r.StatusCode != http.StatusNotFound {
		t.Errorf("GET / without the link: %d", r.StatusCode)
	}

	// The claim waits for the bridge: retry while install mode says it's
	// still starting.
	var resp *http.Response
	for range 60 {
		resp = get("/install/"+h.State().Secret, "")
		if resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusTooManyRequests {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("claim: %d", resp.StatusCode)
	}
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			cookie = c.Value
		}
	}
	if cookie == "" || h.State().View.SessionHash != Hash(cookie) {
		t.Fatalf("session not recorded on the host: %+v", h.State().View)
	}

	page := get("/install", cookie)
	body, _ := io.ReadAll(page.Body)
	script := regexp.MustCompile(`src="(/assets/[^"]+\.js)"`).FindSubmatch(body)
	if page.StatusCode != http.StatusOK || script == nil {
		t.Fatalf("the page: %d %s", page.StatusCode, body)
	}
	if r := get(string(script[1]), cookie); r.StatusCode != http.StatusOK {
		t.Errorf("its script: %d", r.StatusCode)
	}
	if r := get(string(script[1]), ""); r.StatusCode != http.StatusNotFound {
		t.Errorf("its script without the session: %d", r.StatusCode)
	}

	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/install/api/check",
		strings.NewReader(`{"where":"rented","front_door":"linx-443","domain":"example.com","name":"Owner","email":"o@example.com","admin_email":"o@example.com","agreed_to_terms":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", base)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	r, err := client.Do(req)
	if err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("check: %v %v", err, r)
	}
	if accepted.Domain != "example.com" || h.State().View.Accepted == nil {
		t.Errorf("answers didn't reach the host: %+v", accepted)
	}
}

// firstPageTLS makes the first page's certificate as linx setup does, in a
// folder the container's user can read, and a client pool trusting exactly
// it (what the owner does by checking the fingerprint).
func firstPageTLS(t *testing.T, addr string) (dir string, pool *x509.CertPool) {
	t.Helper()
	dir = t.TempDir()
	certPEM, keyPEM, err := NewFirstPageCert([]netip.Addr{netip.MustParseAddr(addr)}, time.Now())
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
