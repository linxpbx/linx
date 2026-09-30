package installer

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"os"
	"testing"
)

// The web screenshots show the card setup makes (web/e2e/door-setup.json):
// it's kept the same as DoorSetup's. UPDATE_FIXTURES=1 writes it again.
func TestDoorSetupFixture(t *testing.T) {
	const fixture = "../../web/e2e/door-setup.json"
	c := DefaultConfig()
	c.Domain.Name = "example.com"
	c.FrontDoor = FrontDoorConfig{Kind: FrontDoorProxy, ProxyAddress: "192.168.1.20"}
	lan := LAN{Address: netip.MustParseAddr("192.168.1.212"), Network: netip.MustParsePrefix("192.168.1.0/24")}
	want, err := json.MarshalIndent(DoorSetup(c, lan), "", "  ")
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
		t.Errorf("%s is stale (%v): run UPDATE_FIXTURES=1 go test ./internal/installer -run TestDoorSetupFixture", fixture, err)
	}
}
