package installer

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/db"
)

// A new install gets Alpine; one whose database already exists (made on
// Debian, before this) keeps Debian; a choice already made is kept, and
// reaches compose through .env.
func TestDecideDatabaseImage(t *testing.T) {
	ctx := context.Background()
	fresh := &fakeRunner{answers: map[string]string{"docker volume inspect linx_postgres-data": "!Error: no such volume"}}
	existing := &fakeRunner{answers: map[string]string{"docker volume inspect linx_postgres-data": "[{}]"}}

	c := DefaultConfig()
	DecideDatabaseImage(ctx, fresh, &c)
	if c.Database.Image != DatabaseAlpine || c.PostgresImage() != db.PostgresImageAlpine {
		t.Errorf("new install: %q %s", c.Database.Image, c.PostgresImage())
	}
	old := DefaultConfig()
	DecideDatabaseImage(ctx, existing, &old)
	if old.Database.Image != DatabaseDebian || old.PostgresImage() != db.PostgresImageDebian {
		t.Errorf("existing database: %q %s", old.Database.Image, old.PostgresImage())
	}
	// Decided once: a database made on Alpine stays Alpine even though its
	// volume now exists.
	DecideDatabaseImage(ctx, existing, &c)
	if c.Database.Image != DatabaseAlpine {
		t.Errorf("a made choice changed: %q", c.Database.Image)
	}
	if env := string(stackDotEnv(old, "abc", LAN{})); !strings.Contains(env, "LINX_POSTGRES_IMAGE="+db.PostgresImageDebian+"\n") {
		t.Errorf(".env doesn't name the Debian image:\n%s", env)
	}
	// Saved, and read back the same.
	back, err := ParseConfig(bytes.NewReader(old.Marshal()))
	if err != nil || back.Database.Image != DatabaseDebian {
		t.Errorf("setup.yaml round trip: %+v, %v", back.Database, err)
	}
	bad := DefaultConfig()
	bad.Database.Image = "ubuntu"
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "database.image") {
		t.Errorf("Validate accepted database.image ubuntu: %v", err)
	}
}
