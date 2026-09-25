package turnconf

import (
	"context"
	"fmt"
	"net"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/turn"
)

// Healthy checks a running coturn from inside its container: it answers a
// STUN request on the UDP port, and its TLS port serves exactly the
// certificate currently deployed in certsDir (so a renewal it missed shows
// up as unhealthy in docker ps and linx doctor).
func Healthy(ctx context.Context, host, realm, certsDir string) error {
	if err := turn.Ping(ctx, net.JoinHostPort(host, fmt.Sprint(ListenPort))); err != nil {
		return fmt.Errorf("UDP port: %w", err)
	}
	if err := certs.ServesCurrent(ctx, net.JoinHostPort(host, fmt.Sprint(TLSPort)), "turn."+realm, certsDir); err != nil {
		return fmt.Errorf("TLS port: %w", err)
	}
	return nil
}
