package pushseal

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"strings"
	"testing"
)

func phone(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

const call = `{"call":"01a1-5f","from":"+97142340100","at":1792428844}`

func TestOnlyThePhoneItIsMeantForCanReadIt(t *testing.T) {
	me, someoneElse := phone(t), phone(t)
	envelope, err := Seal(me.PublicKey(), []byte(call))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(envelope, []byte("+97142340100")) {
		t.Error("the caller's number is in the envelope in the clear")
	}
	got, err := Open(me, envelope)
	if err != nil {
		t.Fatalf("the phone couldn't open its own push: %v", err)
	}
	if string(got) != call {
		t.Errorf("got %q", got)
	}
	if _, err := Open(someoneElse, envelope); err == nil {
		t.Error("another phone opened it")
	}
}

// Every push gets its own ephemeral key, so two pushes of the same words
// don't look alike to the relay or to anyone watching.
func TestTwoPushesNeverLookAlike(t *testing.T) {
	me := phone(t)
	a, err := Seal(me.PublicKey(), []byte(call))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Seal(me.PublicKey(), []byte(call))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Error("the same call sealed twice gave the same bytes")
	}
	// ...but the hint is stable, which is how a phone picks its key.
	if !bytes.Equal(a[1:1+HintSize], b[1:1+HintSize]) {
		t.Error("the key hint changed between two pushes to one phone")
	}
}

// A phone with several accounts (ADR-093) holds several keys and must pick
// one without unwrapping them all in a push handler.
func TestTheHintTellsAPhonesKeysApart(t *testing.T) {
	work, home := phone(t), phone(t)
	if Hint(work.PublicKey()) == Hint(home.PublicKey()) {
		t.Fatal("two keys shared a hint")
	}
	envelope, err := Seal(work.PublicKey(), []byte(call))
	if err != nil {
		t.Fatal(err)
	}
	hint := Hint(work.PublicKey())
	if !bytes.Equal(envelope[1:1+HintSize], hint[:]) {
		t.Error("the envelope doesn't name the key it was sealed to")
	}
	if _, err := Open(home, envelope); err == nil || !strings.Contains(err.Error(), "another key") {
		t.Errorf("the wrong account's key gave %v", err)
	}
}

func TestATamperedEnvelopeIsRefused(t *testing.T) {
	me := phone(t)
	envelope, err := Seal(me.PublicKey(), []byte(call))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what string
		at   int
	}{
		{"the version", 0},
		{"the ephemeral key", 1 + HintSize + 2},
		{"the nonce", 1 + HintSize + EphemeralSize + 1},
		{"the ciphertext", len(envelope) - 3},
	} {
		bad := bytes.Clone(envelope)
		bad[c.at] ^= 0xff
		if _, err := Open(me, bad); err == nil {
			t.Errorf("changing %s was accepted", c.what)
		}
	}
}

func TestATruncatedEnvelopeIsRefused(t *testing.T) {
	me := phone(t)
	envelope, err := Seal(me.PublicKey(), []byte(call))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, Overhead - 1, len(envelope) - 1} {
		if _, err := Open(me, envelope[:n]); err == nil {
			t.Errorf("an envelope cut to %d bytes was accepted", n)
		}
	}
}

// A VoIP push may carry 5 KB. The envelope must not be what spends it.
func TestTheEnvelopeIsSmall(t *testing.T) {
	me := phone(t)
	envelope, err := Seal(me.PublicKey(), []byte(call))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(envelope) - len(call); got != Overhead {
		t.Errorf("overhead = %d, Overhead says %d", got, Overhead)
	}
	if len(envelope) > 1024 {
		t.Errorf("a sealed call push is %d bytes", len(envelope))
	}
}

func TestAPhonesKeyIsReadAsItSendsIt(t *testing.T) {
	me := phone(t)
	got, err := ParsePublicKey(me.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(me.PublicKey()) {
		t.Error("the key came back different")
	}
	for _, bad := range [][]byte{nil, {4}, bytes.Repeat([]byte{4}, EphemeralSize)} {
		if _, err := ParsePublicKey(bad); err == nil {
			t.Errorf("%d bytes was accepted as a key", len(bad))
		}
	}
}

// A phone from a later release may seal with a scheme this Linx doesn't
// know; it should say so rather than return nonsense.
func TestAnUnknownVersionIsNamed(t *testing.T) {
	me := phone(t)
	envelope, err := Seal(me.PublicKey(), []byte(call))
	if err != nil {
		t.Fatal(err)
	}
	envelope[0] = 9
	_, err = Open(me, envelope)
	if err == nil || !strings.Contains(err.Error(), "version 9") {
		t.Errorf("err = %v", err)
	}
}
