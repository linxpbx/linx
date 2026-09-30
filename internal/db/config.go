// Package db connects the control plane to PostgreSQL and applies its
// migrations (ADR-026): pgx, plain SQL, no ORM. linx-private (the internal
// Docker network only containers on it can reach) is the trust boundary, so
// the connection itself doesn't use TLS.
package db

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The database's images, pinned by digest (multi-arch index): Alpine for new
// installs, Debian kept for installs whose database was made on it
// (installer.DecideDatabaseImage, docs/RESOURCES.md §3). Both Postgres 18.6.
const (
	PostgresImageAlpine = "postgres:18-alpine@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873"
	PostgresImageDebian = "postgres:18@sha256:86c951e05bf56c93d95d397747fb8820ac76cc3bedb78f43abd83eedbe3666ae"
)

// PostgresImage is what the Docker integration tests run: a new install's.
const PostgresImage = PostgresImageAlpine

// InitDBArgs make a new database use Postgres's own built-in C.UTF-8
// locale, never the C library's (glibc and musl sort text differently), so
// a database can later move between images without its text indexes going
// stale. compose.yaml passes them; they only matter when the data volume is
// new.
const InitDBArgs = "--locale-provider=builtin --builtin-locale=C.UTF-8"

// Config is control-plane's database connection, read from LINX_DB_*
// environment variables that compose.yaml sets. The password is never an
// environment variable; it's a Docker secret file.
type Config struct {
	Host, Port, Name, User string
	// PasswordFile holds the database password (a Docker secret).
	PasswordFile string
}

// ConfigFromEnv reads the configuration from the environment, defaulting to
// the values compose.yaml uses.
func ConfigFromEnv(getenv func(string) string) Config {
	return Config{
		Host:         envOr(getenv, "LINX_DB_HOST", "postgres"),
		Port:         envOr(getenv, "LINX_DB_PORT", "5432"),
		Name:         envOr(getenv, "LINX_DB_NAME", "linx"),
		User:         envOr(getenv, "LINX_DB_USER", "linx"),
		PasswordFile: envOr(getenv, "LINX_DB_PASSWORD_FILE", "/run/secrets/linx_db_password"),
	}
}

func envOr(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

// DSN builds the connection string, reading the password from PasswordFile.
func (c Config) DSN() (string, error) {
	pw, err := os.ReadFile(c.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("database password: %w", err)
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, strings.TrimSpace(string(pw))),
		Host:   c.Host + ":" + c.Port,
		Path:   "/" + c.Name,
	}
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Connect opens the pool and checks it can reach the database.
func Connect(ctx context.Context, c Config) (*pgxpool.Pool, error) {
	dsn, err := c.DSN()
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}
	return pool, nil
}
