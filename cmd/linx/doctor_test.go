package main

import (
	"bytes"
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/doctor"
)

func TestDoctor(t *testing.T) {
	saved := func() ([]byte, error) {
		return []byte("version: 1\ndomain:\n  name: lab.linxpbx.com\n  dns_provider: cloudflare\n"), nil
	}
	checks := doctor.Env{Runner: hostRunner{}, Now: time.Now, Stat: func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }}
	tests := []struct {
		name     string
		env      doctorEnv
		wantCode int
		wantOut  string
	}{
		{"not root", doctorEnv{savedConfig: saved, checks: checks}, 1, "must run as root"},
		{"not set up", doctorEnv{isRoot: true, savedConfig: func() ([]byte, error) { return nil, fs.ErrNotExist }, checks: checks}, 1, "isn't set up"},
		{"docker down", doctorEnv{isRoot: true, savedConfig: saved, checks: checks}, 1, "Fix: Start it"},
	}
	for _, tt := range tests {
		var out, errOut bytes.Buffer
		code := runDoctor(context.Background(), nil, &out, &errOut, tt.env)
		if code != tt.wantCode || !strings.Contains(out.String()+errOut.String(), tt.wantOut) {
			t.Errorf("%s: code %d, output:\n%s%s", tt.name, code, out.String(), errOut.String())
		}
	}
}
