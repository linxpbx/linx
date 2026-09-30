package help

import (
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// Search over the guides (docs/HELP.md §3): each guide is cut into its
// sections, a question into stemmed words without filler, and sections are
// ranked with BM25, titles, headings and keywords counting more than the
// text. No library: the guides are small and this stays a few hundred
// kilobytes of memory.

// Index searches one set of guides. The server keeps two, built apart
// (§6): one with only the public guides, for people who aren't signed in,
// and one with every guide.
type Index struct {
	guides   map[string]*Guide
	order    []*Guide // guide list order: by section, then title
	sections []section
	df       map[string]int
	avgLen   float64
}

type section struct {
	guide   *Guide
	heading string // "" for the part before the first ## heading
	anchor  string
	lines   []string // what people read, one line per paragraph or item
	stems   []string // every line's words, for matched lines
	tf      map[string]float64
	length  float64
}

// Weights of where a word is found, against 1 for the text.
const (
	weightTitle   = 3
	weightHeading = 2
	weightKeyword = 2
	bm25K1        = 1.2
	bm25B         = 0.75
)

// NewIndex indexes guides. Guides that aren't in it can't be found through
// it, whatever is asked.
func NewIndex(guides []Guide) *Index {
	ix := &Index{guides: map[string]*Guide{}, df: map[string]int{}}
	for i := range guides {
		g := &guides[i]
		ix.guides[g.Name] = g
		ix.order = append(ix.order, g)
	}
	sort.SliceStable(ix.order, func(a, b int) bool {
		sa, sb := slices.Index(Sections, ix.order[a].Section), slices.Index(Sections, ix.order[b].Section)
		if sa != sb {
			return sa < sb
		}
		return ix.order[a].Title < ix.order[b].Title
	})
	total := 0.0
	for _, g := range ix.order {
		var kw []string
		for _, k := range g.Keywords {
			kw = append(kw, Words(k)...)
		}
		title := Words(g.Title)
		cur := section{guide: g}
		finish := func() {
			if cur.heading == "" && len(cur.lines) == 0 {
				return
			}
			tf := map[string]float64{}
			add := func(words []string, w float64) {
				for _, s := range words {
					tf[s] += w
				}
			}
			add(title, weightTitle)
			add(kw, weightKeyword)
			add(Words(cur.heading), weightHeading)
			for _, l := range cur.lines {
				add(Words(l), 1)
			}
			cur.tf = tf
			for s, n := range tf {
				ix.df[s]++
				cur.length += n
			}
			total += cur.length
			ix.sections = append(ix.sections, cur)
		}
		for _, b := range g.Blocks {
			switch b.Type {
			case BlockHeading:
				if b.Level == 1 {
					continue
				}
				finish()
				cur = section{guide: g, heading: b.Text, anchor: b.Anchor}
			case BlockParagraph:
				cur.lines = append(cur.lines, PlainText(b.Inlines))
			case BlockList:
				cur.lines = append(cur.lines, listLines(b)...)
			case BlockCode:
				cur.lines = append(cur.lines, strings.Split(b.Text, "\n")...)
			}
		}
		finish()
	}
	if len(ix.sections) > 0 {
		ix.avgLen = total / float64(len(ix.sections))
	}
	return ix
}

func listLines(b Block) []string {
	var out []string
	for _, it := range b.Items {
		out = append(out, PlainText(it.Inlines))
		if it.List != nil {
			out = append(out, listLines(*it.List)...)
		}
	}
	return out
}

// Guide returns a guide in this index.
func (ix *Index) Guide(name string) (*Guide, bool) {
	g, ok := ix.guides[name]
	return g, ok
}

// Guides is every guide in this index, in guide list order.
func (ix *Index) Guides() []*Guide { return ix.order }

// Result is one section that matched.
type Result struct {
	Guide   string   `json:"guide"`
	Title   string   `json:"title"`
	Heading string   `json:"heading,omitempty"`
	Anchor  string   `json:"anchor,omitempty"`
	Lines   []string `json:"lines"`
	score   float64
}

// Most results and lines a search gives back, and the longest question it
// reads.
const (
	MaxResults      = 10
	maxPerGuide     = 3
	maxLines        = 2
	maxLineRunes    = 240
	MaxQuestionLen  = 500
	maxQuestionWord = 24
)

// Search ranks the sections of the guides allowed says a caller may read
// for question. Nothing found is an empty list.
func (ix *Index) Search(question string, allowed func(*Guide) bool) []Result {
	if len(question) > MaxQuestionLen {
		question = question[:MaxQuestionLen]
	}
	terms := map[string]float64{}
	for _, w := range Words(question) {
		terms[w] = 1
	}
	for _, w := range Words(everyday(question)) {
		if _, ok := terms[w]; !ok {
			terms[w] = 0.8
		}
	}
	if len(terms) == 0 || len(terms) > 2*maxQuestionWord {
		return []Result{}
	}
	n := float64(len(ix.sections))
	var found []Result
	for i := range ix.sections {
		s := &ix.sections[i]
		if !allowed(s.guide) {
			continue
		}
		score := 0.0
		for t, qw := range terms {
			f := s.tf[t]
			if f == 0 {
				continue
			}
			df := float64(ix.df[t])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			score += qw * idf * f * (bm25K1 + 1) / (f + bm25K1*(1-bm25B+bm25B*s.length/ix.avgLen))
		}
		if score > 0 {
			found = append(found, Result{Guide: s.guide.Name, Title: s.guide.Title, Heading: s.heading, Anchor: s.anchor,
				Lines: matchedLines(s.lines, terms), score: score})
		}
	}
	sort.SliceStable(found, func(a, b int) bool { return found[a].score > found[b].score })
	out := []Result{}
	perGuide := map[string]int{}
	for _, r := range found {
		if perGuide[r.Guide] == maxPerGuide {
			continue
		}
		perGuide[r.Guide]++
		out = append(out, r)
		if len(out) == MaxResults {
			break
		}
	}
	return out
}

// matchedLines are the section's lines with the most of the question's
// words, in the order they're written.
func matchedLines(lines []string, terms map[string]float64) []string {
	type hit struct {
		i int
		n float64
	}
	var hits []hit
	for i, l := range lines {
		n := 0.0
		for _, w := range Words(l) {
			n += terms[w]
		}
		if n > 0 {
			hits = append(hits, hit{i, n})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].n > hits[b].n })
	if len(hits) > maxLines {
		hits = hits[:maxLines]
	}
	sort.Slice(hits, func(a, b int) bool { return hits[a].i < hits[b].i })
	out := []string{}
	for _, h := range hits {
		l := []rune(lines[h.i])
		if len(l) > maxLineRunes {
			l = append(l[:maxLineRunes-1], '…')
		}
		out = append(out, string(l))
	}
	return out
}

