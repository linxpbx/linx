// Package version holds build metadata stamped in at link time via -ldflags.
package version

import "fmt"

// Set by the Makefile: -X linxpbx.com/linx/internal/version.Version=...
var (
	Version = "dev"
	Commit  = "unknown"
)

// String returns a single human-readable version line.
func String(component string) string {
	return fmt.Sprintf("%s %s (%s)", component, Version, Commit)
}
