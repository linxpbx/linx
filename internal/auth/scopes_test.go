package auth

import (
	"slices"
	"strings"
	"testing"
)

// The app is a client, never an admin (owner, 2026-10-03): a phone's token
// may only read what an ordinary person reads. Running Linx stays with the
// web app and the command line.
func TestDeviceScopesAreClientOnly(t *testing.T) {
	scopes := DeviceScopes()
	if len(scopes) == 0 {
		t.Fatal("a phone's token holds no scopes at all")
	}
	for _, s := range scopes {
		switch {
		case !ValidScope(s):
			t.Errorf("%q isn't a scope", s)
		case strings.HasSuffix(s, ":write"):
			t.Errorf("a phone's token holds %q: an app never changes how Linx is run", s)
		case Sensitive(s):
			t.Errorf("a phone's token holds the sensitive scope %q", s)
		case !slices.Contains(roleCeilings[RoleUser], s):
			t.Errorf("a phone's token holds %q, which an ordinary person can't", s)
		}
	}
	// Changing the list is a decision, not an accident.
	if want := []string{"extensions:read", "team:read"}; !slices.Equal(scopes, want) {
		t.Errorf("a phone's token holds %v, want %v (docs/PHASE2.md §4)", scopes, want)
	}
	// And the caller can't widen it by keeping the slice.
	scopes[0] = "users:write"
	if DeviceScopes()[0] == "users:write" {
		t.Error("DeviceScopes hands out the list itself")
	}
}
