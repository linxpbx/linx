// Package dbtest starts a throwaway PostgreSQL in Docker for integration
// tests (make test-docker).
package dbtest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"linxpbx.com/linx/internal/db"
)

// Start runs db.PostgresImage in a container named name and returns a pool
// connected to it (not migrated). It skips the test unless
// LINX_DOCKER_TESTS=1, and removes the container when the test ends.
func Start(t *testing.T, ctx context.Context, name string) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("LINX_DOCKER_TESTS") != "1" {
		t.Skip("set LINX_DOCKER_TESTS=1 (make test-docker)")
	}
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args[:min(3, len(args))], " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	exec.Command("docker", "rm", "--force", name).Run()
	t.Cleanup(func() { exec.Command("docker", "rm", "--force", name).Run() })

	docker("run", "--detach", "--name", name, "--publish", "127.0.0.1::5432",
		"--env", "POSTGRES_USER=linx", "--env", "POSTGRES_PASSWORD=test-password", "--env", "POSTGRES_DB=linx",
		db.PostgresImage)

	// "127.0.0.1:PORT"
	_, portStr, ok := strings.Cut(docker("port", name, "5432/tcp"), ":")
	if !ok {
		t.Fatalf("unexpected docker port output")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	pwFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(pwFile, []byte("test-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := db.Config{Host: "127.0.0.1", Port: strconv.Itoa(port), Name: "linx", User: "linx", PasswordFile: pwFile}

	deadline := time.Now().Add(60 * time.Second)
	for {
		pool, err := db.Connect(ctx, cfg)
		if err == nil {
			t.Cleanup(pool.Close)
			return pool
		}
		if time.Now().After(deadline) {
			t.Fatalf("database never became ready: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
