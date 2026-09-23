package certs

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/duckdns"
	"github.com/go-acme/lego/v4/registration"
)

// Issuer IDs, used in logs, metrics and meta.json.
const (
	IssuerLEStaging = "letsencrypt-staging"
	IssuerLE        = "letsencrypt"
	IssuerZeroSSL   = "zerossl"
)

const (
	// propagationTimeout is how long to wait for the DNS-01 record to appear
	// at every resolver; pollInterval is how often to look.
	propagationTimeout = 10 * time.Minute
	pollInterval       = 10 * time.Second
	// settleDelay is an extra wait after every resolver sees the record, because
	// Let's Encrypt checks from several places whose caches we can't query.
	settleDelay = time.Minute
)

const (
	zeroSSLDirectory = "https://acme.zerossl.com/v2/DV90"
	zeroSSLEABURL    = "https://api.zerossl.com/acme/eab-credentials-email"
)

// Issued is a certificate chain and its private key, both PEM.
type Issued struct {
	Chain []byte
	Key   []byte
}

// Issuer obtains a certificate for names from one CA.
type Issuer interface {
	ID() string
	Obtain(ctx context.Context, names []string) (*Issued, error)
}

// Issuers returns the CAs to try, in order: Let's Encrypt staging alone, or
// Let's Encrypt production then ZeroSSL.
func Issuers(c Config, dns challenge.Provider) []Issuer {
	if c.Staging {
		return []Issuer{&acmeIssuer{id: IssuerLEStaging, dirURL: lego.LEDirectoryStaging, cfg: c, dns: dns}}
	}
	return []Issuer{
		&acmeIssuer{id: IssuerLE, dirURL: lego.LEDirectoryProduction, cfg: c, dns: dns},
		&acmeIssuer{id: IssuerZeroSSL, dirURL: zeroSSLDirectory, cfg: c, dns: dns, eabURL: zeroSSLEABURL},
	}
}

// DNSProvider builds the DNS-01 provider, reading tokens from secret files.
func DNSProvider(c Config) (challenge.Provider, error) {
	token, err := readSecret(c.TokenFile)
	if err != nil {
		return nil, err
	}
	switch c.Provider {
	case ProviderCloudflare:
		pc := cloudflare.NewDefaultConfig()
		pc.AuthToken = token
		pc.PropagationTimeout, pc.PollingInterval = propagationTimeout, pollInterval
		if c.ZoneTokenFile != "" {
			if pc.ZoneToken, err = readSecret(c.ZoneTokenFile); err != nil {
				return nil, err
			}
		}
		return cloudflare.NewDNSProviderConfig(pc)
	case ProviderDuckDNS:
		pc := duckdns.NewDefaultConfig()
		pc.Token = token
		pc.PropagationTimeout, pc.PollingInterval = propagationTimeout, pollInterval
		return duckdns.NewDNSProviderConfig(pc)
	}
	return nil, fmt.Errorf("unsupported DNS provider %q", c.Provider)
}

type acmeIssuer struct {
	id     string
	dirURL string
	eabURL string // set for CAs that need external account binding
	cfg    Config
	dns    challenge.Provider
}

func (a *acmeIssuer) ID() string { return a.id }

// settle wraps lego's propagation check: once the record is visible
// everywhere, it waits once more before Let's Encrypt is asked to look.
func settle(d time.Duration, sleep func(time.Duration)) dns01.WrapPreCheckFunc {
	return func(_, fqdn, value string, check dns01.PreCheckFunc) (bool, error) {
		ok, err := check(fqdn, value)
		if ok && err == nil {
			sleep(d)
		}
		return ok, err
	}
}

