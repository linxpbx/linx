package dbsecret

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func randKey(t *testing.T) [KeySize]byte {
	t.Helper()
	var k [KeySize]byte
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	s := NewSealer(randKey(t))
	blob, err := s.Seal("webhook_endpoint:1", []byte("whsec_topsecret"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Open("webhook_endpoint:1", blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "whsec_topsecret" {
		t.Errorf("Open() = %q, want %q", got, "whsec_topsecret")
	}
}

func TestOpenWrongRowID(t *testing.T) {
	s := NewSealer(randKey(t))
	blob, err := s.Seal("webhook_endpoint:1", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open("webhook_endpoint:2", blob); err == nil {
		t.Error("Open() with the wrong row id succeeded")
	}
}

func TestOpenWrongKey(t *testing.T) {
	blob, err := NewSealer(randKey(t)).Seal("row:1", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSealer(randKey(t)).Open("row:1", blob); err == nil {
		t.Error("Open() with a different key succeeded")
	}
}

func TestOpenTampered(t *testing.T) {
	s := NewSealer(randKey(t))
	blob, err := s.Seal("row:1", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-1] ^= 0xff
	if _, err := s.Open("row:1", blob); err == nil {
		t.Error("Open() with a tampered blob succeeded")
	}
}

func TestOpenTooShort(t *testing.T) {
	s := NewSealer(randKey(t))
	if _, err := s.Open("row:1", []byte("short")); err == nil {
		t.Error("Open() with a too-short blob succeeded")
	}
}

func TestSealDeterministicKeyID(t *testing.T) {
	key := randKey(t)
	a, err := NewSealer(key).Seal("row:1", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	// Sealing again with the same key must produce a blob whose key id
	// (the first keyIDSize bytes) another Sealer for the same key accepts.
	if _, err := NewSealer(key).Open("row:1", a); err != nil {
		t.Errorf("a second Sealer built from the same key couldn't open it: %v", err)
	}
}

func TestLoadKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	want := randKey(t)
	if err := os.WriteFile(path, want[:], 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Error("LoadKey() returned a different key than was written")
	}
}

func TestLoadKeyWrongSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte("too short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(path); err == nil {
		t.Error("LoadKey() with a wrong-size file succeeded")
	}
}

func TestLoadKeyMissing(t *testing.T) {
	if _, err := LoadKey(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("LoadKey() with a missing file succeeded")
	}
}

func TestKeyPathFromEnv(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if got, want := KeyPathFromEnv(getenv), "/run/secrets/linx_db_encryption_key"; got != want {
		t.Errorf("default = %q, want %q", got, want)
	}
	env["LINX_DB_ENCRYPTION_KEY_FILE"] = "/tmp/custom_key"
	if got, want := KeyPathFromEnv(getenv), "/tmp/custom_key"; got != want {
		t.Errorf("override = %q, want %q", got, want)
	}
}
