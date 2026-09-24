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
		c.CertsDir != "/var/lib/linx/certs" || c.SIPPort != 5061 {
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
	c := Config{
		ConfDir:    filepath.Join(root, "conf"),
		VarDir:     filepath.Join(root, "var"),
		RunDir:     filepath.Join(root, "run"),
		ScratchDir: filepath.Join(root, "scratch"),
		CertsDir:   "/var/lib/linx/certs",
		SIPPort:    5061,
	}
	if err := c.Render(); err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, dir := range []string{c.ConfDir, c.VarDir, c.RunDir, filepath.Join(c.ScratchDir, "spool"), filepath.Join(c.ScratchDir, "log")} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("expected directory %s to exist", dir)
		}
	}

	wantFiles := []string{"asterisk.conf", "logger.conf", "modules.conf", "manager.conf", "http.conf", "extensions.conf", "pjsip.conf"}
	for _, name := range wantFiles {
		if _, err := os.Stat(filepath.Join(c.ConfDir, name)); err != nil {
			t.Errorf("expected %s to be rendered: %v", name, err)
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
