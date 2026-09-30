package installer

import (
	"context"
	"fmt"
	"slices"

	"linxpbx.com/linx/internal/db"
)

// Which image the database runs on (docs/RESOURCES.md §3 item 1, owner
// decision 2026-09-29). New installs get Alpine (about 240 MB smaller, 27 MB
// instead of 65 MB of memory at rest) with Postgres's own built-in C.UTF-8
// locale, so the database no longer depends on the C library at all. An
// install whose database was made on the Debian image keeps it: Debian's
// Postgres sorts text with glibc and Alpine's with musl, and moving a
// database between them silently breaks the order its text indexes were
// built in. Such an install moves to Alpine by restoring a backup onto a
// fresh install (a dump and reload rebuilds every index).
const (
	DatabaseAlpine = "alpine"
	DatabaseDebian = "debian"
)

// DatabaseImages lists the choices.
var DatabaseImages = []string{DatabaseAlpine, DatabaseDebian}

// postgresVolume is the database's Docker volume (compose project "linx").
const postgresVolume = "linx_postgres-data"

// DecideDatabaseImage fills in c.Database.Image the first time setup runs
// with this version: Debian if a database already exists here (it was made
// on Debian, the only image before), Alpine for a new install. An install
// that already has a choice keeps it.
func DecideDatabaseImage(ctx context.Context, r Runner, c *Config) {
	if c.Database.Image != "" {
		return
	}
	if _, err := r.Run(ctx, nil, "docker", "volume", "inspect", postgresVolume); err == nil {
		c.Database.Image = DatabaseDebian
		return
	}
	c.Database.Image = DatabaseAlpine
}

// PostgresImage is the pinned image for c's choice (Alpine when none is
// made yet: a new install).
func (c Config) PostgresImage() string {
	if c.Database.Image == DatabaseDebian {
		return db.PostgresImageDebian
	}
	return db.PostgresImageAlpine
}

func validateDatabase(d DatabaseConfig) error {
	if d.Image != "" && !slices.Contains(DatabaseImages, d.Image) {
		return fmt.Errorf("database.image: must be alpine or debian, got %q", d.Image)
	}
	return nil
}

func (c Config) databaseName() string {
	if c.Database.Image == DatabaseDebian {
		return "Debian (this database was made on it)"
	}
	return "Alpine"
}
