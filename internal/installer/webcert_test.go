package installer

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/install"
)

func webCertConfig(kind, proxy string) Config {
	c := DefaultConfig()
	c.Domain.Name, c.Domain.DNSProvider = "example.com", DNSCloudflare
	c.Certificates.Email = "o@example.com"
	c.FrontDoor = FrontDoorConfig{Kind: kind, ProxyAddress: proxy}
	return c
}

var homeLAN = LAN{Address: netip.MustParseAddr("192.168.1.212"), Network: netip.MustParsePrefix("192.168.1.0/24")}

func TestCertViewPerFrontDoor(t *testing.T) {
	facts := install.Facts{PublicAddress: "203.0.113.5"}
	for _, tc := range []struct {
		kind, proxy string
		lan         LAN
		mode        string
		files       int
		steps       string
	}{
		{FrontDoorLinx443, "", LAN{}, install.CertPort443, 0, ""},
		{FrontDoorLinx443, "", homeLAN, install.CertPort443, 0, "send TCP and UDP port 443 to this server (192.168.1.212)"},
		{FrontDoorPangolin, "192.168.1.20", homeLAN, install.CertPort443, 1, "keep TCP port 443 going to Pangolin (192.168.1.20)"},
		{FrontDoorNginx, "192.168.1.20", homeLAN, install.CertPort443, 2, "UDP port 443 to this server (192.168.1.212)"},
		{FrontDoorHTTPProxy, "192.168.1.20", homeLAN, install.CertToken, 1, "TCP port 5349"},
		{FrontDoorHomeOnly, "", homeLAN, install.CertToken, 0, ""},
	} {
		v := CertView(webCertConfig(tc.kind, tc.proxy), tc.lan, facts)
		if v.Mode != tc.mode || v.Domain != "example.com" || v.FrontDoor != tc.kind {
			t.Errorf("%s: %+v", tc.kind, v)
		}
		if (v.Mode == install.CertPort443) != (len(v.AddRecords) == 2) {
			t.Errorf("%s: records %+v", tc.kind, v.AddRecords)
		} else if len(v.AddRecords) == 2 && (v.AddRecords[0].Name != "example.com" || v.AddRecords[1].Name != "turn.example.com" || v.AddRecords[1].Value != "203.0.113.5") {
			t.Errorf("%s: records %+v", tc.kind, v.AddRecords)
		}
		if tc.steps == "" && tc.files == 0 {
			if v.Setup != nil {
				t.Errorf("%s: setup %+v", tc.kind, v.Setup)
			}
			continue
		}
		if v.Setup == nil || len(v.Setup.Files) != tc.files || !strings.Contains(strings.Join(v.Setup.Steps, "\n"), tc.steps) {
			t.Errorf("%s: setup %+v", tc.kind, v.Setup)
		}
	}
	// Pangolin's block is the one setup writes for it.
	v := CertView(webCertConfig(FrontDoorPangolin, "192.168.1.20"), homeLAN, facts)
	if v.Setup.Files[0].Text != string(PangolinTraefik("example.com", homeLAN.Address)) {
		t.Error("Pangolin's block differs from PangolinTraefik")
	}
}

