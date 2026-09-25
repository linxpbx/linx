package turn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIssue(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 500, time.UTC)
	person := uuid.MustParse("01920000-0000-7000-8000-000000000001")
	i := &Issuer{Secret: []byte("north"), URLs: DefaultURLs("example.com"), Now: func() time.Time { return now }}
	c := i.Issue(person)
	if want := "1790254800:" + person.String(); c.Username != want {
		t.Errorf("username %q, want %q", c.Username, want)
	}
	if !c.ExpiresAt.Equal(now.Add(TTL).Truncate(time.Second)) {
		t.Errorf("expires %v", c.ExpiresAt)
	}
	// coturn's own check: base64(HMAC-SHA1(secret, username)), computed
	// independently with openssl:
	//   printf '1790254800:01920000-...-000000000001' | openssl dgst -sha1 -hmac north -binary | base64
	if want := "CO+d7lJftU09ii82FUsj4lnndnA="; c.Password != want {
		t.Errorf("password %q, want %q", c.Password, want)
	}
	if c.URLs[0] != "turn:turn.example.com:443?transport=udp" || c.URLs[1] != "turns:turn.example.com:443?transport=tcp" {
		t.Errorf("urls %v", c.URLs)
	}
}

func TestURLsFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if u, err := URLsFromEnv(env(map[string]string{"LINX_DOMAIN": "x.org"})); err != nil || len(u) != 2 {
		t.Errorf("default: %v %v", u, err)
	}
	u, err := URLsFromEnv(env(map[string]string{"LINX_TURN_URLS": "turns:turn.x.org:5349?transport=tcp, turn:turn.x.org:3478"}))
	if err != nil || len(u) != 2 || u[1] != "turn:turn.x.org:3478" {
		t.Errorf("custom: %v %v", u, err)
	}
	if _, err := URLsFromEnv(env(map[string]string{"LINX_TURN_URLS": "https://x"})); err == nil {
		t.Error("accepted a non-TURN URL")
	}
	if _, err := URLsFromEnv(env(nil)); err == nil {
		t.Error("accepted no domain")
	}
}

func TestLoadSecret(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "s")
		os.WriteFile(p, []byte(s), 0o600)
		return p
	}
	good := strings.Repeat("aB3", 12)
	if b, err := LoadSecret(write(good + "\n")); err != nil || string(b) != good {
		t.Errorf("good: %q %v", b, err)
	}
	for _, bad := range []string{"short", strings.Repeat("a", 40) + " x", strings.Repeat("a", 40) + "#", strings.Repeat("a", 40) + "\nx"} {
		if _, err := LoadSecret(write(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
