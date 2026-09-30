package help

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// These tests keep the guides in docs/help in step with the app
// (docs/HELP.md §5): every screen has a guide, every link, picture and
// bolded button name still exists.

const repo = "../.."

func loadGuides(t *testing.T) []Guide {
	t.Helper()
	guides, err := Load(os.DirFS(repo), "docs/help")
	if err != nil {
		t.Fatal(err)
	}
	if len(guides) == 0 {
		t.Fatal("no guides in docs/help")
	}
	return guides
}

// webSource is the web app's own code (not its tests), file by file.
func webSource(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join(repo, "web/src")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !(strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".tsx")) || strings.Contains(p, ".test.") || strings.HasSuffix(p, ".d.ts") {
			return nil
		}
		b, err := os.ReadFile(p)
		out[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var routePatterns = []*regexp.Regexp{
	regexp.MustCompile(`path === "(/[^"]*)"`),
	regexp.MustCompile(`path: "(/[^"]*)"`),
	regexp.MustCompile(`navigate\("(/[^"]*)"`),
	regexp.MustCompile(`_PATH = "(/[^"]*)"`),
}

// notScreens are addresses in the app that aren't a page anyone reads a
// guide for.
var notScreens = map[string]string{
	"/company-done": "a small window that closes itself after company sign-in",
}

// appRoutes finds the screens' addresses in the web app's code.
func appRoutes(t *testing.T) []string {
	t.Helper()
	var routes []string
	for p, src := range webSource(t) {
		for _, re := range routePatterns {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				_, skip := notScreens[m[1]]
				if !skip && !strings.HasPrefix(m[1], "/api/") && !slices.Contains(routes, m[1]) {
					routes = append(routes, m[1])
				}
			}
		}
		// Addresses with a part that changes (App.tsx: /setup/<token>).
		if strings.Contains(src, `/^\/setup\/([^/]+)$/`) && strings.HasSuffix(p, "App.tsx") {
			routes = append(routes, "/setup/<link>")
		}
	}
	slices.Sort(routes)
	if len(routes) < 20 || !slices.Contains(routes, "/setup/<link>") {
		t.Fatalf("found only these routes, the patterns need updating: %v", routes)
	}
	return routes
}

func TestGuidesFrontMatter(t *testing.T) {
	guides := loadGuides(t)
	byName := map[string]Guide{}
	for _, g := range guides {
		byName[g.Name] = g
	}
	for _, name := range PublicGuides {
		if g, ok := byName[name]; !ok || g.Audience != AudiencePublic {
			t.Errorf("public guide %s is missing or not audience: public", name)
		}
	}
	if _, ok := byName["whats-new"]; !ok {
		t.Error("docs/help/whats-new.md is missing")
	}
	for _, s := range Sections {
		n := 0
		for _, g := range guides {
			if g.Section == s {
				n++
			}
		}
		if n == 0 {
			t.Errorf("section %s has no guides", s)
		}
	}
}

func TestEveryScreenHasAGuide(t *testing.T) {
	routes := appRoutes(t)
	named := map[string]bool{}
	for _, g := range loadGuides(t) {
		for _, s := range g.Screens {
			if !slices.Contains(routes, s) {
				t.Errorf("%s: screens names %s, which the app no longer has", g.Name, s)
			}
			named[s] = true
		}
	}
	for _, r := range routes {
		if !named[r] {
			t.Errorf("no guide names the screen %s in screens", r)
		}
	}
}

var (
	linkRE  = regexp.MustCompile(`(!?)\[[^\]]*\]\(([^)]*)\)`)
	shotRE  = regexp.MustCompile("shot\\(page, `\\$\\{scheme\\}-([a-z0-9-]+)`\\)")
	boldRE  = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	spaceRE = regexp.MustCompile(`\s+`)
)

// Links go to other guides (name, name#heading, #heading) and pictures to
// the screenshots make screens takes (screen:<name>, light and dark). A
// guide never links to the design docs or to other sites.
func TestGuideLinksAndPictures(t *testing.T) {
	guides := loadGuides(t)
	byName := map[string]Guide{}
	for _, g := range guides {
		byName[g.Name] = g
	}
	spec, err := os.ReadFile(filepath.Join(repo, "web/e2e/screens.spec.ts"))
	if err != nil {
		t.Fatal(err)
	}
	shots := map[string]bool{}
	for _, m := range shotRE.FindAllStringSubmatch(string(spec), -1) {
		shots[m[1]] = true
	}
	if len(shots) < 20 {
		t.Fatalf("found only %d screenshots in screens.spec.ts, the pattern needs updating", len(shots))
	}
	for _, g := range guides {
		for _, m := range linkRE.FindAllStringSubmatch(g.Body, -1) {
			target := m[2]
			if m[1] == "!" {
				name, ok := strings.CutPrefix(target, "screen:")
				if !ok || !shots[name] {
					t.Errorf("%s: picture %q is not screen:<a screenshot make screens takes>", g.Name, target)
				}
				continue
			}
			name, anchor, _ := strings.Cut(target, "#")
			if name == "" {
				name = g.Name
			}
			other, ok := byName[name]
			switch {
			case !NamePattern.MatchString(name) || !ok:
				t.Errorf("%s: link %q goes to no guide", g.Name, target)
			case anchor != "" && !slices.Contains(other.Headings, anchor):
				t.Errorf("%s: link %q: %s has no heading %q (has %v)", g.Name, target, name, anchor, other.Headings)
			}
		}
	}
}

