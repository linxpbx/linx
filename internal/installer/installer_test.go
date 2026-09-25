package installer

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/hostinfo"
)

// fakeRunner answers commands from a table keyed by "name arg1 arg2...".
// Unlisted commands fail as if the program didn't exist.
type fakeRunner struct {
	answers map[string]string // command -> output; prefix "!" means exit error
}

func (f *fakeRunner) Run(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	out, ok := f.answers[cmd]
	if !ok {
		return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	if rest, failed := strings.CutPrefix(out, "!"); failed {
		return []byte(rest), errors.New("exit status 1")
	}
	return []byte(out), nil
}

func dpkgStatus(pkg string) string {
	return "dpkg-query --show --showformat=${db:Status-Status} " + pkg
}

var ubuntu = hostinfo.Info{
	GOOS: "linux", OSID: "ubuntu", OSVersionID: "24.04", OSCodename: "noble", OSName: "Ubuntu 24.04.3 LTS",
	Arch: "amd64", CPUs: 8, MemBytes: 16 * gib, DiskFree: 200 * gib, DiskTotal: 250 * gib, RootDevice: "/dev/sda1",
}

func TestCheckHost(t *testing.T) {
	pi := hostinfo.Info{GOOS: "linux", OSID: "debian", OSVersionID: "12", Arch: "arm64", CPUs: 4,
		MemBytes: 3900 << 20, DiskFree: 20 * gib, DiskTotal: 29 * gib, Model: "Raspberry Pi 5", RootDevice: "/dev/mmcblk0p2"}
	pi32 := pi
	pi32.OSID, pi32.Arch = "raspbian", "arm"
	small := ubuntu
	small.MemBytes, small.DiskFree = 2*gib, 5*gib
	oldUbuntu := ubuntu
	oldUbuntu.OSVersionID = "22.04"
	mac := hostinfo.Info{GOOS: "darwin"}

	tests := []struct {
		name      string
		h         hostinfo.Info
		wantFail  bool
		wantWarns int
	}{
		{"ubuntu ok", ubuntu, false, 0},
		{"pi on sd card with little disk", pi, false, 2},
		{"32-bit pi os", pi32, true, 2},
		{"too small", small, true, 0},
		{"old ubuntu", oldUbuntu, true, 0},
		{"not linux", mac, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := CheckHost(tt.h)
			warns := 0
			for _, f := range fs {
				if f.Level == Warn {
					warns++
				}
			}
			if HasFailure(fs) != tt.wantFail || warns != tt.wantWarns {
				t.Errorf("fail=%v warns=%d, want fail=%v warns=%d: %+v", HasFailure(fs), warns, tt.wantFail, tt.wantWarns, fs)
			}
		})
	}
}

func TestSuggestProfile(t *testing.T) {
	tests := []struct {
		h    hostinfo.Info
		want string
	}{
		{hostinfo.Info{Model: "Raspberry Pi 5 Model B", CPUs: 4, MemBytes: 8 * gib}, ProfileLite},
		{hostinfo.Info{CPUs: 4, MemBytes: 16 * gib}, ProfileLite},
		{hostinfo.Info{CPUs: 8, MemBytes: 7800 << 20}, ProfileLite},
		{hostinfo.Info{CPUs: 6, MemBytes: 16 * gib}, ProfileStandard},
		{hostinfo.Info{CPUs: 16, MemBytes: 16 * gib}, ProfileStandard},
		{hostinfo.Info{CPUs: 8, MemBytes: 31 * gib}, ProfilePerformance},
	}
	for _, tt := range tests {
		if got, reason := SuggestProfile(tt.h); got != tt.want || reason == "" {
			t.Errorf("SuggestProfile(%d cpu, %d MiB) = %q (%q), want %q", tt.h.CPUs, tt.h.MemBytes>>20, got, reason, tt.want)
		}
	}
}

func TestVersionLess(t *testing.T) {
	tests := []struct {
		v    string
		want bool
	}{
		{"27.0.0", false}, {"28.1.1", false}, {"26.1.5", true}, {"20.10.24+dfsg1", true},
		{"27.5.1-0ubuntu3~24.04.2", false}, {"v2.29.7", true}, {"v2.30.3", false}, {"2.3", true},
	}
	for _, tt := range tests {
		min := MinEngine
		if strings.HasPrefix(strings.TrimPrefix(tt.v, "v"), "2.") {
			min = MinCompose
		}
		if got := versionLess(tt.v, min); got != tt.want {
			t.Errorf("versionLess(%q) = %v, want %v", tt.v, got, tt.want)
		}
	}
}

