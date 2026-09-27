package backup

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	files := map[string]string{
		"config":          "cfg",
		"keys/abc":        "key",
		"data/00/0011":    "pack",
		"index/i1":        "idx",
		"snapshots/s1":    "snap",
		"locks/stale":     "lock",
		"somethingelse/x": "x",
	}
	for name, body := range files {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func TestPackAndUnpackRepository(t *testing.T) {
	repo := writeRepo(t)
	var buf bytes.Buffer
	if err := PackRepository(repo, &buf); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	got, err := UnpackRepository(&buf, out)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"config": "cfg", "keys/abc": "key", "data/00/0011": "pack", "index/i1": "idx", "snapshots/s1": "snap"} {
		b, err := os.ReadFile(filepath.Join(got, filepath.FromSlash(name)))
		if err != nil || string(b) != want {
			t.Errorf("%s: %q, %v", name, b, err)
		}
	}
	for _, left := range []string{"locks", "somethingelse"} {
		if _, err := os.Stat(filepath.Join(got, left)); err == nil {
			t.Errorf("%s was carried", left)
		}
	}
}

func TestPackRefusesNonRepositoryAndLinks(t *testing.T) {
	if err := PackRepository(t.TempDir(), &bytes.Buffer{}); err == nil {
		t.Error("packed a folder with no config")
	}
	repo := writeRepo(t)
	if err := os.Symlink("/etc/passwd", filepath.Join(repo, "data", "link")); err != nil {
		t.Fatal(err)
	}
	if err := PackRepository(repo, &bytes.Buffer{}); err == nil {
		t.Error("packed a symlink")
	}
}

type entry struct {
	name string
	typ  byte
	body string
	link string
}

func tarOf(t *testing.T, entries ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o600, Size: int64(len(e.body)), Linkname: e.link}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	return &buf
}

func TestUnpackRefusesUnsafeEntries(t *testing.T) {
	cfg := entry{name: "linx-backup/config", typ: tar.TypeReg, body: "c"}
	for name, bad := range map[string]entry{
		"dot dot":         {name: "linx-backup/data/../../evil", typ: tar.TypeReg, body: "x"},
		"absolute":        {name: "/etc/evil", typ: tar.TypeReg, body: "x"},
		"outside top":     {name: "other/config", typ: tar.TypeReg, body: "x"},
		"unknown folder":  {name: "linx-backup/evil/x", typ: tar.TypeReg, body: "x"},
		"symlink":         {name: "linx-backup/data/l", typ: tar.TypeSymlink, link: "/etc/passwd"},
		"hard link":       {name: "linx-backup/data/l", typ: tar.TypeLink, link: "linx-backup/config"},
		"backslash":       {name: "linx-backup/data\\..\\x", typ: tar.TypeReg, body: "x"},
		"device":          {name: "linx-backup/data/d", typ: tar.TypeChar},
		"not normalised":  {name: "linx-backup/data//x", typ: tar.TypeReg, body: "x"},
		"top is the name": {name: "linx-backup/..", typ: tar.TypeDir},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := UnpackRepository(tarOf(t, cfg, bad), dir); err == nil {
				t.Fatal("accepted")
			}
			if _, err := os.Stat("/etc/evil"); err == nil {
				t.Fatal("wrote outside")
			}
		})
	}
}

func TestUnpackRefusesDuplicatesAndNonBackups(t *testing.T) {
	cfg := entry{name: "linx-backup/config", typ: tar.TypeReg, body: "c"}
	if _, err := UnpackRepository(tarOf(t, cfg, cfg), t.TempDir()); err == nil {
		t.Error("accepted a file twice")
	}
	if _, err := UnpackRepository(tarOf(t, entry{name: "linx-backup/data/x", typ: tar.TypeReg, body: "x"}), t.TempDir()); err == nil {
		t.Error("accepted a file with no repository config")
	}
	_, err := UnpackRepository(strings.NewReader("not a tar file at all, just some text that is long enough"), t.TempDir())
	if err == nil {
		t.Error("accepted garbage")
	}
}
