package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
)

const (
	// TokenIssuer and TokenAudience are fixed claims of API access tokens.
	TokenIssuer   = "linx"
	TokenAudience = "linx-api"
	// AccessTokenTTL is how long an OAuth access token lasts (ADR-027). A
	// phone's device token lasts the same (docs/PHASE2.md §4).
	AccessTokenTTL = 15 * time.Minute
	// EnrollAudience and EnrollTokenTTL are the one-time token that sets up
	// a phone (ADR-012, ADR-073): it can do nothing else, and only once.
	EnrollAudience = "linx-enroll"
	EnrollTokenTTL = 10 * time.Minute
	// KindDevice marks an access token a phone got with its certificate,
	// rather than an OAuth client's.
	KindDevice = "device"
	// clockLeeway tolerates small clock differences between instances.
	clockLeeway = 30 * time.Second
)

// SigningKeyPathFromEnv is the Docker secret holding the 32-byte Ed25519
// seed, overridable for tests.
func SigningKeyPathFromEnv(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv("LINX_JWT_SIGNING_KEY_FILE")); v != "" {
		return v
	}
	return "/run/secrets/linx_jwt_signing_key"
}

// LoadSigningKey reads the Ed25519 seed the installer generated.
func LoadSigningKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("token signing key: %w", err)
	}
	if len(b) != ed25519.SeedSize {
		return nil, fmt.Errorf("token signing key: %s must be exactly %d bytes, got %d", path, ed25519.SeedSize, len(b))
	}
	return ed25519.NewKeyFromSeed(b), nil
}

// Tokens issues and verifies API access tokens: JWT, EdDSA only (the
// algorithm is pinned; "none" and every other algorithm are refused).
type Tokens struct {
	key    ed25519.PrivateKey
	pub    ed25519.PublicKey
	kid    string
	signer jose.Signer
}

// NewTokens builds a Tokens from the signing key.
func NewTokens(key ed25519.PrivateKey) (*Tokens, error) {
	pub := key.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)
	kid := base64.RawURLEncoding.EncodeToString(sum[:8])
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid),
	)
	if err != nil {
		return nil, fmt.Errorf("token signer: %w", err)
	}
	return &Tokens{key: key, pub: pub, kid: kid, signer: signer}, nil
}

// AccessClaims are what an access token says about its holder.
type AccessClaims struct {
	// ClientID is the OAuth client's id (a UUID, the token's subject).
	ClientID uuid.UUID
	TenantID uuid.UUID
	Scopes   []string
	// JTI is the token's unique id, checked against token_revocation.
	JTI       string
	ExpiresAt time.Time
	// Kind is KindDevice for a phone's token (ClientID is then the device's
	// id); empty for an OAuth client's.
	Kind string
}

type tokenClaims struct {
	jwt.Claims
	TenantID string `json:"tid"`
	Scope    string `json:"scope"`
	Kind     string `json:"knd,omitempty"`
}

// Issue signs a new access token for client, valid for AccessTokenTTL.
func (t *Tokens) Issue(client, tenant uuid.UUID, scopes []string, now time.Time) (string, AccessClaims, error) {
	return t.issue(client, tenant, scopes, "", now)
}

// IssueDevice signs an access token for a phone that has just proved it
// holds its certificate's key (docs/PHASE2.md §4). It is an ordinary access
// token marked as a device's, so revoking the device stops it at once.
func (t *Tokens) IssueDevice(device, tenant uuid.UUID, scopes []string, now time.Time) (string, AccessClaims, error) {
	return t.issue(device, tenant, scopes, KindDevice, now)
}

func (t *Tokens) issue(client, tenant uuid.UUID, scopes []string, kind string, now time.Time) (string, AccessClaims, error) {
	jti, err := uuid.NewV7()
	if err != nil {
		return "", AccessClaims{}, err
	}
	exp := now.Add(AccessTokenTTL).Truncate(time.Second)
	c := tokenClaims{
		Claims: jwt.Claims{
			Issuer:    TokenIssuer,
			Subject:   client.String(),
			Audience:  jwt.Audience{TokenAudience},
			Expiry:    jwt.NewNumericDate(exp),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        jti.String(),
		},
		TenantID: tenant.String(),
		Scope:    strings.Join(scopes, " "),
		Kind:     kind,
	}
	raw, err := jwt.Signed(t.signer).Claims(c).Serialize()
	if err != nil {
		return "", AccessClaims{}, fmt.Errorf("signing token: %w", err)
	}
	return raw, AccessClaims{ClientID: client, TenantID: tenant, Scopes: scopes, JTI: c.ID, ExpiresAt: exp, Kind: kind}, nil
}

// ErrTokenInvalid is returned for any token that fails verification. The
// reason is deliberately not given to the caller.
var ErrTokenInvalid = errors.New("access token is not valid")

// ErrTokenExpired is returned for a genuine token that has expired, so the
// client knows to fetch a new one.
var ErrTokenExpired = errors.New("access token has expired")

