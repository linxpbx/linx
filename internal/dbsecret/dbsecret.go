// Package dbsecret encrypts values the control plane stores and must use
// again — webhook signing secrets, bot tokens, provider URLs with tokens in
// them (ADR-030). AES-256-GCM, a random nonce per value, and the owning row's
// id as additional authenticated data, so a ciphertext can't be moved to
// another row. Every blob carries the id of the key that encrypted it, so a
// future key rotation can tell old ciphertexts from new ones.
package dbsecret

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// keyIDSize is the length, in bytes, of the key fingerprint prefixed to every
// blob Seal produces.
const keyIDSize = 8

// Sealer encrypts and decrypts values with one AES-256-GCM key.
type Sealer struct {
	id  [keyIDSize]byte
	key [KeySize]byte
}

// NewSealer derives the key's id from the key itself (a short fingerprint),
// so nothing outside this package needs to track which id belongs to which
// key.
func NewSealer(key [KeySize]byte) *Sealer {
	sum := sha256.Sum256(key[:])
	s := &Sealer{key: key}
	copy(s.id[:], sum[:keyIDSize])
	return s
}

// Seal encrypts plaintext for one database row. rowID identifies that row
// (e.g. "webhook_endpoint:<uuid>") and must be passed unchanged to Open;
// a different rowID, or another row's ciphertext, fails to decrypt.
func (s *Sealer) Seal(rowID string, plaintext []byte) ([]byte, error) {
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("dbsecret: %w", err)
	}
	out := make([]byte, 0, keyIDSize+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, s.id[:]...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, []byte(rowID))
	return out, nil
}

// Open decrypts a blob Seal produced for the same rowID.
func (s *Sealer) Open(rowID string, blob []byte) ([]byte, error) {
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	if len(blob) < keyIDSize+gcm.NonceSize() {
		return nil, fmt.Errorf("dbsecret: ciphertext too short")
	}
	id, rest := blob[:keyIDSize], blob[keyIDSize:]
	if !bytes.Equal(id, s.id[:]) {
		return nil, fmt.Errorf("dbsecret: encrypted with key %s, this sealer holds %s", hex.EncodeToString(id), hex.EncodeToString(s.id[:]))
	}
	nonce, ct := rest[:gcm.NonceSize()], rest[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, []byte(rowID))
	if err != nil {
		return nil, fmt.Errorf("dbsecret: %w", err)
	}
	return pt, nil
}

func (s *Sealer) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return nil, fmt.Errorf("dbsecret: %w", err)
	}
	return cipher.NewGCM(block)
}

// LoadKey reads the KeySize-byte key from a Docker secret file.
func LoadKey(path string) ([KeySize]byte, error) {
	var key [KeySize]byte
	b, err := os.ReadFile(path)
	if err != nil {
		return key, fmt.Errorf("database encryption key: %w", err)
	}
	if len(b) != KeySize {
		return key, fmt.Errorf("database encryption key: %s must be exactly %d bytes, got %d", path, KeySize, len(b))
	}
	copy(key[:], b)
	return key, nil
}

// KeyPathFromEnv is the Docker secret path, overridable for tests.
func KeyPathFromEnv(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv("LINX_DB_ENCRYPTION_KEY_FILE")); v != "" {
		return v
	}
	return "/run/secrets/linx_db_encryption_key"
}
