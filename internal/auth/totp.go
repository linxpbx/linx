package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 specifies HMAC-SHA1 for TOTP; this isn't used for anything else.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters (RFC 6238; ADR-036 "written with the Go standard
// library"): 30-second steps, 6-digit codes, checked one step either side
// of now to allow for clock drift.
const (
	totpStep      = 30 * time.Second
	totpDigits    = 6
	totpSkewSteps = 1
	// totpSecretLen is 160 bits, RFC 4226's recommended HOTP/TOTP secret size.
	totpSecretLen = 20
)

var totpSecretEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random secret, ready to seal and store.
func NewTOTPSecret() ([]byte, error) {
	secret := make([]byte, totpSecretLen)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generating TOTP secret: %w", err)
	}
	return secret, nil
}

// TOTPProvisioningURI is what an authenticator app's QR code encodes
// (otpauth://totp/...), naming Linx and the account it's for.
func TOTPProvisioningURI(secret []byte, issuer, accountEmail string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(accountEmail)
	q := url.Values{
		"secret":    {totpSecretEncoding.EncodeToString(secret)},
		"issuer":    {issuer},
		"algorithm": {"SHA1"},
		"digits":    {fmt.Sprintf("%d", totpDigits)},
		"period":    {fmt.Sprintf("%d", int(totpStep.Seconds()))},
	}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// totpCode computes the 6-digit code for secret at step counter.
func totpCode(secret []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, secret)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[offset])&0x7f)<<24 | uint32(sum[offset+1])<<16 | uint32(sum[offset+2])<<8 | uint32(sum[offset+3])
	code %= 1000000
	return fmt.Sprintf("%06d", code)
}

// normalizeTOTPCode strips spaces and dashes a person might type between
// digits.
func normalizeTOTPCode(code string) string {
	code = strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, code)
	return strings.TrimSpace(code)
}

// ValidTOTPCode reports whether code matches secret at now, allowing for
// totpSkewSteps of clock drift either way. Each accepted code is only ever
// checked against a small, fixed set of counters (no state kept here), so
// callers that must refuse code reuse track the last accepted counter
// themselves; Linx doesn't do that in this slice (ADR-036 doesn't ask for it).
func ValidTOTPCode(secret []byte, code string, now time.Time) bool {
	code = normalizeTOTPCode(code)
	if len(code) != totpDigits {
		return false
	}
	counter := uint64(now.Unix()) / uint64(totpStep.Seconds())
	for d := -totpSkewSteps; d <= totpSkewSteps; d++ {
		c := counter
		if d < 0 {
			if c < uint64(-d) {
				continue
			}
			c -= uint64(-d)
		} else {
			c += uint64(d)
		}
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, c)), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// recoveryCodeCount and recoveryCodeGroupLen: 10 codes (ADR-036), each 10
// characters from the same unambiguous alphabet public ids use, shown as
// two groups of 5 for readability.
const (
	recoveryCodeCount    = 10
	recoveryCodeGroupLen = 5
)

// NewRecoveryCodes returns recoveryCodeCount fresh codes, shown to the
// caller once (with a dash for readability), and their SHA-256 hashes
// (of the normalized form, matching what ValidRecoveryCode checks), which
// is all that's stored.
func NewRecoveryCodes() (codes []string, hashes [][]byte, err error) {
	codes = make([]string, recoveryCodeCount)
	hashes = make([][]byte, recoveryCodeCount)
	for i := range codes {
		code, err := randomRecoveryCode()
		if err != nil {
			return nil, nil, err
		}
		codes[i] = code
		hashes[i] = HashSecret(normalizeRecoveryCode(code))
	}
	return codes, hashes, nil
}

// randomRecoveryCode returns one code from the same lowercase, unambiguous
// alphabet public ids use (no 0/1/l/o confusion), as two groups of 5.
func randomRecoveryCode() (string, error) {
	b := make([]byte, recoveryCodeGroupLen*2)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating recovery code: %w", err)
	}
	s := publicIDEncoding.EncodeToString(b)[:recoveryCodeGroupLen*2]
	return s[:recoveryCodeGroupLen] + "-" + s[recoveryCodeGroupLen:], nil
}

// normalizeRecoveryCode strips the dash and any spaces a person might type,
// and lower-cases it, so entering a code is forgiving.
func normalizeRecoveryCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "")
	return strings.ReplaceAll(code, " ", "")
}
