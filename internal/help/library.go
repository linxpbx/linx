package help

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
)

// DefaultDir is where the control-plane image keeps the guides and their
// pictures (deploy/docker/control-plane.Dockerfile).
const DefaultDir = "/usr/share/linx/help"

// PicturesDir is the pictures' folder inside the guides' folder: the
// screenshots guides use, as <light|dark>-<name>.webp, which make screens
// writes (web/e2e/screens.spec.ts).
const PicturesDir = "pictures"

// PictureFile is a picture's file name as Help asks for it.
var PictureFile = regexp.MustCompile(`^(light|dark)-([a-z0-9-]{1,64})\.webp$`)

// Library is the guides a server serves (docs/HELP.md §6): two indexes
// built apart, so a caller who isn't signed in only ever reads the public
// one, and every answer is worked out from the caller's role on the server.
type Library struct {
	public, full *Index
	// pictures by file name, with the guides that use them. Their bytes
	// stay on disk until asked for (the low-resource rule, CLAUDE.md).
	pictures map[string]picture
	fsys     fs.FS
}

type picture struct {
	etag   string
	guides []*Guide // in the full index
}

// Open reads the guides and pictures in fsys (its top folder), checking
// them as the tests do: a bad guide, or a picture a guide uses that isn't
// there, fails.
func Open(fsys fs.FS) (*Library, error) {
	guides, err := Load(fsys, ".")
	if err != nil {
		return nil, err
	}
	if len(guides) == 0 {
		return nil, fmt.Errorf("no guides")
	}
	var public []Guide
	for _, g := range guides {
		if g.Audience == AudiencePublic {
			public = append(public, g)
		}
	}
	lib := &Library{public: NewIndex(public), full: NewIndex(guides), pictures: map[string]picture{}, fsys: fsys}
	for _, g := range lib.full.Guides() {
		for _, name := range g.Pictures() {
			for _, scheme := range []string{"light", "dark"} {
				file := scheme + "-" + name + ".webp"
				p, ok := lib.pictures[file]
				if !ok {
					b, err := fs.ReadFile(fsys, path.Join(PicturesDir, file))
					if err != nil {
						return nil, fmt.Errorf("%s uses a picture that isn't there: %w", g.Name, err)
					}
					sum := sha256.Sum256(b)
					p = picture{etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
				}
				p.guides = append(p.guides, g)
				lib.pictures[file] = p
			}
		}
	}
	return lib, nil
}

// index is the only index a caller may search: the public one without a
// session, whatever else the request says.
func (l *Library) index(role string) *Index {
	if role == "" {
		return l.public
	}
	return l.full
}

func readable(role string) func(*Guide) bool {
	return func(g *Guide) bool { return CanRead(role, g.Audience) }
}

// Guides is the guide list a caller with role ("" when not signed in) sees.
func (l *Library) Guides(role string) []*Guide {
	var out []*Guide
	for _, g := range l.index(role).Guides() {
		if CanRead(role, g.Audience) {
			out = append(out, g)
		}
	}
	return out
}

// Guide is one guide, if role may read it. A guide that isn't there and one
// the caller may not read are the same false, so callers answer both alike.
func (l *Library) Guide(role, name string) (*Guide, bool) {
	if !NamePattern.MatchString(name) {
		return nil, false
	}
	g, ok := l.index(role).Guide(name)
	if !ok || !CanRead(role, g.Audience) {
		return nil, false
	}
	return g, true
}

// Search searches the guides role may read.
func (l *Library) Search(role, question string) []Result {
	return l.index(role).Search(question, readable(role))
}

// Excerpts are the best whole sections of the guides role may read for
// question, up to maxWords words: what a written answer is made from.
func (l *Library) Excerpts(role, question string, maxWords int) []Excerpt {
	return l.index(role).Excerpts(question, readable(role), maxWords)
}

// Picture is a picture's bytes and ETag, if a guide role may read uses it.
func (l *Library) Picture(role, file string) (data []byte, etag string, ok bool) {
	if !PictureFile.MatchString(file) {
		return nil, "", false
	}
	p, found := l.pictures[file]
	if !found {
		return nil, "", false
	}
	for _, g := range p.guides {
		if _, ok := l.Guide(role, g.Name); ok {
			b, err := fs.ReadFile(l.fsys, path.Join(PicturesDir, file))
			return b, p.etag, err == nil
		}
	}
	return nil, "", false
}

// Pictures is the screenshots a guide shows, by name.
func (g *Guide) Pictures() []string {
	var out []string
	for _, b := range g.Blocks {
		if b.Type == BlockPicture && !slices.Contains(out, b.Picture) {
			out = append(out, b.Picture)
		}
	}
	return out
}
