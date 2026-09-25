package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"

	"linxpbx.com/linx/internal/auth"
)

// serveEcho serves the caller's address as the server sees it, through the
// front-door listener, trusting only `trusted`.
func serveEcho(t *testing.T, trusted string) string {
	t.Helper()
	ips, err := auth.NewClientIPResolver(trusted)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.RemoteAddr)
	})}
	go srv.Serve(proxyListener(ips)(ln))
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// get sends one request, with a PROXY v2 header naming `client` first if
// it's set, and returns the body ("" if the connection was refused).
func get(t *testing.T, addr, client string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(7 * time.Second))
	if client != "" {
		h := proxyproto.HeaderProxyFromAddrs(2, &net.TCPAddr{IP: net.ParseIP(client), Port: 40000}, c.RemoteAddr())
		if _, err := h.WriteTo(c); err != nil {
			t.Fatal(err)
		}
	}
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestFrontDoorProxyProtocol(t *testing.T) {
	// From the trusted front door: its header gives the visitor's address.
	trusted := serveEcho(t, "127.0.0.1")
	if got := get(t, trusted, "203.0.113.7"); !strings.HasPrefix(got, "203.0.113.7:") {
		t.Errorf("trusted front door with a header: server saw %q", got)
	}
	// The trusted front door must send one.
	if got := get(t, trusted, ""); got != "" {
		t.Errorf("trusted front door without a header was served: %q", got)
	}
	// Anyone else: served as themselves, and a header is refused.
	direct := serveEcho(t, "192.0.2.1")
	if got := get(t, direct, ""); !strings.HasPrefix(got, "127.0.0.1:") {
		t.Errorf("direct connection: server saw %q", got)
	}
	if got := get(t, direct, "203.0.113.7"); got != "" {
		t.Errorf("an untrusted peer claimed another address: server saw %q", got)
	}
}
