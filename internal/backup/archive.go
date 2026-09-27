package backup

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// A backup file (docs/BACKUP.md §8 step 5) is what "Download" in System →
// Backups gives the browser and what the setup wizard's "A backup file on
// my computer" takes back: a plain tar of a local restic repository's own
// files under one top folder. Nothing in it is decrypted — restic's
// encryption is its only protection, and the file is useless without the
// repository's password, exactly like the folder it came from.

// ArchiveDir is the one top-level folder every entry of a backup file is
// under.
const ArchiveDir = "linx-backup"

// MaxArchiveSize is the most a backup file may unpack to (and the upload
// cap, docs/BACKUP.md §8 step 5: about 2 GB).
const MaxArchiveSize = 2 << 30

// MaxFileSize is the most a backup file itself may be: MaxArchiveSize plus
// room for the tar format's own headers. It caps uploads, on both sides of
// the host/container boundary.
const MaxFileSize = MaxArchiveSize + 128<<20

// maxArchiveEntries bounds how many files a backup file may hold. restic
// keeps its data in packs of a few MB, so even a very large Linx database
// is a few thousand files.
const maxArchiveEntries = 200_000

// repoDirs are a restic repository's top-level entries worth carrying.
// "locks" (stale by the time anyone restores) and anything else are left out.
var repoDirs = map[string]bool{"config": true, "data": true, "index": true, "keys": true, "snapshots": true}

// PackRepository writes repo (a local restic repository's folder) to w as a
// backup file. Only regular files and folders are carried; a symlink or
// anything else in the repository is refused rather than followed.
func PackRepository(repo string, w io.Writer) error {
	if _, err := os.Stat(filepath.Join(repo, "config")); err != nil {
		return fmt.Errorf("%s isn't a backup repository (no config file): %w", repo, err)
	}
	tw := tar.NewWriter(w)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: ArchiveDir + "/", Mode: 0o700, ModTime: time.Now()}); err != nil {
		return err
	}
	err := filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repo, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if top, _, _ := strings.Cut(rel, "/"); !repoDirs[top] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		name := ArchiveDir + "/" + rel
		switch {
		case d.IsDir():
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o700, ModTime: info.ModTime()})
		case info.Mode().IsRegular():
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.CopyN(tw, f, info.Size())
			return err
		default:
			return fmt.Errorf("%s isn't a plain file or folder; not packing it", p)
		}
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// UnpackRepository reads a backup file from r into dir (which must exist
// and be empty) and returns the repository's folder inside it. It accepts
// only plain files and folders under ArchiveDir, with plain relative names
// (no "..", no absolute paths, no links), at most MaxArchiveSize in all:
// the file comes from a browser, so nothing in it is trusted.
func UnpackRepository(r io.Reader, dir string) (string, error) {
	tr := tar.NewReader(r)
	var total int64
	entries := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("this isn't a Linx backup file (%v)", err)
		}
		if entries++; entries > maxArchiveEntries {
			return "", errors.New("this backup file has too many files in it")
		}
		rel, err := archiveName(h.Name)
		if err != nil {
			return "", err
		}
		if rel == "" {
			continue // the top folder itself
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if h.Size < 0 || total+h.Size > MaxArchiveSize {
				return "", errors.New("this backup file is too large")
			}
			total += h.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return "", err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return "", fmt.Errorf("this backup file is damaged (%s twice?): %w", rel, err)
			}
			_, err = io.CopyN(f, tr, h.Size)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return "", fmt.Errorf("this backup file is damaged or incomplete: %w", err)
			}
		default:
			return "", fmt.Errorf("this backup file holds something that isn't a plain file (%s)", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "config")); err != nil {
		return "", errors.New("this isn't a Linx backup file (there's no backup inside it)")
	}
	return dir, nil
}

// archiveName checks one entry's name and returns it relative to
// ArchiveDir ("" for ArchiveDir itself).
func archiveName(name string) (string, error) {
	bad := fmt.Errorf("this isn't a Linx backup file (it holds %q)", name)
	if strings.ContainsAny(name, "\\\x00") {
		return "", bad
	}
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == ArchiveDir {
		return "", nil
	}
	rel, ok := strings.CutPrefix(trimmed, ArchiveDir+"/")
	if !ok || rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "../") || rel == ".." || path.IsAbs(rel) {
		return "", bad
	}
	if top, _, _ := strings.Cut(rel, "/"); !repoDirs[top] {
		return "", bad
	}
	return rel, nil
}