func TestInstallCertPlan(t *testing.T) {
	c := webCertConfig(FrontDoorLinx443, "")
	p := InstallCertPlan(c, LAN{}, "sha-abc", netip.MustParseAddr("203.0.113.5"))
	var env string
	var paths, cmds []string
	for _, s := range p {
		if s.File != nil {
			paths = append(paths, s.File.Path)
			if s.File.Path == installStackEnv {
				env = string(s.File.Data)
			}
		}
		if s.Cmd != nil {
			cmds = append(cmds, s.Cmd.String())
		}
	}
	if !slices.Contains(paths, HAProxyConfigFile) {
		t.Errorf("linx-sni's settings not written: %v", paths)
	}
	for _, want := range []string{"LINX_VERSION=sha-abc\n", "LINX_INSTALL_ADDRESS=203.0.113.5\n", "LINX_DOMAIN=example.com\n",
		"LINX_ACME_EMAIL=o@example.com\n", "LINX_TRUSTED_PROXIES=linx-sni\n", "LINX_WEB_ADDRESS=127.0.0.1\n",
		"LINX_SNI_ADDRESS=0.0.0.0\n", "COMPOSE_PROFILES=linx-443\n"} {
		if !strings.Contains(env, want) {
			t.Errorf("install.env lacks %q:\n%s", want, env)
		}
	}
	if len(cmds) != 1 || !strings.Contains(cmds[0], "up --detach --wait") {
		t.Errorf("commands: %v", cmds)
	}

	// Pangolin: 8443 on the home address, trusting only Pangolin's.
	p = InstallCertPlan(webCertConfig(FrontDoorPangolin, "192.168.1.20"), homeLAN, "sha-abc", homeLAN.Address)
	for _, s := range p {
		if s.File != nil && s.File.Path == installStackEnv {
			env = string(s.File.Data)
		}
	}
	for _, want := range []string{"LINX_TRUSTED_PROXIES=192.168.1.20\n", "LINX_WEB_ADDRESS=192.168.1.212\n", "COMPOSE_PROFILES=\n", "LINX_PROXY_PROTOCOL=true\n"} {
		if !strings.Contains(env, want) {
			t.Errorf("Pangolin's install.env lacks %q:\n%s", want, env)
		}
	}
}

func TestCertdRun(t *testing.T) {
	got := strings.Join(CertdRun(false, "-bootstrap", "staging"), " ")
	if !strings.HasSuffix(got, "run --rm --no-TTY certd -bootstrap staging") || strings.Contains(got, "linx_dns_token") {
		t.Errorf("without the token: %s", got)
	}
	got = strings.Join(CertdRun(true, "-once"), " ")
	if !strings.Contains(got, "--volume "+DNSTokenPath+":/run/secrets/linx_dns_token:ro certd -once") {
		t.Errorf("with the token: %s", got)
	}
	if got := strings.Join(RecordsArgs(webCertConfig(FrontDoorHomeOnly, ""), homeLAN), " "); got != "-records @,turn -address 192.168.1.212" {
		t.Errorf("home only records: %s", got)
	}
	if got := strings.Join(RecordsArgs(webCertConfig(FrontDoorHTTPProxy, "192.168.1.20"), homeLAN), " "); got != "-records @,turn" {
		t.Errorf("proxy records: %s", got)
	}
	s := SaveDNSTokenPlan(" tok \n")[0]
	if string(s.File.Data) != "tok" || s.File.Mode != 0o440 || s.File.Gid != nonrootGID {
		t.Errorf("token file: %+v", s.File)
	}
}

// A rented server where the owner skipped the DNS token (docs/INSTALL.md
// §5): saved, read back, and certd renews through port 443 with no records.
func TestNoDNSToken(t *testing.T) {
	c := webCertConfig(FrontDoorLinx443, "")
	c.Version = 1
	c.Certificates.Staging = false
	c.Certificates.NoDNSToken = true
	back, err := ParseConfig(strings.NewReader(string(c.Marshal())))
	if err != nil || !back.Certificates.NoDNSToken {
		t.Fatalf("read back: %+v %v\n%s", back.Certificates, err, c.Marshal())
	}
	env := string(stackDotEnv(c, "abc", LAN{}))
	for _, want := range []string{"LINX_CERT_CHALLENGE=tls-alpn-01\n", "LINX_DNS_RECORDS=\n"} {
		if !strings.Contains(env, want) {
			t.Errorf(".env lacks %q:\n%s", want, env)
		}
	}
	c.Certificates.NoDNSToken = false
	if env := string(stackDotEnv(c, "abc", LAN{})); !strings.Contains(env, "LINX_CERT_CHALLENGE=dns-01\n") {
		t.Errorf("with a token:\n%s", env)
	}
	bad := webCertConfig(FrontDoorPangolin, "192.168.1.20")
	bad.Version = 1
	bad.Certificates.Staging = false
	bad.Certificates.NoDNSToken = true
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "no_dns_token") {
		t.Errorf("no token behind Pangolin: %v", err)
	}
}
