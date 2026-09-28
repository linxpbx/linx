package install

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
)

// NewSecret is a link's (or a session's) secret: 256 random bits, 43
// URL-safe characters.
func NewSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported systems
	return base64.RawURLEncoding.EncodeToString(b)
}

var secretRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// ValidSecret reports whether s looks like NewSecret's output.
func ValidSecret(s string) bool { return secretRE.MatchString(s) }

// Hash is the hex SHA-256 of a secret: what's kept and compared, so the
// control plane never holds the link's secret itself.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Matches reports, in constant time, whether secret hashes to hash.
func Matches(secret, hash string) bool {
	if !ValidSecret(secret) || len(hash) != sha256.Size*2 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(Hash(secret)), []byte(hash)) == 1
}

// ValidHash reports whether h is a hex SHA-256.
func ValidHash(h string) bool {
	if len(h) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

// BrowserName is a plain name for a browser from its User-Agent, for the
// terminal's "Link opened (Chrome, 203.0.113.9)".
func BrowserName(ua string) string {
	switch {
	case strings.Contains(ua, "Edg/"):
		return "Edge"
	case strings.Contains(ua, "Firefox/"):
		return "Firefox"
	case strings.Contains(ua, "Chrome/"), strings.Contains(ua, "CriOS/"):
		return "Chrome"
	case strings.Contains(ua, "Safari/"):
		return "Safari"
	}
	return "a browser"
}
