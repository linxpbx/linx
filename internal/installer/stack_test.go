package installer

import (
	"os"
	"strings"
	"testing"

	"linxpbx.com/linx/deploy/compose"
)

func TestStackPlan(t *testing.T) {
	c := DefaultConfig()
	c.Domain.Name = "lab.linxpbx.com"
	s := StackPlan(c, "  test-token-xxxxxxxxxxxxxxxx\n", "sha-"+strings.Repeat("a", 40))

	var titles, cmds []string
	files := map[string]*File{}
	for _, st := range s.Plan {
		titles = append(titles, st.Title)
		if st.File != nil {
			files[st.File.Path] = st.File
		}
		if st.Cmd != nil {
			cmds = append(cmds, st.Cmd.String())
		}
	}

	tok := files[DNSTokenPath]
	if tok == nil || string(tok.Data) != "test-token-xxxxxxxxxxxxxxxx" || tok.Mode != 0o440 || tok.Gid != 65532 || tok.DirMode != 0o700 {
		t.Errorf("token file = %+v", tok)
	}
	pw := files[DBPasswordPath]
	if pw == nil || len(pw.Data) == 0 || pw.Mode != 0o440 || pw.Gid != 65532 || pw.DirMode != 0o700 {
		t.Errorf("database password file = %+v", pw)
	}
	key := files[DBEncryptionKeyPath]
	if key == nil || len(key.Data) != 32 || key.Mode != 0o440 || key.Gid != 65532 || key.DirMode != 0o700 {
		t.Errorf("database encryption key file = %+v", key)
	}
	jwtKey := files[JWTSigningKeyPath]
	if jwtKey == nil || len(jwtKey.Data) != 32 || jwtKey.Mode != 0o440 || jwtKey.Gid != 65532 || jwtKey.DirMode != 0o700 {
		t.Errorf("token signing key file = %+v", jwtKey)
	}
	if string(jwtKey.Data) == string(key.Data) {
		t.Error("token signing key and database encryption key are the same")
	}
	if f := files["/etc/linx/compose.yaml"]; f == nil || string(f.Data) != string(compose.File) {
		t.Error("compose.yaml not installed from the embedded copy")
	}
	env := string(files["/etc/linx/.env"].Data)
	for _, want := range []string{
		"LINX_VERSION=sha-aaaa", "LINX_DOMAIN=lab.linxpbx.com\n", "LINX_DNS_PROVIDER=cloudflare\n",
		"LINX_ACME_EMAIL=\n", "LINX_ACME_STAGING=true\n", "LINX_CERT_WILDCARD=true\n",
	} {
		if !strings.Contains(env, want) {
			t.Errorf(".env missing %q:\n%s", want, env)
		}
	}
	if strings.Contains(env, "test-token") {
		t.Error(".env contains the DNS token")
	}

	// The certificate is fetched once before the daemon starts, so a bad token
	// or domain stops setup with the provider's error.
	dc := "docker compose --file /etc/linx/compose.yaml "
	want := []string{dc + "pull --quiet", dc + "run --rm certd -once", dc + "up --detach --wait"}
	if strings.Join(cmds, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(cmds, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(strings.Join(titles, "\n"), "Get a test certificate for *.lab.linxpbx.com") {
		t.Errorf("titles: %q", titles)
	}
}

// TestComposeSecretsMatchInstaller keeps compose.yaml's secret files where
// setup writes them.
func TestComposeSecretsMatchInstaller(t *testing.T) {
	for _, p := range []string{DNSTokenPath, SecretsDir + "/" + secretStepCAPassword, DBPasswordPath, DBEncryptionKeyPath, JWTSigningKeyPath} {
		rel := "./" + strings.TrimPrefix(p, StackDir+"/")
		if !strings.Contains(string(compose.File), "file: "+rel+"\n") {
			t.Errorf("compose.yaml doesn't read %s from %s", p, rel)
		}
	}
}

func TestExistingOrNewKeyBytes(t *testing.T) {
	path := t.TempDir() + "/key"
	a := existingOrNewKeyBytes(path, 32)
	if len(a) != 32 {
		t.Fatalf("len = %d, want 32", len(a))
	}
	// Not saved by existingOrNewKeyBytes itself (that's fileStep's job), so
	// asking again without saving first gives a different key.
	b := existingOrNewKeyBytes(path, 32)
	if string(a) == string(b) {
		t.Error("two unsaved calls returned the same key")
	}
	if err := os.WriteFile(path, a, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := existingOrNewKeyBytes(path, 32); string(got) != string(a) {
		t.Error("existing key not reused")
	}
}

func TestImageTag(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	if got, err := ImageTag(sha); err != nil || got != "sha-"+sha {
		t.Errorf("ImageTag = %q, %v", got, err)
	}
	for _, bad := range []string{"unknown", "0123456", sha + "-dirty", strings.ToUpper(sha)} {
		if _, err := ImageTag(bad); err == nil {
			t.Errorf("ImageTag(%q) succeeded", bad)
		}
	}
}

func TestValidateDNSToken(t *testing.T) {
	for tok, ok := range map[string]bool{
		"0123456789abcdef0123456789abcdef01234567": true,
		"c0ffee00-1234-4abc-8def-0123456789ab":     true, // DuckDNS UUID
		"":                                         false,
		"short":                                    false,
		"token with spaces 0123456789":             false,
	} {
		if err := ValidateDNSToken(tok); (err == nil) != ok {
			t.Errorf("ValidateDNSToken(%q) = %v", tok, err)
		}
	}
}
