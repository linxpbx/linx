package main

import (
	"io"
	"log/slog"
	"testing"

	"linxpbx.com/linx/internal/certs"
)

func TestFollowerFromEnv(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if f, err := followerFromEnv(certs.Config{}, env(nil), log); f != nil || err != nil {
		t.Errorf("no records: %v, %v", f, err)
	}
	f, err := followerFromEnv(certs.Config{}, env(map[string]string{"LINX_DNS_RECORDS": "meet,api,turn", "LINX_DNS_ADDRESS": "192.168.1.20"}), log)
	if err != nil || len(f.Hosts) != 3 || f.Fixed.String() != "192.168.1.20" {
		t.Errorf("home only: %+v, %v", f, err)
	}
	for _, bad := range []map[string]string{
		{"LINX_DNS_RECORDS": "meet,www"},
		{"LINX_DNS_RECORDS": "meet", "LINX_DNS_ADDRESS": "fd00::1"},
		{"LINX_DNS_RECORDS": "meet", "LINX_DNS_ADDRESS": "home"},
	} {
		if _, err := followerFromEnv(certs.Config{}, env(bad), log); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
