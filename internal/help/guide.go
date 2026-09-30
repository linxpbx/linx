// Package help reads the help guides in docs/help (docs/HELP.md): one
// Markdown file per guide, with front matter saying who may read it, where it
// sits in the guide list and which screens it is about. The same rules check
// the guides in tests and, later, build the search index into the image.
package help

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Who may read a guide (docs/HELP.md §6). Anything else fails: nothing is
// ever public by default.
const (
	AudiencePublic      = "public"
	AudienceEveryone    = "everyone"
	AudienceAdmin       = "admin"
	AudienceSystemAdmin = "system_admin"
)

// Sections of the guide list, in the order Help shows them (§2).
var Sections = []string{"whats-new", "install", "everyday", "admin", "running"}

// PublicGuides is the only guides that may open without signing in: the
// sign-in ones (§8 item 3). Opening another needs a code change here and a
// review, never just a front-matter edit.
var PublicGuides = []string{
	"signing-in",
	"passkeys-and-authenticator",
	"lost-authenticator",
	"locked-out",
	"set-password-link",
}

// NamePattern is what a guide's name (its file name without .md) must look
// like; addresses are looked up by name, never turned into paths.
var NamePattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// Guide is one parsed guide.
type Guide struct {
	Name     string
	Title    string
	Audience string
	Section  string
	Keywords []string
	Screens  []string
	Body     string   // the Markdown after the front matter
	Blocks   []Block  // Body as Help shows it
	Headings []string // the anchors of its ## and ### headings
}

// Load reads every *.md guide in dir of fsys and checks each one's front
// matter. It fails on the first bad guide.
func Load(fsys fs.FS, dir string) ([]Guide, error) {
	names, err := fs.Glob(fsys, path.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	var guides []Guide
	for _, p := range names {
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, err
		}
		g, err := Parse(strings.TrimSuffix(path.Base(p), ".md"), string(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		guides = append(guides, g)
	}
	return guides, nil
}

// Parse reads one guide and checks its front matter.
func Parse(name, text string) (Guide, error) {
	g := Guide{Name: name}
	if !NamePattern.MatchString(name) {
		return g, fmt.Errorf("name %q must match %s", name, NamePattern)
	}
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return g, fmt.Errorf("no front matter")
	}
	front, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return g, fmt.Errorf("front matter not closed")
	}
	g.Body = body
	seen := map[string]bool{}
	for _, line := range strings.Split(front, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return g, fmt.Errorf("front matter line %q is not key: value", line)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if seen[key] {
			return g, fmt.Errorf("%s given twice", key)
		}
		seen[key] = true
		switch key {
		case "title":
			g.Title = value
		case "audience":
			g.Audience = value
		case "section":
			g.Section = value
		case "keywords":
			g.Keywords, ok = list(value)
		case "screens":
			g.Screens, ok = list(value)
		default:
			return g, fmt.Errorf("unknown front matter key %q", key)
		}
		if !ok {
			return g, fmt.Errorf("%s must be a [list, of, words]", key)
		}
	}
	switch {
	case g.Title == "":
		return g, fmt.Errorf("no title")
	case !slices.Contains([]string{AudiencePublic, AudienceEveryone, AudienceAdmin, AudienceSystemAdmin}, g.Audience):
		return g, fmt.Errorf("audience %q is not public, everyone, admin or system_admin", g.Audience)
	case g.Audience == AudiencePublic && !slices.Contains(PublicGuides, name):
		return g, fmt.Errorf("only the sign-in guides may be public (help.PublicGuides)")
	case !slices.Contains(Sections, g.Section):
		return g, fmt.Errorf("section %q is not one of %v", g.Section, Sections)
	case (g.Section == "whats-new") != (name == "whats-new"):
		return g, fmt.Errorf("only whats-new is in the whats-new section")
	case !seen["keywords"] || !seen["screens"]:
		return g, fmt.Errorf("keywords and screens are required (screens may be [])")
	}
	blocks, err := parseBlocks(body)
	if err != nil {
		return g, err
	}
	g.Blocks = blocks
	for _, b := range blocks {
		if b.Type == BlockHeading && b.Level > 1 {
			g.Headings = append(g.Headings, b.Anchor)
		}
	}
	return g, nil
}

func list(v string) ([]string, bool) {
	inner, ok := strings.CutPrefix(v, "[")
	if !ok {
		return nil, false
	}
	inner, ok = strings.CutSuffix(inner, "]")
	if !ok {
		return nil, false
	}
	var out []string
	for _, w := range strings.Split(inner, ",") {
		if w = strings.TrimSpace(w); w != "" {
			out = append(out, w)
		}
	}
	return out, true
}

var notAnchor = regexp.MustCompile(`[^a-z0-9]+`)

// Anchor is a heading's link name: "Lost your phone?" → "lost-your-phone".
func Anchor(heading string) string {
	return strings.Trim(notAnchor.ReplaceAllString(strings.ToLower(heading), "-"), "-")
}

// CanRead says whether a person with this role (none when not signed in:
// "") may read a guide for this audience.
func CanRead(role, audience string) bool {
	switch audience {
	case AudiencePublic:
		return true
	case AudienceEveryone:
		return role != ""
	case AudienceAdmin:
		// Reporters see every admin page (read-only), so its guides too.
		return role == "admin" || role == "system_admin" || role == "reporter"
	case AudienceSystemAdmin:
		return role == "system_admin"
	}
	return false
}