// Verify checks a token's signature, algorithm, issuer, audience and times.
// It does not check revocation; the caller does that against the database.
func (t *Tokens) Verify(raw string, now time.Time) (AccessClaims, error) {
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil || len(tok.Headers) != 1 || tok.Headers[0].KeyID != t.kid {
		return AccessClaims{}, ErrTokenInvalid
	}
	var c tokenClaims
	if err := tok.Claims(t.pub, &c); err != nil {
		return AccessClaims{}, ErrTokenInvalid
	}
	// go-jose treats exp, nbf and iat as optional; ours always carry them.
	if c.Expiry == nil || c.NotBefore == nil || c.IssuedAt == nil || c.ID == "" {
		return AccessClaims{}, ErrTokenInvalid
	}
	if c.Expiry.Time().Sub(c.IssuedAt.Time()) > AccessTokenTTL {
		return AccessClaims{}, ErrTokenInvalid
	}
	if err := c.ValidateWithLeeway(jwt.Expected{
		Issuer:      TokenIssuer,
		AnyAudience: jwt.Audience{TokenAudience},
		Time:        now,
	}, clockLeeway); err != nil {
		if errors.Is(err, jwt.ErrExpired) {
			return AccessClaims{}, ErrTokenExpired
		}
		return AccessClaims{}, ErrTokenInvalid
	}
	client, err := uuid.Parse(c.Subject)
	if err != nil {
		return AccessClaims{}, ErrTokenInvalid
	}
	tenant, err := uuid.Parse(c.TenantID)
	if err != nil {
		return AccessClaims{}, ErrTokenInvalid
	}
	if c.Kind != "" && c.Kind != KindDevice {
		return AccessClaims{}, ErrTokenInvalid
	}
	return AccessClaims{
		ClientID:  client,
		TenantID:  tenant,
		Scopes:    strings.Fields(c.Scope),
		JTI:       c.ID,
		ExpiresAt: c.Expiry.Time(),
		Kind:      c.Kind,
	}, nil
}

// EnrollClaims are what an enrollment token says: which ticket it is for.
// Everything else about the ticket — whose phone, which extension, how long
// it has left, whether it has been used — is in the database.
type EnrollClaims struct {
	EnrollmentID uuid.UUID
	TenantID     uuid.UUID
	JTI          uuid.UUID
	ExpiresAt    time.Time
}

// IssueEnrollment signs the one-time token that sets up a phone. jti is the
// ticket's own, so the token can be used exactly once (ADR-012): the ticket
// row records that it has been.
func (t *Tokens) IssueEnrollment(enrollment, tenant, jti uuid.UUID, now time.Time) (string, error) {
	exp := now.Add(EnrollTokenTTL).Truncate(time.Second)
	c := tokenClaims{
		Claims: jwt.Claims{
			Issuer:    TokenIssuer,
			Subject:   enrollment.String(),
			Audience:  jwt.Audience{EnrollAudience},
			Expiry:    jwt.NewNumericDate(exp),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        jti.String(),
		},
		TenantID: tenant.String(),
	}
	raw, err := jwt.Signed(t.signer).Claims(c).Serialize()
	if err != nil {
		return "", fmt.Errorf("signing enrollment token: %w", err)
	}
	return raw, nil
}

// VerifyEnrollment checks an enrollment token's signature, algorithm,
// audience and times. Whether the ticket is still open is the caller's
// to check.
func (t *Tokens) VerifyEnrollment(raw string, now time.Time) (EnrollClaims, error) {
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil || len(tok.Headers) != 1 || tok.Headers[0].KeyID != t.kid {
		return EnrollClaims{}, ErrTokenInvalid
	}
	var c tokenClaims
	if err := tok.Claims(t.pub, &c); err != nil {
		return EnrollClaims{}, ErrTokenInvalid
	}
	if c.Expiry == nil || c.NotBefore == nil || c.IssuedAt == nil || c.ID == "" {
		return EnrollClaims{}, ErrTokenInvalid
	}
	if c.Expiry.Time().Sub(c.IssuedAt.Time()) > EnrollTokenTTL {
		return EnrollClaims{}, ErrTokenInvalid
	}
	if err := c.ValidateWithLeeway(jwt.Expected{
		Issuer:      TokenIssuer,
		AnyAudience: jwt.Audience{EnrollAudience},
		Time:        now,
	}, clockLeeway); err != nil {
		if errors.Is(err, jwt.ErrExpired) {
			return EnrollClaims{}, ErrTokenExpired
		}
		return EnrollClaims{}, ErrTokenInvalid
	}
	enrollment, err := uuid.Parse(c.Subject)
	if err != nil {
		return EnrollClaims{}, ErrTokenInvalid
	}
	tenant, err := uuid.Parse(c.TenantID)
	if err != nil {
		return EnrollClaims{}, ErrTokenInvalid
	}
	jti, err := uuid.Parse(c.ID)
	if err != nil {
		return EnrollClaims{}, ErrTokenInvalid
	}
	return EnrollClaims{EnrollmentID: enrollment, TenantID: tenant, JTI: jti, ExpiresAt: c.Expiry.Time()}, nil
}
