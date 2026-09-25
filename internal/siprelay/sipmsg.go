package siprelay

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
)

// message is what the relay reads from one SIP message (RFC 3261 §7): its
// first line and the few headers its checks need. It never rewrites
// anything; the bytes are forwarded as they came.
type message struct {
	request bool
	method  string // requests
	status  int    // responses
	// fromUser and toUser are the user parts of the From and To URIs ("" if
	// there's none); fromCount counts From headers, which must be one.
	fromUser, toUser   string
	fromCount, toCount int
	callID, cseq       string
	authUsers          []string // username= of every (Proxy-)Authorization
	staleChallenge     bool     // a 401/407 whose challenge says stale=true
	keepAlive          bool     // only blank lines (RFC 5626 keep-alive)
}

// allowedMethods are the requests a browser's phone line sends or answers
// (JsSIP: sign in, calls, keypad tones, keep-alive checks). Anything else is
// refused, not forwarded.
var allowedMethods = map[string]bool{
	"REGISTER": true, "INVITE": true, "ACK": true, "BYE": true, "CANCEL": true,
	"OPTIONS": true, "INFO": true, "UPDATE": true, "PRACK": true,
	"MESSAGE": true, "NOTIFY": true, "SUBSCRIBE": true, "REFER": true,
}

var errMalformed = errors.New("not a SIP message")

// compactForms maps RFC 3261 §7.3.3's one-letter header names to full ones,
// for the headers the relay reads.
var compactForms = map[string]string{"f": "from", "t": "to", "i": "call-id"}

func parseMessage(b []byte) (message, error) {
	var m message
	if len(bytes.TrimSpace(b)) == 0 {
		m.keepAlive = true
		return m, nil
	}
	head, _, _ := bytes.Cut(b, []byte("\r\n\r\n"))
	lines := strings.Split(string(head), "\r\n")
	if err := m.firstLine(lines[0]); err != nil {
		return m, err
	}
	// Unfold continuation lines (RFC 3261 §7.3.1) into their header.
	var headers []string
	for _, l := range lines[1:] {
		if l == "" {
			break
		}
		if (l[0] == ' ' || l[0] == '\t') && len(headers) > 0 {
			headers[len(headers)-1] += " " + strings.TrimSpace(l)
			continue
		}
		headers = append(headers, l)
	}
	for _, h := range headers {
		name, value, ok := strings.Cut(h, ":")
		if !ok {
			return m, errMalformed
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if full, ok := compactForms[name]; ok {
			name = full
		}
		value = strings.TrimSpace(value)
		switch name {
		case "from":
			m.fromCount++
			m.fromUser = uriUser(value)
		case "to":
			m.toCount++
			m.toUser = uriUser(value)
		case "call-id":
			m.callID = value
		case "cseq":
			m.cseq = strings.Join(strings.Fields(value), " ")
		case "authorization", "proxy-authorization":
			m.authUsers = append(m.authUsers, digestParam(value, "username"))
		case "www-authenticate", "proxy-authenticate":
			if strings.EqualFold(digestParam(value, "stale"), "true") {
				m.staleChallenge = true
			}
		}
	}
	return m, nil
}

// checkFraming makes sure a browser's websocket message is exactly one SIP
// message, framed so Asterisk can only read it the way parseMessage did:
// every header line ends in CRLF (no bare CR or LF, which a lenient parser
// could take as a line break the relay didn't see, hiding a header), and
// the body is exactly Content-Length bytes (so nothing after it can be read
// as a second message the relay never checked).
func checkFraming(b []byte) error {
	head, body, ok := bytes.Cut(b, []byte("\r\n\r\n"))
	if !ok {
		return errMalformed
	}
	for i, c := range head {
		switch {
		case c == 0,
			c == '\r' && (i+1 == len(head) || head[i+1] != '\n'),
			c == '\n' && (i == 0 || head[i-1] != '\r'):
			return errMalformed
		}
	}
	length, seen := 0, false
	for _, l := range strings.Split(string(head), "\r\n")[1:] {
		name, value, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "content-length" && name != "l" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if seen || err != nil || n < 0 {
			return errMalformed
		}
		length, seen = n, true
	}
	if len(body) != length {
		return errMalformed
	}
	return nil
}

func (m *message) firstLine(l string) error {
	if rest, ok := strings.CutPrefix(l, "SIP/2.0 "); ok {
		code, _, _ := strings.Cut(rest, " ")
		n, err := strconv.Atoi(code)
		if err != nil || n < 100 || n > 699 {
			return errMalformed
		}
		m.status = n
		return nil
	}
	f := strings.Split(l, " ")
	if len(f) != 3 || f[2] != "SIP/2.0" || f[0] == "" || f[1] == "" {
		return errMalformed
	}
	m.request, m.method = true, f[0]
	return nil
}

// uriUser returns the user part of a From/To header value's URI:
// `"Name" <sip:user@host>;tag=x` or `sip:user@host;tag=x`. It doesn't
// unescape anything: the relay compares it byte for byte with the one
// username it allows, so any other spelling of that name is refused
// rather than risk Asterisk reading it differently.
func uriUser(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) {
		// Skip the quoted display name, which may itself hold "<" or "@".
		i := 1
		for ; i < len(v) && v[i] != '"'; i++ {
			if v[i] == '\\' {
				i++
			}
		}
		if i >= len(v) {
			return ""
		}
		v = v[i+1:]
	}
	var uri string
	if lt := strings.IndexByte(v, '<'); lt >= 0 {
		gt := strings.IndexByte(v[lt:], '>')
		if gt < 0 {
			return ""
		}
		uri = v[lt+1 : lt+gt]
	} else {
		uri, _, _ = strings.Cut(v, ";")
	}
	uri = strings.TrimSpace(uri)
	lower := strings.ToLower(uri)
	switch {
	case strings.HasPrefix(lower, "sip:"):
		uri = uri[4:]
	case strings.HasPrefix(lower, "sips:"):
		uri = uri[5:]
	default:
		return ""
	}
	user, _, ok := strings.Cut(uri, "@")
	if !ok {
		return ""
	}
	return user
}

// digestParam returns a parameter of a Digest credentials or challenge
// header (`Digest username="x", realm="y", ...`), unquoted.
func digestParam(v, name string) string {
	_, params, _ := strings.Cut(strings.TrimSpace(v), " ")
	for _, p := range splitParams(params) {
		k, val, ok := strings.Cut(p, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), name) {
			continue
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		return val
	}
	return ""
}

// splitParams splits on commas outside quoted strings.
func splitParams(s string) []string {
	var out []string
	quoted, start := false, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if quoted {
				i++
			}
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}
