package asteriskconf

import (
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
		c.DBPasswordFile != "/run/secrets/linx_asterisk_db_password" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestConfigFromEnvOverride(t *testing.T) {
	c := ConfigFromEnv(env(map[string]string{"LINX_ASTERISK_CONF_DIR": "/custom/conf"}))
	if c.ConfDir != "/custom/conf" {
		t.Fatalf("ConfDir = %q, want /custom/conf", c.ConfDir)
	}
}

func TestRender(t *testing.T) {
	root := t.TempDir()
	pwFile := filepath.Join(root, "linx_asterisk_db_password")
	if err := os.WriteFile(pwFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Config{
		ConfDir:        filepath.Join(root, "conf"),
		VarDir:         filepath.Join(root, "var"),
		RunDir:         filepath.Join(root, "run"),
		ScratchDir:     filepath.Join(root, "scratch"),
		CertsDir:       "/var/lib/linx/certs",
		SIPPort:        5061,
		DBHost:         "postgres",
		DBPort:         "5432",
		DBName:         "linx",
		DBPasswordFile: pwFile,
	}
	if err := c.Render(); err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, dir := range []string{c.ConfDir, c.VarDir, c.RunDir, filepath.Join(c.ScratchDir, "spool"), filepath.Join(c.ScratchDir, "log")} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("expected directory %s to exist", dir)
		}
	}

	wantFiles := []string{"asterisk.conf", "logger.conf", "modules.conf", "manager.conf", "http.conf", "extensions.conf",
		"pjsip.conf", "sorcery.conf", "extconfig.conf", "odbcinst.ini", "odbc.ini", "res_odbc.conf"}
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
	for _, want := range []string{"bind=0.0.0.0:5061", "cert_file=/var/lib/linx/certs/current/fullchain.pem", "priv_key_file=/var/lib/linx/certs/current/privkey.pem"} {
		if !strings.Contains(string(pjsip), want) {
			t.Errorf("pjsip.conf missing %q:\n%s", want, pjsip)
		}
	}

	asteriskConf, err := os.ReadFile(filepath.Join(c.ConfDir, "asterisk.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(asteriskConf), "astetcdir => "+c.ConfDir) {
		t.Errorf("asterisk.conf missing astetcdir override:\n%s", asteriskConf)
	}

	manager, err := os.ReadFile(filepath.Join(c.ConfDir, "manager.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manager), "enabled = no") {
		t.Errorf("manager.conf must disable AMI:\n%s", manager)
	}
}
