package db

import (
	"strings"
	"testing"

	"linxpbx.com/linx/deploy/compose"
)

// TestComposePostgres: compose.yaml takes the database image from setup
// (installer.Config.PostgresImage, one of this package's pinned images) and
// makes a new database with InitDBArgs, as the Docker integration tests do.
func TestComposePostgres(t *testing.T) {
	c := string(compose.File)
	if !strings.Contains(c, "image: ${LINX_POSTGRES_IMAGE:?set by linx setup}\n") {
		t.Error("compose.yaml's postgres image should come from LINX_POSTGRES_IMAGE")
	}
	if !strings.Contains(c, `POSTGRES_INITDB_ARGS: "`+InitDBArgs+`"`) {
		t.Errorf("compose.yaml doesn't pass POSTGRES_INITDB_ARGS %q", InitDBArgs)
	}
	for _, img := range []string{PostgresImageAlpine, PostgresImageDebian} {
		if !strings.Contains(img, "@sha256:") {
			t.Errorf("%s isn't pinned by digest", img)
		}
	}
}
