package certs

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/tlsalpn01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"

	"linxpbx.com/linx/internal/dnsname"
)

// The web install's first certificate (docs/INSTALL.md §4, ADR-058): before
// there's a DNS token, Let's Encrypt checks the names by connecting to them
// on port 443 with the acme-tls/1 protocol (TLS-ALPN-01). certd makes the
// challenge certificate and leaves it in ChallengeDir; the control plane in
// install mode, which is what port 443 reaches through every front door,
// hands it out (install.Challenges). Staging goes first: a staging success
// proves DNS and port 443 work without using up the real service's limit
// of failed tries.

// BootstrapHosts are the names the first certificate covers, each a DNS
// record the owner adds by hand: meet. (the web app) and turn. (calls
// through firewalls, which front doors tell apart from the web app by
// name on the same port 443). api. is the same server as meet. and isn't
// needed to finish setting up; sip. points at the home network, where Let's
// Encrypt can't reach it. Both come with the wildcard, once there's a DNS
// token (and Linx makes their records itself).
var BootstrapHosts = []string{"meet", "turn"}

// ACMETLS1 is the protocol Let's Encrypt's check asks for.
const ACMETLS1 = tlsalpn01.ACMETLS1Protocol

// ChallengeFile is where a name's challenge certificate and key are, in PEM.
func ChallengeFile(dir, name string) string { return filepath.Join(dir, name+".pem") }

// Bootstrap is certd -bootstrap's configuration.
type Bootstrap struct {
	Domain   string
	Email    string
	CertsDir string
	StateDir string
	// ChallengeDir is shared with the control plane (a tmpfs volume).
	ChallengeDir string
	// Directory replaces both Let's Encrypt directories: tests only
	// (Pebble). The CA's own certificate then comes from
	// LEGO_CA_CERTIFICATES.
	Directory string
}

// BootstrapFromEnv reads the configuration from the environment.
func BootstrapFromEnv(getenv func(string) string) (Bootstrap, error) {
	b := Bootstrap{
		Domain:       strings.ToLower(strings.TrimSpace(getenv("LINX_DOMAIN"))),
		Email:        strings.TrimSpace(getenv("LINX_ACME_EMAIL")),
		CertsDir:     envOr(getenv, "LINX_CERTS_DIR", "/var/lib/linx/certs"),
		StateDir:     envOr(getenv, "LINX_STATE_DIR", "/var/lib/linx/state"),
		ChallengeDir: envOr(getenv, "LINX_CHALLENGE_DIR", "/var/lib/linx/acme-challenge"),
		Directory:    strings.TrimSpace(getenv("LINX_ACME_TEST_DIRECTORY")),
	}
	var errs []error
	if err := dnsname.ValidDomain(b.Domain); err != nil {
		errs = append(errs, fmt.Errorf("LINX_DOMAIN: %w", err))
	}
	if !emailRE.MatchString(b.Email) {
		errs = append(errs, fmt.Errorf("LINX_ACME_EMAIL: %q is not an email address", b.Email))
	}
	if b.Directory != "" && !strings.HasPrefix(b.Directory, "https://") {
		errs = append(errs, errors.New("LINX_ACME_TEST_DIRECTORY: must be an https:// URL"))
	}
	return b, errors.Join(errs...)
}

// Names are the first certificate's names.
func (b Bootstrap) Names() []string {
	names := make([]string, len(BootstrapHosts))
	for i, h := range BootstrapHosts {
		names[i] = h + "." + b.Domain
	}
	return names
}

