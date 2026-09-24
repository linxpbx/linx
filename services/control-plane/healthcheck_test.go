package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunHealthcheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	env := func(k string) string {
		if k == "LINX_LISTEN_ADDR" {
			return ":" + port
		}
		return ""
	}
	if code := runHealthcheck(env, nil); code != 0 {
		t.Errorf("healthy server: exit %d", code)
	}
	status = http.StatusServiceUnavailable
	if code := runHealthcheck(env, nil); code != 1 {
		t.Errorf("unhealthy server: exit %d", code)
	}
	srv.Close()
	if code := runHealthcheck(env, nil); code != 1 {
		t.Errorf("no server: exit %d", code)
	}
}