func TestDetectAndAssessDocker(t *testing.T) {
	tests := []struct {
		name    string
		answers map[string]string
		want    DockerAction
		source  string
	}{
		{"missing", map[string]string{}, DockerInstall, ""},
		{"official current", map[string]string{
			"docker version --format {{.Server.Version}}": "28.4.0",
			"docker compose version --short":              "2.39.2",
			dpkgStatus("docker-ce"):                       "installed",
		}, DockerReady, SourceOfficial},
		{"distro too old, no compose", map[string]string{
			"docker version --format {{.Server.Version}}": "20.10.24+dfsg1",
			"docker compose version --short":              "!docker: 'compose' is not a docker command.",
			dpkgStatus("docker-ce"):                       "!no packages found",
			dpkgStatus("docker.io"):                       "installed",
		}, DockerUpgrade, SourceDistro},
		{"daemon down", map[string]string{
			"docker version --format {{.Server.Version}}": "!Cannot connect to the Docker daemon",
			dpkgStatus("docker-ce"):                       "installed",
		}, DockerUpgrade, SourceOfficial},
		{"snap", map[string]string{
			"docker version --format {{.Server.Version}}": "28.1.1",
			"docker compose version --short":              "2.33.1",
			"snap list docker":                            "docker 28.1.1",
		}, DockerBlocked, SourceSnap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := DetectDocker(context.Background(), &fakeRunner{answers: tt.answers})
			got, msg := s.Assess()
			if got != tt.want || s.Source != tt.source || msg == "" {
				t.Errorf("got action %d source %q (%s), want %d %q", got, s.Source, msg, tt.want, tt.source)
			}
		})
	}
}

func TestDockerPlanReplacesDistroDocker(t *testing.T) {
	r := &fakeRunner{answers: map[string]string{
		"dpkg --print-architecture": "amd64\n",
		dpkgStatus("docker.io"):     "installed",
		dpkgStatus("containerd"):    "installed",
		dpkgStatus("runc"):          "installed",
		dpkgStatus("docker-doc"):    "!not-installed",
	}}
	p, err := DockerPlan(context.Background(), r, ubuntu)
	if err != nil {
		t.Fatal(err)
	}
	var cmds, files []string
	var sources string
	for _, s := range p {
		if s.Cmd != nil {
			cmds = append(cmds, s.Cmd.String())
		}
		if s.File != nil {
			files = append(files, s.File.Path)
			if strings.HasSuffix(s.File.Path, ".sources") {
				sources = string(s.File.Data)
			}
		}
	}
	for _, want := range []string{
		"apt-get remove -y docker.io containerd runc",
		"apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin",
		"useradd --system --user-group --home-dir /var/lib/linx --no-create-home --shell /usr/sbin/nologin linx",
		"usermod --append --groups docker linx",
		"docker run --rm --pull missing " + helloWorldImage,
	} {
		if !slices.Contains(cmds, want) {
			t.Errorf("plan is missing %q\nplan: %s", want, strings.Join(cmds, "\n"))
		}
	}
	if cmds[0] != "apt-get remove -y docker.io containerd runc" {
		t.Errorf("conflicting packages must be removed first, got %q", cmds[0])
	}
	if !slices.Contains(files, "/etc/apt/keyrings/docker.asc") {
		t.Errorf("plan doesn't install the signing key: %v", files)
	}
	for _, want := range []string{"URIs: https://download.docker.com/linux/ubuntu\n", "Suites: noble\n", "Architectures: amd64\n", "Signed-By: /etc/apt/keyrings/docker.asc\n"} {
		if !strings.Contains(sources, want) {
			t.Errorf("docker.sources missing %q:\n%s", want, sources)
		}
	}
}

func TestDockerPlanRejects32BitPackages(t *testing.T) {
	pi := hostinfo.Info{OSID: "debian", OSCodename: "bookworm"}
	r := &fakeRunner{answers: map[string]string{"dpkg --print-architecture": "armhf"}}
	if _, err := DockerPlan(context.Background(), r, pi); err == nil {
		t.Fatal("expected an error for armhf")
	}
}

// TestDockerKeyFingerprint checks the embedded key is Docker's published key.
func TestDockerKeyFingerprint(t *testing.T) {
	const want = "9DC858229FC7DD38854AE2D88D81803C0EBFCD88"
	if got := primaryKeyFingerprint(t, dockerKey); got != want {
		t.Fatalf("embedded Docker key fingerprint = %s, want %s", got, want)
	}
}

// primaryKeyFingerprint computes the OpenPGP v4 fingerprint of the first
// packet (the primary public key) in an ASCII-armoured key: SHA-1 over
// 0x99, a two-byte length and the packet body (RFC 4880 §12.2).
func primaryKeyFingerprint(t *testing.T, armored []byte) string {
	t.Helper()
	var b64 strings.Builder
	inBody := false
	for _, line := range strings.Split(string(armored), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "-----BEGIN"):
			continue
		case !inBody && line == "":
			inBody = true
		case strings.HasPrefix(line, "=") || strings.HasPrefix(line, "-----END"):
			inBody = false
		case inBody:
			b64.WriteString(line)
		}
	}
	data, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		t.Fatal(err)
	}
	tag := data[0]
	if tag&0x80 == 0 {
		t.Fatal("not an OpenPGP packet")
	}
	var body []byte
	if tag&0x40 == 0 { // old format
		if (tag>>2)&0x0f != 6 {
			t.Fatalf("first packet tag %d, want 6 (public key)", (tag>>2)&0x0f)
		}
		switch tag & 3 {
		case 0:
			body = data[2 : 2+int(data[1])]
		case 1:
			body = data[3 : 3+(int(data[1])<<8|int(data[2]))]
		default:
			t.Fatal("unsupported packet length type")
		}
	} else {
		t.Fatal("new-format packet headers not supported by this test")
	}
	h := sha1.New()
	h.Write([]byte{0x99, byte(len(body) >> 8), byte(len(body))})
	h.Write(body)
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
}