// BootstrapResult is certd -bootstrap's one line on stdout, under
// ResultKey (the logs go to stderr), read by linx setup.
type BootstrapResult struct {
	OK     bool   `json:"ok"`
	Kind   string `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// ResultKey marks the result line among whatever else is printed.
const ResultKey = "linx_certd_result"

// ParseBootstrapResult finds certd -bootstrap's result in its output: nil
// for success, a *BootstrapError otherwise; found is false when there's
// no result line at all (certd didn't get that far).
func ParseBootstrapResult(out []byte) (found bool, err error) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var m map[string]BootstrapResult
		if !strings.Contains(lines[i], ResultKey) || json.Unmarshal([]byte(strings.TrimSpace(lines[i])), &m) != nil {
			continue
		}
		r, ok := m[ResultKey]
		if !ok {
			continue
		}
		if r.OK {
			return true, nil
		}
		return true, &BootstrapError{Kind: r.Kind, Detail: r.Detail}
	}
	return false, nil
}

// Problem kinds, for the install page's plain words.
const (
	ProblemConnection  = "connection"   // Let's Encrypt couldn't connect to port 443
	ProblemDNS         = "dns"          // it couldn't find the name
	ProblemWrongAnswer = "wrong_answer" // something other than Linx answered
	ProblemRateLimited = "rate_limited"
	ProblemOther       = "other"
)

// BootstrapError is a failed try, with its kind and Let's Encrypt's own
// words.
type BootstrapError struct {
	Kind   string
	Detail string
}

func (e *BootstrapError) Error() string { return e.Kind + ": " + e.Detail }

var problemRE = regexp.MustCompile(`urn:ietf:params:acme:error:([A-Za-z]+) :: ([^,]*)`)

// Classify turns an ACME error into a BootstrapError.
func Classify(err error) *BootstrapError {
	if err == nil {
		return nil
	}
	var be *BootstrapError
	if errors.As(err, &be) {
		return be
	}
	typ, detail := "", err.Error()
	var pd *acme.ProblemDetails
	if errors.As(err, &pd) {
		typ, detail = strings.TrimPrefix(pd.Type, "urn:ietf:params:acme:error:"), pd.Detail
		// A failed check reports the real reason as a subproblem.
		for _, sp := range pd.SubProblems {
			typ, detail = strings.TrimPrefix(sp.Type, "urn:ietf:params:acme:error:"), sp.Detail
			break
		}
	} else if m := problemRE.FindStringSubmatch(detail); m != nil {
		typ, detail = m[1], strings.TrimSpace(m[2])
	}
	kind := ProblemOther
	switch typ {
	case "connection", "incorrectResponse":
		kind = ProblemConnection
		if strings.Contains(detail, "acmeIdentifier") || strings.Contains(detail, "ALPN") ||
			strings.Contains(detail, "self-signed") || strings.Contains(detail, "certificate") {
			kind = ProblemWrongAnswer
		}
	case "tls", "unauthorized":
		kind = ProblemWrongAnswer
	case "dns":
		kind = ProblemDNS
	case "rateLimited":
		kind = ProblemRateLimited
	}
	if len(detail) > 500 {
		detail = detail[:500] + "…"
	}
	return &BootstrapError{Kind: kind, Detail: detail}
}

// Obtain gets the first certificate by TLS-ALPN-01. With staging it only
// proves the names are reachable (the certificate isn't kept); otherwise
// it's deployed to CertsDir, where the control plane picks it up on the
// next handshake.
func (b Bootstrap) Obtain(ctx context.Context, staging bool, now time.Time) error {
	id, dir := IssuerLE, lego.LEDirectoryProduction
	if staging {
		id, dir = IssuerLEStaging, lego.LEDirectoryStaging
	}
	if b.Directory != "" {
		dir = b.Directory
	}
	acct, err := loadAccount(filepath.Join(b.StateDir, "accounts", id), b.Email)
	if err != nil {
		return err
	}
	res, err := b.obtain(ctx, dir, acct)
	var pd *acme.ProblemDetails
	if errors.As(err, &pd) && pd.Type == "urn:ietf:params:acme:error:accountDoesNotExist" {
		// The account saved here is gone at the CA (it was reset, or it's
		// a different CA's): make a new one, once, with a new client (the
		// old one signs as the old account).
		acct.Registration = nil
		res, err = b.obtain(ctx, dir, acct)
	}
	if err != nil {
		return err
	}
	if staging {
		return nil
	}
	leaf, err := parseLeaf(res.Certificate)
	if err != nil {
		return err
	}
	return Store{Dir: b.CertsDir}.Deploy(res.Certificate, res.PrivateKey, Meta{
		Names: b.Names(), Issuer: id, NotAfter: leaf.NotAfter, IssuedAt: now.UTC(),
	})
}

func (b Bootstrap) obtain(ctx context.Context, dir string, acct *account) (*certificate.Resource, error) {
	lc := lego.NewConfig(acct)
	lc.CADirURL = dir
	lc.UserAgent = "linx-certd"
	lc.Certificate.KeyType = certcrypto.EC256
	client, err := lego.NewClient(lc)
	if err != nil {
		return nil, err
	}
	if err := client.Challenge.SetTLSALPN01Provider(ChallengeWriter{Dir: b.ChallengeDir}); err != nil {
		return nil, err
	}
	if acct.Registration == nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// The Subscriber Agreement was ticked on the install page.
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, fmt.Errorf("registering account: %w", err)
		}
		acct.Registration = reg
		if err := acct.save(); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return client.Certificate.Obtain(certificate.ObtainRequest{Domains: b.Names(), Bundle: true})
}

// ChallengeWriter is lego's TLS-ALPN-01 provider: it leaves each name's
// challenge certificate in Dir for the control plane to hand out.
type ChallengeWriter struct{ Dir string }

// Present writes domain's challenge certificate.
func (c ChallengeWriter) Present(domain, _, keyAuth string) error {
	certPEM, keyPEM, err := tlsalpn01.ChallengeBlocks(domain, keyAuth)
	if err != nil {
		return err
	}
	if err := dnsname.ValidDomain(domain); err != nil {
		return err
	}
	path := ChallengeFile(c.Dir, domain)
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	if err := writeSynced(tmp, append(keyPEM, certPEM...), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CleanUp removes it.
func (c ChallengeWriter) CleanUp(domain, _, _ string) error {
	err := os.Remove(ChallengeFile(c.Dir, domain))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Challenges hands out the challenge certificates certd leaves in Dir.
type Challenges struct{ Dir string }

// IsChallenge reports whether a handshake is Let's Encrypt's check: it
// offers acme-tls/1 and nothing else (RFC 8737 §3).
func IsChallenge(hello *tls.ClientHelloInfo) bool {
	return len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == ACMETLS1
}

// ErrNoChallenge means there's no check under way for that name.
var ErrNoChallenge = errors.New("no certificate check under way for that name")

// Certificate is the challenge certificate for name, if certd has one.
func (c Challenges) Certificate(name string) (*tls.Certificate, error) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if dnsname.ValidDomain(name) != nil {
		return nil, ErrNoChallenge
	}
	b, err := os.ReadFile(ChallengeFile(c.Dir, name))
	if err != nil {
		return nil, ErrNoChallenge
	}
	cert, err := tls.X509KeyPair(b, b)
	if err != nil {
		return nil, fmt.Errorf("challenge certificate for %s: %w", name, err)
	}
	return &cert, nil
}
