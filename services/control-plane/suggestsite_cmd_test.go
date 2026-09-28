package main

import (
	"bytes"
	"testing"
	"time"

	"linxpbx.com/linx/internal/settings"
)

func TestSuggestSiteCommand(t *testing.T) {
	st := newFakeStore()
	st.settings = defaultSettings()
	run := func(args ...string) int {
		var out, errb bytes.Buffer
		return suggestSiteCommand(t.Context(), st, &settings.Service{Store: st, Now: time.Now}, args, &out, &errb)
	}
	if code := run("castle"); code != 2 {
		t.Errorf("unknown place: %d", code)
	}
	if code := run("home"); code != 0 || st.settings.SiteKind != "home" {
		t.Fatalf("home: %d %q", code, st.settings.SiteKind)
	}
	// Someone already chose: left alone.
	st.settings.SiteKind = "business"
	if code := run("home"); code != 0 || st.settings.SiteKind != "business" {
		t.Errorf("already chosen: %d %q", code, st.settings.SiteKind)
	}
}
