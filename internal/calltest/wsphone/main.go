// Command wsphone is a minimal SIP-over-secure-websocket client for the call
// suite (internal/calltest): it signs a device in (internal/calltest/sipws)
// from a container on the linx-sipws network, where the control plane's
// /sip relay connects from. It never handles audio. Never shipped.
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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"

	"linxpbx.com/linx/internal/calltest/sipws"
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

	p := sipws.New(c, user, pass)
	code, err := p.Register(ctx)
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
	if _, err := p.WaitRequest(holdCtx, "NONE"); !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return fmt.Errorf("connection dropped while held: %w", err)
	}
	if code, err = p.Register(ctx); err != nil || code != 200 {
		return fmt.Errorf("signing in again over the held connection: %d %v", code, err)
	}
	fmt.Println("registered again")
	return nil
}
