// Package trunkstatus is how Linx knows whether each trunk is working
// (docs/TRUNKS.md §9, §10). Asterisk's ARI has no event or resource for
// outbound registrations, so Asterisk's entrypoint asks its console
// ("pjsip show registrations", "pjsip show contacts": the keep-alive
// checks every trunk's AOR gets) every few seconds and writes what it
// finds to a small file in a memory-only volume; the control plane reads
// it (and raises trunk.status_changed and the trunk-down alert), and
// `linx doctor` runs the same console commands through docker exec. The
// parsers here serve all three.
package trunkstatus

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileName is the status file in the status directory.
const FileName = "trunk-status.json"

// EndpointPrefix is how trunks' PJSIP objects are named (trunk.Trunk's
// Endpoint): only these are reported.
const EndpointPrefix = "trunk-"

// Registration states as "pjsip show registrations" prints them.
const (
	RegRegistered   = "Registered"
	RegUnregistered = "Unregistered"
	RegRejected     = "Rejected" // refused, or no answer after its retries
	RegStopping     = "Stopping"
	RegStopped      = "Stopped"
)

// Contact states as "pjsip show contacts" prints them (the keep-alive
// OPTIONS every 60 s, internal/trunkconf).
const (
	ContactAvail   = "Avail"
	ContactUnavail = "Unavail"
	ContactNonQual = "NonQual"
	ContactUnknown = "Unknown"
	ContactCreated = "Created"
	ContactRemoved = "Removed"
)

// Trunk is what Asterisk says about one trunk. Empty fields: Asterisk has
// no such object (a trunk with no registration, or not loaded yet).
type Trunk struct {
	Registration string `json:"registration,omitempty"`
	Contact      string `json:"contact,omitempty"`
}

// File is the status file.
type File struct {
	// WrittenAt is when Asterisk was last asked. A file much older than
	// the write interval means the entrypoint (or Asterisk) isn't
	// answering: the control plane then treats every trunk as unknown
	// rather than down.
	WrittenAt time.Time `json:"written_at"`
	// Trunks by PJSIP endpoint name ("trunk-<uuid>").
	Trunks map[string]Trunk `json:"trunks"`
}

// ParseRegistrations reads "pjsip show registrations": each registration's
// name (before the "/" of "name/server-uri") and its status.
//
//	 <Registration/ServerURI..............................>  <Auth....................>  <Status.......>
//	==========================================================================================
//
//	 trunk-1111.../sip:192.0.2  trunk-1111...  Rejected          (exp. 18s ago)
func ParseRegistrations(out string) map[string]string {
	regs := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || strings.HasPrefix(f[0], "<") {
			continue
		}
		name, _, ok := strings.Cut(f[0], "/")
		if !ok || !strings.HasPrefix(name, EndpointPrefix) {
			continue
		}
		// With an auth object the status is the third field; without, the
		// second.
		status := f[2]
		if !isRegStatus(status) && isRegStatus(f[1]) {
			status = f[1]
		}
		if isRegStatus(status) {
			regs[name] = status
		}
	}
	return regs
}

func isRegStatus(s string) bool {
	switch s {
	case RegRegistered, RegUnregistered, RegRejected, RegStopping, RegStopped:
		return true
	}
	return false
}

// ParseContacts reads "pjsip show contacts": each trunk AOR's contact
// status. A trunk has one contact (its provider's address).
//
//	Contact:  trunk-2222.../sip b0e9390783 Avail         0.497
func ParseContacts(out string) map[string]string {
	contacts := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[0] != "Contact:" || strings.HasPrefix(f[1], "<") {
			continue
		}
		aor, _, _ := strings.Cut(f[1], "/")
		if !strings.HasPrefix(aor, EndpointPrefix) {
			continue
		}
		status := f[3]
		// Several contacts on one AOR (not something Linx renders): any
		// reachable one counts.
		if prev, ok := contacts[aor]; ok && prev == ContactAvail {
			continue
		}
		contacts[aor] = status
	}
	return contacts
}

// Merge combines both commands' answers into the status file's trunks.
func Merge(registrations, contacts map[string]string) map[string]Trunk {
	out := map[string]Trunk{}
	for name, s := range registrations {
		t := out[name]
		t.Registration = s
		out[name] = t
	}
	for name, s := range contacts {
		t := out[name]
		t.Contact = s
		out[name] = t
	}
	return out
}

// Write replaces the status file in dir atomically, readable by everyone
// (it holds only states, never an address or a login).
func Write(dir string, f File) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".trunk-status-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, FileName))
}

// ErrNoFile is Read finding no status file yet.
var ErrNoFile = errors.New("no trunk status yet")

// Read reads the status file in dir.
func Read(dir string) (File, error) {
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return File{}, ErrNoFile
	}
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return File{}, fmt.Errorf("trunk status: %w", err)
	}
	return f, nil
}

// A trunk's status, as the API, events and doctor say it.
const (
	StatusRegistered  = "registered"  // Linx is signed in to the provider (and it answers)
	StatusReachable   = "reachable"   // the provider or phone system answers
	StatusRejected    = "rejected"    // the provider answers but refuses Linx's login
	StatusUnreachable = "unreachable" // no answer
	StatusUnknown     = "unknown"     // not checked yet
	StatusDisabled    = "disabled"    // turned off
)

// Up reports whether status means calls can use the trunk.
func Up(status string) bool { return status == StatusRegistered || status == StatusReachable }

// Down reports whether status means the trunk isn't working.
func Down(status string) bool { return status == StatusRejected || status == StatusUnreachable }

// Decide turns what Asterisk says about a trunk into its status and a
// plain-words reason. registers: the trunk signs in to its provider
// (trunk.KindRegistration).
func Decide(registers bool, t Trunk) (status, detail string) {
	switch t.Contact {
	case ContactUnavail:
		return StatusUnreachable, "It doesn't answer Linx's keep-alive checks."
	}
	if registers {
		switch t.Registration {
		case RegRegistered:
			return StatusRegistered, "Linx is signed in to it."
		case RegRejected:
			if t.Contact == ContactAvail {
				return StatusRejected, "It answers, but refuses Linx's login. Check the login and password."
			}
			return StatusUnreachable, "Linx can't sign in to it: it refused the login or didn't answer."
		case "":
			return StatusUnknown, "Asterisk hasn't loaded it yet."
		default:
			return StatusUnknown, "Linx is signing in to it."
		}
	}
	switch t.Contact {
	case ContactAvail:
		return StatusReachable, "It answers Linx's keep-alive checks."
	case "":
		return StatusUnknown, "Asterisk hasn't loaded it yet."
	default:
		return StatusUnknown, "Waiting for the first keep-alive check."
	}
}
