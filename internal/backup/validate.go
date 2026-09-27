package backup

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

// Restore sources (docs/BACKUP.md §4): a restic repository in a folder on
// this server, or a destination set up here with `linx backup destination
// add`.
const (
	SourceFolder      = "folder"
	SourceDestination = "destination"
)

var (
	destinationNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	snapshotIDRE      = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
)

// CheckDestinationName refuses a destination name that isn't letters,
// digits, "-" and "_" (at most 64): names become file names under the
// root-only destinations directory (Manifest.PasswordPath), and a restore
// request carries one in from the browser.
func CheckDestinationName(name string) error {
	if !destinationNameRE.MatchString(name) {
		return errors.New("a destination name is letters, digits, \"-\" and \"_\" (at most 64), starting with a letter or digit")
	}
	return nil
}

// CheckFolder refuses anything but a plain absolute path. restic reads a
// repository string starting with a backend name ("sftp:", "s3:",
// "rclone:", ...) as that backend, so a folder must start with "/" to only
// ever mean a local directory.
func CheckFolder(path string) error {
	if !strings.HasPrefix(path, "/") || len(path) > 4096 || strings.ContainsAny(path, "\x00\n\r") {
		return errors.New("the folder must be a full path on the server, starting with /")
	}
	if filepath.Clean(path) != path {
		return errors.New("the folder must be a plain path (no \"..\", \".\" or doubled or trailing \"/\")")
	}
	return nil
}

// CheckSnapshot accepts "latest" or a snapshot id (restic's short or full
// hex id).
func CheckSnapshot(s string) error {
	if s == "latest" || snapshotIDRE.MatchString(s) {
		return nil
	}
	return errors.New(`the backup to restore is "latest" or a backup id (8 to 64 letters a-f and digits)`)
}

// CheckRestoreSource checks a restore request's source and location.
func CheckRestoreSource(source, location string) error {
	switch source {
	case SourceFolder:
		return CheckFolder(location)
	case SourceDestination:
		return CheckDestinationName(location)
	default:
		return errors.New(`the source is "folder" or "destination"`)
	}
}
