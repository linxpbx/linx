package help

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A guide's body as blocks, the only shapes Help shows (docs/HELP.md §7
// step 2). The web app draws them as its own elements, never as HTML, so a
// guide can't put markup or script on a page. Markdown outside this small
// set (tables, quotes, HTML, ####) fails Parse, so a guide can't use
// something Help would show wrongly.

// Block types.
const (
	BlockHeading   = "heading"
	BlockParagraph = "paragraph"
	BlockList      = "list"
	BlockCode      = "code"
	BlockPicture   = "picture"
)

// Inline types.
const (
	InlineText   = "text"
	InlineBold   = "bold"
	InlineItalic = "italic"
	InlineCode   = "code"
	InlineLink   = "link"
)

// Block is one heading, paragraph, list, code block or picture.
type Block struct {
	Type string `json:"type"`
	// Heading: 1 for the guide's title, 2 or 3; Anchor is its link name.
	Level  int    `json:"level,omitempty"`
	Anchor string `json:"anchor,omitempty"`
	// Heading and code: the text; paragraph: its inline parts.
	Text    string   `json:"text,omitempty"`
	Inlines []Inline `json:"inlines,omitempty"`
	// List.
	Ordered bool       `json:"ordered,omitempty"`
	Start   int        `json:"start,omitempty"`
	Items   []ListItem `json:"items,omitempty"`
	// Picture: a make screens shot's name (light and dark) and its words.
	Picture string `json:"picture,omitempty"`
	Alt     string `json:"alt,omitempty"`
}

// ListItem is one item of a list, with an optional list inside it.
type ListItem struct {
	Inlines []Inline `json:"inlines"`
	List    *Block   `json:"list,omitempty"`
}

// Inline is a run of text: plain, bold, italic, code or a link to a guide.
type Inline struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// Link: the guide (empty for this one) and the heading in it.
	Guide  string `json:"guide,omitempty"`
	Anchor string `json:"anchor,omitempty"`
}

var (
	pictureLine = regexp.MustCompile(`^!\[([^\]]*)\]\(screen:([a-z0-9-]{1,64})\)$`)
	bulletLine  = regexp.MustCompile(`^( *)[-*] (.*)$`)
	numberLine  = regexp.MustCompile(`^( *)([0-9]{1,3})\. (.*)$`)
	linkAt      = regexp.MustCompile(`^\[([^\]]*)\]\(([^)]*)\)`)
)

// parseBlocks reads a guide's body.
func parseBlocks(body string) ([]Block, error) {
	var (
		out   []Block
		para  []string
		list  *Block // the open top-level list
		fence *strings.Builder
	)
	flushPara := func() {
		if len(para) > 0 {
			out = append(out, Block{Type: BlockParagraph, Inlines: parseInlines(strings.Join(para, " "))})
			para = nil
		}
	}
	flushList := func() {
		if list != nil {
			out = append(out, *list)
			list = nil
		}
	}
	for n, line := range strings.Split(body, "\n") {
		fail := func(what string) ([]Block, error) {
			return nil, fmt.Errorf("line %d: %s (Help shows only headings, paragraphs, lists, code and pictures)", n+1, what)
		}
		if fence != nil {
			if strings.HasPrefix(line, "```") {
				out = append(out, Block{Type: BlockCode, Text: strings.TrimSuffix(fence.String(), "\n")})
				fence = nil
			} else {
				fence.WriteString(line + "\n")
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flushPara()
			flushList()
			continue
		}
		if strings.HasPrefix(line, "```") {
			flushPara()
			flushList()
			fence = &strings.Builder{}
			continue
		}
		if m := bulletLine.FindStringSubmatch(line); m != nil {
			flushPara()
			if _, ok := addItem(&list, len(m[1]), false, 0, m[2]); !ok {
				return fail("a list inside a list must be indented under an item")
			}
			continue
		}
		if m := numberLine.FindStringSubmatch(line); m != nil {
			flushPara()
			start, _ := strconv.Atoi(m[2])
			if _, ok := addItem(&list, len(m[1]), true, start, m[3]); !ok {
				return fail("a list inside a list must be indented under an item")
			}
			continue
		}
		if list != nil && strings.HasPrefix(line, " ") {
			// More words for the last item.
			item := lastItem(list)
			item.Inlines = parseInlines(plainJoin(item.Inlines) + " " + trimmed)
			continue
		}
		flushList()
		for _, h := range []struct {
			prefix string
			level  int
		}{{"# ", 1}, {"## ", 2}, {"### ", 3}} {
			if text, ok := strings.CutPrefix(line, h.prefix); ok {
				flushPara()
				out = append(out, Block{Type: BlockHeading, Level: h.level, Text: text, Anchor: Anchor(text)})
				line = ""
				break
			}
		}
		if line == "" {
			continue
		}
		if m := pictureLine.FindStringSubmatch(trimmed); m != nil {
			flushPara()
			out = append(out, Block{Type: BlockPicture, Picture: m[2], Alt: m[1]})
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "#"):
			return fail("only #, ## and ### headings")
		case strings.HasPrefix(trimmed, ">"), strings.HasPrefix(trimmed, "|"), strings.HasPrefix(trimmed, "<"),
			strings.HasPrefix(trimmed, "+ "), strings.HasPrefix(trimmed, "---"), strings.HasPrefix(trimmed, "!["):
			return fail(fmt.Sprintf("%q isn't something Help shows", trimmed[:min(len(trimmed), 20)]))
		case strings.HasPrefix(line, " "):
			return fail("an indented line that isn't part of a list")
		}
		para = append(para, trimmed)
	}
	if fence != nil {
		return nil, fmt.Errorf("a ``` code block is never closed")
	}
	flushPara()
	flushList()
	return out, nil
}

