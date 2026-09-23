package db

import (
	"strings"
	"testing"

	"linxpbx.com/linx/deploy/compose"
)

// TestComposePostgresImage keeps compose.yaml and the Docker integration test
// on one Postgres image.
func TestComposePostgresImage(t *testing.T) {
	if !strings.Contains(string(compose.File), "image: "+PostgresImage+"\n") {
		t.Errorf("compose.yaml postgres image differs from PostgresImage %s", PostgresImage)
	}
}
