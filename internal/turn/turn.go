// Package turn issues short-lived credentials for Linx's relay, linx-coturn
// (ADR-039, docs/WEB.md §5): coturn's "TURN REST API" scheme, where the
// relay checks a credential against a secret it shares with the control
// plane, so nothing is stored per credential and nothing needs revoking.
//
// The username is "<expiry unix time>:<person id>"; the password is
// base64(HMAC-SHA1(secret, username)). coturn refuses it after the expiry,
// and applies its per-person quota to the part after the colon.
package turn

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TTL is how long a credential works (docs/WEB.md §5: 1 h). The page asks
// for a fresh one before it runs out; an allocation made with it keeps
// working as long as the page refreshes the allocation itself.
const TTL = time.Hour

// SecretPathFromEnv is where the shared secret is (Docker secret
// linx_turn_secret; LINX_TURN_SECRET_FILE overrides it).
func SecretPathFromEnv(getenv func(string) string) string {
	if p := getenv("LINX_TURN_SECRET_FILE"); p != "" {
		return p
	}
	return "/run/secrets/linx_turn_secret"
}

// MinSecretLen is the shortest secret LoadSecret accepts.
const MinSecretLen = 32

// LoadSecret reads the shared secret. It ends up in coturn's configuration
// file as a bare value, so it must be one printable word.
func LoadSecret(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(b))
	if err := CheckSecret(s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return []byte(s), nil
}

// CheckSecret reports whether s is usable as the shared secret.
func CheckSecret(s string) error {
	if len(s) < MinSecretLen {
		return fmt.Errorf("the relay secret is shorter than %d characters", MinSecretLen)
	}
	for _, r := range s {
		if r <= ' ' || r > '~' || r == '#' || r == '"' || r == '\\' {
			return errors.New("the relay secret contains characters coturn's configuration can't hold")
		}
	}
	return nil
}

// Credentials are what a browser puts in RTCPeerConnection's iceServers.
type Credentials struct {
	URLs      []string
	Username  string
	Password  string
	ExpiresAt time.Time
}

// Issuer makes credentials.
type Issuer struct {
	Secret []byte
	// URLs are the relay's addresses as browsers reach them, e.g.
	// "turn:turn.example.com:443?transport=udp" (docs/WEB.md §3: which
	// ones depends on the front door).
	URLs []string
	Now  func() time.Time
}

// Issue returns credentials for person, valid for TTL.
func (i *Issuer) Issue(person uuid.UUID) Credentials {
	exp := i.Now().Add(TTL).Truncate(time.Second)
	user := strconv.FormatInt(exp.Unix(), 10) + ":" + person.String()
	return Credentials{URLs: i.URLs, Username: user, Password: Password(i.Secret, user), ExpiresAt: exp.UTC()}
}

// Password is coturn's REST API password for username.
func Password(secret []byte, username string) string {
	m := hmac.New(sha1.New, secret)
	m.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// DefaultURLs are the relay's addresses when LINX_TURN_URLS isn't set:
// turn.<domain> on 443, UDP first, then TLS for networks that allow only
// web traffic (docs/WEB.md §2).
func DefaultURLs(domain string) []string {
	host := "turn." + domain
	return []string{"turn:" + host + ":443?transport=udp", "turns:" + host + ":443?transport=tcp"}
}

// URLsFromEnv reads LINX_TURN_URLS (comma-separated), else DefaultURLs.
func URLsFromEnv(getenv func(string) string) ([]string, error) {
	v := strings.TrimSpace(getenv("LINX_TURN_URLS"))
	if v == "" {
		d := strings.TrimSpace(getenv("LINX_DOMAIN"))
		if d == "" {
			return nil, errors.New("set LINX_DOMAIN or LINX_TURN_URLS")
		}
		return DefaultURLs(d), nil
	}
	var urls []string
	for _, u := range strings.Split(v, ",") {
		u = strings.TrimSpace(u)
		if !strings.HasPrefix(u, "turn:") && !strings.HasPrefix(u, "turns:") {
			return nil, fmt.Errorf("LINX_TURN_URLS: %q isn't a turn: or turns: address", u)
		}
		urls = append(urls, u)
	}
	return urls, nil
}
