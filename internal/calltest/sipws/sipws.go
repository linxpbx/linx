// Package sipws is a minimal SIP-over-websocket phone for the call suite
// (internal/calltest): it signs a browser's device in and can wait for a
// call, the way JsSIP does from a page, over any websocket (Asterisk's own,
// or the control plane's /sip relay in front of it). It never handles
// audio. Never shipped.
package sipws

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/coder/websocket"

	"linxpbx.com/linx/internal/pbx"
)

// Phone is one signed-in-or-not browser line on an open websocket.
type Phone struct {
	c           *websocket.Conn
	user, pass  string
	callID, tag string
	cseq        int
	msgs        chan string
	readErr     error // set before msgs closes
}

// New starts reading c for user/pass. One reader for the whole connection:
// a read whose context ends closes the websocket (coder/websocket), so
// reads never get shorter deadlines.
func New(c *websocket.Conn, user, pass string) *Phone {
	p := &Phone{c: c, user: user, pass: pass, callID: token() + "@sipws", tag: token(), msgs: make(chan string, 32)}
	go func() {
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				p.readErr = err
				close(p.msgs)
				return
			}
			p.msgs <- string(b)
		}
	}()
	return p
}

// Register sends a REGISTER, answers a digest challenge once, and returns
// the final status code.
func (p *Phone) Register(ctx context.Context) (int, error) {
	return p.register(ctx, "")
}

// RegisterAs is Register with a different From/To user than the one it
// authenticates as (for checking the relay refuses it).
func (p *Phone) RegisterAs(ctx context.Context, fromUser string) (int, error) {
	return p.register(ctx, fromUser)
}

func (p *Phone) register(ctx context.Context, fromUser string) (int, error) {
	if fromUser == "" {
		fromUser = p.user
	}
	authz := ""
	for range 2 {
		p.cseq++
		req := fmt.Sprintf("REGISTER sip:linx SIP/2.0\r\n"+
			"Via: SIP/2.0/WSS sipws.invalid;branch=z9hG4bK%s\r\n"+
			"Max-Forwards: 70\r\n"+
			"From: <sip:%s@linx>;tag=%s\r\n"+
			"To: <sip:%s@linx>\r\n"+
			"Call-ID: %s\r\n"+
			"CSeq: %d REGISTER\r\n"+
			"Contact: <sip:%s@sipws.invalid;transport=ws>;expires=300\r\n"+
			"Expires: 300\r\n%s"+
			"Content-Length: 0\r\n\r\n", token(), fromUser, p.tag, fromUser, p.callID, p.cseq, fromUser, authz)
		if err := p.c.Write(ctx, websocket.MessageText, []byte(req)); err != nil {
			return 0, err
		}
		resp, err := p.response(ctx)
		if err != nil {
			return 0, err
		}
		code := StatusCode(resp)
		if code != 401 || authz != "" {
			return code, nil
		}
		authz, err = p.authorization(resp)
		if err != nil {
			return 0, err
		}
	}
	panic("unreachable")
}

// response reads until a final response to this phone's REGISTER, answering
// any request Asterisk sends meanwhile.
func (p *Phone) response(ctx context.Context) (string, error) {
	for {
		msg, err := p.Read(ctx)
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(msg, "SIP/2.0 ") {
			if strings.HasPrefix(msg, "OPTIONS ") {
				if err := p.Reply(ctx, msg, "200 OK"); err != nil {
					return "", err
				}
			}
			continue
		}
		if code := StatusCode(msg); code >= 200 && strings.Contains(msg, fmt.Sprintf("CSeq: %d REGISTER", p.cseq)) {
			return msg, nil
		}
	}
}

// Read returns the next message Asterisk sent.
func (p *Phone) Read(ctx context.Context) (string, error) {
	select {
	case msg, ok := <-p.msgs:
		if !ok {
			return "", p.readErr
		}
		return msg, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// WaitRequest reads until a request with this method arrives, answering
// Asterisk's keep-alive checks meanwhile.
func (p *Phone) WaitRequest(ctx context.Context, method string) (string, error) {
	for {
		msg, err := p.Read(ctx)
		if err != nil {
			return "", err
		}
		switch {
		case strings.HasPrefix(msg, method+" "):
			return msg, nil
		case strings.HasPrefix(msg, "OPTIONS "):
			if err := p.Reply(ctx, msg, "200 OK"); err != nil {
				return "", err
			}
		}
	}
}

// Reply answers req with status ("486 Busy Here"), without a body.
func (p *Phone) Reply(ctx context.Context, req, status string) error {
	var hdrs []string
	for _, l := range strings.Split(req, "\r\n") {
		for _, h := range []string{"Via:", "From:", "To:", "Call-ID:", "CSeq:"} {
			if strings.HasPrefix(l, h) {
				if h == "To:" && !strings.Contains(l, "tag=") && !strings.HasPrefix(status, "1") {
					l += ";tag=" + p.tag
				}
				hdrs = append(hdrs, l)
			}
		}
	}
	resp := "SIP/2.0 " + status + "\r\n" + strings.Join(hdrs, "\r\n") + "\r\nContent-Length: 0\r\n\r\n"
	return p.c.Write(ctx, websocket.MessageText, []byte(resp))
}

var challengeParam = regexp.MustCompile(`(\w+)="?([^",]*)"?`)

// authorization answers a WWW-Authenticate digest challenge (RFC 2617,
// qop=auth), with the same MD5 digest phones use (pbx.DigestHash).
func (p *Phone) authorization(resp string) (string, error) {
	var challenge string
	for _, l := range strings.Split(resp, "\r\n") {
		if v, ok := strings.CutPrefix(l, "WWW-Authenticate: Digest "); ok {
			challenge = v
			break
		}
	}
	if challenge == "" {
		return "", errors.New("401 without a digest challenge")
	}
	params := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	const uri, nc = "sip:linx", "00000001"
	cnonce := token()
	ha1 := pbx.DigestHash(p.user, p.pass)
	ha2 := md5hex("REGISTER:" + uri)
	response := md5hex(ha1 + ":" + params["nonce"] + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	h := fmt.Sprintf(`Authorization: Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=MD5, cnonce="%s", qop=auth, nc=%s`,
		p.user, params["realm"], params["nonce"], uri, response, cnonce, nc)
	if o := params["opaque"]; o != "" {
		h += `, opaque="` + o + `"`
	}
	return h + "\r\n", nil
}

// StatusCode is a response's status code (0 for a request).
func StatusCode(resp string) int {
	var code int
	fmt.Sscanf(resp, "SIP/2.0 %d", &code)
	return code
}

// Candidates returns an SDP's ICE candidate addresses (a=candidate lines'
// connection addresses).
func Candidates(sdp string) []string {
	var out []string
	for _, l := range strings.Split(sdp, "\r\n") {
		if v, ok := strings.CutPrefix(l, "a=candidate:"); ok {
			if f := strings.Fields(v); len(f) >= 6 {
				out = append(out, f[4])
			}
		}
	}
	return out
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func token() string { return strings.ToLower(rand.Text()[:12]) }