// Obtain registers (or reuses) the ACME account and orders a certificate.
// lego's calls aren't cancellable; ctx is checked between steps.
func (a *acmeIssuer) Obtain(ctx context.Context, names []string) (*Issued, error) {
	acct, err := loadAccount(filepath.Join(a.cfg.StateDir, "accounts", a.id), a.cfg.Email)
	if err != nil {
		return nil, err
	}
	lc := lego.NewConfig(acct)
	lc.CADirURL = a.dirURL
	lc.UserAgent = "linx-certd"
	lc.Certificate.KeyType = certcrypto.EC256
	client, err := lego.NewClient(lc)
	if err != nil {
		return nil, err
	}
	err = client.Challenge.SetDNS01Provider(a.dns,
		dns01.AddRecursiveNameservers(a.cfg.Resolvers),
		dns01.RecursiveNSsPropagationRequirement(),
		dns01.WrapPreCheck(settle(settleDelay, time.Sleep)))
	if err != nil {
		return nil, err
	}
	if acct.Registration == nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if a.eabURL != "" {
			kid, hmac, err := zeroSSLEAB(ctx, lc.HTTPClient, a.eabURL, a.cfg.Email)
			if err != nil {
				return nil, err
			}
			acct.Registration, err = client.Registration.RegisterWithExternalAccountBinding(
				registration.RegisterEABOptions{TermsOfServiceAgreed: true, Kid: kid, HmacEncoded: hmac})
			if err != nil {
				return nil, fmt.Errorf("registering account: %w", err)
			}
		} else {
			acct.Registration, err = client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
			if err != nil {
				return nil, fmt.Errorf("registering account: %w", err)
			}
		}
		if err := acct.save(); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err := client.Certificate.Obtain(certificate.ObtainRequest{Domains: names, Bundle: true})
	if err != nil {
		return nil, err
	}
	return &Issued{Chain: res.Certificate, Key: res.PrivateKey}, nil
}

// zeroSSLEAB fetches external account binding credentials for an email.
func zeroSSLEAB(ctx context.Context, hc *http.Client, endpoint, email string) (kid, hmac string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(url.Values{"email": {email}}.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("zerossl account binding: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Success bool   `json:"success"`
		Kid     string `json:"eab_kid"`
		HMAC    string `json:"eab_hmac_key"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return "", "", fmt.Errorf("zerossl account binding: HTTP %d: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || !body.Success || body.Kid == "" || body.HMAC == "" {
		return "", "", fmt.Errorf("zerossl account binding: HTTP %d, not granted", resp.StatusCode)
	}
	return body.Kid, body.HMAC, nil
}

// account is an ACME account, persisted per CA as key.pem + account.json.
type account struct {
	dir          string
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

func (a *account) GetEmail() string                        { return a.Email }
func (a *account) GetRegistration() *registration.Resource { return a.Registration }
func (a *account) GetPrivateKey() crypto.PrivateKey        { return a.key }

// loadAccount loads the account in dir, creating its key on first use. A saved
// registration is dropped if the contact email changed, so a new one is made.
func loadAccount(dir, email string) (*account, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	a := &account{dir: dir, Email: email}
	keyPath := filepath.Join(dir, "key.pem")
	b, err := os.ReadFile(keyPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, err
		}
		if err := writeSynced(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return nil, err
		}
		a.key = k
		return a, nil
	case err != nil:
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("%s: not PEM", keyPath)
	}
	if a.key, err = x509.ParseECPrivateKey(block.Bytes); err != nil {
		return nil, fmt.Errorf("%s: %w", keyPath, err)
	}

	var saved struct {
		Email        string                 `json:"email"`
		Registration *registration.Resource `json:"registration"`
	}
	b, err = os.ReadFile(filepath.Join(dir, "account.json"))
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &saved); err != nil {
		return nil, fmt.Errorf("account.json: %w", err)
	}
	if saved.Email == email {
		a.Registration = saved.Registration
	}
	return a, nil
}

func (a *account) save() error {
	b, err := json.MarshalIndent(map[string]any{
		"email": a.Email, "registration": a.Registration, "saved_at": time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(a.dir, "account.json")
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	if err := writeSynced(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
