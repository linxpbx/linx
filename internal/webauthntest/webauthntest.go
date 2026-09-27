// Package webauthntest is a software passkey device for tests: it answers
// the options Linx's server sends the way a browser and a platform
// authenticator would (attestation "none", ES256, user verified), so the
// passkey ceremonies can be tested without a browser. The browser suite
// uses Chromium's own virtual authenticator instead.
package webauthntest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

var b64 = base64.RawURLEncoding

// Device holds one passkey. Origin is what the "browser" puts in
// clientDataJSON; SignCount is the counter the next answer reports (a test
// can set it back to look like a copied key); Synced sets the backup flags.
type Device struct {
	Origin     string
	SignCount  uint32
	Synced     bool
	key        *ecdsa.PrivateKey
	credID     []byte
	userHandle []byte
	rpID       string
}

// New returns a device with no passkey yet, answering from origin.
func New(origin string) *Device { return &Device{Origin: origin} }

// CredentialID is the device's passkey id, once Create has made one.
func (d *Device) CredentialID() []byte { return d.credID }

type creationOptions struct {
	Challenge string `json:"challenge"`
	RP        struct {
		ID string `json:"id"`
	} `json:"rp"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
}

type requestOptions struct {
	Challenge string `json:"challenge"`
	RPID      string `json:"rpId"`
}

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

func (d *Device) flags(attested bool) byte {
	f := byte(0x01 | 0x04) // user present, user verified
	if d.Synced {
		f |= 0x08 | 0x10 // backup eligible, backed up
	}
	if attested {
		f |= 0x40
	}
	return f
}

func authData(rpID string, flags byte, count uint32, attested []byte) []byte {
	h := sha256.Sum256([]byte(rpID))
	out := append([]byte{}, h[:]...)
	out = append(out, flags)
	out = binary.BigEndian.AppendUint32(out, count)
	return append(out, attested...)
}

// Create answers registration options (the JSON the server's .../options
// returned) with a new passkey, as PublicKeyCredential.toJSON() would.
func (d *Device) Create(options []byte) ([]byte, error) {
	var o creationOptions
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	credID := make([]byte, 32)
	if _, err := rand.Read(credID); err != nil {
		return nil, err
	}
	uh, err := b64.DecodeString(o.User.ID)
	if err != nil {
		return nil, fmt.Errorf("user id: %w", err)
	}
	d.key, d.credID, d.userHandle, d.rpID = key, credID, uh, o.RP.ID

	pub, err := key.PublicKey.Bytes() // 0x04 || X || Y
	if err != nil {
		return nil, err
	}
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: pub[1:33], -3: pub[33:65]})
	if err != nil {
		return nil, err
	}
	attested := make([]byte, 16) // AAGUID: zero, as "none" attestation may
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(credID)))
	attested = append(attested, credID...)
	attested = append(attested, cose...)
	ad := authData(o.RP.ID, d.flags(true), d.SignCount, attested)
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": ad})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(credID), "rawId": b64.EncodeToString(credID), "type": "public-key",
		"authenticatorAttachment": "platform", "clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(clientData("webauthn.create", o.Challenge, d.Origin)),
			"attestationObject": b64.EncodeToString(att),
			"transports":        []string{"internal", "hybrid"},
		},
	})
}

// Get answers sign-in options with the device's passkey, and counts one
// more use.
func (d *Device) Get(options []byte) ([]byte, error) {
	if d.key == nil {
		return nil, fmt.Errorf("no passkey on this device yet")
	}
	var o requestOptions
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, err
	}
	rpID := o.RPID
	if rpID == "" {
		rpID = d.rpID
	}
	d.SignCount++
	ad := authData(rpID, d.flags(false), d.SignCount, nil)
	cd := clientData("webauthn.get", o.Challenge, d.Origin)
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, d.key, digest[:])
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(d.credID), "rawId": b64.EncodeToString(d.credID), "type": "public-key",
		"authenticatorAttachment": "platform", "clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(ad),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(d.userHandle),
		},
	})
}