// addItem adds an item to the open list: at the top level (indent 0) or
// inside the last item (indented).
func addItem(list **Block, indent int, ordered bool, start int, text string) (*Block, bool) {
	item := ListItem{Inlines: parseInlines(text)}
	if indent == 0 {
		if *list == nil || (*list).Ordered != ordered {
			if *list != nil {
				return nil, false
			}
			*list = &Block{Type: BlockList, Ordered: ordered}
			if ordered {
				(*list).Start = start
			}
		}
		(*list).Items = append((*list).Items, item)
		return *list, true
	}
	if *list == nil || len((*list).Items) == 0 || indent > 4 {
		return nil, false
	}
	parent := &(*list).Items[len((*list).Items)-1]
	if parent.List == nil {
		parent.List = &Block{Type: BlockList, Ordered: ordered}
		if ordered {
			parent.List.Start = start
		}
	} else if parent.List.Ordered != ordered {
		return nil, false
	}
	parent.List.Items = append(parent.List.Items, item)
	return *list, true
}

func lastItem(list *Block) *ListItem {
	item := &list.Items[len(list.Items)-1]
	if item.List != nil {
		return &item.List.Items[len(item.List.Items)-1]
	}
	return item
}

// plainJoin is inlines back as Markdown, to add a continuation line to.
func plainJoin(in []Inline) string {
	var b strings.Builder
	for _, i := range in {
		switch i.Type {
		case InlineBold:
			b.WriteString("**" + i.Text + "**")
		case InlineItalic:
			b.WriteString("*" + i.Text + "*")
		case InlineCode:
			b.WriteString("`" + i.Text + "`")
		case InlineLink:
			target := i.Guide
			if i.Anchor != "" {
				target += "#" + i.Anchor
			}
			b.WriteString("[" + i.Text + "](" + target + ")")
		default:
			b.WriteString(i.Text)
		}
	}
	return b.String()
}

// parseInlines splits text into plain, **bold**, *italic*, `code` and
// [link](guide#heading) runs. A marker with no closing one is plain text.
func parseInlines(s string) []Inline {
	var out []Inline
	var text strings.Builder
	add := func(typ, t string) {
		if text.Len() > 0 {
			out = append(out, Inline{Type: InlineText, Text: text.String()})
			text.Reset()
		}
		out = append(out, Inline{Type: typ, Text: t})
	}
	for i := 0; i < len(s); {
		rest := s[i:]
		switch {
		case rest[0] == '`':
			if end := strings.IndexByte(rest[1:], '`'); end >= 0 {
				add(InlineCode, rest[1:1+end])
				i += end + 2
				continue
			}
		case rest[0] == '[':
			if m := linkAt.FindStringSubmatch(rest); m != nil {
				add(InlineLink, m[1])
				guide, anchor, _ := strings.Cut(m[2], "#")
				out[len(out)-1].Guide, out[len(out)-1].Anchor = guide, anchor
				i += len(m[0])
				continue
			}
		case strings.HasPrefix(rest, "**"):
			if end := strings.Index(rest[2:], "**"); end > 0 {
				add(InlineBold, rest[2:2+end])
				i += end + 4
				continue
			}
		case rest[0] == '*' && len(rest) > 1 && rest[1] != ' ':
			if end := strings.IndexByte(rest[1:], '*'); end > 0 {
				add(InlineItalic, rest[1:1+end])
				i += end + 2
				continue
			}
		}
		text.WriteByte(s[i])
		i++
	}
	if text.Len() > 0 {
		out = append(out, Inline{Type: InlineText, Text: text.String()})
	}
	return out
}

// PlainText is inlines as the words people read.
func PlainText(in []Inline) string {
	var b strings.Builder
	for _, i := range in {
		b.WriteString(i.Text)
	}
	return b.String()
}
