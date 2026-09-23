package db

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
)

// TestMigrateDocker runs Migrate against a real Postgres and checks the
// tables it creates. It needs Docker: make test-docker.
func TestMigrateDocker(t *testing.T) {
	if os.Getenv("LINX_DOCKER_TESTS") != "1" {
		t.Skip("set LINX_DOCKER_TESTS=1 (make test-docker)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const container = "linx-db-migrate-test"
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args[:min(3, len(args))], " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	exec.Command("docker", "rm", "--force", container).Run()
	t.Cleanup(func() { exec.Command("docker", "rm", "--force", container).Run() })

	docker("run", "--detach", "--name", container, "--publish", "127.0.0.1::5432",
		"--env", "POSTGRES_USER=linx", "--env", "POSTGRES_PASSWORD=test-password", "--env", "POSTGRES_DB=linx",
		PostgresImage)

	// "127.0.0.1:PORT"
	_, portStr, ok := strings.Cut(docker("port", container, "5432/tcp"), ":")
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
	cfg := Config{Host: "127.0.0.1", Port: strconv.Itoa(port), Name: "linx", User: "linx", PasswordFile: pwFile}

	pool, err := connectRetry(ctx, cfg, 60*time.Second)
	if err != nil {
		t.Fatalf("database never became ready: %v", err)
	}
	defer pool.Close()

	version, err := Migrate(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Errorf("Migrate() version = %d, want 1", version)
	}

	for _, table := range []string{"tenant", "audit_log", "schema_migrations"} {
		var exists bool
		if err := pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)", table,
		).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s not created", table)
		}
	}

	// Running again is a no-op and returns the same version.
	version2, err := Migrate(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if version2 != 1 {
		t.Errorf("second Migrate() version = %d, want 1", version2)
	}
}

func connectRetry(ctx context.Context, cfg Config, timeout time.Duration) (*pgxpool.Pool, error) {
	deadline := time.Now().Add(timeout)
	for {
		p, err := Connect(ctx, cfg)
		if err == nil {
			return p, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}
