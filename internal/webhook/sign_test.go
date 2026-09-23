package webhook

import (
	"strings"
	"testing"
	"time"
)

// The test vector from the Standard Webhooks reference libraries.
func TestSignStandardWebhooksVector(t *testing.T) {
	secret := "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	ts := time.Unix(1614265330, 0)
	body := []byte(`{"test": 2432232314}`)
	got, err := Sign([]string{secret}, "msg_p5jXN8AQM9LWM0D4loKWxJek", ts, body)
	if err != nil {
		t.Fatal(err)
	}
	if want := "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="; got != want {
		t.Errorf("Sign() = %q, want %q", got, want)
	}
}

func TestSignTwoSecretsDuringRotation(t *testing.T) {
	oldS, _ := NewSecret()
	newS, _ := NewSecret()
	ts := time.Now()
	body := []byte(`{"type":"webhook.test"}`)
	h, err := Sign([]string{newS, oldS}, "id1", ts, body)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(h)); n != 2 {
		t.Fatalf("header has %d signatures, want 2", n)
	}
	for _, s := range []string{oldS, newS} {
		if !Verify(s, "id1", ts, body, h) {
			t.Errorf("a receiver holding %s... can't verify", s[:10])
		}
	}
	other, _ := NewSecret()
	if Verify(other, "id1", ts, body, h) {
		t.Error("an unrelated secret verified")
	}
	if Verify(newS, "id2", ts, body, h) || Verify(newS, "id1", ts.Add(time.Second), body, h) || Verify(newS, "id1", ts, []byte("{}"), h) {
		t.Error("a signature verified with a changed id, timestamp or body")
	}
}

func TestNewSecret(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSecret()
	if a == b || !strings.HasPrefix(a, SecretPrefix) {
		t.Errorf("NewSecret() = %q, %q", a, b)
	}
	key, err := secretKey(a)
	if err != nil || len(key) != 32 {
		t.Errorf("secretKey() = %d bytes, %v", len(key), err)
	}
	if _, err := Sign([]string{"nope"}, "id", time.Now(), nil); err == nil {
		t.Error("Sign() accepted a secret without whsec_")
	}
}
