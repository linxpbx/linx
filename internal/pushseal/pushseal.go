// Package pushseal seals a wake push so that only the phone it is meant for
// can read it (docs/PUSH_RELAY.md §3, ADR-094).
//
// A push today carries the call's id, the caller's number and the time where
// Apple can read them. That is already more than Linx would like, and once a
// relay forwards pushes for other people's servers there is one more party
// in the path. So the details are sealed from the Linx server to the app:
// the relay carries a blob it cannot open, and Apple carries the same blob.
//
// The scheme is the usual one for this (ECIES over P-256): an ephemeral key
// for every push, ECDH against the phone's own encryption key, HKDF-SHA256
// to a 32-byte key, AES-256-GCM. There is nothing novel in it on purpose.
//
// The phone's encryption key is **not** the key that signs its identity
// (ADR-073). One key does one job, and the signing key lives in the Secure
// Enclave for signing.
package pushseal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// Version is the envelope's first byte. A phone that meets a version it
// doesn't know leaves the push alone rather than guessing.
const Version = 1

// Sizes of the envelope's fixed parts.
const (
	// HintSize is how much of the recipient's key the envelope names, so a
	// phone holding several accounts knows which key to try without
	// unwrapping all of them. Four bytes is plenty to tell four keys apart
	// and far too few to identify a phone to anyone watching.
	HintSize = 4
	// EphemeralSize is an uncompressed P-256 point (X9.62).
	EphemeralSize = 65
	NonceSize     = 12
	tagSize       = 16

	// Overhead is what sealing adds to the plaintext.
	Overhead = 1 + HintSize + EphemeralSize + NonceSize + tagSize
)

// info binds the derived key to this scheme and to both parties, so a key
// agreed for one purpose can't be reused for another.
const info = "linx push seal v1"

// Hint is the four bytes of a recipient's public key that an envelope names.
func Hint(recipient *ecdh.PublicKey) [HintSize]byte {
	sum := sha256.Sum256(recipient.Bytes())
	var h [HintSize]byte
	copy(h[:], sum[:HintSize])
	return h
}

// Seal locks plaintext so that only the holder of recipient's private key
// can read it.
func Seal(recipient *ecdh.PublicKey, plaintext []byte) ([]byte, error) {
	return seal(rand.Reader, recipient, plaintext)
}

// seal is Seal with the randomness supplied, so a test can pin an envelope
// byte for byte against the app's own reading of it.
func seal(random io.Reader, recipient *ecdh.PublicKey, plaintext []byte) ([]byte, error) {
	if recipient == nil {
		return nil, errors.New("pushseal: no recipient key")
	}
	ephemeral, err := ecdh.P256().GenerateKey(random)
	if err != nil {
		return nil, fmt.Errorf("pushseal: making an ephemeral key: %w", err)
	}
	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return nil, fmt.Errorf("pushseal: key agreement: %w", err)
	}

	hint := Hint(recipient)
	header := make([]byte, 0, 1+HintSize+EphemeralSize)
	header = append(header, Version)
	header = append(header, hint[:]...)
	header = append(header, ephemeral.PublicKey().Bytes()...)

	key, err := derive(shared, recipient.Bytes(), ephemeral.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, fmt.Errorf("pushseal: nonce: %w", err)
	}
	// The header is authenticated but not encrypted: swapping the ephemeral
	// key or the hint for another envelope's breaks the tag.
	out := make([]byte, 0, len(header)+NonceSize+len(plaintext)+tagSize)
	out = append(out, header...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, header), nil
}

// Open reads an envelope sealed to the holder of key. It is written for the
// tests and for anything server-side that needs to check its own work; the
// phone does this in Swift.
func Open(key *ecdh.PrivateKey, envelope []byte) ([]byte, error) {
	if key == nil {
		return nil, errors.New("pushseal: no key")
	}
	if len(envelope) < Overhead {
		return nil, errors.New("pushseal: envelope too short")
	}
	if envelope[0] != Version {
		return nil, fmt.Errorf("pushseal: envelope version %d, this Linx reads %d", envelope[0], Version)
	}
	header := envelope[:1+HintSize+EphemeralSize]
	if hint := Hint(key.PublicKey()); string(header[1:1+HintSize]) != string(hint[:]) {
		return nil, errors.New("pushseal: sealed to another key")
	}
	ephemeral, err := ecdh.P256().NewPublicKey(header[1+HintSize:])
	if err != nil {
		return nil, fmt.Errorf("pushseal: ephemeral key: %w", err)
	}
	shared, err := key.ECDH(ephemeral)
	if err != nil {
		return nil, fmt.Errorf("pushseal: key agreement: %w", err)
	}
	derived, err := derive(shared, key.PublicKey().Bytes(), ephemeral.Bytes())
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(derived)
	if err != nil {
		return nil, err
	}
	rest := envelope[len(header):]
	return gcm.Open(nil, rest[:NonceSize], rest[NonceSize:], header)
}

func derive(shared, recipient, ephemeral []byte) ([]byte, error) {
	context := make([]byte, 0, len(info)+len(recipient)+len(ephemeral))
	context = append(context, info...)
	context = append(context, recipient...)
	context = append(context, ephemeral...)
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, nil, context), key); err != nil {
		return nil, fmt.Errorf("pushseal: deriving the key: %w", err)
	}
	return key, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("pushseal: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("pushseal: gcm: %w", err)
	}
	return gcm, nil
}

// ParsePublicKey reads the phone's encryption key as it sends it at
// enrolment: an uncompressed P-256 point.
func ParsePublicKey(b []byte) (*ecdh.PublicKey, error) {
	if len(b) != EphemeralSize {
		return nil, fmt.Errorf("pushseal: a phone's encryption key is %d bytes, got %d", EphemeralSize, len(b))
	}
	return ecdh.P256().NewPublicKey(b)
}
