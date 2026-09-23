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

// PostgresImage is the image compose.yaml runs, pinned by digest (multi-arch
// index). Kept here so the Docker integration test uses the same image; a
// test keeps this constant and compose.yaml from drifting apart.
const PostgresImage = "postgres:18@sha256:86c951e05bf56c93d95d397747fb8820ac76cc3bedb78f43abd83eedbe3666ae"

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
