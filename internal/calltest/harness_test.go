package calltest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"linxpbx.com/linx/internal/ari"
	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/doctor"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/store"
)

// Names of everything the suite creates in Docker, removed before and after.
const (
	prefix  = "linx-calltest"
	netName = prefix + "-net"
	// outsideNet is a second network Asterisk is on but phones may not
	// connect from (LINX_SIP_NETWORKS lists netName's subnet only).
	outsideNet = prefix + "-outside"
	// sipwsNet stands in for linx-sipws: Asterisk's browser websocket
	// listens there only, and wsphone (in place of the control plane's
	// relay) connects from there.
	sipwsNet   = prefix + "-sipws"
	pgName     = prefix + "-postgres"
	astName    = prefix + "-asterisk"
	ariPass    = "test-ari-password"
	astDBPass  = "test-asterisk-db-password"
	sippImage  = "linx-sipp-test:dev"
	sipTimeout = "90s"
)

// asteriskTmpfs are compose.yaml's tmpfs mounts for Asterisk (checked by
// TestComposeAsteriskTmpfs), so the suite's restart test covers them.
var asteriskTmpfs = []string{"/etc/asterisk:uid=100,gid=101,mode=0750", "/var/run/asterisk:uid=100,gid=101,mode=0750"}

// env is one running phone system: Postgres, Asterisk and the ARI app.
type env struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	store  *store.Store
	tenant uuid.UUID
	dir    string
	// The test CA, for renewSIPCert.
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
}

type phone struct {
	number   string
	ext      pbx.Extension
	dev      pbx.Device
	password string
}

func docker(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args[:min(3, len(args))], " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// asteriskImage is the image under test: LINX_ASTERISK_IMAGE, else the one
// `make image SERVICE=asterisk` builds.
func asteriskImage() string {
	if v := os.Getenv("LINX_ASTERISK_IMAGE"); v != "" {
		return v
	}
	return "linx-asterisk:dev"
}

// start brings up Postgres and Asterisk on a private network, with the app
// newApp returns serving Asterisk's ARI connection from this test process.
func start(t *testing.T, ctx context.Context, newApp func(*env) ari.App) *env {
	t.Helper()
	if os.Getenv("LINX_DOCKER_TESTS") != "1" {
		t.Skip("set LINX_DOCKER_TESTS=1 (make test-docker)")
	}
	if exec.Command("docker", "image", "inspect", asteriskImage()).Run() != nil {
		t.Skipf("no %s image: run make image SERVICE=asterisk first", asteriskImage())
	}
	if exec.Command("docker", "image", "inspect", sippImage).Run() != nil {
		docker(t, ctx, "buildx", "build", "-q", "-f", "../../deploy/docker/sipp-test.Dockerfile", "--load", "-t", sippImage, "../..")
	}

	cleanup := func() {
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name="+prefix).Output()
		for _, id := range strings.Fields(string(out)) {
			exec.Command("docker", "rm", "--force", id).Run()
		}
		exec.Command("docker", "network", "rm", netName, outsideNet, sipwsNet).Run()
	}
	cleanup()
	t.Cleanup(cleanup)
	docker(t, ctx, "network", "create", netName)
	docker(t, ctx, "network", "create", outsideNet)
	docker(t, ctx, "network", "create", "--internal", sipwsNet)
	subnet := docker(t, ctx, "network", "inspect", "--format", "{{(index .IPAM.Config 0).Subnet}}", netName)

	pool := dbtest.Start(t, ctx, pgName)
	docker(t, ctx, "network", "connect", "--alias", "postgres", netName, pgName)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	e := &env{t: t, ctx: ctx, pool: pool, store: store.New(pool), dir: t.TempDir()}
	tenant, err := e.store.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.tenant = tenant
	e.writeFiles()
	e.buildWSPhone()
	if err := db.EnsureAsteriskRole(ctx, pool, filepath.Join(e.dir, "secrets", "linx_asterisk_db_password")); err != nil {
		t.Fatal(err)
	}
	// "sign in, call, answer, hang up" checks Asterisk uses this name.
	if err := db.SetSIPDomain(ctx, pool, "sip.linx.test"); err != nil {
		t.Fatal(err)
	}

	// The ARI app, over TLS with a certificate for the name Asterisk dials.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(e.dir, "ari.pem"), filepath.Join(e.dir, "ari.key"))
	if err != nil {
		t.Fatal(err)
	}
	h := &ari.Handler{User: asteriskconf.ARIUser, Password: []byte(ariPass), App: newApp(e),
		Log: slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))}
	srv := &http.Server{Handler: h, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 5 * time.Second}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() {
		h.Close()
		srv.Close()
	})
	port := ln.Addr().(*net.TCPAddr).Port

	// Created, attached to every network, then started: the entrypoint
	// resolves linx-sipws as it starts.
	d := e.dir
	docker(t, ctx, "create", "--name", astName, "--network", netName, "--network-alias", "asterisk",
		"--add-host", "host.docker.internal:host-gateway",
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--tmpfs", asteriskTmpfs[0], "--tmpfs", asteriskTmpfs[1], "--tmpfs", "/tmp",
		"--tmpfs", "/var/lib/asterisk:uid=100,gid=101,mode=0750",
		// For this test process's own TLS checks (sipAddr).
		"--publish", "127.0.0.1::5061",
		"--volume", filepath.Join(d, "certs")+":/var/lib/linx/certs:ro",
		"--volume", filepath.Join(d, "sipws")+":/var/lib/linx/sipws-certs:ro",
		"--init", // as compose.yaml
		"--env", "LINX_CERT_CHECK_INTERVAL=1s",
		"--volume", filepath.Join(d, "ca")+":/etc/linx/ca:ro",
		"--volume", filepath.Join(d, "secrets", "linx_asterisk_db_password")+":/run/secrets/linx_asterisk_db_password:ro",
		"--volume", filepath.Join(d, "secrets", "linx_ari_password")+":/run/secrets/linx_ari_password:ro",
		"--env", "LINX_ARI_URL=wss://host.docker.internal:"+strconv.Itoa(port)+"/ari",
		"--env", "LINX_SIP_NETWORKS="+subnet,
		asteriskImage())
	docker(t, ctx, "network", "connect", "--alias", "asterisk", outsideNet, astName)
	docker(t, ctx, "network", "connect", "--alias", asteriskconf.DefaultSIPWSHost, sipwsNet, astName)
	docker(t, ctx, "start", astName)
	e.waitAsterisk()
	return e
}