// Words cuts text into the stems search compares: lower case, without
// question words and filler.
func Words(text string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if stopWords[w] {
			continue
		}
		out = append(out, Stem(w))
	}
	return out
}

// Stem cuts common English endings off a word, so "registering",
// "registered" and "registers" all match "register". It only has to treat
// the guides and the questions the same way, not be good English.
func Stem(w string) string {
	if len(w) <= 3 || !isLetters(w) {
		return w
	}
	switch {
	case strings.HasSuffix(w, "sses"):
		w = w[:len(w)-2]
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		w = w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		w = w[:len(w)-1]
	}
	for _, suffix := range []string{"ing", "ed", "ly"} {
		if base, ok := strings.CutSuffix(w, suffix); ok && len(base) >= 3 && hasVowel(base) {
			w = base
			// "setting" → "sett" → "set"; but not "call" → "cal".
			if n := len(w); n >= 2 && w[n-1] == w[n-2] && !strings.ContainsRune("lsz", rune(w[n-1])) {
				w = w[:n-1]
			}
			break
		}
	}
	if len(w) > 3 && strings.HasSuffix(w, "e") {
		w = w[:len(w)-1]
	}
	if len(w) > 3 && strings.HasSuffix(w, "y") {
		w = w[:len(w)-1] + "i"
	}
	return w
}

