package stepca

import (
	"context"
	"crypto/x509"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/installer"
)

// startTestCA runs a throwaway step-ca (the image compose.yaml uses) with a
// linx-services JWK provisioner, published on a random localhost port. It
// returns the CA URL, the root certificate's path and the provisioner
// password. Callers skip unless LINX_DOCKER_TESTS=1.
func startTestCA(t *testing.T, name string) (caURL, rootPath string, password []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args[:min(3, len(args))], " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	volume := name + "-vol"
	cleanup := func() {
		exec.Command("docker", "rm", "--force", name).Run()
		exec.Command("docker", "volume", "rm", "--force", volume).Run()
	}
	cleanup()
	t.Cleanup(cleanup)

	const pw = "test-provisioner-password"
	docker("run", "--rm", "--volume", volume+":/home/step", "--entrypoint", "bash", installer.StepCAImage, "-c",
		`set -e; printf %s '`+pw+`' > /tmp/pw
step ca init --deployment-type standalone --name "Linx Test CA" --dns localhost --dns step-ca --address :9000 \
  --provisioner linx-services --password-file /tmp/pw --provisioner-password-file /tmp/pw >/dev/null
step ca provisioner update linx-services --x509-max-dur 24h --x509-default-dur 24h >/dev/null
printf %s '`+pw+`' > /home/step/secrets/password`)
	docker("run", "--detach", "--name", name, "--volume", volume+":/home/step", "--publish", "127.0.0.1::9000",
		"--entrypoint", "step-ca", installer.StepCAImage, "--password-file", "/home/step/secrets/password", "/home/step/config/ca.json")
	port := docker("port", name, "9000/tcp")
	port = port[strings.LastIndex(port, ":")+1:]

	dir := t.TempDir()
	rootPath = filepath.Join(dir, "root_ca.crt")
	if err := os.WriteFile(rootPath, []byte(docker("exec", name, "cat", "/home/step/certs/root_ca.crt")), 0o644); err != nil {
		t.Fatal(err)
	}
	caURL = "https://localhost:" + port
	roots, err := LoadRoots(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(caURL, ServicesProvisioner, nil, roots)
	for i := 0; ; i++ {
		var out map[string]any
		if err := c.do(ctx, "GET", "/health", nil, &out); err == nil {
			break
		} else if i > 60 {
			t.Fatalf("step-ca never became healthy: %v\n%s", err, docker("logs", name))
		}
		time.Sleep(500 * time.Millisecond)
	}
	return caURL, rootPath, []byte(pw)
}

func TestIssueDocker(t *testing.T) {
	if os.Getenv("LINX_DOCKER_TESTS") != "1" {
		t.Skip("set LINX_DOCKER_TESTS=1 (make test-docker)")
	}
	caURL, rootPath, pw := startTestCA(t, "linx-stepca-test")
	roots, err := LoadRoots(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	c := NewClient(caURL, ServicesProvisioner, pw, roots)
	cert, err := c.Issue(ctx, "control-plane", []string{"control-plane", "linx-control-plane"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(cert.Certificate) < 2 {
		t.Fatalf("want leaf + intermediate, got %d certificates", len(cert.Certificate))
	}
	inter := x509.NewCertPool()
	for _, der := range cert.Certificate[1:] {
		ic, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		inter.AddCert(ic)
	}
	if _, err := cert.Leaf.Verify(x509.VerifyOptions{DNSName: "linx-control-plane", Roots: roots, Intermediates: inter}); err != nil {
		t.Errorf("certificate doesn't verify against the root: %v", err)
	}
	if life := cert.Leaf.NotAfter.Sub(cert.Leaf.NotBefore); life > 25*time.Hour || life < 23*time.Hour {
		t.Errorf("lifetime %v, want about 24h", life)
	}

	// A second certificate reuses the decrypted provisioner key.
	if _, err := c.Issue(ctx, "control-plane", nil); err != nil {
		t.Errorf("second Issue: %v", err)
	}

	bad := NewClient(caURL, ServicesProvisioner, []byte("wrong"), roots)
	if _, err := bad.Issue(ctx, "control-plane", nil); err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Errorf("wrong password: err = %v", err)
	}
	missing := NewClient(caURL, "nope", pw, roots)
	if _, err := missing.Issue(ctx, "x", nil); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown provisioner: err = %v", err)
	}
}
