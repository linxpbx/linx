package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"linxpbx.com/linx/internal/trunkcert"
)

// runTrunk adds, lists, tests and removes phone lines (docs/TRUNKS.md §10)
// by running the control plane's own trunk command inside its container,
// like runUser. Its input is passed through (docker exec -i), with a
// terminal when there is one, so `linx trunk add` can ask its questions
// and read the password without showing it.
func runTrunk(ctx context.Context, args []string, stdout, stderr io.Writer, env apiKeyEnv) int {
	if len(args) > 0 && args[0] == "cert" {
		return runTrunkCert(args[1:], ".", time.Now(), stdout, stderr)
	}
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx trunk changes the phone system's lines and must run as root. Try: sudo linx trunk list")
		return 1
	}
	dockerArgs := []string{"exec", "-i"}
	if env.terminal {
		dockerArgs = append(dockerArgs, "-t")
	}
	dockerArgs = append(dockerArgs, controlPlaneContainer, controlPlaneBinary, "trunk")
	args, err := pinFromHost(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	code, err := env.run(ctx, stdout, stderr, "docker", append(dockerArgs, args...)...)
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't run docker: %v\n", err)
		return 1
	}
	if code >= 125 {
		fmt.Fprintln(stderr, "\nLinx doesn't seem to be running. Check with: sudo linx doctor")
		return 1
	}
	return code
}

// pinFromHost reads `--pin FILE` here, on the server, and passes the
// certificate itself (--pin-pem, base64): the command runs inside the
// control plane's container, which can't see the server's files. A
// certificate is public, so it may show in the process list.
func pinFromHost(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		var file string
		switch {
		case a == "--pin" || a == "-pin":
			if i+1 >= len(args) {
				return nil, errors.New("--pin needs a certificate file")
			}
			i++
			file = args[i]
		case strings.HasPrefix(a, "--pin=") || strings.HasPrefix(a, "-pin="):
			_, file, _ = strings.Cut(a, "=")
		default:
			out = append(out, a)
			continue
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("Can't read the certificate to pin: %v", err)
		}
		out = append(out, "--pin-pem", base64.StdEncoding.EncodeToString(b))
	}
	return out, nil
}

const trunkCertUsage = `Usage: linx trunk cert ADDRESS [--out NAME]

Makes a certificate for a phone system on your network (a Grandstream UCM,
another Linx) to use for its encrypted link with Linx, naming exactly the
address Linx connects to (an IPv4 address or a name). Writes NAME.crt (the
certificate) and NAME.key (its private key) here; NAME defaults to
linx-line-ADDRESS. Upload both to the phone system's TLS settings, then:

  sudo linx trunk add --host ADDRESS --pin NAME.crt ...

Linx never keeps the key: delete NAME.key once it's uploaded.
`

// runTrunkCert is `linx trunk cert`: it runs here, not in the control
// plane, so the phone system's private key never enters Linx.
func runTrunkCert(args []string, dir string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trunk cert", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("out", "", "")
	var address string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		address, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil || address == "" || fs.NArg() > 0 {
		fmt.Fprint(stderr, trunkCertUsage)
		return 2
	}
	if *name == "" {
		*name = "linx-line-" + strings.TrimSuffix(address, ".")
	}
	if strings.ContainsAny(*name, "/\\") {
		fmt.Fprintln(stderr, "--out is a file name here, without a folder.")
		return 2
	}
	c, err := trunkcert.New(address, now)
	if err != nil {
		fmt.Fprintf(stderr, "Can't make a certificate for that: %v.\n", err)
		return 1
	}
	crt, key := filepath.Join(dir, *name+".crt"), filepath.Join(dir, *name+".key")
	for _, p := range []string{crt, key} {
		if _, err := os.Stat(p); err == nil {
			fmt.Fprintf(stderr, "%s already exists; move it away or choose another --out.\n", p)
			return 1
		}
	}
	// The key first, readable by its owner only, and never overwriting.
	if err := writeNew(key, c.KeyPEM, 0o600); err != nil {
		fmt.Fprintf(stderr, "Can't write %s: %v\n", key, err)
		return 1
	}
	if err := writeNew(crt, c.CertPEM, 0o644); err != nil {
		os.Remove(key)
		fmt.Fprintf(stderr, "Can't write %s: %v\n", crt, err)
		return 1
	}
	fmt.Fprintf(stdout, `Made a certificate for %[1]s, valid until %[2]s:
  %[3]s  the certificate
  %[4]s  its private key (keep it secret)
SHA-256 fingerprint: %[5]s

Next:
  1. Upload both files to the phone system's TLS settings (on a UCM: the
     SIP settings' TLS tab) and save.
  2. Connect it:  sudo linx trunk add --host %[1]s --pin %[3]s ...
     linx trunk add shows the fingerprint it sees: it must be the one above.
  3. Delete %[4]s: Linx doesn't need it.
`, strings.TrimSuffix(address, "."), c.NotAfter.Format("2 Jan 2006"), crt, key, c.Fingerprint)
	return 0
}

func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
