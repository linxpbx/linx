package certs

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DNSState is what linx-certd remembers about the records it keeps right,
// in its private state volume (dns.json): kept across restarts and shared
// by setup's one-off -records run and the follower.
type DNSState struct {
	// Path is the file; "" keeps it in memory only (tests).
	Path string

	mu sync.Mutex
	s  dnsStateFile
	ok bool // loaded
}

type dnsStateFile struct {
	// Owned are the records Linx made or changed at companies whose
	// records can't carry a note saying so (Cloudflare's comment does it
	// there): name → the address Linx left it at. A record still there is
	// Linx's to change; one someone has changed since is theirs.
	Owned map[string]string `json:"owned,omitempty"`
	// Address is what the followed names point at, Previous what they
	// pointed at before, and Changed when that changed.
	Address  string    `json:"address,omitempty"`
	Previous string    `json:"previous,omitempty"`
	Changed  time.Time `json:"changed,omitzero"`
}

// StatePath is dns.json in dir.
func StatePath(dir string) string { return filepath.Join(dir, "dns.json") }

// load reads the file once; a missing or unreadable one starts empty
// (records then count as someone else's until Linx writes them again).
func (d *DNSState) load() {
	if d.ok {
		return
	}
	d.ok = true
	if d.Path == "" {
		return
	}
	b, err := os.ReadFile(d.Path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &d.s)
}

func (d *DNSState) save() error {
	if d.Path == "" {
		return nil
	}
	b, err := json.Marshal(d.s)
	if err != nil {
		return err
	}
	tmp := d.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.Path)
}

// Owner is the address Linx left name at ("": not Linx's).
func (d *DNSState) Owner(name string) string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.load()
	return d.s.Owned[name]
}

// Own records that Linx left name at addr.
func (d *DNSState) Own(name, addr string) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.load()
	if d.s.Owned[name] == addr {
		return nil
	}
	if d.s.Owned == nil {
		d.s.Owned = map[string]string{}
	}
	d.s.Owned[name] = addr
	return d.save()
}

// Followed records that the followed names now point at addr, at now.
func (d *DNSState) Followed(addr string, now time.Time) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.load()
	if d.s.Address == addr {
		return nil
	}
	d.s.Previous, d.s.Address, d.s.Changed = d.s.Address, addr, now.UTC()
	return d.save()
}

// DNSChange is the last time the followed names moved.
type DNSChange struct {
	Address  string    `json:"address,omitempty"`
	Previous string    `json:"previous,omitempty"`
	Changed  time.Time `json:"changed,omitzero"`
}

// LastChange is what the followed names point at and when that changed.
func (d *DNSState) LastChange() DNSChange {
	if d == nil {
		return DNSChange{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.load()
	return DNSChange{Address: d.s.Address, Previous: d.s.Previous, Changed: d.s.Changed}
}

// owned is a copy of Owned (tests).
func (d *DNSState) owned() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.load()
	return maps.Clone(d.s.Owned)
}
