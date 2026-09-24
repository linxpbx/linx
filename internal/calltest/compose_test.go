package calltest

import (
	"strings"
	"testing"

	"linxpbx.com/linx/deploy/compose"
)

// TestComposeAsteriskTmpfs keeps compose.yaml's Asterisk tmpfs mounts and
// the call suite's on the same options, so the suite's restart test checks
// what a real install runs.
func TestComposeAsteriskTmpfs(t *testing.T) {
	for _, m := range asteriskTmpfs {
		if !strings.Contains(string(compose.File), "      - "+m+"\n") {
			t.Errorf("compose.yaml has no Asterisk tmpfs mount %q", m)
		}
	}
}