// waitAsterisk waits until Asterisk answers its console and has loaded
// PJSIP (so the TLS transport is up) and its browser websocket listener.
func (e *env) waitAsterisk() {
	deadline := time.Now().Add(60 * time.Second)
	for {
		out, err := exec.Command("docker", "exec", astName, "asterisk", "-rx", "pjsip show transports").CombinedOutput()
		tcp, _ := exec.Command("docker", "exec", astName, "cat", "/proc/net/tcp").CombinedOutput()
		ws := slices.ContainsFunc(doctor.ListeningTCP(string(tcp)), func(a netip.AddrPort) bool { return a.Port() == asteriskconf.SIPWSPort })
		if err == nil && strings.Contains(string(out), "transport-tls") && ws {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("Asterisk never came up (websocket listening: %v): %v %s\n%s", ws, err, out, e.asteriskLogs())
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// asteriskCLI runs an Asterisk console command.
func (e *env) asteriskCLI(cmd string) string {
	return docker(e.t, e.ctx, "exec", astName, "asterisk", "-rx", cmd)
}

// sipAddr is where this test process reaches Asterisk's SIP TLS port.
func (e *env) sipAddr() string {
	addr, _, _ := strings.Cut(docker(e.t, e.ctx, "port", astName, "5061/tcp"), "\n")
	return addr
}

// asteriskLogs is Asterisk's recent log, without the console connections
// every asteriskCLI call adds.
func (e *env) asteriskLogs() string {
	out, _ := exec.Command("docker", "logs", "--tail", "1500", astName).CombinedOutput()
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if !strings.Contains(l, "Remote UNIX connection") {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines[max(0, len(lines)-150):], "\n")
}

// newPhone creates an extension with one device whose password this test
// knows.
func (e *env) newPhone(number, name string) phone {
	e.t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ext := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: e.tenant, Number: number, DisplayName: name,
		Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := e.store.CreateExtension(e.ctx, ext, e.audit("extension.create")); err != nil {
		e.t.Fatal(err)
	}
	user := pbx.NewSIPUsername()
	pw := pbx.NewDevicePassword()
	dev := pbx.Device{ID: uuid.Must(uuid.NewV7()), TenantID: e.tenant, ExtensionID: ext.ID, Name: name + "'s phone",
		Kind: pbx.KindSoftphone, SIPUsername: user, DigestHash: pbx.DigestHash(user, pw), Enabled: true, Version: 1,
		CreatedAt: now, UpdatedAt: now}
	if err := e.store.CreateDevice(e.ctx, dev, e.audit("device.create")); err != nil {
		e.t.Fatal(err)
	}
	return phone{number: number, ext: ext, dev: dev, password: pw}
}

func (e *env) audit(action string) auth.AuditEntry {
	return auth.AuditEntry{TenantID: &e.tenant, Actor: "system", Action: action, Result: auth.ResultOK}
}

// sipp starts a SIPp phone in the background and returns its container
// name. extra are further SIPp arguments.
func (e *env) sipp(name, scenario string, p phone, extra ...string) string {
	e.t.Helper()
	return e.sippOn(netName, name, scenario, p, extra...)
}

// sippOn is sipp from a given Docker network.
func (e *env) sippOn(network, name, scenario string, p phone, extra ...string) string {
	e.t.Helper()
	cname := prefix + "-sipp-" + name
	exec.Command("docker", "rm", "--force", cname).Run()
	testdata, err := filepath.Abs("testdata")
	if err != nil {
		e.t.Fatal(err)
	}
	args := []string{"run", "--detach", "--name", cname, "--network", network,
		"--volume", testdata + ":/scenarios:ro", "--volume", filepath.Join(e.dir, "sipp") + ":/tls:ro",
		sippImage, "asterisk:5061", "-t", "l1",
		"-tls_cert", "/tls/client.pem", "-tls_key", "/tls/client.key", "-tls_ca", "/tls/root_ca.crt",
		"-sf", "/scenarios/" + scenario, "-m", "1", "-nostdin",
		"-timeout", sipTimeout, "-timeout_error",
		"-set", "user", p.dev.SIPUsername, "-au", p.dev.SIPUsername, "-ap", p.password,
		"-trace_err", "-error_file", "/dev/stderr",
	}
	args = append(args, extra...)
	docker(e.t, e.ctx, args...)
	return cname
}

// wait waits for a SIPp phone to finish and fails the test (with its log)
// unless it succeeded.
func (e *env) wait(cname string) {
	e.t.Helper()
	code := docker(e.t, e.ctx, "wait", cname)
	if code != "0" {
		logs, _ := exec.Command("docker", "logs", "--tail", "60", cname).CombinedOutput()
		e.t.Fatalf("SIPp %s exited %s:\n%s\n--- Asterisk:\n%s", cname, code, logs, e.asteriskLogs())
	}
}

// run runs a SIPp phone to completion.
func (e *env) run(name, scenario string, p phone, extra ...string) {
	e.t.Helper()
	e.wait(e.sipp(name, scenario, p, extra...))
}

// newWebPhone creates an extension with one web device (a browser's line,
// which the control plane creates at sign-in from step 4 on).
func (e *env) newWebPhone(number, name string) phone {
	e.t.Helper()
	p := e.newPhone(number, name)
	if _, err := e.pool.Exec(e.ctx, `UPDATE device SET kind = 'web' WHERE id = $1`, p.dev.ID); err != nil {
		e.t.Fatal(err)
	}
	p.dev.Kind = pbx.KindWeb
	return p
}

// buildWSPhone builds wsphone for the containers' platform, to run in the
// SIPp image (it's static).
func (e *env) buildWSPhone() {
	e.t.Helper()
	cmd := exec.CommandContext(e.ctx, "go", "build", "-trimpath", "-o", filepath.Join(e.dir, "wsphone", "wsphone"), "./wsphone")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("building wsphone: %v\n%s", err, out)
	}
	os.Chmod(filepath.Join(e.dir, "wsphone"), 0o755)
}

// wsphone starts wsphone for a device on sipwsNet and returns its container
// name.
func (e *env) wsphone(name string, p phone, extra ...string) string {
	e.t.Helper()
	cname := prefix + "-ws-" + name
	exec.Command("docker", "rm", "--force", cname).Run()
	args := []string{"run", "--detach", "--name", cname, "--network", sipwsNet,
		"--volume", filepath.Join(e.dir, "wsphone") + ":/wsphone:ro", "--volume", filepath.Join(e.dir, "ca") + ":/ca:ro",
		"--entrypoint", "/wsphone/wsphone", sippImage,
		"-url", "wss://" + asteriskconf.DefaultSIPWSHost + ":" + strconv.Itoa(asteriskconf.SIPWSPort) + asteriskconf.SIPWSPath,
		"-user", p.dev.SIPUsername, "-pass", p.password}
	docker(e.t, e.ctx, append(args, extra...)...)
	return cname
}

// waitWS waits for a wsphone to finish, fails the test unless it
// succeeded, and returns what it printed.
func (e *env) waitWS(cname string) string {
	e.t.Helper()
	code := docker(e.t, e.ctx, "wait", cname)
	logs, _ := exec.Command("docker", "logs", cname).CombinedOutput()
	if code != "0" {
		e.t.Fatalf("wsphone %s exited %s:\n%s\n--- Asterisk:\n%s", cname, code, logs, e.asteriskLogs())
	}
	return string(logs)
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(b []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(b)))
	return len(b), nil
}

// writeFiles creates a throwaway CA, the SIP certificate Asterisk serves
// (for "asterisk"), the ARI certificate this test serves (for
// host.docker.internal), a client certificate SIPp insists on loading, and
// the two secrets. Everything is world-readable: Asterisk and SIPp run as
// other users in their containers.
func (e *env) writeFiles() {
	t := e.t
	for _, sub := range []string{"certs/v1", "sipws/v20", "ca", "secrets", "sipp"} {
		if err := os.MkdirAll(filepath.Join(e.dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{e.dir, filepath.Join(e.dir, "certs"), filepath.Join(e.dir, "sipws")} {
		os.Chmod(d, 0o755)
	}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Linx Test Root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	e.caCert, _ = x509.ParseCertificate(caDER)
	e.caKey = caKey
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	sipCert, sipKey := e.leaf(2, "asterisk")
	ariCert, ariKey := e.leaf(3, "host.docker.internal")
	cliCert, cliKey := e.leaf(4, "sipp")
	wsCert, wsKey := e.leaf(20, asteriskconf.DefaultSIPWSHost)

	files := map[string][]byte{
		// Laid out like linx-certd's volume: versions, and current -> one.
		"certs/v1/fullchain.pem": append(sipCert, rootPEM...),
		"certs/v1/privkey.pem":   sipKey,
		// The browser websocket's, laid out as the control plane writes it.
		"sipws/v20/fullchain.pem":           append(wsCert, rootPEM...),
		"sipws/v20/privkey.pem":             wsKey,
		"ca/root_ca.crt":                    rootPEM,
		"ari.pem":                           ariCert,
		"ari.key":                           ariKey,
		"sipp/client.pem":                   cliCert,
		"sipp/client.key":                   cliKey,
		"sipp/root_ca.crt":                  rootPEM,
		"secrets/linx_asterisk_db_password": []byte(astDBPass),
		"secrets/linx_ari_password":         []byte(ariPass),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(e.dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("v1", filepath.Join(e.dir, "certs", "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("v20", filepath.Join(e.dir, "sipws", "current")); err != nil {
		t.Fatal(err)
	}
}

// leaf issues a certificate for dns from the test CA.
func (e *env) leaf(serial int64, dns string) (certPEM, keyPEM []byte) {
	e.t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: dns}, DNSNames: []string{dns},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, e.caCert, &key.PublicKey, e.caKey)
	if err != nil {
		e.t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
}

// renewSIPCert deploys a new SIP certificate with this serial the way
// linx-certd does: a new version directory, then current swapped to it.
func (e *env) renewSIPCert(serial int64) { e.renewCert("certs", "asterisk", serial) }

// renewSIPWSCert does the same for the browser websocket's certificate, as
// the control plane does.
func (e *env) renewSIPWSCert(serial int64) {
	e.renewCert("sipws", asteriskconf.DefaultSIPWSHost, serial)
}

func (e *env) renewCert(sub, name string, serial int64) {
	e.t.Helper()
	v := "v" + strconv.FormatInt(serial, 10)
	cert, key := e.leaf(serial, name)
	dir := filepath.Join(e.dir, sub, v)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	for name, b := range map[string][]byte{"fullchain.pem": cert, "privkey.pem": key} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
	tmp := filepath.Join(e.dir, sub, "current.tmp")
	if err := os.Symlink(v, tmp); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(e.dir, sub, "current")); err != nil {
		e.t.Fatal(err)
	}
}

// eventually polls cond until it's true or the timeout passes.
func eventually(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
