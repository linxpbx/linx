package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"linxpbx.com/linx/internal/trunkprobe"
)

// Kind is which restic backend a Destination uses (docs/BACKUP.md §3).
type Kind string

const (
	KindLocal Kind = "local"
	KindSFTP  Kind = "sftp"
	KindS3    Kind = "s3"
)

// Destination is one place a backup goes. Its own repository password and
// (for sftp/s3) credentials are separate files alongside the manifest —
// never inside it, the same reasoning as Docker secrets always being
// their own file (docs/BACKUP.md §7).
type Destination struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`

	// Local
	Path string `json:"path,omitempty"`

	// SFTP
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"` // 0 means 22
	User       string `json:"user,omitempty"`
	RemotePath string `json:"remote_path,omitempty"`

	// S3-compatible (AWS S3, Backblaze B2, MinIO, Cloudflare R2, ...)
	Endpoint string `json:"endpoint,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	Region   string `json:"region,omitempty"`
}

// Repo returns the restic repository string for d (docs/BACKUP.md §3).
func (d Destination) Repo() string {
	switch d.Kind {
	case KindSFTP:
		return fmt.Sprintf("sftp:%s@%s:%s", d.User, d.Host, d.RemotePath)
	case KindS3:
		return "s3:" + strings.TrimRight(d.Endpoint, "/") + "/" + d.Bucket
	default:
		return d.Path
	}
}

// sftpPort returns d's port, defaulting to 22.
func (d Destination) sftpPort() int {
	if d.Port > 0 {
		return d.Port
	}
	return 22
}

// sftpCommand is the "-o sftp.command=..." restic uses to connect with a
// specific identity file, instead of relying on root's own ~/.ssh (which
// this Destination doesn't have any relationship to). BatchMode refuses to
// ever prompt (an unattended backup must fail loudly, not hang);
// accept-new records a new host key on first connect and refuses if it
// ever changes afterwards (TOFU — trust on first use).
func (d Destination) sftpCommand(keyFile, knownHostsFile string) string {
	return fmt.Sprintf("ssh -i %s -p %d -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s %s@%s -s sftp",
		keyFile, d.sftpPort(), knownHostsFile, d.User, d.Host)
}

// s3Credentials is what's sealed in a Destination's own credentials file.
type s3Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// destinationsFile is the manifest listing every configured Destination —
// no secrets in it, only what's needed to build a repository string.
const destinationsFile = "destinations.json"

// Manifest is where Destinations and their credentials live on disk.
type Manifest struct {
	// Dir is the manifest's directory (e.g. installer.SecretsDir +
	// "/linx-backup"), root-only.
	Dir string
}

func (m Manifest) manifestPath() string            { return filepath.Join(m.Dir, destinationsFile) }
func (m Manifest) PasswordPath(name string) string { return filepath.Join(m.Dir, name+".password") }
func (m Manifest) SFTPKeyPath(name string) string  { return filepath.Join(m.Dir, name+".sftp_key") }
func (m Manifest) SFTPPublicKeyPath(name string) string {
	return filepath.Join(m.Dir, name+".sftp_key.pub")
}
func (m Manifest) SFTPKnownHostsPath(name string) string {
	return filepath.Join(m.Dir, name+".sftp_known_hosts")
}
func (m Manifest) S3CredentialsPath(name string) string {
	return filepath.Join(m.Dir, name+".s3_credentials")
}

// Load returns every configured destination, or none if the manifest
// doesn't exist yet.
func (m Manifest) Load() ([]Destination, error) {
	b, err := os.ReadFile(m.manifestPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Destination
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("reading %s: %w", m.manifestPath(), err)
	}
	return out, nil
}

// Save writes the whole list of destinations.
func (m Manifest) Save(dests []Destination) error {
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(dests, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.manifestPath(), b, 0o600)
}

// Find returns the named destination, or false if there's none by that name.
func (m Manifest) Find(name string) (Destination, bool, error) {
	dests, err := m.Load()
	if err != nil {
		return Destination{}, false, err
	}
	for _, d := range dests {
		if d.Name == name {
			return d, true, nil
		}
	}
	return Destination{}, false, nil
}

// SaveS3Credentials seals name's S3 access key and secret in their own file.
func (m Manifest) SaveS3Credentials(name, accessKeyID, secretAccessKey string) error {
	b, err := json.Marshal(s3Credentials{AccessKeyID: accessKeyID, SecretAccessKey: secretAccessKey})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(m.S3CredentialsPath(name), b, 0o600)
}

// Target builds the restic Target for d: its password (read from disk;
// generated by the caller when d is first added), and, for sftp/s3, the
// extra options or environment its backend needs.
func (m Manifest) Target(d Destination) (Target, error) {
	pw, err := os.ReadFile(m.PasswordPath(d.Name))
	if err != nil {
		return Target{}, fmt.Errorf("reading %s's repository password: %w", d.Name, err)
	}
	t := Target{Repo: d.Repo(), Password: strings.TrimSpace(string(pw))}
	switch d.Kind {
	case KindSFTP:
		cmd := d.sftpCommand(m.SFTPKeyPath(d.Name), m.SFTPKnownHostsPath(d.Name))
		t.Extra = []string{"-o", "sftp.command=" + cmd}
	case KindS3:
		b, err := os.ReadFile(m.S3CredentialsPath(d.Name))
		if err != nil {
			return Target{}, fmt.Errorf("reading %s's credentials: %w", d.Name, err)
		}
		var creds s3Credentials
		if err := json.Unmarshal(b, &creds); err != nil {
			return Target{}, fmt.Errorf("reading %s's credentials: %w", d.Name, err)
		}
		t.Env = []string{"AWS_ACCESS_KEY_ID=" + creds.AccessKeyID, "AWS_SECRET_ACCESS_KEY=" + creds.SecretAccessKey}
		if d.Region != "" {
			t.Env = append(t.Env, "AWS_DEFAULT_REGION="+d.Region)
		}
	}
	return t, nil
}

// Lookup resolves host to its addresses (net.DefaultResolver.LookupNetIP
// in real use; a fake in tests).
type Lookup func(ctx context.Context, host string) ([]netip.Addr, error)

// DefaultLookup is Lookup's real implementation.
func DefaultLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// CheckDestinationHost refuses a host that resolves to an address that can
// never legitimately be a backup destination: loopback, unspecified,
// multicast, link-local, or one of Linx's own container networks
// (docs/BACKUP.md §7). A private LAN address — a home NAS, the most common
// SFTP destination — is deliberately allowed: only the categories no
// destination could ever really be at are refused (the same distinction
// made for a phone line's own address, internal/trunk.ownAddressReason).
func CheckDestinationHost(ctx context.Context, lookup Lookup, own []netip.Prefix, host string) error {
	refuse := trunkprobe.RefuseOwn(own)
	addrs, err := lookup(ctx, host)
	if err != nil {
		return fmt.Errorf("looking up %s: %w", host, err)
	}
	for _, a := range addrs {
		if reason := refuse(a.Unmap()); reason != "" {
			return fmt.Errorf("%s can't be a backup destination: %s", host, reason)
		}
	}
	return nil
}
