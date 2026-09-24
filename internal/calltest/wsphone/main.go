// Command wsphone is a minimal SIP-over-secure-websocket client for the call
// suite (internal/calltest): it signs a device in the way the control
// plane's /sip relay will, from a container on the linx-sipws network. It
// never handles audio. Never shipped.
//
//	wsphone -url wss://linx-sipws:8089/ws -ca root_ca.crt -user d_x -pass p [-hold 5s]
//
// It prints "cert-serial <n>" (the websocket's certificate) and "registered",
// then, with -hold, keeps the connection open that long and signs in again
// over it ("registered again"). Exit status 0 means every step worked; with
// -want-reject, that the sign-in was refused.
package main

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/coder/websocket"

	"linxpbx.com/linx/internal/pbx"
)

func main() {
	url := flag.String("url", "wss://linx-sipws:8089/ws", "websocket URL")
	caFile := flag.String("ca", "/ca/root_ca.crt", "internal CA root")
	user := flag.String("user", "", "SIP username")
	pass := flag.String("pass", "", "SIP password")
	hold := flag.Duration("hold", 0, "keep the connection open this long, then sign in again over it")
	wantReject := flag.Bool("want-reject", false, "expect the sign-in to be refused")
	flag.Parse()
	if err := run(*url, *caFile, *user, *pass, *hold, *wantReject); err != nil {
		fmt.Fprintln(os.Stderr, "wsphone:", err)
		os.Exit(1)
	}
}

func run(url, caFile, user, pass string, hold time.Duration, wantReject bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second+hold)
	defer cancel()
	root, err := os.ReadFile(caFile)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(root) {
		return errors.New("no CA certificate in " + caFile)
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: tr}, Subprotocols: []string{"sip"},
	})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.CloseNow()
	defer c.Close(websocket.StatusNormalClosure, "")
	if c.Subprotocol() != "sip" {
		return fmt.Errorf("subprotocol %q, want sip", c.Subprotocol())
	}
	fmt.Println("cert-serial", resp.TLS.PeerCertificates[0].SerialNumber)

	p := &phone{c: c, user: user, pass: pass, callID: token() + "@wsphone", tag: token(), msgs: make(chan string, 16)}
	// One reader for the whole connection: a read whose context ends closes
	// the websocket (coder/websocket), so reads never get shorter deadlines.
	go func() {
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				p.readErr = err
				close(p.msgs)
				return
			}
			p.msgs <- string(b)
		}
	}()
	code, err := p.register(ctx)
	if err != nil {
		return err
	}
	if wantReject {
		if code == 200 {
			return errors.New("signed in, want refused")
		}
		fmt.Println("refused", code)
		return nil
	}
	if code != 200 {
		return fmt.Errorf("sign-in answered %d", code)
	}
	fmt.Println("registered")
	if hold == 0 {
		return nil
	}
	// Answer Asterisk's keep-alive checks meanwhile.
	holdCtx, holdCancel := context.WithTimeout(ctx, hold)
	defer holdCancel()
	for {
		msg, err := p.read(holdCtx)
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			break
		}
		if err != nil {
			return fmt.Errorf("connection dropped while held: %w", err)
		}
		if strings.HasPrefix(msg, "OPTIONS ") {
			if err := p.reply200(ctx, msg); err != nil {
				return err
			}
		}
	}
	if code, err = p.register(ctx); err != nil || code != 200 {
		return fmt.Errorf("signing in again over the held connection: %d %v", code, err)
	}
	fmt.Println("registered again")
	return nil
}

type phone struct {
	c           *websocket.Conn
	user, pass  string
	callID, tag string
	cseq        int
	msgs        chan string
	readErr     error // set before msgs closes
}

// register sends a REGISTER, answers a digest challenge once, and returns
// the final status code.
func (p *phone) register(ctx context.Context) (int, error) {
	authz := ""
	for range 2 {
		p.cseq++
		req := fmt.Sprintf("REGISTER sip:linx SIP/2.0\r\n"+
			"Via: SIP/2.0/WSS wsphone.invalid;branch=z9hG4bK%s\r\n"+
			"Max-Forwards: 70\r\n"+
			"From: <sip:%s@linx>;tag=%s\r\n"+
			"To: <sip:%s@linx>\r\n"+
			"Call-ID: %s\r\n"+
			"CSeq: %d REGISTER\r\n"+
			"Contact: <sip:%s@wsphone.invalid;transport=ws>;expires=300\r\n"+
			"Expires: 300\r\n%s"+
			"Content-Length: 0\r\n\r\n", token(), p.user, p.tag, p.user, p.callID, p.cseq, p.user, authz)
		if err := p.c.Write(ctx, websocket.MessageText, []byte(req)); err != nil {
			return 0, err
		}
		resp, err := p.response(ctx)
		if err != nil {
			return 0, err
		}
		code := statusCode(resp)
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
func (p *phone) response(ctx context.Context) (string, error) {
	for {
		msg, err := p.read(ctx)
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(msg, "SIP/2.0 ") {
			if strings.HasPrefix(msg, "OPTIONS ") {
				if err := p.reply200(ctx, msg); err != nil {
					return "", err
				}
			}
			continue
		}
		if code := statusCode(msg); code >= 200 && strings.Contains(msg, fmt.Sprintf("CSeq: %d REGISTER", p.cseq)) {
			return msg, nil
		}
	}
}

func (p *phone) read(ctx context.Context) (string, error) {
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

func (p *phone) reply200(ctx context.Context, req string) error {
	var hdrs []string
	for _, l := range strings.Split(req, "\r\n") {
		for _, h := range []string{"Via:", "From:", "To:", "Call-ID:", "CSeq:"} {
			if strings.HasPrefix(l, h) {
				hdrs = append(hdrs, l)
			}
		}
	}
	resp := "SIP/2.0 200 OK\r\n" + strings.Join(hdrs, "\r\n") + "\r\nContent-Length: 0\r\n\r\n"
	return p.c.Write(ctx, websocket.MessageText, []byte(resp))
}

var challengeParam = regexp.MustCompile(`(\w+)="?([^",]*)"?`)

// authorization answers a WWW-Authenticate digest challenge (RFC 2617,
// qop=auth), with the same MD5 digest phones use (pbx.DigestHash).
func (p *phone) authorization(resp string) (string, error) {
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

func statusCode(resp string) int {
	var code int
	fmt.Sscanf(resp, "SIP/2.0 %d", &code)
	return code
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func token() string { return strings.ToLower(rand.Text()[:12]) }
