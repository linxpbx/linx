// Package auth authenticates and authorises API callers (docs/API.md §3,
// ADR-027): API keys, OAuth client credentials with EdDSA access tokens,
// scopes and role ceilings, and rate limits.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"strings"
)

const (
	// APIKeyPrefix starts every API key, so secret scanners (GitHub,
	// gitleaks) can spot a leaked one.
	APIKeyPrefix = "linx_"
	// ClientSecretPrefix starts every OAuth client secret, for the same reason.
	ClientSecretPrefix = "linxcs_"

	publicIDLen = 12
	secretBytes = 32
	secretLen   = 43 // base64url, no padding, of secretBytes
)

var publicIDEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewPublicID returns a random 12-character lowercase id (60 bits). It is
// public: it names a key or client so its hash can be looked up.
func NewPublicID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return publicIDEncoding.EncodeToString(b)[:publicIDLen]
}

// NewSecret returns 32 random bytes, base64url-encoded.
func NewSecret() string {
	b := make([]byte, secretBytes)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewAPIKey returns a new key's public id, its secret, and the full key the
// caller is shown once: linx_<id>_<secret>.
func NewAPIKey() (publicID, secret, key string) {
	publicID, secret = NewPublicID(), NewSecret()
	return publicID, secret, APIKeyPrefix + publicID + "_" + secret
}

// ParseAPIKey splits a presented key into its public id and secret. ok is
// false if it isn't shaped like a Linx API key.
func ParseAPIKey(key string) (publicID, secret string, ok bool) {
	rest, found := strings.CutPrefix(key, APIKeyPrefix)
	if !found || len(rest) != publicIDLen+1+secretLen || rest[publicIDLen] != '_' {
		return "", "", false
	}
	publicID, secret = rest[:publicIDLen], rest[publicIDLen+1:]
	if !validPublicID(publicID) || !validSecret(secret) {
		return "", "", false
	}
	return publicID, secret, true
}

// NewClientSecret returns a new OAuth client secret's random part and the
// full secret the caller is shown once: linxcs_<secret>.
func NewClientSecret() (secret, full string) {
	secret = NewSecret()
	return secret, ClientSecretPrefix + secret
}

// ParseClientSecret returns the random part of a presented client secret.
func ParseClientSecret(full string) (secret string, ok bool) {
	secret, found := strings.CutPrefix(full, ClientSecretPrefix)
	if !found || !validSecret(secret) {
		return "", false
	}
	return secret, true
}

// ValidPublicID reports whether s could be a key or client public id.
func ValidPublicID(s string) bool { return validPublicID(s) }

func validPublicID(s string) bool {
	if len(s) != publicIDLen {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

func validSecret(s string) bool {
	if len(s) != secretLen {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(b) == secretBytes
}

// HashSecret is what the database stores instead of a secret. The secret is
// 256 random bits, so a slow password hash would add nothing (ADR-027).
func HashSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// SecretMatches compares a presented secret with a stored hash in constant time.
func SecretMatches(storedHash []byte, secret string) bool {
	return subtle.ConstantTimeCompare(storedHash, HashSecret(secret)) == 1
}
