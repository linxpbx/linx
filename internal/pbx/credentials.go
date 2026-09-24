package pbx

import (
	"crypto/md5" // SIP digest auth (RFC 3261) mandates MD5; see DigestHash.
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
)

// DigestRealm is the SIP digest realm every device's password is hashed
// against (ADR-033). It must match migration 0005's asterisk.ps_auths view
// literal exactly: changing it invalidates every stored DigestHash.
const DigestRealm = "linxpbx"

// usernameEncoding avoids visually ambiguous characters (no 0/1/8/9).
var usernameEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewSIPUsername returns a random device SIP username: "d_" + 8 lowercase
// alphanumeric characters (ADR-033). It's public (Asterisk's endpoint id),
// but random, so an extension number never reveals a device's login.
func NewSIPUsername() string {
	b := make([]byte, 5) // 40 bits -> exactly 8 base32 characters
	_, _ = rand.Read(b)  // crypto/rand.Read never fails on supported platforms
	return "d_" + usernameEncoding.EncodeToString(b)
}

// NewDevicePassword returns a new random SIP password (ADR-033: 128+ random
// bits, never chosen by a person), shown to the caller once. Only its
// DigestHash is stored.
func NewDevicePassword() string { return rand.Text() } // 26 characters, 130 bits

// DigestHash is Asterisk's md5_cred for a device: MD5(username:realm:password)
// (ADR-033, RFC 3261 digest authentication). MD5 is what the SIP digest
// protocol requires here, not a security choice of ours — the password
// itself is 128+ random bits, unique to this one device login, and this
// hash's only purpose is proving that exact login to Asterisk.
func DigestHash(username, password string) string {
	sum := md5.Sum([]byte(username + ":" + DigestRealm + ":" + password))
	return hex.EncodeToString(sum[:])
}
