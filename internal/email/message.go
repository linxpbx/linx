package email

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Field limits.
const (
	maxAddress = 254
	maxName    = 100
	maxSubject = 200
	maxText    = 64 << 10
)

// CheckAddress returns a plain reason when a isn't one plain email
// address (no name part, no line breaks), or "".
func CheckAddress(a string) string {
	if a == "" {
		return "Give an email address."
	}
	if len(a) > maxAddress || strings.ContainsAny(a, "\r\n\t <>\",;") || !utf8.ValidString(a) {
		return a + " isn't an email address."
	}
	p, err := mail.ParseAddress(a)
	if err != nil || p.Address != a || p.Name != "" || !strings.Contains(a[strings.LastIndexByte(a, '@')+1:], ".") {
		return a + " isn't an email address."
	}
	return ""
}

// CheckName returns a plain reason when n can't be a "From" name, or "".
func CheckName(n string) string {
	if utf8.RuneCountInString(n) > maxName {
		return "The From name is at most 100 characters."
	}
	if hasControl(n) {
		return "The From name can't have line breaks."
	}
	return ""
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return !utf8.ValidString(s)
}

// checkContent refuses what would break the message's headers (a line
// break in the subject is how header injection works).
func checkContent(c Content) error {
	switch {
	case c.Subject == "" || utf8.RuneCountInString(c.Subject) > maxSubject:
		return fmt.Errorf("an email's subject is 1 to %d characters", maxSubject)
	case hasControl(c.Subject):
		return fmt.Errorf("an email's subject can't have line breaks")
	case len(c.Text) > maxText || !utf8.ValidString(c.Text):
		return fmt.Errorf("an email's text is valid UTF-8 of at most %d bytes", maxText)
	}
	return nil
}

// build writes the whole message: headers, then a plain-text part and an
// HTML copy of it (links clickable, nothing remote, no tracking).
func build(from mail.Address, to []string, c Content, now time.Time) ([]byte, error) {
	if err := checkContent(c); err != nil {
		return nil, err
	}
	for _, a := range to {
		if CheckAddress(a) != "" {
			return nil, fmt.Errorf("%q isn't an email address", a)
		}
	}
	if CheckAddress(from.Address) != "" || CheckName(from.Name) != "" {
		return nil, fmt.Errorf("the sender %q isn't valid", from.String())
	}
	var b bytes.Buffer
	boundary := randomHex(16)
	domain := from.Address[strings.LastIndexByte(from.Address, '@')+1:]
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", from.String())
	header("To", strings.Join(to, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", c.Subject))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+randomHex(16)+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Auto-Submitted", "auto-generated")
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")

	part := func(contentType, body string) error {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", boundary, contentType)
		w := quotedprintable.NewWriter(&b)
		if _, err := w.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n"))); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		b.WriteString("\r\n")
		return nil
	}
	text := strings.ReplaceAll(c.Text, "\r\n", "\n")
	if err := part("text/plain", text); err != nil {
		return nil, err
	}
	if err := part("text/html", toHTML(text)); err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes(), nil
}

var linkPattern = regexp.MustCompile(`https://[^\s<>"]+`)

// toHTML is the plain text as simple HTML: escaped, paragraphs kept, and
// https links clickable.
func toHTML(text string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body style="font-family:-apple-system,'Segoe UI',Roboto,Arial,sans-serif;font-size:15px;line-height:1.5;color:#1a1a1a">`)
	// Linx Cobalt (design/tokens.json): an email can't read the web app's
	// stylesheet.
	b.WriteString(`<p style="font-weight:600;color:#1F5FD6">Linx</p>`)
	for _, para := range strings.Split(strings.TrimSpace(text), "\n\n") {
		b.WriteString("<p>")
		lines := strings.Split(para, "\n")
		for i, line := range lines {
			if i > 0 {
				b.WriteString("<br>")
			}
			last := 0
			for _, m := range linkPattern.FindAllStringIndex(line, -1) {
				b.WriteString(html.EscapeString(line[last:m[0]]))
				u := strings.TrimRight(line[m[0]:m[1]], ".,:;)")
				fmt.Fprintf(&b, `<a href="%s">%s</a>`, html.EscapeString(u), html.EscapeString(u))
				last = m[0] + len(u)
			}
			b.WriteString(html.EscapeString(line[last:]))
		}
		b.WriteString("</p>")
	}
	b.WriteString("</body></html>")
	return b.String()
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
