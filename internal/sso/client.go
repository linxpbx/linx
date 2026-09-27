package sso

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
)

// discoveryTTL is how long a provider's discovery document is kept before
// it's fetched again. Its signing keys refresh on their own whenever a
// token names a key Linx hasn't seen.
const discoveryTTL = time.Hour

// Client is the OpenID Connect client (auth.CompanyProviders): the
// authorization code flow with PKCE, state and nonce, through go-oidc and
// x/oauth2. Every connection to a provider goes through HTTP, which in
// production is the SSRF-guarded client (ADR-028): a provider on the home
// network needs its address on the outbound allowlist.
type Client struct {
	Store       Store
	Sealer      *dbsecret.Sealer
	HTTP        *http.Client
	RedirectURI string
	Now         func() time.Time

	mu    sync.Mutex
	cache map[string]discovered
}

var _ auth.CompanyProviders = (*Client)(nil)

type discovered struct {
	p  *oidc.Provider
	at time.Time
}

// scopes asked of every provider: an ID token with the subject and email.
var scopes = []string{oidc.ScopeOpenID, "email", "profile"}

// Discover fetches issuer's discovery document, checking that it names
// itself as issuer (go-oidc refuses a mismatch).
func (c *Client) Discover(ctx context.Context, issuer string) (*oidc.Provider, error) {
	now := c.Now()
	c.mu.Lock()
	d, ok := c.cache[issuer]
	c.mu.Unlock()
	if ok && now.Sub(d.at) < discoveryTTL {
		return d.p, nil
	}
	// The provider remembers c.HTTP for fetching its signing keys later.
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(oidc.ClientContext(dctx, c.HTTP), issuer)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]discovered{}
	}
	c.cache[issuer] = discovered{p: p, at: now}
	c.mu.Unlock()
	return p, nil
}

// provider returns an enabled provider and its discovery.
func (c *Client) provider(ctx context.Context, tenant, id uuid.UUID) (Provider, *oidc.Provider, oauth2.Config, error) {
	p, err := c.Store.SSOProvider(ctx, tenant, id)
	if err == nil && !p.Enabled {
		err = auth.ErrNotFound
	}
	if err != nil {
		return Provider{}, nil, oauth2.Config{}, err
	}
	d, err := c.Discover(ctx, p.Issuer)
	if err != nil {
		return Provider{}, nil, oauth2.Config{}, fmt.Errorf("discovering %s: %w", p.Issuer, err)
	}
	cfg := oauth2.Config{ClientID: p.ClientID, Endpoint: d.Endpoint(), RedirectURL: c.RedirectURI, Scopes: scopes}
	if len(p.ClientSecretEnc) > 0 {
		secret, err := c.Sealer.Open(SealID(p.ID), p.ClientSecretEnc)
		if err != nil {
			return Provider{}, nil, oauth2.Config{}, fmt.Errorf("opening client secret: %w", err)
		}
		cfg.ClientSecret = string(secret)
	}
	return p, d, cfg, nil
}

// Start implements auth.CompanyProviders.
func (c *Client) Start(ctx context.Context, tenant, id uuid.UUID, state, nonce, verifier string) (string, auth.CompanyProvider, error) {
	p, _, cfg, err := c.provider(ctx, tenant, id)
	if err != nil {
		return "", auth.CompanyProvider{}, err
	}
	url := cfg.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	return url, auth.CompanyProvider{ID: p.ID, Name: p.Name}, nil
}

// Finish implements auth.CompanyProviders: the code is exchanged at the
// provider's token endpoint (with the PKCE verifier), and the ID token's
// signature, issuer, audience, expiry and nonce are checked.
func (c *Client) Finish(ctx context.Context, tenant, id uuid.UUID, code, verifier, nonce string) (auth.CompanyProvider, auth.CompanyIdentity, error) {
	p, d, cfg, err := c.provider(ctx, tenant, id)
	if err != nil {
		return auth.CompanyProvider{}, auth.CompanyIdentity{}, err
	}
	if code == "" {
		return auth.CompanyProvider{}, auth.CompanyIdentity{}, errors.New("no code in the provider's answer")
	}
	xctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, c.HTTP), 15*time.Second)
	defer cancel()
	tok, err := cfg.Exchange(xctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return auth.CompanyProvider{}, auth.CompanyIdentity{}, fmt.Errorf("exchanging the code: %w", err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return auth.CompanyProvider{}, auth.CompanyIdentity{}, errors.New("the provider sent no ID token")
	}
	idt, err := d.Verifier(&oidc.Config{ClientID: p.ClientID, Now: c.Now}).Verify(xctx, raw)
	if err != nil {
		return auth.CompanyProvider{}, auth.CompanyIdentity{}, fmt.Errorf("checking the ID token: %w", err)
	}
	if idt.Nonce != nonce {
		return auth.CompanyProvider{}, auth.CompanyIdentity{}, errors.New("the ID token's nonce doesn't match")
	}
	var claims Claims
	if err := idt.Claims(&claims); err != nil {
		return auth.CompanyProvider{}, auth.CompanyIdentity{}, fmt.Errorf("reading the ID token: %w", err)
	}
	return auth.CompanyProvider{ID: p.ID, Name: p.Name}, Identity(p.Kind, idt.Subject, claims), nil
}

// Claims are the ID token claims Linx reads besides the subject.
type Claims struct {
	Email         string `json:"email"`
	EmailVerified any    `json:"email_verified"`
	// Microsoft (Entra ID): xms_edov is true when the email's domain is
	// verified by the account's own organisation (an optional claim the
	// admin adds to the app); upn is the organisation's sign-in name, always
	// on one of its verified domains.
	XMSEdov any    `json:"xms_edov"`
	UPN     string `json:"upn"`
}

// Identity reads the provider account from claims (docs/ADMIN.md §6): the
// standard email_verified for every kind except Microsoft, which doesn't
// send it; there the email counts only with xms_edov, or else the upn does.
func Identity(kind, subject string, c Claims) auth.CompanyIdentity {
	id := auth.CompanyIdentity{Subject: subject, Email: strings.TrimSpace(c.Email)}
	if kind != KindMicrosoft {
		id.EmailVerified = truthy(c.EmailVerified)
		return id
	}
	switch {
	case id.Email != "" && truthy(c.XMSEdov):
		id.EmailVerified = true
	case strings.Contains(c.UPN, "@"):
		id.Email, id.EmailVerified = strings.TrimSpace(c.UPN), true
	}
	return id
}

// truthy accepts a JSON true, or the strings some providers send instead.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t == 1
	}
	return false
}
