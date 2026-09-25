package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"linxpbx.com/linx/internal/certs"
)

// defaultHealthAddr is the plain-HTTP /healthz listener, on the
// container's own loopback only (main.go).
const defaultHealthAddr = "127.0.0.1:8080"

// runHealthcheck is the container's Docker health check (compose.yaml). The
// image has no shell or curl, so the binary asks its own /healthz, then
// checks its HTTPS port serves exactly the certificate linx-certd deployed
// (so a renewal it missed shows up in docker ps and linx doctor). It
// returns the process exit code: 0 healthy, 1 not.
func runHealthcheck(getenv func(string) string, client *http.Client) int {
	host, port, err := net.SplitHostPort(envOr(getenv, "LINX_HEALTH_ADDR", defaultHealthAddr))
	if err != nil {
		return 1
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	resp, err := client.Get("http://" + net.JoinHostPort(loopback(host), port) + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}

	host, port, err = net.SplitHostPort(envOr(getenv, "LINX_LISTEN_ADDR", ":8443"))
	if err != nil {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	name := "meet." + strings.TrimSpace(getenv("LINX_DOMAIN"))
	if certs.ServesCurrent(ctx, net.JoinHostPort(loopback(host), port), name, envOr(getenv, "LINX_CERTS_DIR", defaultCertsDir)) != nil {
		return 1
	}
	return 0
}

func loopback(host string) string {
	if host == "" || host == "0.0.0.0" || host == "::" {
		return "127.0.0.1"
	}
	return host
}