func isLetters(w string) bool {
	for _, r := range w {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func hasVowel(w string) bool { return strings.ContainsAny(w, "aeiouy") }

// everyday adds Linx's words for the everyday ones people ask with (§3).
func everyday(question string) string {
	q := " " + strings.Join(strings.FieldsFunc(strings.ToLower(question), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	}), " ") + " "
	var extra []string
	for _, e := range everydayWords {
		if strings.Contains(q, " "+e.say+" ") {
			extra = append(extra, e.linx)
		}
	}
	return strings.Join(extra, " ")
}

var everydayWords = []struct{ say, linx string }{
	{"number", "extension"},
	{"numbers", "extension"},
	{"phone number", "extension incoming number"},
	{"phone company", "phone line provider"},
	{"provider", "phone line"},
	{"carrier", "phone line provider"},
	{"trunk", "phone line"},
	{"sip trunk", "phone line"},
	{"dids", "incoming number"},
	{"log in", "sign in"},
	{"login", "sign in"},
	{"logon", "sign in"},
	{"log on", "sign in"},
	{"log out", "sign out"},
	{"logout", "sign out"},
	{"2fa", "authenticator second step"},
	{"mfa", "authenticator second step"},
	{"otp", "authenticator code"},
	{"two factor", "authenticator second step"},
	{"forgot", "locked out lost"},
	{"forgotten", "locked out lost"},
	{"can't get in", "locked out"},
	{"cannot get in", "locked out"},
	{"headset", "microphone speaker"},
	{"mic", "microphone"},
	{"sound", "audio"},
	{"hear", "audio"},
	{"voice", "audio"},
	{"handset", "desk phone"},
	{"ip phone", "desk phone"},
	{"softphone", "phone app"},
	{"register", "sign in"},
	{"registration", "sign in"},
	{"ssl", "certificate"},
	{"https", "certificate"},
	{"padlock", "certificate"},
	{"cert", "certificate"},
	{"reverse proxy", "front door"},
	{"proxy", "front door"},
	{"nginx", "front door"},
	{"caddy", "front door"},
	{"pangolin", "front door"},
	{"port forward", "front door"},
	{"vpn", "private connection wireguard"},
	{"migrate", "moving new server"},
	{"migration", "moving new server"},
	{"upgrade", "whats new"},
	{"logs", "system status"},
	{"reboot", "restart"},
	{"user", "people"},
	{"users", "people"},
	{"staff", "people"},
	{"employee", "people"},
	{"invite", "invite link"},
	{"sso", "company sign in"},
	{"google", "company sign in"},
	{"microsoft", "company sign in"},
	{"okta", "company sign in"},
	{"international", "calling permissions"},
	{"block", "calling permissions"},
	{"api", "api key"},
	{"integration", "webhook api key"},
	{"crm", "webhook api key"},
	{"notification", "alert"},
	{"notifications", "alert"},
	{"email me", "alert"},
	{"backup file", "download"},
	{"hacked", "activity alert"},
	{"who changed", "activity"},
	{"audit", "activity"},
	{"on a call", "busy presence"},
	{"on the phone", "busy presence"},
	{"online", "presence status"},
}

// stopWords are dropped from questions and guides alike: question words and
// filler that say nothing about what's asked.
var stopWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an the and or but if then than so to of in on at by for from with without into onto
		is are was were be been being am do does did doing done have has had having can could will would shall should may might must
		i me my mine we us our you your yours it its it's this that these those there here what which who whom whose when where why how
		not no nor don t s re ve ll d m can't cannot won didn doesn isn aren wasn weren hasn haven hadn couldn shouldn wouldn
		please help want need get got getting make made let lets just also any some all about up out as very really thing things way
		right now currently today still anymore ever already`) {
		m[w] = true
	}
	return m
}()
