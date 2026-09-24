package asteriskconf

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigFromEnvDefaults(t *testing.T) {
	c := ConfigFromEnv(env(nil))
	if c.ConfDir != "/etc/asterisk" || c.VarDir != "/var/lib/asterisk" ||
		c.RunDir != "/var/run/asterisk" || c.ScratchDir != "/tmp/asterisk" ||
		c.CertsDir != "/var/lib/linx/certs" || c.SIPPort != 5061 ||
		c.DBHost != "postgres" || c.DBPort != "5432" || c.DBName != "linx" ||
		c.DBPasswordFile != "/run/secrets/linx_asterisk_db_password" || c.DataDir != "/usr/share/asterisk" ||
		c.ARIURL != "wss://linx-ari:8089/ari" || c.ARIPasswordFile != "/run/secrets/linx_ari_password" ||
		c.CARootFile != "/etc/linx/ca/root_ca.crt" || c.SIPWSHost != "linx-sipws" ||
		c.SIPWSCertsDir != "/var/lib/linx/sipws-certs" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestConfigFromEnvOverride(t *testing.T) {
	c := ConfigFromEnv(env(map[string]string{"LINX_ASTERISK_CONF_DIR": "/custom/conf"}))
	if c.ConfDir != "/custom/conf" {
		t.Fatalf("ConfDir = %q, want /custom/conf", c.ConfDir)
	}
}

// testIfaces are a container's addresses: loopback, two Docker networks,
// linx-sipws (172.22.0.4, what testConfig's LookupHost resolves
// linx-sipws to) and an IPv6 link-local address.
func testIfaces() ([]net.Addr, error) {
	return []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("172.20.0.5"), Mask: net.CIDRMask(16, 32)},
		&net.IPNet{IP: net.ParseIP("172.21.0.3"), Mask: net.CIDRMask(16, 32)},
		&net.IPNet{IP: net.ParseIP("172.22.0.4"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
	}, nil
}

func testLookup(host string) ([]netip.Addr, error) {
	if host != "linx-sipws" {
		return nil, errors.New("no such host")
	}
	return []netip.Addr{netip.MustParseAddr("172.22.0.4")}, nil
}

func testConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	pwFile := filepath.Join(root, "linx_asterisk_db_password")
	if err := os.WriteFile(pwFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ariFile := filepath.Join(root, "linx_ari_password")
	if err := os.WriteFile(ariFile, []byte("ari-s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{
		ConfDir:         filepath.Join(root, "conf"),
		VarDir:          filepath.Join(root, "var"),
		RunDir:          filepath.Join(root, "run"),
		ScratchDir:      filepath.Join(root, "scratch"),
		CertsDir:        "/var/lib/linx/certs",
		SIPPort:         5061,
		DBHost:          "postgres",
		DBPort:          "5432",
		DBName:          "linx",
		DBPasswordFile:  pwFile,
		DataDir:         "/usr/share/asterisk",
		ARIURL:          "wss://linx-ari:8089/ari",
		ARIPasswordFile: ariFile,
		CARootFile:      "/etc/linx/ca/root_ca.crt",
		SIPNetworks:     "192.168.1.0/24",
		SIPWSHost:       "linx-sipws",
		SIPWSCertsDir:   "/var/lib/linx/sipws-certs",
		InterfaceAddrs:  testIfaces,
		LookupHost:      testLookup,
	}
}

func read(t *testing.T, c Config, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(c.ConfDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRender(t *testing.T) {
	c := testConfig(t)
	if err := c.Render(); err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, dir := range []string{c.ConfDir, c.VarDir, c.RunDir, filepath.Join(c.ScratchDir, "spool"), filepath.Join(c.ScratchDir, "log")} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("expected directory %s to exist", dir)
		}
	}

	wantFiles := []string{"asterisk.conf", "logger.conf", "modules.conf", "manager.conf", "http.conf", "extensions.conf",
		"pjsip.conf", "sorcery.conf", "extconfig.conf", "odbcinst.ini", "odbc.ini", "res_odbc.conf",
		"ari.conf", "websocket_client.conf", "func_odbc.conf", "openssl.cnf"}
	for _, name := range wantFiles {
		if _, err := os.Stat(filepath.Join(c.ConfDir, name)); err != nil {
			t.Errorf("expected %s to be rendered: %v", name, err)
		}
	}

	sorcery, err := os.ReadFile(filepath.Join(c.ConfDir, "sorcery.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"endpoint=realtime,ps_endpoints", "auth=realtime,ps_auths", "aor=realtime,ps_aors"} {
		if !strings.Contains(string(sorcery), want) {
			t.Errorf("sorcery.conf missing %q:\n%s", want, sorcery)
		}
	}

	extconfig, err := os.ReadFile(filepath.Join(c.ConfDir, "extconfig.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(extconfig), "ps_endpoints => odbc,asterisk,ps_endpoints") {
		t.Errorf("extconfig.conf missing the ps_endpoints mapping:\n%s", extconfig)
	}

	odbc, err := os.ReadFile(filepath.Join(c.ConfDir, "odbc.ini"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Driver=PostgreSQL", "Servername=postgres", "Port=5432", "Database=linx", "ConnSettings=SET search_path TO asterisk"} {
		if !strings.Contains(string(odbc), want) {
			t.Errorf("odbc.ini missing %q:\n%s", want, odbc)
		}
	}

	odbcinst, err := os.ReadFile(filepath.Join(c.ConfDir, "odbcinst.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(odbcinst), "Driver=/usr/lib/psqlodbcw.so") {
		t.Errorf("odbcinst.ini missing the driver path:\n%s", odbcinst)
	}

	resOdbc, err := os.ReadFile(filepath.Join(c.ConfDir, "res_odbc.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"username=linx_asterisk", "password=s3cret\n", "max_connections=5", "pre-connect=yes"} {
		if !strings.Contains(string(resOdbc), want) {
			t.Errorf("res_odbc.conf missing %q (password should be trimmed):\n%s", want, resOdbc)
		}
	}

	pjsip, err := os.ReadFile(filepath.Join(c.ConfDir, "pjsip.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bind=0.0.0.0:5061", "cert_file=/var/lib/linx/certs/current/fullchain.pem", "priv_key_file=/var/lib/linx/certs/current/privkey.pem",
		"method=sslv23\n", "user_agent=Linx\n"} {
		if !strings.Contains(string(pjsip), want) {
			t.Errorf("pjsip.conf missing %q:\n%s", want, pjsip)
		}
	}

	// method=sslv23 is only safe with OpenSSL's floor at TLS 1.2.
	openssl, err := os.ReadFile(c.OpenSSLConfPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(openssl), "MinProtocol = TLSv1.2\n") {
		t.Errorf("openssl.cnf must set MinProtocol = TLSv1.2:\n%s", openssl)
	}

	asteriskConf, err := os.ReadFile(filepath.Join(c.ConfDir, "asterisk.conf"))
	if err != nil {
		t.Fatal(err)
	}
	// "[directories](!)" would make the section a template Asterisk ignores.
	for _, want := range []string{"[directories]\n", "astetcdir => " + c.ConfDir, "astdatadir => /usr/share/asterisk"} {
		if !strings.Contains(string(asteriskConf), want) {
			t.Errorf("asterisk.conf missing %q:\n%s", want, asteriskConf)
		}
	}

	manager, err := os.ReadFile(filepath.Join(c.ConfDir, "manager.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manager), "enabled = no") {
		t.Errorf("manager.conf must disable AMI:\n%s", manager)
	}
}

func TestRenderARI(t *testing.T) {
	c := testConfig(t)
	if err := c.Render(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"uri = wss://linx-ari:8089/ari", "username = asterisk", "password = ari-s3cret\n",
		"connection_type = persistent", "tls_enabled = yes", "ca_list_file = /etc/linx/ca/root_ca.crt",
		"verify_server_cert = yes", "verify_server_hostname = yes"} {
		if got := read(t, c, "websocket_client.conf"); !strings.Contains(got, want) {
			t.Errorf("websocket_client.conf missing %q:\n%s", want, got)
		}
	}
	for _, want := range []string{"type = outbound_websocket", "apps = linx", "subscribe_all = yes", "read_only = yes"} {
		if got := read(t, c, "ari.conf"); !strings.Contains(got, want) {
			t.Errorf("ari.conf missing %q:\n%s", want, got)
		}
	}
	for _, want := range []string{"exten => *43,1,Answer()", "Dial(${TARGETS},30)", "Playback(ss-noservice)", "Playback(vm-nobodyavail)",
		`GotoIf($["${CALLERID(num)}" = "${EXTEN}"]?linx-messages,not-available,1)`} {
		if got := read(t, c, "extensions.conf"); !strings.Contains(got, want) {
			t.Errorf("extensions.conf missing %q", want)
		}
	}
	if got := read(t, c, "func_odbc.conf"); !strings.Contains(got, "FROM linx_ring_targets WHERE number = '${SQL_ESC(${ARG1})}'") {
		t.Errorf("func_odbc.conf must escape the number:\n%s", got)
	}
}

func TestRenderPhoneNetworks(t *testing.T) {
	c := testConfig(t)
	c.SIPAddress = "192.168.1.20"
	if err := c.Render(); err != nil {
		t.Fatal(err)
	}
	pjsip := read(t, c, "pjsip.conf")
	for _, want := range []string{"external_media_address=192.168.1.20\n", "external_signaling_address=192.168.1.20\n",
		"local_net=172.20.0.0/16\n", "local_net=172.21.0.0/16\n",
		"[phone-networks]\ntype=acl\ndeny=0.0.0.0/0.0.0.0\ndeny=::/0\npermit=192.168.1.0/24\n"} {
		if !strings.Contains(pjsip, want) {
			t.Errorf("pjsip.conf missing %q:\n%s", want, pjsip)
		}
	}
	if strings.Contains(pjsip, "local_net=127.") || strings.Contains(pjsip, "local_net=fe80") {
		t.Errorf("loopback/IPv6 link-local in local_net:\n%s", pjsip)
	}
	if got := read(t, c, "rtp.conf"); !strings.Contains(got, "rtpstart=10000\nrtpend=10199\n") {
		t.Errorf("rtp.conf: %s", got)
	}

	// No LAN: every phone request refused, nothing advertised. Only the
	// control plane's relay, on linx-sipws, gets through.
	c = testConfig(t)
	c.SIPNetworks, c.SIPAddress = "none", "127.0.0.1"
	if err := c.Render(); err != nil {
		t.Fatal(err)
	}
	pjsip = read(t, c, "pjsip.conf")
	if !strings.HasSuffix(pjsip, "[phone-networks]\ntype=acl\ndeny=0.0.0.0/0.0.0.0\ndeny=::/0\npermit=172.22.0.0/24\n") ||
		strings.Contains(pjsip, "external_") {
		t.Errorf("no-LAN pjsip.conf:\n%s", pjsip)
	}
}

func TestRenderSIPWebsocket(t *testing.T) {
	c := testConfig(t)
	if err := c.Render(); err != nil {
		t.Fatal(err)
	}
	// HTTPS on linx-sipws only, never plain HTTP, with the control plane's
	// certificate for linx-sipws.
	http := read(t, c, "http.conf")
	for _, want := range []string{"enabled=no\n", "tlsenable=yes\n", "tlsbindaddr=172.22.0.4:8089\n", "servername=Linx\n",
		"tlscertfile=/var/lib/linx/sipws-certs/current/fullchain.pem\n",
		"tlsprivatekey=/var/lib/linx/sipws-certs/current/privkey.pem\n"} {
		if !strings.Contains(http, want) {
			t.Errorf("http.conf missing %q:\n%s", want, http)
		}
	}
	for _, bad := range []string{"bindaddr=0", "bindport", "CBC", "SHA:"} {
		if strings.Contains(http, bad) {
			t.Errorf("http.conf has %q:\n%s", bad, http)
		}
	}
	pjsip := read(t, c, "pjsip.conf")
	for _, want := range []string{"[transport-wss]\ntype=transport\nprotocol=wss\nbind=172.22.0.4:8089\n", "permit=192.168.1.0/24\npermit=172.22.0.0/24\n"} {
		if !strings.Contains(pjsip, want) {
			t.Errorf("pjsip.conf missing %q:\n%s", want, pjsip)
		}
	}
	if strings.Contains(pjsip, "protocol=ws\n") || strings.Contains(pjsip, "protocol=udp") || strings.Contains(pjsip, "protocol=tcp") {
		t.Errorf("unencrypted transport in pjsip.conf:\n%s", pjsip)
	}

	// Off: no web server at all, no transport, no extra ACL entry.
	c = testConfig(t)
	c.SIPWSHost = "none"
	if err := c.Render(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c, "http.conf"); strings.Contains(got, "tls") || !strings.Contains(got, "enabled=no") {
		t.Errorf("websocket off, http.conf:\n%s", got)
	}
	if got := read(t, c, "pjsip.conf"); strings.Contains(got, "wss") || strings.Contains(got, "172.22.") {
		t.Errorf("websocket off, pjsip.conf:\n%s", got)
	}
}

func TestParseSIPNetworks(t *testing.T) {
	got, err := ParseSIPNetworks(" 192.168.1.0/24, 10.8.0.0/16 ")
	if err != nil || FormatSIPNetworks(got) != "192.168.1.0/24,10.8.0.0/16" {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := ParseSIPNetworks("none"); err != nil || got != nil || FormatSIPNetworks(got) != "none" {
		t.Fatalf("none: %v, %v", got, err)
	}
	for _, bad := range []string{"", "192.168.1.7/24", "192.168.1.0", "lan", "192.168.1.0/24,"} {
		if _, err := ParseSIPNetworks(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRenderRefuses(t *testing.T) {
	t.Run("no phone networks", func(t *testing.T) {
		c := testConfig(t)
		c.SIPNetworks = ""
		if err := c.Render(); err == nil || !strings.Contains(err.Error(), "LINX_SIP_NETWORKS") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("websocket name isn't this container", func(t *testing.T) {
		c := testConfig(t)
		c.LookupHost = func(string) ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("172.22.0.9")}, nil }
		if err := c.Render(); err == nil || !strings.Contains(err.Error(), "LINX_SIPWS_HOST") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("websocket name doesn't resolve", func(t *testing.T) {
		c := testConfig(t)
		c.SIPWSHost = "linx-nowhere"
		if err := c.Render(); err == nil || !strings.Contains(err.Error(), "LINX_SIPWS_HOST") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("plain ws", func(t *testing.T) {
		c := testConfig(t)
		c.ARIURL = "ws://linx-ari:8089/ari"
		if err := c.Render(); err == nil || !strings.Contains(err.Error(), "wss://") {
			t.Fatalf("err = %v", err)
		}
	})
	for _, bad := range []string{"pass;word", "pass\nword", "  "} {
		c := testConfig(t)
		if err := os.WriteFile(c.ARIPasswordFile, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := c.Render(); err == nil {
			t.Errorf("password %q: rendered anyway", bad)
		}
	}
}
