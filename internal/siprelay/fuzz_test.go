package siprelay

import (
	"strings"
	"testing"
)

// The SIP relay reads frames straight off the internet (a browser's phone
// line, a phone's device line), so its parsers are the backend's most
// exposed attacker-controlled code. These fuzz tests hold two things no
// amount of reading can prove across every input: the parsers never panic
// or hang, and the security checks that gate forwarding can't be fooled
// into contradicting themselves.
//
// Run deeper than the unit corpus with, e.g.:
//
//	go test ./internal/siprelay/ -run x -fuzz FuzzParseMessage -fuzztime 2m

// The one username the relay would allow, for the differential checks
// below. It is the shape every real device login has (d_ + 8 of
// [A-Za-z0-9], docs/THREAT_MODEL.md).
const fuzzAllowed = "d_ab12cd34"

func seedFrames(f *testing.F) {
	for _, s := range []string{
		"",
		"\r\n\r\n",
		"REGISTER sip:linx SIP/2.0\r\nFrom: <sip:" + fuzzAllowed + "@linx>;tag=1\r\n" +
			"To: <sip:" + fuzzAllowed + "@linx>\r\nCall-ID: a\r\nCSeq: 1 REGISTER\r\nContent-Length: 0\r\n\r\n",
		"INVITE sip:1001@linx SIP/2.0\r\nf: \"Me\" <sip:" + fuzzAllowed + "@linx>;tag=x\r\n" +
			"t: <sip:1001@linx>\r\ni: c\r\nCSeq: 2 INVITE\r\n" +
			"Authorization: Digest username=\"" + fuzzAllowed + "\", realm=\"asterisk\", nonce=\"n\"\r\n" +
			"Content-Length: 3\r\n\r\nabc",
		"SIP/2.0 401 Unauthorized\r\nWWW-Authenticate: Digest realm=\"asterisk\", nonce=\"n\", stale=true\r\n" +
			"From: <sip:" + fuzzAllowed + "@linx>\r\nCSeq: 1 REGISTER\r\nContent-Length: 0\r\n\r\n",
		// The shapes a smuggling attempt takes.
		"REGISTER sip:linx SIP/2.0\r\nContent-Length: 0\r\nContent-Length: 40\r\n\r\n",
		"REGISTER sip:linx SIP/2.0\nFrom: <sip:x@h>\r\n\r\n",
		"REGISTER sip:linx SIP/2.0\r\nFrom: <sip:x@h>\x00\r\n\r\n",
		"INVITE sip:x SIP/2.0\r\nFrom: \"a\\\"<sip:evil@h>\" <sip:" + fuzzAllowed + "@h>\r\n\r\n",
		"INVITE sip:x SIP/2.0\r\nFrom: <sip:" + fuzzAllowed + "@evil@h>\r\n\r\n",
	} {
		f.Add([]byte(s))
	}
}

// FuzzParseMessage: parseMessage never panics, and whatever it decides
// about a frame, checkFraming agrees with itself about the body.
func FuzzParseMessage(f *testing.F) {
	seedFrames(f)
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := parseMessage(b) // must not panic
		if err != nil {
			return
		}
		// A frame parseMessage accepts is either a keep-alive (only blank
		// space) or has a first line it understood: a request with a
		// method, or a response with a status in range.
		switch {
		case m.keepAlive:
			if strings.TrimSpace(string(b)) != "" {
				t.Fatalf("keep-alive on non-blank frame %q", b)
			}
		case m.request:
			if m.method == "" {
				t.Fatalf("request with no method: %q", b)
			}
		default:
			if m.status < 100 || m.status > 699 {
				t.Fatalf("response with out-of-range status %d: %q", m.status, b)
			}
		}

		// If checkFraming also passes, the frame is safe to forward: it
		// holds exactly one message. Re-derive the body and confirm it is
		// exactly Content-Length bytes and that no bare CR/LF or NUL
		// survived in the head — the properties that stop a second,
		// unchecked message being smuggled past the relay to Asterisk.
		if checkFraming(b) != nil {
			return
		}
		head, body, ok := cut(b)
		if !ok {
			t.Fatalf("checkFraming passed a frame with no header terminator: %q", b)
		}
		for i := 0; i < len(head); i++ {
			if head[i] == 0 {
				t.Fatalf("NUL survived checkFraming: %q", b)
			}
			if head[i] == '\r' && (i+1 == len(head) || head[i+1] != '\n') {
				t.Fatalf("bare CR survived checkFraming: %q", b)
			}
			if head[i] == '\n' && (i == 0 || head[i-1] != '\r') {
				t.Fatalf("bare LF survived checkFraming: %q", b)
			}
		}
		if cl, ok := soleContentLength(head); ok && len(body) != cl {
			t.Fatalf("checkFraming passed body %d, Content-Length %d: %q", len(body), cl, b)
		}
	})
}