func TestPortainerPlan(t *testing.T) {
	s := PortainerPlan(netip.MustParseAddr("192.168.1.20"))
	if s.URL != "https://192.168.1.20:9443" || s.LocalOnly || len(s.Password) < 20 {
		t.Errorf("unexpected setup: %+v", s)
	}
	var compose string
	for _, st := range s.Plan {
		if st.File != nil && strings.HasSuffix(st.File.Path, "compose.yaml") {
			compose = string(st.File.Data)
		}
		if st.File != nil && strings.Contains(st.File.Path, "secrets") && st.File.Mode != 0o600 {
			t.Errorf("password file mode = %v, want 0600", st.File.Mode)
		}
	}
	for _, want := range []string{`"192.168.1.20:9443:9443"`, "@sha256:", "no-new-privileges:true", "--admin-password-file"} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose.yaml missing %q:\n%s", want, compose)
		}
	}
	if local := PortainerPlan(netip.MustParseAddr("127.0.0.1")); !local.LocalOnly {
		t.Error("loopback bind should be LocalOnly")
	}
}

func TestLANOrLoopback(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.20": "192.168.1.20", "10.0.0.5": "10.0.0.5", "172.16.3.4": "172.16.3.4",
		"203.0.113.7": "127.0.0.1", "100.64.1.2": "127.0.0.1",
	} {
		if got := lanOrLoopback(netip.MustParseAddr(in)).String(); got != want {
			t.Errorf("lanOrLoopback(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	c := Config{Version: 1, Docker: DockerConfig{Install: true}, ContainerUI: ContainerUIPortainer, ResourceProfile: ProfileLite,
		Domain:       DomainConfig{Name: "lab.linxpbx.com", DNSProvider: DNSCloudflare},
		Certificates: CertificateConfig{Staging: false, Wildcard: false, Email: "ops+pbx@example.com"},
		FrontDoor:    FrontDoorConfig{Kind: FrontDoorPangolin, ProxyAddress: "192.168.1.30"}}
	got, err := ParseConfig(bytes.NewReader(c.Marshal()))
	if err != nil {
		t.Fatal(err)
	}
	if got != c {
		t.Errorf("round trip = %+v, want %+v", got, c)
	}
}

func TestParseConfigErrors(t *testing.T) {
	for _, in := range []string{
		"version: 2\n",
		"version: 1\ncontainer_ui: dockge\n",
		"version: 1\nresource_profile: huge\n",
		"version: 1\ncontainer-ui: none\n", // typo'd key
		"version: 1\ndomain:\n  name: '*.example.com'\n",
		"version: 1\ndomain:\n  name: example.com\n  dns_provider: route53\n",
		"version: 1\ndomain:\n  name: example.com\n  dns_provider: duckdns\n",
		"version: 1\ndomain:\n  name: a.b.duckdns.org\n  dns_provider: duckdns\n",
		"version: 1\ncertificates:\n  staging: false\n",                  // production needs an email
		"version: 1\ncertificates:\n  email: \"a'b@example.com\"\n",      // unsafe in .env
		"version: 1\ncertificates:\n  email: \"ops@example.com$HOME\"\n", // unsafe in .env
	} {
		if _, err := ParseConfig(strings.NewReader(in)); err == nil {
			t.Errorf("ParseConfig(%q) succeeded, want error", in)
		}
	}
	c, err := ParseConfig(strings.NewReader("version: 1\n"))
	if err != nil || c != DefaultConfig() {
		t.Errorf("defaults: %+v, %v", c, err)
	}
	c, err = ParseConfig(strings.NewReader("version: 1\ndomain:\n  name: Me.DuckDNS.org\n  dns_provider: duckdns\n"))
	if err != nil || c.Domain.Name != "me.duckdns.org" {
		t.Errorf("duckdns: %+v, %v", c.Domain, err)
	}
}

func TestExecuteWritesFilesAndStopsOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "secret")
	r := &fakeRunner{answers: map[string]string{"true": "", "false": "!line1\nboom"}}
	p := Plan{
		fileStep("write", path, []byte("pw"), 0o600, 0o700),
		cmdStep("ok", "true"),
		cmdStep("fails", "false"),
		cmdStep("never", "true"),
	}
	var titles []string
	err := p.Execute(context.Background(), r, func(s Step) { titles = append(titles, s.Title) })

	var se *StepError
	if !errors.As(err, &se) || se.Step.Title != "fails" || !strings.Contains(se.Output, "boom") {
		t.Fatalf("err = %v", err)
	}
	if fmt.Sprint(titles) != "[write ok fails]" {
		t.Errorf("ran %v", titles)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("file: %v %v", st, err)
	}
}

func TestExampleConfigParses(t *testing.T) {
	f, err := os.Open("../../deploy/setup.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := ParseConfig(f); err != nil {
		t.Fatal(err)
	}
}
