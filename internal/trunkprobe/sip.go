package trunkprobe

import (
	"bufio"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// maxMessage bounds one SIP message read from a provider.
const maxMessage = 64 << 10

// response is a SIP response, as much of it as the probe reads.
type response struct {
	Status  int
	Reason  string
	Headers map[string][]string // lower-case names
	Body    string
}

func (r response) header(name string) string {
	if v := r.Headers[strings.ToLower(name)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func randomToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// request is one SIP request the probe sends.
type request struct {
	Method    string
	URI       string
	Transport string // TLS, TCP or UDP (the Via's)
	From, To  string // name-addr, without tag
	CallID    string
	CSeq      int
	Extra     []string // more header lines
}

func (q request) bytes(local net.Addr, branch string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s SIP/2.0\r\n", q.Method, q.URI)
	fmt.Fprintf(&b, "Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport\r\n", q.Transport, local.String(), branch)
	b.WriteString("Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: %s;tag=%s\r\n", q.From, q.CallID[:8])
	fmt.Fprintf(&b, "To: %s\r\n", q.To)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", q.CallID)
	fmt.Fprintf(&b, "CSeq: %d %s\r\n", q.CSeq, q.Method)
	b.WriteString("User-Agent: Linx\r\n")
	for _, h := range q.Extra {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("Content-Length: 0\r\n\r\n")
	return []byte(b.String())
}

// exchange sends q and returns its final response (1xx are skipped). Over
// UDP it retransmits as RFC 3261's timer A does, until deadline.
func exchange(conn net.Conn, udp bool, q request, deadline time.Time) (response, error) {
	branch := randomToken()
	msg := q.bytes(conn.LocalAddr(), branch)
	if err := conn.SetDeadline(deadline); err != nil {
		return response{}, err
	}
	if !udp {
		if _, err := conn.Write(msg); err != nil {
			return response{}, err
		}
		r := bufio.NewReaderSize(io.LimitReader(conn, 4*maxMessage), 4096)
		for {
			resp, err := readStream(r)
			if err != nil {
				return response{}, err
			}
			if resp.Status >= 200 && matches(resp, q) {
				return resp, nil
			}
		}
	}
	wait := 500 * time.Millisecond
	buf := make([]byte, maxMessage)
	for {
		if _, err := conn.Write(msg); err != nil {
			return response{}, err
		}
		until := time.Now().Add(wait)
		if until.After(deadline) {
			until = deadline
		}
		_ = conn.SetReadDeadline(until)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() && time.Now().Before(deadline) {
					break // retransmit
				}
				return response{}, err
			}
			resp, err := parse(string(buf[:n]))
			if err != nil {
				continue
			}
			if resp.Status >= 200 && matches(resp, q) {
				return resp, nil
			}
		}
		wait = min(wait*2, 4*time.Second)
	}
}

// matches: the response is to q (same Call-ID and CSeq).
func matches(r response, q request) bool {
	return r.header("call-id") == q.CallID && r.header("cseq") == strconv.Itoa(q.CSeq)+" "+q.Method
}

// readStream reads one message from a TCP or TLS stream.
func readStream(r *bufio.Reader) (response, error) {
	var head strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return response{}, err
		}
		if head.Len() == 0 && strings.TrimSpace(line) == "" {
			continue // keep-alive CRLFs
		}
		head.WriteString(line)
		if head.Len() > maxMessage {
			return response{}, errors.New("reply too long")
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	resp, err := parse(head.String())
	if err != nil {
		return response{}, err
	}
	if n, _ := strconv.Atoi(resp.header("content-length")); n > 0 {
		if n > maxMessage {
			return response{}, errors.New("reply too long")
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return response{}, err
		}
		resp.Body = string(body)
	}
	return resp, nil
}

// compact is SIP's short header names the probe reads.
var compact = map[string]string{"i": "call-id", "l": "content-length", "c": "content-type", "v": "via", "f": "from", "t": "to"}

// parse reads a response's status line and headers (and a UDP datagram's
// body).
func parse(msg string) (response, error) {
	head, body, _ := strings.Cut(msg, "\r\n\r\n")
	lines := strings.Split(strings.ReplaceAll(head, "\r\n", "\n"), "\n")
	f := strings.SplitN(lines[0], " ", 3)
	if len(f) < 2 || f[0] != "SIP/2.0" {
		return response{}, fmt.Errorf("not a SIP response: %.40q", lines[0])
	}
	status, err := strconv.Atoi(f[1])
	if err != nil || status < 100 || status > 699 {
		return response{}, fmt.Errorf("not a SIP response: %.40q", lines[0])
	}
	r := response{Status: status, Headers: map[string][]string{}, Body: body}
	if len(f) == 3 {
		r.Reason = f[2]
	}
	for _, l := range lines[1:] {
		name, value, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if long, ok := compact[name]; ok {
			name = long
		}
		r.Headers[name] = append(r.Headers[name], strings.TrimSpace(value))
	}
	return r, nil
}

// challenge is a Digest WWW-Authenticate / Proxy-Authenticate.
type challenge struct {
	realm, nonce, opaque, algorithm, qop string
}

func parseChallenge(h string) (challenge, bool) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	if !strings.EqualFold(scheme, "Digest") {
		return challenge{}, false
	}
	var c challenge
	for _, part := range splitParams(rest) {
		k, v, _ := strings.Cut(part, "=")
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "realm":
			c.realm = v
		case "nonce":
			c.nonce = v
		case "opaque":
			c.opaque = v
		case "algorithm":
			c.algorithm = v
		case "qop":
			for _, q := range strings.Split(v, ",") {
				if strings.TrimSpace(q) == "auth" {
					c.qop = "auth"
				}
			}
		}
	}
	return c, c.nonce != ""
}

// splitParams splits on commas outside quotes.
func splitParams(s string) []string {
	var out []string
	quoted, start := false, 0
	for i, r := range s {
		switch r {
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

// authorization answers c (RFC 3261 digest; RFC 8760 SHA-256).
func (c challenge) authorization(method, uri, username, password string) (string, error) {
	var h func() hash.Hash
	switch strings.ToUpper(c.algorithm) {
	case "", "MD5":
		h = md5.New
	case "SHA-256":
		h = sha256.New
	default:
		return "", fmt.Errorf("it asks for a sign-in method Linx doesn't know (%s)", c.algorithm)
	}
	sum := func(s string) string {
		x := h()
		x.Write([]byte(s))
		return hex.EncodeToString(x.Sum(nil))
	}
	ha1 := sum(username + ":" + c.realm + ":" + password)
	ha2 := sum(method + ":" + uri)
	var b strings.Builder
	fmt.Fprintf(&b, `Digest username="%s", realm="%s", nonce="%s", uri="%s"`, username, c.realm, c.nonce, uri)
	if c.qop == "auth" {
		cnonce := randomToken()
		fmt.Fprintf(&b, `, response="%s", qop=auth, nc=00000001, cnonce="%s"`,
			sum(ha1+":"+c.nonce+":00000001:"+cnonce+":auth:"+ha2), cnonce)
	} else {
		fmt.Fprintf(&b, `, response="%s"`, sum(ha1+":"+c.nonce+":"+ha2))
	}
	if c.algorithm != "" {
		fmt.Fprintf(&b, ", algorithm=%s", c.algorithm)
	}
	if c.opaque != "" {
		fmt.Fprintf(&b, `, opaque="%s"`, c.opaque)
	}
	return b.String(), nil
}
