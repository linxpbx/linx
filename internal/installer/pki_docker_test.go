package installer

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPKIBootstrapDocker runs the real bootstrap and the compose step-ca
// service, then checks health, certificate lifetimes and the root backup.
// It needs Docker: make test-docker.
func TestPKIBootstrapDocker(t *testing.T) {
	if os.Getenv("LINX_DOCKER_TESTS") != "1" {
		t.Skip("set LINX_DOCKER_TESTS=1 (make test-docker)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const (
		project = "linx-pki-test"
		volume  = "linx-pki-test-ca"
		network = "linx-pki-test-private"
	)
	dir := t.TempDir()
	secrets, backup := filepath.Join(dir, "secrets"), filepath.Join(dir, "ca-backup")
	for _, d := range []string{secrets, backup} {
		if err := os.Mkdir(d, 0o777); err != nil {
			t.Fatal(err)
		}
		os.Chmod(d, 0o777) // the container's step user writes here; umask may have trimmed it
	}
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args[:min(3, len(args))], " "), err, tail(string(out), 20))
		}
		return string(out)
	}
	compose := []string{"compose", "--project-name", project, "--file", filepath.Join(dir, "compose.yaml")}
	cleanup := func() {
		exec.Command("docker", "rm", "--force", "linx-step-ca").Run()
		exec.Command("docker", "volume", "rm", "--force", volume).Run()
		exec.Command("docker", "network", "rm", network, network+"-egress").Run()
	}
	cleanup()
	t.Cleanup(cleanup)

	// 1. Secrets and bootstrap, as linx setup plans them but in a temp dir.
	s := PKIPlan(false)
	var boot *Cmd
	for _, st := range s.Plan {
		if st.File != nil {
			if err := os.WriteFile(filepath.Join(secrets, filepath.Base(st.File.Path)), st.File.Data, 0o444); err != nil {
				t.Fatal(err)
			}
		}
		if st.Cmd != nil && st.Cmd.Name == "docker" {
			boot = st.Cmd
		}
	}
	args := make([]string, len(boot.Args))
	for i, a := range boot.Args {
		a = strings.Replace(a, SecretsDir+"/", secrets+"/", 1)
		a = strings.Replace(a, CABackupDir+":", backup+":", 1)
		args[i] = strings.Replace(a, StepCAVolume+":", volume+":", 1)
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(), boot.Env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap: %v\n%s", err, tail(string(out), 20))
	}
	// Running it again must refuse to replace the CA.
	cmd = exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(), boot.Env...)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "already exists") {
		t.Errorf("second bootstrap should refuse: %v\n%s", err, out)
	}
	if !CAExists(ctx, volumeRunner{volume}) {
		t.Error("CAExists = false after bootstrap")
	}

	// 2. The compose service, with the volume and network renamed.
	b, err := os.ReadFile("../../deploy/compose/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	y := strings.NewReplacer(" name: "+StepCAVolume, " name: "+volume, " name: linx-private", " name: "+network,
		" name: linx-egress", " name: "+network+"-egress").Replace(string(b))
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(secrets, "linx_dns_token"), []byte("unused"), 0o444)
	t.Setenv("LINX_VERSION", "test")
	t.Setenv("LINX_DOMAIN", "example.test")
	t.Setenv("LINX_DNS_PROVIDER", "cloudflare")
	docker(append(compose, "up", "--detach", "--wait", "--wait-timeout", "60", "step-ca")...)

	// 3. Certificates from each provisioner, and the lifetime caps.
	out := filepath.Join(dir, "out")
	os.Mkdir(out, 0o777)
	os.Chmod(out, 0o777)
	script := `set -e
export STEPPATH=/tmp/step
issue() { step ca certificate "$1" "/out/$1.crt" /tmp/key --ca-url https://linx-step-ca:9000 --root /home/step/certs/root_ca.crt \
  --provisioner "$2" --provisioner-password-file "/run/secrets/linx_ca_$3_password" --force "${@:4}"; }
issue svc linx-services services
issue dev linx-devices devices
issue big linx-services services --not-after 48h || echo "48h refused"
step crypto key public /backup/root_ca_key --password-file <(printf %s "$PASS") >/dev/null && echo "backup ok"
`
	res := docker("run", "--rm", "--network", network, "--tmpfs", "/tmp", "--env", "PASS="+s.Passphrase,
		"--volume", volume+":/home/step:ro", "--volume", secrets+":/run/secrets:ro", "--volume", backup+":/backup:ro",
		"--volume", out+":/out", "--entrypoint", "bash", StepCAImage, "-c", script)
	for _, want := range []string{"48h refused", "backup ok"} {
		if !strings.Contains(res, want) {
			t.Errorf("missing %q in:\n%s", want, res)
		}
	}
	for name, want := range map[string]time.Duration{"svc": 24 * time.Hour, "dev": 7 * 24 * time.Hour} {
		pemBytes, err := os.ReadFile(filepath.Join(out, name+".crt"))
		if err != nil {
			t.Fatal(err)
		}
		blk, _ := pem.Decode(pemBytes)
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		// step-ca backdates NotBefore by a minute.
		if got := c.NotAfter.Sub(c.NotBefore) - time.Minute; got != want {
			t.Errorf("%s certificate lifetime %v, want %v", name, got, want)
		}
	}
	if ls := docker("run", "--rm", "--volume", volume+":/home/step:ro", "--entrypoint", "ls", StepCAImage, "/home/step/secrets"); strings.Contains(ls, "root_ca_key") {
		t.Errorf("root key left in the CA volume: %s", ls)
	}
}

// volumeRunner runs real commands with the production volume name swapped.
type volumeRunner struct{ volume string }

func (v volumeRunner) Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	for i, a := range args {
		args[i] = strings.Replace(a, StepCAVolume, v.volume, 1)
	}
	return ExecRunner{}.Run(ctx, env, name, args...)
}
