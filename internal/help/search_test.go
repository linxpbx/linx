package help

import (
	"slices"
	"strings"
	"testing"
)

func everyGuide(*Guide) bool { return true }

// Plain questions, worded the ways people ask, find the right guide near the
// top (docs/HELP.md §3).
func TestSearchFindsTheRightGuide(t *testing.T) {
	ix := NewIndex(loadGuides(t))
	for q, want := range map[string]string{
		"how do I add a desk phone?":           "desk-phones-and-phone-apps",
		"why can't my phone register?":         "phone-wont-register",
		"I forgot my password":                 "locked-out",
		"lost my phone with the authenticator": "lost-authenticator",
		"no sound on calls":                    "no-audio",
		"connect my phone company":             "phone-lines",
		"restore a backup":                     "restore",
		"add a new user":                       "people-and-invites",
		"a number for reception":               "extensions",
		"block international calls":            "calling-permissions",
		"move linx to another server":          "moving-to-a-new-server",
		"my line is down":                      "line-down",
		"log in with google":                   "company-sign-in",
		"certificate":                          "install-linx",
		"test my microphone":                   "microphone-and-speaker",
		"nginx reverse proxy":                  "front-doors",
		"restart a service":                    "system-status",
	} {
		got := ix.Search(q, everyGuide)
		var names []string
		for _, r := range got {
			if !slices.Contains(names, r.Guide) {
				names = append(names, r.Guide)
			}
		}
		if i := slices.Index(names, want); i < 0 || i > 2 {
			t.Errorf("%q: want %s in the first 3 guides, got %v", q, want, names)
		}
	}
}

func TestSearchNothingFound(t *testing.T) {
	ix := NewIndex(loadGuides(t))
	for _, q := range []string{"", "   ", "how do I", "xyzzy plugh", "?!"} {
		if got := ix.Search(q, everyGuide); len(got) != 0 {
			t.Errorf("%q found %v", q, got)
		}
	}
}

func TestSearchResults(t *testing.T) {
	ix := NewIndex(loadGuides(t))
	got := ix.Search("backup password", everyGuide)
	if len(got) == 0 || len(got) > MaxResults {
		t.Fatalf("got %d results", len(got))
	}
	per := map[string]int{}
	for _, r := range got {
		per[r.Guide]++
		if per[r.Guide] > maxPerGuide || len(r.Lines) > maxLines || r.Title == "" {
			t.Errorf("result %+v", r)
		}
		if g, _ := ix.Guide(r.Guide); r.Anchor != "" && !slices.Contains(g.Headings, r.Anchor) {
			t.Errorf("%s has no heading %s", r.Guide, r.Anchor)
		}
	}
	// The filter decides what a caller sees.
	for _, r := range ix.Search("backup password", func(g *Guide) bool { return g.Audience == AudienceEveryone }) {
		if g, _ := ix.Guide(r.Guide); g.Audience != AudienceEveryone {
			t.Errorf("found %s (%s)", r.Guide, g.Audience)
		}
	}
}

func TestStem(t *testing.T) {
	for _, group := range [][]string{
		{"register", "registering", "registered", "registers"},
		{"phone", "phones"},
		{"setting", "settings", "set"},
		{"call", "calls", "calling", "called"},
		{"address", "addresses"},
		{"entry", "entries"},
		{"move", "moving", "moved"},
	} {
		for _, w := range group[1:] {
			if Stem(w) != Stem(group[0]) {
				t.Errorf("Stem(%q) = %q, Stem(%q) = %q", w, Stem(w), group[0], Stem(group[0]))
			}
		}
	}
	for _, w := range []string{"6464", "2fa", "status", "access"} {
		if Stem(w) == "" {
			t.Errorf("Stem(%q) is empty", w)
		}
	}
}

func TestExcerptsAreWholeSectionsWithinTheWordLimit(t *testing.T) {
	g := func(name, title, body string) Guide {
		t.Helper()
		guide, err := Parse(name, "---\ntitle: "+title+"\naudience: everyone\nsection: everyday\nkeywords: []\nscreens: []\n---\n# "+title+"\n\n"+body+"\n")
		if err != nil {
			t.Fatal(err)
		}
		return guide
	}
	ix := NewIndex([]Guide{
		g("desk-phones", "Desk phones", "## Adding one\n\nScan the code on the desk phone.\n\n- Open the page\n- Press the button"),
		g("voicemail", "Voicemail", "## Long\n\n"+strings.Repeat("desk ", 50)),
	})
	all := func(*Guide) bool { return true }
	got := ix.Excerpts("desk phone", all, 30)
	if len(got) != 1 || got[0].Guide != "desk-phones" || got[0].Heading != "Adding one" ||
		got[0].Text != "Scan the code on the desk phone.\nOpen the page\nPress the button" {
		t.Errorf("%+v", got)
	}
	if got := ix.Excerpts("desk phone", func(g *Guide) bool { return g.Name != "desk-phones" }, 3000); len(got) != 1 || got[0].Guide != "voicemail" {
		t.Errorf("a guide the caller can't read was used: %+v", got)
	}
}
