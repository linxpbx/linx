package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRunTrunk(t *testing.T) {
	var gotArgs []string
	env := func(root, terminal bool, code int) apiKeyEnv {
		return apiKeyEnv{isRoot: root, terminal: terminal, run: func(_ context.Context, _, _ io.Writer, _ string, args ...string) (int, error) {
			gotArgs = args
			return code, nil
		}}
	}
	var out, errb bytes.Buffer
	if code := runTrunk(t.Context(), []string{"add"}, &out, &errb, env(true, true, 0)); code != 0 ||
		!slices.Equal(gotArgs, []string{"exec", "-i", "-t", "linx-control-plane", "/usr/local/bin/service", "trunk", "add"}) {
		t.Fatalf("code %d, ran %q", code, gotArgs)
	}
	if code := runTrunk(t.Context(), []string{"list"}, &out, &errb, env(true, false, 0)); code != 0 ||
		!slices.Equal(gotArgs, []string{"exec", "-i", "linx-control-plane", "/usr/local/bin/service", "trunk", "list"}) {
		t.Fatalf("code %d, ran %q", code, gotArgs)
	}
	if code := runTrunk(t.Context(), []string{"list"}, &out, &errb, env(false, true, 0)); code != 1 || !strings.Contains(errb.String(), "root") {
		t.Fatalf("non-root: code %d, %q", code, errb.String())
	}
}

func TestTrunkPinReadHere(t *testing.T) {
	pem := filepath.Join(t.TempDir(), "ucm.crt")
	if err := os.WriteFile(pem, []byte("CERT"), 0o644); err != nil {
		t.Fatal(err)
	}
	var gotArgs []string
	env := apiKeyEnv{isRoot: true, run: func(_ context.Context, _, _ io.Writer, _ string, args ...string) (int, error) {
		gotArgs = args
		return 0, nil
	}}
	enc := base64.StdEncoding.EncodeToString([]byte("CERT"))
	for _, args := range [][]string{{"add", "--pin", pem, "--yes"}, {"add", "--pin=" + pem, "--yes"}} {
		var out, errb bytes.Buffer
		if code := runTrunk(t.Context(), args, &out, &errb, env); code != 0 {
			t.Fatalf("%v: code %d %s", args, code, errb.String())
		}
		if tail := gotArgs[len(gotArgs)-4:]; !slices.Equal(tail, []string{"add", "--pin-pem", enc, "--yes"}) {
			t.Errorf("%v: ran %q", args, gotArgs)
		}
	}
	var out, errb bytes.Buffer
	if code := runTrunk(t.Context(), []string{"add", "--pin", "/nonexistent.crt"}, &out, &errb, env); code != 1 {
		t.Errorf("missing file: code %d", code)
	}
}

func TestTrunkCert(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := runTrunkCert([]string{"192.168.1.60"}, dir, time.Now(), &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	crt, key := filepath.Join(dir, "linx-line-192.168.1.60.crt"), filepath.Join(dir, "linx-line-192.168.1.60.key")
	if _, err := tls.LoadX509KeyPair(crt, key); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(key); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", fi.Mode().Perm())
	}
	if !strings.Contains(out.String(), "SHA-256 fingerprint:") || !strings.Contains(out.String(), "--pin "+crt) {
		t.Errorf("output:\n%s", out.String())
	}
	// Never overwrites; bad input and usage are refused.
	for _, args := range [][]string{{"192.168.1.60"}, {"not an address"}, {}, {"192.168.1.60", "--out", "a/b"}} {
		out.Reset()
		errb.Reset()
		if code := runTrunkCert(args, dir, time.Now(), &out, &errb); code == 0 {
			t.Errorf("%v accepted", args)
		}
	}
	// Needs no root, and doesn't reach the control plane.
	env := apiKeyEnv{run: func(context.Context, io.Writer, io.Writer, string, ...string) (int, error) {
		t.Fatal("ran docker")
		return 0, nil
	}}
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(t.TempDir())
	if code := runTrunk(t.Context(), []string{"cert", "ucm.home.arpa", "--out", "ucm"}, &out, &errb, env); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
}
