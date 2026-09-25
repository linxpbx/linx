package turnconf

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/turn"
)

// Names of everything the test creates in Docker, removed before and after.
const (
	dtPrefix  = "linx-turntest"
	dtMedia   = dtPrefix + "-media"
	dtPublic  = dtPrefix + "-public"
	dtCoturn  = dtPrefix + "-coturn"
	dtPeer    = dtPrefix + "-asterisk"
	dtOther   = dtPrefix + "-stranger"
	dtRealm   = "linx.test"
	dtEchoUDP = 10000
)

func coturnImage() string {
	if v := os.Getenv("LINX_COTURN_IMAGE"); v != "" {
		return v
	}
	return "linx-coturn:dev"
}

func dockerRun(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args[:min(3, len(args))], " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestCoturnDocker runs the real linx-coturn image (building it if it isn't
// there: make image SERVICE=coturn) between a stand-in for Asterisk's audio
// and this test's own TURN client, and checks what the relay allows
// (docs/WEB.md §2, §7): only Control-plane-style credentials, only
// Asterisk as a peer, TLS with the deployed certificate, the per-person
// quota, and certificate renewal without a restart. It needs Docker: make
// test-docker.
func TestCoturnDocker(t *testing.T) {
	if os.Getenv("LINX_DOCKER_TESTS") != "1" {
		t.Skip("set LINX_DOCKER_TESTS=1 (make test-docker)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if exec.Command("docker", "image", "inspect", coturnImage()).Run() != nil {
		dockerRun(t, ctx, "buildx", "build", "-q", "-f", "../../deploy/docker/coturn.Dockerfile", "--load", "-t", coturnImage(), "../..")
	}
	cleanup := func() {
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name="+dtPrefix).Output()
		for _, id := range strings.Fields(string(out)) {
			exec.Command("docker", "rm", "--force", id).Run()
		}
		exec.Command("docker", "network", "rm", dtMedia, dtPublic).Run()
	}
	cleanup()
	t.Cleanup(cleanup)

	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	echo := filepath.Join(dir, "udpecho")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", echo, "./udpecho")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building udpecho: %v\n%s", err, out)
	}
	secret := strings.Repeat("Zq9", 16)
	os.WriteFile(filepath.Join(dir, "secret"), []byte(secret+"\n"), 0o644)
	certs := filepath.Join(dir, "certs")
	caKey, caCert := testCA(t)
	deploy := func(serial int64) {
		v := filepath.Join(certs, "v"+strconv.FormatInt(serial, 10))
		os.MkdirAll(v, 0o755)
		chain, key := leafFor(t, caKey, caCert, serial, "*."+dtRealm)
		os.WriteFile(filepath.Join(v, "fullchain.pem"), chain, 0o644)
		os.WriteFile(filepath.Join(v, "privkey.pem"), key, 0o644)
		tmp := filepath.Join(certs, "current.tmp")
		os.Remove(tmp)
		os.Symlink(filepath.Base(v), tmp)
		os.Rename(tmp, filepath.Join(certs, "current"))
	}
	os.MkdirAll(certs, 0o755)
	deploy(10)

	dockerRun(t, ctx, "network", "create", "--internal", dtMedia)
	dockerRun(t, ctx, "network", "create", dtPublic)
	echoCmd := func(name, alias string) {
		dockerRun(t, ctx, "run", "--detach", "--name", name, "--network", dtMedia, "--network-alias", alias,
			"--volume", echo+":/udpecho:ro", "--entrypoint", "/udpecho", coturnImage(), "-listen", ":"+strconv.Itoa(dtEchoUDP))
	}
	echoCmd(dtPeer, asteriskconf.DefaultMediaHost)
	echoCmd(dtOther, "stranger")
	peerIP := netip.MustParseAddr(dockerRun(t, ctx, "inspect", "--format", "{{(index .NetworkSettings.Networks \""+dtMedia+"\").IPAddress}}", dtPeer))
	strangerIP := netip.MustParseAddr(dockerRun(t, ctx, "inspect", "--format", "{{(index .NetworkSettings.Networks \""+dtMedia+"\").IPAddress}}", dtOther))

	// As compose.yaml runs it: read-only, no capabilities, its own user.
	dockerRun(t, ctx, "create", "--name", dtCoturn, "--network", dtPublic,
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--tmpfs", "/tmp",
		"--publish", "127.0.0.1::3478/udp", "--publish", "127.0.0.1::5349/tcp",
		"--env", "LINX_DOMAIN="+dtRealm, "--env", "LINX_CHECK_INTERVAL=1s",
		"--volume", certs+":/var/lib/linx/certs:ro",
		"--volume", filepath.Join(dir, "secret")+":/run/secrets/linx_turn_secret:ro",
		coturnImage())
	dockerRun(t, ctx, "network", "connect", dtMedia, dtCoturn)
	dockerRun(t, ctx, "start", dtCoturn)
	logs := func() string {
		out, _ := exec.Command("docker", "logs", "--tail", "80", dtCoturn).CombinedOutput()
		return string(out)
	}
	healthy := func() error {
		out, err := exec.Command("docker", "exec", dtCoturn, "/usr/local/bin/coturn-entrypoint", "healthcheck").CombinedOutput()
		if err != nil {
			return &exec.ExitError{Stderr: out}
		}
		return nil
	}
	deadline := time.Now().Add(30 * time.Second)
	for healthy() != nil {
		if time.Now().After(deadline) {
			t.Fatalf("coturn never became healthy: %v\n%s", healthy(), logs())
		}
		time.Sleep(300 * time.Millisecond)
	}
	udpAddr := strings.TrimSpace(strings.Split(dockerRun(t, ctx, "port", dtCoturn, "3478/udp"), "\n")[0])
	tlsAddr := strings.TrimSpace(strings.Split(dockerRun(t, ctx, "port", dtCoturn, "5349/tcp"), "\n")[0])
	person := uuid.New()
	issuer := &turn.Issuer{Secret: []byte(secret), Now: time.Now}
	peer := netip.AddrPortFrom(peerIP, dtEchoUDP)

	udpClient := func(user, pass string) *turnClient {
		t.Helper()
		c, err := net.Dial("udp", udpAddr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return &turnClient{conn: c, user: user, pass: pass}
	}

	t.Run("relays to the phone system", func(t *testing.T) {
		cred := issuer.Issue(person)
		c := udpClient(cred.Username, cred.Password)
		if err := c.allocate(); err != nil {
			t.Fatalf("allocate: %v\n%s", err, logs())
		}
		if !c.Relayed.IsValid() || c.Relayed.Port() < RelayMin || c.Relayed.Port() > RelayMax {
			t.Errorf("relayed address %v", c.Relayed)
		}
		if err := c.permit(peer); err != nil {
			t.Fatalf("permission for the phone system: %v", err)
		}
		got, err := c.echo(peer, []byte("hello, asterisk"))
		if err != nil || string(got) != "hello, asterisk" {
			t.Fatalf("echo: %q %v", got, err)
		}
	})

	t.Run("and to nothing else", func(t *testing.T) {
		cred := issuer.Issue(person)
		c := udpClient(cred.Username, cred.Password)
		if err := c.allocate(); err != nil {
			t.Fatal(err)
		}
		for what, p := range map[string]netip.AddrPort{
			"another container": netip.AddrPortFrom(strangerIP, dtEchoUDP),
			"the relay itself":  c.Relayed,
			"the internet":      netip.MustParseAddrPort("1.1.1.1:53"),
			"a LAN":             netip.MustParseAddrPort("192.168.1.1:5061"),
			"the Docker host":   netip.MustParseAddrPort("172.17.0.1:22"),
			"loopback":          netip.MustParseAddrPort("127.0.0.1:3478"),
			"multicast":         netip.MustParseAddrPort("224.0.0.1:5000"),
		} {
			if err := c.permit(p); err != turnError(403) {
				t.Errorf("%s (%v): %v, want 403", what, p, err)
			}
		}
	})

	t.Run("refuses bad credentials", func(t *testing.T) {
		good := issuer.Issue(person)
		expired := (&turn.Issuer{Secret: []byte(secret), Now: func() time.Time { return time.Now().Add(-2 * turn.TTL) }}).Issue(person)
		other := (&turn.Issuer{Secret: []byte(strings.Repeat("x", 48)), Now: time.Now}).Issue(person)
		for what, cred := range map[string][2]string{
			"wrong password":   {good.Username, "not-the-password"},
			"expired":          {expired.Username, expired.Password},
			"another secret":   {other.Username, other.Password},
			"no expiry prefix": {person.String(), turn.Password([]byte(secret), person.String())},
		} {
			if err := udpClient(cred[0], cred[1]).allocate(); err != turnError(401) {
				t.Errorf("%s: %v, want 401", what, err)
			}
		}
	})

	t.Run("per-person quota", func(t *testing.T) {
		someone := uuid.New()
		var err error
		for i := 0; i <= UserQuota && err == nil; i++ {
			cred := issuer.Issue(someone)
			err = udpClient(cred.Username, cred.Password).allocate()
			if i < UserQuota && err != nil {
				t.Fatalf("allocation %d: %v", i+1, err)
			}
		}
		if err != turnError(486) {
			t.Errorf("allocation %d: %v, want 486 (quota reached)", UserQuota+1, err)
		}
		cred := issuer.Issue(uuid.New())
		if err := udpClient(cred.Username, cred.Password).allocate(); err != nil {
			t.Errorf("someone else's allocation: %v", err)
		}
	})

	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	dialTLS := func(cfg *tls.Config) (*tls.Conn, error) {
		cfg.RootCAs, cfg.ServerName = roots, "turn."+dtRealm
		return tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", tlsAddr, cfg)
	}

	t.Run("over TLS", func(t *testing.T) {
		conn, err := dialTLS(&tls.Config{MinVersion: tls.VersionTLS12})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if s := conn.ConnectionState().PeerCertificates[0].SerialNumber; s.Int64() != 10 {
			t.Errorf("serves certificate %v, want 10", s)
		}
		cred := issuer.Issue(person)
		c := &turnClient{conn: conn, stream: true, user: cred.Username, pass: cred.Password}
		if err := c.allocate(); err != nil {
			t.Fatal(err)
		}
		if err := c.permit(peer); err != nil {
			t.Fatal(err)
		}
		if got, err := c.echo(peer, []byte("over tls")); err != nil || string(got) != "over tls" {
			t.Fatalf("echo: %q %v", got, err)
		}
		if _, err := dialTLS(&tls.Config{MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}); err == nil {
			t.Error("TLS 1.1 accepted")
		}
	})

	t.Run("picks up a renewed certificate", func(t *testing.T) {
		// A relayed session open across the renewal carries on.
		cred := issuer.Issue(person)
		c := udpClient(cred.Username, cred.Password)
		if err := c.allocate(); err != nil {
			t.Fatal(err)
		}
		if err := c.permit(peer); err != nil {
			t.Fatal(err)
		}
		deploy(11)
		deadline := time.Now().Add(20 * time.Second)
		for {
			conn, err := dialTLS(&tls.Config{})
			if err == nil {
				serial := conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
				conn.Close()
				if serial == 11 {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("still not serving the renewed certificate: %v\n%s", err, logs())
			}
			time.Sleep(300 * time.Millisecond)
		}
		if got, err := c.echo(peer, []byte("still here")); err != nil || !bytes.Equal(got, []byte("still here")) {
			t.Errorf("relay after renewal: %q %v", got, err)
		}
		if err := healthy(); err != nil {
			t.Errorf("unhealthy after renewal: %v", err)
		}
	})

	t.Run("runs as configured", func(t *testing.T) {
		conf := dockerRun(t, ctx, "exec", dtCoturn, "cat", "/tmp/linx-coturn/turnserver.conf")
		if !strings.Contains(conf, "\nallowed-peer-ip="+peerIP.String()+"\n") {
			t.Errorf("allowed peer isn't %v:\n%s", peerIP, conf)
		}
		if user := dockerRun(t, ctx, "exec", dtCoturn, "id", "-u"); user != "65532" {
			t.Errorf("runs as uid %s", user)
		}
		if os.Getenv("LINX_TURNTEST_LOGS") != "" {
			t.Log(logs())
		}
	})
}

func testCA(t *testing.T) (*ecdsa.PrivateKey, *x509.Certificate) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Linx Test Root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return key, cert
}

func leafFor(t *testing.T, caKey *ecdsa.PrivateKey, ca *x509.Certificate, serial int64, name string) (chainPEM, keyPEM []byte) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalPKCS8PrivateKey(key)
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})...)
	return chain, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})
}
