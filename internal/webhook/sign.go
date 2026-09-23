// Package webhook delivers events to admin-registered HTTPS endpoints in
// the Standard Webhooks format (docs/API.md §4, ADR-028): a transactional
// outbox, a worker that fans events out to subscribed endpoints and sends
// them through the SSRF-guarded client (internal/safehttp), retries for
// about a day, a delivery log and replay.
package webhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// SecretPrefix starts every signing secret (Standard Webhooks).
const SecretPrefix = "whsec_"

// secretSize is the random part of a signing secret, in bytes.
const secretSize = 32

// NewSecret returns a new signing secret, whsec_<base64 of 32 random bytes>.
func NewSecret() (string, error) {
	b := make([]byte, secretSize)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return SecretPrefix + base64.StdEncoding.EncodeToString(b), nil
}

// secretKey returns the HMAC key inside a whsec_ secret.
func secretKey(secret string) ([]byte, error) {
	b64, ok := strings.CutPrefix(secret, SecretPrefix)
	if !ok {
		return nil, errors.New("webhook secret doesn't start with " + SecretPrefix)
	}
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(key) == 0 {
		return nil, errors.New("webhook secret isn't valid base64")
	}
	return key, nil
}

// Sign returns the webhook-signature header value for a message: one
// "v1,<base64 HMAC-SHA256 of id.timestamp.body>" per secret, separated by
// spaces. During a rotation both the new and the old secret sign, so a
// receiver holding either accepts it.
func Sign(secrets []string, msgID string, ts time.Time, body []byte) (string, error) {
	if len(secrets) == 0 {
		return "", errors.New("no signing secret")
	}
	sigs := make([]string, 0, len(secrets))
	for _, s := range secrets {
		key, err := secretKey(s)
		if err != nil {
			return "", err
		}
		sigs = append(sigs, "v1,"+base64.StdEncoding.EncodeToString(mac(key, msgID, ts, body)))
	}
	return strings.Join(sigs, " "), nil
}

func mac(key []byte, msgID string, ts time.Time, body []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msgID + "." + strconv.FormatInt(ts.Unix(), 10) + "."))
	h.Write(body)
	return h.Sum(nil)
}

// Verify checks a webhook-signature header the way a receiver would: true
// if any v1 signature matches secret. (Receivers use a Standard Webhooks
// library; this is for tests and `linx` tooling.)
func Verify(secret, msgID string, ts time.Time, body []byte, header string) bool {
	key, err := secretKey(secret)
	if err != nil {
		return false
	}
	want := mac(key, msgID, ts, body)
	for _, sig := range strings.Fields(header) {
		v, b64, ok := strings.Cut(sig, ",")
		if !ok || v != "v1" {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(b64)
		if err == nil && hmac.Equal(got, want) {
			return true
		}
	}
	return false
}
