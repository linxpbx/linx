// Package stepca gets TLS certificates for Linx's own services from the
// internal CA (step-ca, ADR-011) through a JWK provisioner: the same flow
// `step ca certificate` uses, in a few stdlib-and-go-jose calls instead of the
// step CLI or its large client library.
//
//  1. GET /provisioners, find ours, decrypt its private key with the
//     provisioner password (PBES2 JWE).
//  2. Sign a one-time token (JWT) for the certificate's name and SANs.
//  3. POST /1.0/sign with a fresh key's CSR and the token.
//
// A Renewer keeps a service's certificate fresh (renewed at two-thirds of its
// lifetime; linx-services certificates last 24 h) and serves it to
// tls.Config.GetCertificate.
package stepca

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// ServicesProvisioner is the step-ca provisioner for Linx's own services
// (24 h certificates; internal/installer/ca-init.sh).
const ServicesProvisioner = "linx-services"

// tokenLifetime is how long a one-time token is valid: just long enough for
// the sign request it's made for.
const tokenLifetime = 5 * time.Minute

// Client issues certificates from one step-ca provisioner.
type Client struct {
	// URL is the CA's base URL, e.g. https://step-ca:9000. The token's
	// audience is derived from it, so it must use a name in the CA's
	// certificate (ca-init.sh: linx-step-ca, step-ca, localhost).
	URL         string
	Provisioner string
	Password    []byte
	// HTTP must trust the internal CA's root (NewClient does this).
	HTTP *http.Client
	Now  func() time.Time

	mu  sync.Mutex
	key *jose.JSONWebKey
}

// NewClient returns a Client that trusts only the given root certificate(s)
// for the CA's own TLS.
func NewClient(caURL, provisioner string, password []byte, roots *x509.CertPool) *Client {
	return &Client{
		URL:         strings.TrimRight(caURL, "/"),
		Provisioner: provisioner,
		Password:    password,
		HTTP: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}},
		},
		Now: time.Now,
	}
}

// LoadRoots reads a PEM file of root certificates into a pool.
func LoadRoots(path string) (*x509.CertPool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("internal CA root: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("internal CA root: no certificate in %s", path)
	}
	return pool, nil
}

// Issue returns a new certificate (with its intermediate) for commonName and
// dnsNames, signed by the CA, and a fresh P-256 key that never leaves this
// process.
func (c *Client) Issue(ctx context.Context, commonName string, dnsNames []string) (*tls.Certificate, error) {
	sans := dnsNames
	if len(sans) == 0 {
		sans = []string{commonName}
	}
	ott, err := c.token(ctx, commonName, sans)
	if err != nil {
		return nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: commonName},
		DNSNames: sans,
	}, key)
	if err != nil {
		return nil, fmt.Errorf("certificate request: %w", err)
	}

	body, err := json.Marshal(map[string]string{
		"csr": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})),
		"ott": ott,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		CertChain []string `json:"certChain"`
		Crt       string   `json:"crt"`
		CA        string   `json:"ca"`
	}
	if err := c.do(ctx, http.MethodPost, "/1.0/sign", body, &resp); err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	chain := resp.CertChain
	if len(chain) == 0 {
		chain = []string{resp.Crt, resp.CA}
	}

	cert := &tls.Certificate{PrivateKey: key}
	for _, p := range chain {
		rest := []byte(p)
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			if block.Type == "CERTIFICATE" {
				cert.Certificate = append(cert.Certificate, block.Bytes)
			}
		}
	}
	if len(cert.Certificate) == 0 {
		return nil, errors.New("sign: the CA returned no certificate")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	if !leaf.PublicKey.(*ecdsa.PublicKey).Equal(&key.PublicKey) {
		return nil, errors.New("sign: the CA returned a certificate for a different key")
	}
	cert.Leaf = leaf
	return cert, nil
}

// token signs a one-time token (step-ca's "ott") with the provisioner's key.
func (c *Client) token(ctx context.Context, subject string, sans []string) (string, error) {
	key, err := c.provisionerKey(ctx)
	if err != nil {
		return "", err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.SignatureAlgorithm(key.Algorithm), Key: key},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", fmt.Errorf("provisioner key: %w", err)
	}
	now := c.Now()
	claims := jwt.Claims{
		Issuer:    c.Provisioner,
		Subject:   subject,
		Audience:  jwt.Audience{c.URL + "/1.0/sign"},
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		Expiry:    jwt.NewNumericDate(now.Add(tokenLifetime)),
		ID:        rand.Text(),
	}
	return jwt.Signed(signer).Claims(claims).Claims(map[string]any{"sans": sans}).Serialize()
}

// provisionerKey fetches and decrypts the provisioner's private key once;
// later certificates reuse it.
func (c *Client) provisionerKey(ctx context.Context) (*jose.JSONWebKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != nil {
		return c.key, nil
	}

	var list struct {
		Provisioners []struct {
			Type         string `json:"type"`
			Name         string `json:"name"`
			EncryptedKey string `json:"encryptedKey"`
		} `json:"provisioners"`
	}
	if err := c.do(ctx, http.MethodGet, "/provisioners?limit=100", nil, &list); err != nil {
		return nil, fmt.Errorf("provisioners: %w", err)
	}
	for _, p := range list.Provisioners {
		if p.Name != c.Provisioner {
			continue
		}
		if p.Type != "JWK" || p.EncryptedKey == "" {
			return nil, fmt.Errorf("provisioner %s: not a JWK provisioner with a stored key", c.Provisioner)
		}
		jwe, err := jose.ParseEncrypted(p.EncryptedKey,
			[]jose.KeyAlgorithm{jose.PBES2_HS256_A128KW},
			[]jose.ContentEncryption{jose.A128GCM, jose.A256GCM})
		if err != nil {
			return nil, fmt.Errorf("provisioner %s key: %w", c.Provisioner, err)
		}
		plain, err := jwe.Decrypt(c.Password)
		if err != nil {
			return nil, fmt.Errorf("provisioner %s key: wrong password? %w", c.Provisioner, err)
		}
		var key jose.JSONWebKey
		if err := key.UnmarshalJSON(plain); err != nil {
			return nil, fmt.Errorf("provisioner %s key: %w", c.Provisioner, err)
		}
		if key.IsPublic() || key.KeyID == "" {
			return nil, fmt.Errorf("provisioner %s key: not a private key with an id", c.Provisioner)
		}
		c.key = &key
		return c.key, nil
	}
	return nil, fmt.Errorf("provisioner %s: not found on the CA", c.Provisioner)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	u, err := url.Parse(c.URL + path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e)
		return fmt.Errorf("%s %s: HTTP %d %s", method, path, resp.StatusCode, e.Message)
	}
	return json.Unmarshal(b, out)
}
