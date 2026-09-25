package turnconf

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"linxpbx.com/linx/internal/certs"
)

// STUN (RFC 5389) message types and magic cookie, for the health check.
const (
	stunBindingRequest = 0x0001
	stunBindingSuccess = 0x0101
	stunMagicCookie    = 0x2112A442
)

// Healthy checks a running coturn from inside its container: it answers a
// STUN request on the UDP port, and its TLS port serves exactly the
// certificate currently deployed in certsDir (so a renewal it missed shows
// up as unhealthy in docker ps and linx doctor).
func Healthy(ctx context.Context, host, realm, certsDir string) error {
	if err := stunPing(ctx, net.JoinHostPort(host, fmt.Sprint(ListenPort))); err != nil {
		return fmt.Errorf("UDP port: %w", err)
	}
	if err := certs.ServesCurrent(ctx, net.JoinHostPort(host, fmt.Sprint(TLSPort)), "turn."+realm, certsDir); err != nil {
		return fmt.Errorf("TLS port: %w", err)
	}
	return nil
}

func stunPing(ctx context.Context, addr string) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:], stunBindingRequest)
	binary.BigEndian.PutUint32(req[4:], stunMagicCookie)
	if _, err := rand.Read(req[8:20]); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(3 * time.Second)
	}
	c.SetDeadline(deadline)
	if _, err := c.Write(req); err != nil {
		return err
	}
	resp := make([]byte, 1500)
	n, err := c.Read(resp)
	if err != nil {
		return err
	}
	if n < 20 || binary.BigEndian.Uint16(resp[0:]) != stunBindingSuccess || !bytes.Equal(resp[8:20], req[8:20]) {
		return errors.New("no STUN answer")
	}
	return nil
}
