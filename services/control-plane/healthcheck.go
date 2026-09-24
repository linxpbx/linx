package main

import (
	"net"
	"net/http"
	"time"
)

// runHealthcheck is the container's Docker health check (compose.yaml). The
// image has no shell or curl, so the binary asks its own /healthz. It
// returns the process exit code: 0 healthy, 1 not.
func runHealthcheck(getenv func(string) string, client *http.Client) int {
	addr := getenv("LINX_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
