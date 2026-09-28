package main

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/settings"
)

func runAdminAccessCmd(t *testing.T, st *fakeStore, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := adminAccessCommand(t.Context(), st, &settings.Service{Store: st, Now: time.Now}, args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestAdminAccessCommand(t *testing.T) {
	t.Setenv("LINX_DOMAIN", "example.com")
	st := newFakeStore()
	cur := defaultSettings()
	cur.AdminNetworkRestricted = true
	cur.AdminNetworks = []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}
	st.settings = cur

	code, out, _ := runAdminAccessCmd(t, st)
	if code != 0 || !strings.Contains(out, "only from: 192.168.1.0/24") {
		t.Fatalf("status: code %d, %q", code, out)
	}

	// No "confirm it's you" and no admin network: root on the server is the check.
	code, out, errOut := runAdminAccessCmd(t, st, "open")
	if code != 0 || !strings.Contains(out, "from anywhere again") || !strings.Contains(out, "https://example.com/") {
		t.Fatalf("open: code %d, %q, %q", code, out, errOut)
	}
	if st.settings.AdminNetworkRestricted || len(st.settings.AdminNetworks) != 1 {
		t.Fatalf("settings after open = %+v, want unrestricted with the list kept", st.settings)
	}

	code, out, _ = runAdminAccessCmd(t, st, "open")
	if code != 0 || !strings.Contains(out, "Nothing changed") {
		t.Fatalf("open again: code %d, %q", code, out)
	}
	code, out, _ = runAdminAccessCmd(t, st)
	if code != 0 || !strings.Contains(out, "from anywhere") {
		t.Fatalf("status after open: code %d, %q", code, out)
	}

	if code, _, _ := runAdminAccessCmd(t, st, "close"); code != 2 {
		t.Fatalf("unknown command: code %d, want 2", code)
	}
}
