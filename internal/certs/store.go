package certs

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// Store layout in CertsDir (a volume consumers mount read-only):
//
//	current -> 20260923T101500.000000000Z   (symlink, swapped atomically)
//	20260923T101500.000000000Z/fullchain.pem
//	20260923T101500.000000000Z/privkey.pem
//	20260923T101500.000000000Z/meta.json
//
// Consumers always read current/…, so they never see a half-written pair.
const (
	CurrentLink   = "current"
	FullchainFile = "fullchain.pem"
	PrivkeyFile   = "privkey.pem"
	MetaFile      = "meta.json"
	keepVersions  = 2
)

// Meta describes a deployed certificate.
type Meta struct {
	Names    []string  `json:"names"`
	Issuer   string    `json:"issuer"`
	Staging  bool      `json:"staging"`
	NotAfter time.Time `json:"not_after"`
	IssuedAt time.Time `json:"issued_at"`
}

// Deployed is the certificate currently served.
type Deployed struct {
	Meta Meta
	Leaf *x509.Certificate
}

// Store reads and atomically replaces the deployed certificate.
type Store struct {
	Dir string
}

// Current returns the deployed certificate, or (nil, nil) if there is none.
func (s Store) Current() (*Deployed, error) {
	dir := filepath.Join(s.Dir, CurrentLink)
	chain, err := os.ReadFile(filepath.Join(dir, FullchainFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	leaf, err := parseLeaf(chain)
	if err != nil {
		return nil, err
	}
	var m Meta
	b, err := os.ReadFile(filepath.Join(dir, MetaFile))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", MetaFile, err)
	}
	return &Deployed{Meta: m, Leaf: leaf}, nil
}

// Deploy writes a new certificate version and switches current to it.
func (s Store) Deploy(chain, key []byte, m Meta) error {
	if _, err := parseLeaf(chain); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	name := m.IssuedAt.UTC().Format("20060102T150405.000000000Z")
	dir := filepath.Join(s.Dir, name)
	if err := os.Mkdir(dir, 0o750); err != nil {
		return err
	}
	meta, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// The key is group-readable: consumers share linx-certd's group (compose).
	for _, f := range []struct {
		name string
		data []byte
		perm os.FileMode
	}{
		{FullchainFile, chain, 0o644},
		{PrivkeyFile, key, 0o640},
		{MetaFile, meta, 0o644},
	} {
		if err := writeSynced(filepath.Join(dir, f.name), f.data, f.perm); err != nil {
			return err
		}
	}
	if err := syncDir(dir); err != nil {
		return err
	}

	tmp := filepath.Join(s.Dir, CurrentLink+".tmp")
	_ = os.Remove(tmp)
	if err := os.Symlink(name, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.Dir, CurrentLink)); err != nil {
		return err
	}
	if err := syncDir(s.Dir); err != nil {
		return err
	}
	return s.prune(name)
}

// prune removes old versions, keeping the newest few for rollback.
func (s Store) prune(current string) error {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return err
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() && isVersion(e.Name()) {
			versions = append(versions, e.Name())
		}
	}
	sort.Strings(versions)
	if len(versions) <= keepVersions {
		return nil
	}
	var errs []error
	for _, v := range versions[:len(versions)-keepVersions] {
		if v != current {
			errs = append(errs, os.RemoveAll(filepath.Join(s.Dir, v)))
		}
	}
	return errors.Join(errs...)
}

func isVersion(name string) bool {
	_, err := time.Parse("20060102T150405.000000000Z", name)
	return err == nil
}

func parseLeaf(chain []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(chain)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("certificate chain: no PEM certificate found")
	}
	return x509.ParseCertificate(block.Bytes)
}

func writeSynced(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// sameNames reports whether a and b cover the same names, in any order.
func sameNames(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