// A button or heading quoted in **bold** must still be in the web app, so
// renaming one on a screen fails here until its guide follows.
func TestGuideBoldTextIsOnAScreen(t *testing.T) {
	var all strings.Builder
	for _, src := range webSource(t) {
		all.WriteString(spaceRE.ReplaceAllString(src, " "))
	}
	corpus := strings.ReplaceAll(all.String(), "&apos;", "'")
	for _, g := range loadGuides(t) {
		for _, m := range boldRE.FindAllStringSubmatch(g.Body, -1) {
			if text := spaceRE.ReplaceAllString(m[1], " "); !strings.Contains(corpus, text) {
				t.Errorf("%s: **%s** is on no screen (renamed? use *italics* for emphasis)", g.Name, text)
			}
		}
	}
}

func TestParseFailsClosed(t *testing.T) {
	ok := "---\ntitle: T\naudience: everyone\nsection: admin\nkeywords: [a, b]\nscreens: []\n---\n## Hi there\n"
	g, err := Parse("some-guide", ok)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.Keywords, []string{"a", "b"}) || !slices.Equal(g.Headings, []string{"hi-there"}) {
		t.Fatalf("parsed %+v", g)
	}
	for name, text := range map[string]string{
		"no audience":        strings.Replace(ok, "audience: everyone\n", "", 1),
		"unknown audience":   strings.Replace(ok, "everyone", "Everyone", 1),
		"public not allowed": strings.Replace(ok, "everyone", "public", 1),
		"unknown section":    strings.Replace(ok, "section: admin", "section: misc", 1),
		"no screens":         strings.Replace(ok, "screens: []\n", "", 1),
		"bad list":           strings.Replace(ok, "[a, b]", "a, b", 1),
		"unknown key":        strings.Replace(ok, "title: T", "title: T\nowner: x", 1),
		"no front matter":    "## Hi\n",
	} {
		if _, err := Parse("some-guide", text); err == nil {
			t.Errorf("%s: parsed without an error", name)
		}
	}
	if _, err := Parse("signing-in", strings.Replace(ok, "everyone", "public", 1)); err != nil {
		t.Errorf("a listed public guide: %v", err)
	}
	for _, bad := range []string{"../x", "X", "a/b", "a.md", "", strings.Repeat("a", 65)} {
		if _, err := Parse(bad, ok); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

func TestCanRead(t *testing.T) {
	for _, tc := range []struct {
		role, audience string
		want           bool
	}{
		{"", AudiencePublic, true}, {"", AudienceEveryone, false}, {"", AudienceAdmin, false},
		{"user", AudienceEveryone, true}, {"user", AudienceAdmin, false},
		{"reporter", AudienceAdmin, true}, {"reporter", AudienceSystemAdmin, false},
		{"admin", AudienceSystemAdmin, false}, {"system_admin", AudienceSystemAdmin, true},
		{"system_admin", "", false}, {"system_admin", "bogus", false},
	} {
		if got := CanRead(tc.role, tc.audience); got != tc.want {
			t.Errorf("CanRead(%q, %q) = %v", tc.role, tc.audience, got)
		}
	}
}

// Every picture a guide shows is in docs/help/pictures, light and dark, and
// nothing else is: make screens saves them (web/e2e/screens.spec.ts), and a
// picture no guide uses any more is deleted.
func TestGuidePicturesAreSaved(t *testing.T) {
	used := map[string]bool{}
	for _, g := range loadGuides(t) {
		for _, p := range g.Pictures() {
			used["light-"+p+".webp"], used["dark-"+p+".webp"] = true, true
		}
	}
	entries, err := os.ReadDir(filepath.Join(repo, "docs/help", PicturesDir))
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[e.Name()] = true
		if !used[e.Name()] {
			t.Errorf("docs/help/pictures/%s: no guide uses it; delete it", e.Name())
		}
	}
	for f := range used {
		if !have[f] {
			t.Errorf("docs/help/pictures/%s is missing: run make screens", f)
		}
	}
	if _, err := Open(os.DirFS(filepath.Join(repo, "docs/help"))); err != nil {
		t.Errorf("the control plane can't open the guides: %v", err)
	}
}
