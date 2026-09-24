package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (ADR-036). Chosen for a server with no GPU offload
// and no need to compete with a login-heavy web service: about 50 ms per
// hash on modest hardware.
const (
	argon2Time    = 2
	argon2Memory  = 64 * 1024 // KiB
	argon2Threads = 2
	argon2KeyLen  = 32
	saltLen       = 16
)

// MinPasswordLength is the shortest password Linx accepts (ADR-036).
const MinPasswordLength = 12

// HashPassword returns a self-describing Argon2id hash: the algorithm and
// its parameters travel with the hash, so they can change later without
// breaking existing passwords.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	sum := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2Memory, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword reports whether password matches a hash HashPassword
// produced, in constant time.
func VerifyPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var mem uint32
	var t uint32
	var p uint8
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil || n != 3 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, mem, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// PasswordPolicyError explains why a password was refused.
type PasswordPolicyError struct{ Detail string }

func (e *PasswordPolicyError) Error() string { return e.Detail }

// CheckPasswordPolicy checks length and against a short list of the most
// common passwords (ADR-036: "checked against a bundled list of common
// passwords"). It isn't a full breach-list check — that needs a network
// call this server shouldn't make on every sign-up.
func CheckPasswordPolicy(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return &PasswordPolicyError{Detail: "Use at least " + strconv.Itoa(MinPasswordLength) + " characters."}
	}
	if len(password) > 200 {
		return &PasswordPolicyError{Detail: "That password is too long."}
	}
	if commonPasswords[strings.ToLower(password)] {
		return &PasswordPolicyError{Detail: "That password is too easy to guess. Choose another."}
	}
	return nil
}

// commonPasswords is a small bundled list of the passwords people reuse
// most (ADR-036), lower-cased. Long enough to satisfy MinPasswordLength but
// still worth refusing: reused across breaches, so a public list is enough.
var commonPasswords = func() map[string]bool {
	m := make(map[string]bool, len(commonPasswordList))
	for _, p := range commonPasswordList {
		m[p] = true
	}
	return m
}()

var commonPasswordList = []string{
	"password123456", "passw0rd123456", "letmein123456", "welcome123456",
	"qwertyuiop1234", "123456789012345", "iloveyou123456", "admin123456789",
	"changeme123456", "password1234567", "trustno1trustno", "football1234567",
	"princess1234567", "dragon1234567890", "monkey1234567890", "sunshine123456",
	"superman1234567", "starwars1234567", "whatever1234567", "abc123456789012",
}
