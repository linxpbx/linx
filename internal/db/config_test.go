package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	c := ConfigFromEnv(getenv)
	if c.Host != "postgres" || c.Port != "5432" || c.Name != "linx" || c.User != "linx" ||
		c.PasswordFile != "/run/secrets/linx_db_password" {
		t.Errorf("defaults = %+v", c)
	}

	env["LINX_DB_HOST"] = "db.example"
	env["LINX_DB_PORT"] = "5433"
	env["LINX_DB_NAME"] = "linxtest"
	env["LINX_DB_USER"] = "tester"
	env["LINX_DB_PASSWORD_FILE"] = "/tmp/pw"
	c = ConfigFromEnv(getenv)
	if c.Host != "db.example" || c.Port != "5433" || c.Name != "linxtest" || c.User != "tester" || c.PasswordFile != "/tmp/pw" {
		t.Errorf("overrides = %+v", c)
	}
}

func TestConfigDSN(t *testing.T) {
	dir := t.TempDir()
	pwFile := filepath.Join(dir, "password")
	// A password with characters that must be percent-encoded in a URL.
	if err := os.WriteFile(pwFile, []byte("p@ss/word?&=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Config{Host: "postgres", Port: "5432", Name: "linx", User: "linx", PasswordFile: pwFile}
	dsn, err := c.DSN()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"postgres://linx:", "@postgres:5432/linx", "sslmode=disable"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN() = %q, missing %q", dsn, want)
		}
	}
	if strings.Contains(dsn, "p@ss/word?&=") {
		t.Errorf("DSN() = %q, password not encoded", dsn)
	}
}

func TestConfigDSNMissingPasswordFile(t *testing.T) {
	c := Config{Host: "postgres", Port: "5432", Name: "linx", User: "linx", PasswordFile: filepath.Join(t.TempDir(), "missing")}
	if _, err := c.DSN(); err == nil {
		t.Error("DSN() with a missing password file succeeded")
	}
}
