package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitForCertificate(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	if waitForCertificate(dir, 600*time.Millisecond, log) {
		t.Fatal("found a certificate that isn't there")
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.MkdirAll(filepath.Join(dir, "v1"), 0o755)
		os.WriteFile(filepath.Join(dir, "v1", "fullchain.pem"), []byte("x"), 0o644)
		os.Symlink("v1", filepath.Join(dir, "current"))
	}()
	if !waitForCertificate(dir, 5*time.Second, log) {
		t.Fatal("missed the certificate written while waiting")
	}
}