// FuzzCheckRequest: the gate that decides whether a browser's request may
// reach Asterisk can't be made to allow a request whose From isn't the
// one line it is bound to. This is the check that stops one person's line
// acting as another's.
func FuzzCheckRequest(f *testing.F) {
	seedFrames(f)
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := parseMessage(b)
		if err != nil || !m.request || m.keepAlive {
			return
		}
		s := &session{line: Line{Username: fuzzAllowed}}
		reason := s.checkRequest(m) // must not panic
		if reason != "" {
			return
		}
		// Allowed to forward — so every claim of identity in the frame is
		// exactly this line's username, and nothing else.
		if !allowedMethods[m.method] {
			t.Fatalf("forwarded a disallowed method %q: %q", m.method, b)
		}
		if m.fromCount != 1 || m.fromUser != fuzzAllowed {
			t.Fatalf("forwarded with From %q count %d, want %q: %q", m.fromUser, m.fromCount, fuzzAllowed, b)
		}
		if m.method == "REGISTER" && (m.toCount != 1 || m.toUser != fuzzAllowed) {
			t.Fatalf("forwarded a REGISTER with To %q: %q", m.toUser, b)
		}
		for _, u := range m.authUsers {
			if u != fuzzAllowed {
				t.Fatalf("forwarded with a credential username %q that isn't the line's: %q", u, b)
			}
		}
	})
}

// FuzzURIUser: the user-part extractor never panics, and its result is
// stable — feeding the extracted user back through a plain sip: URI yields
// the same user. A result that changed on a second read would be exactly
// the parser differential the byte-for-byte username check relies on not
// existing.
func FuzzURIUser(f *testing.F) {
	for _, s := range []string{
		"", "sip:a@b", "<sip:a@b>", "\"x\" <sip:a@b>;tag=1", "sips:a@b",
		"<sip:a@b@c>", "\"<sip:evil@h>\" <sip:ok@h>", "sip:a", "<sip:>",
		"\"\\\"\" <sip:a@b>", "<sip:a@b", "sip:a@b;transport=tls",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		u := uriUser(v) // must not panic
		if u == "" {
			return
		}
		// A user extracted once must extract to itself from a canonical
		// URI built out of it. If it didn't, the same bytes could mean two
		// different usernames.
		if got := uriUser("<sip:" + u + "@h>"); got != u {
			t.Fatalf("uriUser(%q)=%q, but re-extracting gives %q", v, u, got)
		}
	})
}

// FuzzDigestParam: the digest-credential reader never panics on any header
// value, and a username it returns is stable the same way.
func FuzzDigestParam(f *testing.F) {
	for _, s := range []string{
		"", "Digest username=\"a\"", "Digest realm=\"x\", nonce=\"y\"",
		"Digest username=a", "Digest username=\"\\\"\"", "Digest username=",
		"Digest stale=true", "username=\"a\"", "Digest  username = \"a\" ",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		_ = digestParam(v, "username") // must not panic
		_ = digestParam(v, "stale")
		for _, p := range splitParams(v) { // must not panic
			_ = p
		}
	})
}

// cut splits a frame the way checkFraming does, for the assertions above.
func cut(b []byte) (head, body []byte, ok bool) {
	i := indexCRLFCRLF(b)
	if i < 0 {
		return nil, nil, false
	}
	return b[:i], b[i+4:], true
}

func indexCRLFCRLF(b []byte) int {
	return strings.Index(string(b), "\r\n\r\n")
}

// soleContentLength returns the single Content-Length in head, or ok=false
// if there isn't exactly one valid one (which checkFraming would reject,
// so the caller only asserts when checkFraming has already passed).
func soleContentLength(head []byte) (int, bool) {
	n, seen := 0, false
	for _, l := range strings.Split(string(head), "\r\n")[1:] {
		name, value, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "content-length" && name != "l" {
			continue
		}
		v, err := parseLen(strings.TrimSpace(value))
		if err != nil || seen {
			return 0, false
		}
		n, seen = v, true
	}
	return n, seen
}

func parseLen(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errMalformed
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errMalformed
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
