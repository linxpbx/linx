package backup

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDestinationRepo(t *testing.T) {
	cases := []struct {
		d    Destination
		want string
	}{
		{Destination{Kind: KindLocal, Path: "/var/backups/linx"}, "/var/backups/linx"},
		{Destination{Kind: KindSFTP, User: "linx", Host: "nas.lan", RemotePath: "backups/linx"}, "sftp:linx@nas.lan:backups/linx"},
		{Destination{Kind: KindS3, Endpoint: "https://s3.example.com/", Bucket: "linx-backups"}, "s3:https://s3.example.com/linx-backups"},
	}
	for _, c := range cases {
		if got := c.d.Repo(); got != c.want {
			t.Errorf("Repo() = %q, want %q", got, c.want)
		}
	}
}

func TestDestinationSFTPCommandUsesBatchModeAndAcceptNew(t *testing.T) {
	d := Destination{Kind: KindSFTP, User: "linx", Host: "nas.lan", Port: 2222}
	cmd := d.sftpCommand("/keys/nas.sftp_key", "/keys/nas.known_hosts")
	for _, want := range []string{"-i /keys/nas.sftp_key", "-p 2222", "BatchMode=yes", "StrictHostKeyChecking=accept-new", "UserKnownHostsFile=/keys/nas.known_hosts", "linx@nas.lan", "-s sftp"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("sftpCommand() = %q, missing %q", cmd, want)
		}
	}
}

func TestDestinationSFTPPortDefaultsTo22(t *testing.T) {
	d := Destination{Kind: KindSFTP, Host: "nas.lan"}
	if got := d.sftpPort(); got != 22 {
		t.Fatalf("sftpPort() = %d, want 22", got)
	}
}

func TestManifestSaveLoadFind(t *testing.T) {
	m := Manifest{Dir: t.TempDir()}
	if dests, err := m.Load(); err != nil || dests != nil {
		t.Fatalf("Load() on an empty manifest = %v, %v, want nil, nil", dests, err)
	}
	dests := []Destination{
		{Name: "local", Kind: KindLocal, Path: "/var/backups/linx"},
		{Name: "nas", Kind: KindSFTP, Host: "nas.lan", User: "linx", RemotePath: "backups"},
	}
	if err := m.Save(dests); err != nil {
		t.Fatal(err)
	}
	got, err := m.Load()
	if err != nil || len(got) != 2 {
		t.Fatalf("Load() = %v, %v", got, err)
	}
	d, found, err := m.Find("nas")
	if err != nil || !found || d.Host != "nas.lan" {
		t.Fatalf("Find(nas) = %v, %v, %v", d, found, err)
	}
	if _, found, err := m.Find("nope"); err != nil || found {
		t.Fatalf("Find(nope) = found %v, err %v, want not found", found, err)
	}
}

func TestManifestTargetLocal(t *testing.T) {
	m := Manifest{Dir: t.TempDir()}
	d := Destination{Name: "local", Kind: KindLocal, Path: "/var/backups/linx"}
	if err := writeFile(t, m.PasswordPath("local"), "the-password"); err != nil {
		t.Fatal(err)
	}
	target, err := m.Target(d)
	if err != nil {
		t.Fatal(err)
	}
	if target.Repo != "/var/backups/linx" || target.Password != "the-password" {
		t.Fatalf("Target() = %+v", target)
	}
	if len(target.Env) != 0 || len(target.Extra) != 0 {
		t.Fatalf("a local destination shouldn't need Env or Extra: %+v", target)
	}
}

func TestManifestTargetSFTPSetsExtraSFTPCommand(t *testing.T) {
	m := Manifest{Dir: t.TempDir()}
	d := Destination{Name: "nas", Kind: KindSFTP, Host: "nas.lan", User: "linx", RemotePath: "backups"}
	if err := writeFile(t, m.PasswordPath("nas"), "pw"); err != nil {
		t.Fatal(err)
	}
	target, err := m.Target(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(target.Extra) != 2 || target.Extra[0] != "-o" {
		t.Fatalf("Target().Extra = %v", target.Extra)
	}
	if !strings.Contains(target.Extra[1], "sftp.command=") {
		t.Fatalf("Target().Extra[1] = %q, missing sftp.command", target.Extra[1])
	}
}

func TestManifestTargetS3SetsEnvFromSealedCredentials(t *testing.T) {
	m := Manifest{Dir: t.TempDir()}
	d := Destination{Name: "b2", Kind: KindS3, Endpoint: "https://s3.example.com", Bucket: "linx", Region: "us-east-1"}
	if err := writeFile(t, m.PasswordPath("b2"), "pw"); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveS3Credentials("b2", "AKID", "SECRET"); err != nil {
		t.Fatal(err)
	}
	target, err := m.Target(d)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"AWS_ACCESS_KEY_ID=AKID": false, "AWS_SECRET_ACCESS_KEY=SECRET": false, "AWS_DEFAULT_REGION=us-east-1": false}
	for _, e := range target.Env {
		want[e] = true
	}
	for k, v := range want {
		if !v {
			t.Fatalf("Target().Env missing %q: %v", k, target.Env)
		}
	}
}

func TestManifestTargetMissingPasswordIsAnError(t *testing.T) {
	m := Manifest{Dir: t.TempDir()}
	if _, err := m.Target(Destination{Name: "local", Kind: KindLocal, Path: "/x"}); err == nil {
		t.Fatal("Target() succeeded with no password file, want an error")
	}
}

func TestCheckDestinationHostRefusesLoopbackAndOwnNetworks(t *testing.T) {
	own := []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")}
	lookup := func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "loopback.test":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		case "own.test":
			return []netip.Addr{netip.MustParseAddr("172.20.0.5")}, nil
		case "nas.lan":
			return []netip.Addr{netip.MustParseAddr("192.168.1.50")}, nil
		default:
			return nil, errors.New("no such host")
		}
	}
	if err := CheckDestinationHost(context.Background(), lookup, own, "loopback.test"); err == nil {
		t.Fatal("loopback host should be refused")
	}
	if err := CheckDestinationHost(context.Background(), lookup, own, "own.test"); err == nil {
		t.Fatal("Linx's own network should be refused")
	}
	if err := CheckDestinationHost(context.Background(), lookup, own, "nas.lan"); err != nil {
		t.Fatalf("a private LAN address (a home NAS) should be allowed: %v", err)
	}
	if err := CheckDestinationHost(context.Background(), lookup, own, "nowhere.invalid"); err == nil {
		t.Fatal("an unresolvable host should be refused")
	}
}

func writeFile(t *testing.T, path, content string) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestCheckSFTP(t *testing.T) {
	for _, ok := range [][3]string{{"nas.home.arpa", "backup", "/srv/linx"}, {"192.168.1.10", "u_1.x", "backups/linx"}, {"[fd00::1]", "root", "/b"}} {
		if err := CheckSFTP(ok[0], ok[1], ok[2]); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	for _, bad := range [][3]string{
		{"-oProxyCommand=touch /tmp/x", "u", "/b"}, {"nas", "-oProxyCommand=x", "/b"}, {"nas home", "u", "/b"},
		{"nas", "u", "/b -oProxyCommand=x"}, {"nas", "u", ""}, {"nas", ".u", "/b"}, {"", "u", "/b"}, {"nas", "u", "/b\n"},
	} {
		if err := CheckSFTP(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
