package installer

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"linxpbx.com/linx/deploy/compose"
	"linxpbx.com/linx/internal/turn"
)

func TestStackPlan(t *testing.T) {
	c := DefaultConfig()
	c.Domain.Name = "lab.linxpbx.com"
	lan := LAN{Address: netip.MustParseAddr("192.168.1.20"), Network: netip.MustParsePrefix("192.168.1.0/24")}
	s := StackPlan(c, "  test-token-xxxxxxxxxxxxxxxx\n", "sha-"+strings.Repeat("a", 40), lan)

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
	astPw := files[AsteriskDBPasswordPath]
	if astPw == nil || len(astPw.Data) == 0 || astPw.Mode != 0o440 || astPw.Gid != 65532 || astPw.DirMode != 0o700 {
		t.Errorf("asterisk database password file = %+v", astPw)
	}
	ariPw := files[ARIPasswordPath]
	if ariPw == nil || len(ariPw.Data) == 0 || ariPw.Mode != 0o440 || ariPw.Gid != 65532 || ariPw.DirMode != 0o700 {
		t.Errorf("ARI password file = %+v", ariPw)
	}
	if string(ariPw.Data) == string(astPw.Data) {
		t.Error("ARI password and asterisk database password are the same")
	}
	turnSecret := files[TURNSecretPath]
	if turnSecret == nil || turn.CheckSecret(string(turnSecret.Data)) != nil || turnSecret.Mode != 0o440 || turnSecret.Gid != 65532 {
		t.Errorf("relay secret file = %+v", turnSecret)
	}
	if f := files["/etc/linx/compose.yaml"]; f == nil || string(f.Data) != string(compose.File) {
		t.Error("compose.yaml not installed from the embedded copy")
	}
	env := string(files["/etc/linx/.env"].Data)
	for _, want := range []string{
		"LINX_VERSION=sha-aaaa", "LINX_DOMAIN=lab.linxpbx.com\n", "LINX_DNS_PROVIDER=cloudflare\n",
		"LINX_ACME_EMAIL=\n", "LINX_ACME_STAGING=true\n", "LINX_CERT_WILDCARD=true\n",
		"LINX_SIP_ADDRESS=192.168.1.20\n", "LINX_SIP_NETWORKS=192.168.1.0/24\n",
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
	if !strings.Contains(strings.Join(titles, "\n"), "Get a test certificate for lab.linxpbx.com and *.lab.linxpbx.com") {
		t.Errorf("titles: %q", titles)
	}
}

// TestComposeSecretsMatchInstaller keeps compose.yaml's secret files where
// setup writes them.
func TestComposeSecretsMatchInstaller(t *testing.T) {
	for _, p := range []string{DNSTokenPath, SecretsDir + "/" + secretStepCAPassword, DBPasswordPath, DBEncryptionKeyPath, JWTSigningKeyPath, AsteriskDBPasswordPath} {
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

func TestHostTimezone(t *testing.T) {
	old := localtimePath
	t.Cleanup(func() { localtimePath = old })
	dir := t.TempDir()
	link := func(target string) {
		t.Helper()
		localtimePath = filepath.Join(dir, "localtime-"+strings.ReplaceAll(target, "/", "_"))
		if err := os.Symlink(target, localtimePath); err != nil {
			t.Fatal(err)
		}
	}
	// Where timedatectl points /etc/localtime: the name after zoneinfo/.
	link("/usr/share/zoneinfo/Asia/Dubai")
	if got := HostTimezone(); got != "Asia/Dubai" {
		t.Errorf("Asia/Dubai: got %q", got)
	}
	link("../usr/share/zoneinfo/Etc/UTC")
	if got := HostTimezone(); got != "Etc/UTC" {
		t.Errorf("Etc/UTC: got %q", got)
	}
	// Anything odd falls back to UTC.
	for _, bad := range []string{"/usr/share/zoneinfo/Not/AZone", "/somewhere/else", "/usr/share/zoneinfo/../../etc/passwd"} {
		link(bad)
		if got := HostTimezone(); got != "UTC" {
			t.Errorf("%s: got %q, want UTC", bad, got)
		}
	}
	localtimePath = filepath.Join(dir, "missing")
	if got := HostTimezone(); got != "UTC" {
		t.Errorf("no /etc/localtime: got %q", got)
	}
}

func TestStackSplit(t *testing.T) {
	c := DefaultConfig()
	c.Domain.Name = "pbx.example.com"
	before, up := StackPlan(c, "test-token-xxxxxxxxxxxxxxxx", "sha-"+strings.Repeat("a", 40), LAN{}).Split()
	if up.Cmd == nil || !strings.HasSuffix(up.Cmd.String(), "up --detach --wait") || len(before) == 0 {
		t.Fatalf("split: %d steps before, then %+v", len(before), up)
	}
	for _, st := range before {
		if st.Cmd != nil && strings.Contains(st.Cmd.String(), " up ") {
			t.Errorf("started before the last step: %s", st.Cmd)
		}
	}
	if got := StartServicesStep("x", "control-plane").Cmd.String(); !strings.HasSuffix(got, "up --detach --wait control-plane") {
		t.Errorf("start one: %s", got)
	}
}

// Every Linx container resolves only its own services' short names, never
// with the host's search domain (found in the install demo: "step-ca" became
// step-ca.<search domain>, which a wildcard DNS record sent to the
// internet). A container sharing another's network (WireGuard) inherits it.
func TestComposeNoSearchDomain(t *testing.T) {
	for name, file := range map[string][]byte{"compose.yaml": compose.File, "install.yaml": compose.InstallFile} {
		var c struct {
			Services map[string]struct {
				DNSSearch   any    `yaml:"dns_search"`
				NetworkMode string `yaml:"network_mode"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(file, &c); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for svc, s := range c.Services {
			if s.NetworkMode != "" {
				continue
			}
			if s.DNSSearch != "." {
				t.Errorf("%s: service %s has dns_search %v, want \".\"", name, svc, s.DNSSearch)
			}
		}
	}
}

// Updates leave the previous version's images behind; setup removes only
// Linx's own unused ones (checked on the install demo's server: the running
// version, postgres, step-ca and Portainer were kept).
func TestPruneOldImagesStep(t *testing.T) {
	got := PruneOldImagesStep().Cmd.String()
	if got != "docker image prune --all --force --filter label=org.opencontainers.image.source=https://github.com/linxpbx/linx" {
		t.Errorf("%s", got)
	}
	for _, f := range []string{"deploy/docker/asterisk.Dockerfile", "deploy/docker/coturn.Dockerfile", "deploy/docker/control-plane.Dockerfile", "deploy/docker/go-service.Dockerfile"} {
		b, err := os.ReadFile("../../" + f)
		if err != nil || !strings.Contains(string(b), `org.opencontainers.image.source="https://github.com/linxpbx/linx"`) {
			t.Errorf("%s doesn't carry the label the cleanup selects by (%v)", f, err)
		}
	}
}
