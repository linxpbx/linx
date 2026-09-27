package backup

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

// Restore sources (docs/BACKUP.md §4): a restic repository in a folder on
// this server, a destination set up here with `linx backup destination
// add`, or a backup file uploaded from the browser (§8 step 5; location is
// the upload's id).
const (
	SourceFolder      = "folder"
	SourceDestination = "destination"
	SourceUpload      = "upload"
)

var (
	destinationNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	snapshotIDRE      = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
	uploadIDRE        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
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
	case SourceUpload:
		return CheckUploadID(location)
	default:
		return errors.New(`the source is "folder", "destination" or "upload"`)
	}
}

// CheckUploadID accepts an uploaded backup file's id: a lowercase UUID,
// which becomes a file name in the transfer folder.
func CheckUploadID(id string) error {
	if !uploadIDRE.MatchString(id) {
		return errors.New("that uploaded backup file isn't known")
	}
	return nil
}

var (
	sftpHostRE = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9](?:[A-Za-z0-9.-]{0,252}[A-Za-z0-9])?)$`)
	sftpUserRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)
)

// CheckSFTP refuses an SFTP host, user or remote path that could be read as
// something else in the ssh command restic runs (Destination.sftpCommand):
// restic splits it on spaces, and ssh takes a word starting with "-" as an
// option (-oProxyCommand runs a program). A host is a name or address, a
// user plain letters, digits and ".-_", a path has no spaces or control
// characters.
func CheckSFTP(host, user, remotePath string) error {
	if !sftpHostRE.MatchString(host) {
		return errors.New("the host must be a plain name or address (like nas.home.arpa or 192.168.1.10)")
	}
	if !sftpUserRE.MatchString(user) {
		return errors.New("the user must be letters, digits, \".\", \"-\" and \"_\", not starting with \"-\" or \".\"")
	}
	if remotePath == "" || len(remotePath) > 4096 || strings.ContainsFunc(remotePath, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return errors.New("the remote path must have no spaces or control characters")
	}
	return nil
}
