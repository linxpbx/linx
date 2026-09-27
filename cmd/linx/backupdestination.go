package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"

	"golang.org/x/crypto/ssh"

	"linxpbx.com/linx/internal/backup"
)

const backupDestinationUsage = backupUsage

// runBackupDestination implements `linx backup destination add|list|remove`.
func runBackupDestination(ctx context.Context, args []string, stdout, stderr io.Writer, env backupEnv) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, backupDestinationUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx backup destination must run as root. Try: sudo linx backup destination ...")
		return 1
	}
	switch sub {
	case "add":
		return runBackupDestinationAdd(ctx, rest, stdout, stderr, env)
	case "list":
		return runBackupDestinationList(rest, stdout, stderr, env)
	case "remove":
		return runBackupDestinationRemove(rest, stdout, stderr, env)
	default:
		fmt.Fprint(stderr, backupDestinationUsage)
		return 2
	}
}

func runBackupDestinationAdd(ctx context.Context, args []string, stdout, stderr io.Writer, env backupEnv) int {
	fs := flag.NewFlagSet("backup destination add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, backupDestinationUsage) }
	kind := fs.String("kind", "", "")
	path := fs.String("path", "", "")
	host := fs.String("host", "", "")
	port := fs.Int("port", 0, "")
	user := fs.String("user", "", "")
	remotePath := fs.String("remote-path", "", "")
	endpoint := fs.String("endpoint", "", "")
	bucket := fs.String("bucket", "", "")
	region := fs.String("region", "", "")
	accessKeyID := fs.String("access-key-id", "", "")
	secretAccessKey := fs.String("secret-access-key", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, backupDestinationUsage)
		return 2
	}
	name := fs.Arg(0)

	m := env.destinationsManifest()
	if _, found, err := m.Find(name); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	} else if found {
		fmt.Fprintf(stderr, "There's already a backup destination named %q.\n", name)
		return 1
	}

	d := backup.Destination{Name: name, Kind: backup.Kind(*kind)}
	var hostToCheck string
	switch d.Kind {
	case backup.KindLocal:
		if *path == "" {
			fmt.Fprintln(stderr, "A local destination needs --path.")
			return 1
		}
		d.Path = *path
	case backup.KindSFTP:
		if *host == "" || *user == "" || *remotePath == "" {
			fmt.Fprintln(stderr, "An sftp destination needs --host, --user and --remote-path.")
			return 1
		}
		d.Host, d.Port, d.User, d.RemotePath = *host, *port, *user, *remotePath
		hostToCheck = *host
	case backup.KindS3:
		if *endpoint == "" || *bucket == "" || *accessKeyID == "" || *secretAccessKey == "" {
			fmt.Fprintln(stderr, "An s3 destination needs --endpoint, --bucket, --access-key-id and --secret-access-key.")
			return 1
		}
		d.Endpoint, d.Bucket, d.Region = *endpoint, *bucket, *region
		u, err := url.Parse(*endpoint)
		if err != nil || u.Hostname() == "" {
			fmt.Fprintf(stderr, "%q isn't a valid endpoint URL.\n", *endpoint)
			return 1
		}
		hostToCheck = u.Hostname()
	default:
		fmt.Fprintln(stderr, "--kind must be one of: local, sftp, s3.")
		return 1
	}

	if hostToCheck != "" {
		own, err := env.ownNetworks()
		if err != nil {
			fmt.Fprintf(stderr, "Couldn't check that address: %v\n", err)
			return 1
		}
		if err := backup.CheckDestinationHost(ctx, env.lookup, own, hostToCheck); err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		}
	}

	password, err := backup.NewPassword()
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if err := os.WriteFile(m.PasswordPath(name), []byte(password), 0o600); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	var sftpPublicKey string
	if d.Kind == backup.KindSFTP {
		pub, err := writeSFTPKeypair(m, name)
		if err != nil {
			fmt.Fprintf(stderr, "Couldn't generate an SSH key: %v\n", err)
			return 1
		}
		sftpPublicKey = pub
		if err := os.WriteFile(m.SFTPKnownHostsPath(name), nil, 0o600); err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		}
	}
	if d.Kind == backup.KindS3 {
		if err := m.SaveS3Credentials(name, *accessKeyID, *secretAccessKey); err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		}
	}

	dests, err := m.Load()
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	dests = append(dests, d)
	if err := m.Save(dests); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Added backup destination %q (%s).\n\n", name, d.Kind)
	fmt.Fprintf(stdout, "Its repository password (shown once — save it somewhere safe, separate from the backup itself; without it, this destination's backups can never be read back):\n\n  %s\n\n", password)
	if sftpPublicKey != "" {
		fmt.Fprintf(stdout, "Add this public key to %s's authorized_keys for %s (shown once):\n\n  %s\n", *host, *user, sftpPublicKey)
	}
	return 0
}

func runBackupDestinationList(args []string, stdout, stderr io.Writer, env backupEnv) int {
	if len(args) > 0 {
		fmt.Fprint(stderr, backupDestinationUsage)
		return 2
	}
	dests, err := env.destinationsManifest().Load()
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if len(dests) == 0 {
		fmt.Fprintf(stdout, "No backup destinations configured. Backing up goes to %s only.\n", defaultBackupRepo)
		return 0
	}
	for _, d := range dests {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", d.Name, d.Kind, d.Repo())
	}
	return 0
}

func runBackupDestinationRemove(args []string, stdout, stderr io.Writer, env backupEnv) int {
	fs := flag.NewFlagSet("backup destination remove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, backupDestinationUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, backupDestinationUsage)
		return 2
	}
	name := fs.Arg(0)

	m := env.destinationsManifest()
	dests, err := m.Load()
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	i := slices.IndexFunc(dests, func(d backup.Destination) bool { return d.Name == name })
	if i < 0 {
		fmt.Fprintf(stderr, "No backup destination named %q.\n", name)
		return 1
	}
	d := dests[i]
	dests = slices.Delete(dests, i, i+1)
	if err := m.Save(dests); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	for _, p := range []string{m.PasswordPath(name), m.SFTPKeyPath(name), m.SFTPPublicKeyPath(name), m.SFTPKnownHostsPath(name), m.S3CredentialsPath(name)} {
		_ = os.Remove(p)
	}
	fmt.Fprintf(stdout, "Removed backup destination %q (%s). Its own backed-up snapshots are untouched — only the credentials to reach them from this server are gone.\n", name, d.Kind)
	return 0
}

// writeSFTPKeypair generates a new ed25519 keypair for name, writes the
// private key (OpenSSH format, no passphrase — it's already only readable
// by root, the same trust as any other Docker secret) and returns the
// public key line to hand to the remote server's admin.
func writeSFTPKeypair(m backup.Manifest, name string) (publicKeyLine string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(m.SFTPKeyPath(name), pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	line := ssh.MarshalAuthorizedKey(sshPub)
	if err := os.WriteFile(m.SFTPPublicKeyPath(name), line, 0o600); err != nil {
		return "", err
	}
	if len(line) == 0 {
		return "", errors.New("empty public key")
	}
	return string(line), nil
}
