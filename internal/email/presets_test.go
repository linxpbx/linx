package email

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// TestPresetsFixture keeps the web page's copy of the presets current.
func TestPresetsFixture(t *testing.T) {
	const fixture = "../../web/src/lib/email-presets.json"
	want, err := json.MarshalIndent(Presets, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("UPDATE_FIXTURES") != "" {
		if err := os.WriteFile(fixture, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(fixture)
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("%s is stale (%v): run UPDATE_FIXTURES=1 go test ./internal/email -run TestPresetsFixture", fixture, err)
	}
}

func TestPresets(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Presets {
		if seen[p.ID] || p.Name == "" || p.Password == "" || p.Where == "" {
			t.Errorf("%+v", p)
		}
		seen[p.ID] = true
		if p.Port != 0 && p.Security != SecurityTLS && p.Security != SecuritySTARTTLS {
			t.Errorf("%s: security %q", p.ID, p.Security)
		}
		if !p.UsernameIsAddress && p.Username == "" {
			t.Errorf("%s: no user name label", p.ID)
		}
	}
	if !seen[PresetOther] {
		t.Error("no Something else")
	}
}
