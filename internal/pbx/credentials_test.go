package pbx

import (
	"crypto/md5"
	"encoding/hex"
	"regexp"
	"testing"
)

var sipUsernameRE = regexp.MustCompile(`^d_[a-z2-7]{8}$`)

func TestNewSIPUsername(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		u := NewSIPUsername()
		if !sipUsernameRE.MatchString(u) {
			t.Fatalf("NewSIPUsername() = %q, doesn't match %s", u, sipUsernameRE)
		}
		if seen[u] {
			t.Fatalf("NewSIPUsername() repeated %q", u)
		}
		seen[u] = true
	}
}

func TestNewDevicePassword(t *testing.T) {
	a, b := NewDevicePassword(), NewDevicePassword()
	if a == b {
		t.Fatal("two passwords were identical")
	}
	if len(a) < 20 {
		t.Fatalf("password %q looks too short for 128+ random bits", a)
	}
}

func TestDigestHash(t *testing.T) {
	got := DigestHash("d_abcd1234", "hunter2-but-actually-random")
	want := md5.Sum([]byte("d_abcd1234:" + DigestRealm + ":hunter2-but-actually-random"))
	if got != hex.EncodeToString(want[:]) {
		t.Errorf("DigestHash() = %q, want %q", got, hex.EncodeToString(want[:]))
	}
	if len(got) != 32 {
		t.Errorf("DigestHash() length = %d, want 32", len(got))
	}
	// Same inputs, same hash: Asterisk must be able to recompute it.
	if again := DigestHash("d_abcd1234", "hunter2-but-actually-random"); again != got {
		t.Error("DigestHash() isn't deterministic")
	}
	// A different password changes the hash.
	if other := DigestHash("d_abcd1234", "different"); other == got {
		t.Error("DigestHash() didn't change with the password")
	}
}
