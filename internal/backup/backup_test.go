package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// fakeRestic simulates restic's process behaviour for one call: whatever
// stdout it's given is written to the runner's writer, and if err is set,
// the runner returns it (as if it already folded restic's stderr in, per
// Runner's contract).
type fakeRestic struct {
	calls   [][]string
	stdout  []string // one entry per call, in order
	err     []error  // one entry per call, in order (nil = success)
	callNum int
}

func (f *fakeRestic) run(_ context.Context, w io.Writer, name string, args ...string) error {
	if name != "restic" {
		return errors.New("unexpected command: " + name)
	}
	f.calls = append(f.calls, args)
	i := f.callNum
	f.callNum++
	if w != nil && i < len(f.stdout) {
		_, _ = io.WriteString(w, f.stdout[i])
	}
	if i < len(f.err) {
		return f.err[i]
	}
	return nil
}

func (f *fakeRestic) lastArgs() []string { return f.calls[len(f.calls)-1] }

func TestNewPasswordAndPairID(t *testing.T) {
	p1, err := NewPassword()
	if err != nil || len(p1) < 32 {
		t.Fatalf("NewPassword() = %q, %v", p1, err)
	}
	p2, _ := NewPassword()
	if p1 == p2 {
		t.Fatal("two passwords were identical")
	}
	id1, err := NewPairID()
	if err != nil || id1 == "" {
		t.Fatalf("NewPairID() = %q, %v", id1, err)
	}
	id2, _ := NewPairID()
	if id1 == id2 {
		t.Fatal("two pair ids were identical")
	}
}

func TestInitRepoTreatsAlreadyInitializedAsSuccess(t *testing.T) {
	f := &fakeRestic{err: []error{errors.New("Fatal: create repository at /repo failed: config file already initialized")}}
	if err := InitRepo(context.Background(), f.run, "/repo", "pw"); err != nil {
		t.Fatalf("InitRepo() = %v, want nil for an already-initialized repository", err)
	}
	args := f.lastArgs()
	if args[0] != "-r" || args[1] != "/repo" || args[2] != "--password-file" || args[len(args)-1] != "init" {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestInitRepoRealFailure(t *testing.T) {
	f := &fakeRestic{err: []error{errors.New("permission denied")}}
	if err := InitRepo(context.Background(), f.run, "/repo", "pw"); err == nil {
		t.Fatal("InitRepo() succeeded, want an error")
	}
}

func TestInitRepoUsesAPasswordFileNotAnArgument(t *testing.T) {
	f := &fakeRestic{}
	if err := InitRepo(context.Background(), f.run, "/repo", "super-secret-password"); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.lastArgs() {
		if strings.Contains(a, "super-secret-password") {
			t.Fatalf("the password was passed as a bare argument: %v", f.lastArgs())
		}
	}
	// The --password-file argument names a file that existed only for the
	// duration of the call; it must be gone now.
	var pf string
	for i, a := range f.lastArgs() {
		if a == "--password-file" {
			pf = f.lastArgs()[i+1]
		}
	}
	if pf == "" {
		t.Fatal("no --password-file argument found")
	}
	if _, err := os.Stat(pf); !os.IsNotExist(err) {
		t.Fatalf("password file %q wasn't cleaned up: %v", pf, err)
	}
}

func TestBackupParsesTheSnapshotID(t *testing.T) {
	f := &fakeRestic{stdout: []string{
		`{"message_type":"status","percent_done":0.5}` + "\n" +
			`{"message_type":"summary","snapshot_id":"abc123"}` + "\n",
	}}
	id, err := Backup(context.Background(), f.run, "/repo", "pw", "pair-1", "/tmp/dump.sql")
	if err != nil {
		t.Fatal(err)
	}
	if id != "abc123" {
		t.Fatalf("snapshot id = %q, want abc123", id)
	}
	args := f.lastArgs()
	found := false
	for i, a := range args {
		if a == "--tag" && args[i+1] == "pair:pair-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pair tag missing from args: %v", args)
	}
}

func TestBackupNoSummaryLineIsAnError(t *testing.T) {
	f := &fakeRestic{stdout: []string{`{"message_type":"status"}` + "\n"}}
	if _, err := Backup(context.Background(), f.run, "/repo", "pw", "pair-1", "/tmp/dump.sql"); err == nil {
		t.Fatal("Backup() succeeded with no summary line, want an error")
	}
}

func TestBackupCommandFailure(t *testing.T) {
	f := &fakeRestic{err: []error{errors.New("no space left on device")}}
	if _, err := Backup(context.Background(), f.run, "/repo", "pw", "pair-1", "/tmp/dump.sql"); err == nil {
		t.Fatal("Backup() succeeded, want an error")
	}
}

func TestForgetKeepsLast(t *testing.T) {
	f := &fakeRestic{}
	if err := Forget(context.Background(), f.run, "/repo", "pw", 14); err != nil {
		t.Fatal(err)
	}
	args := f.lastArgs()
	found := false
	for i, a := range args {
		if a == "--keep-last" && args[i+1] == "14" {
			found = true
		}
	}
	if !found {
		t.Fatalf("--keep-last 14 missing from args: %v", args)
	}
}

func TestPairIDForSnapshot(t *testing.T) {
	f := &fakeRestic{stdout: []string{`[{"short_id":"abc123","tags":["other:x","pair:pair-42"]}]`}}
	id, err := PairIDForSnapshot(context.Background(), f.run, "/repo", "pw", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if id != "pair-42" {
		t.Fatalf("pair id = %q, want pair-42", id)
	}
}

func TestPairIDForSnapshotMissingTag(t *testing.T) {
	f := &fakeRestic{stdout: []string{`[{"short_id":"abc123","tags":["other:x"]}]`}}
	if _, err := PairIDForSnapshot(context.Background(), f.run, "/repo", "pw", "abc123"); err == nil {
		t.Fatal("PairIDForSnapshot() succeeded for a snapshot with no pair tag, want an error")
	}
}

func TestPairIDForSnapshotNotFound(t *testing.T) {
	f := &fakeRestic{stdout: []string{`[]`}}
	if _, err := PairIDForSnapshot(context.Background(), f.run, "/repo", "pw", "nope"); err == nil {
		t.Fatal("PairIDForSnapshot() succeeded for an unknown snapshot, want an error")
	}
}

func TestRestoreKeysOnlyIncludesTheKeysDir(t *testing.T) {
	f := &fakeRestic{}
	dir := t.TempDir()
	if err := RestoreKeys(context.Background(), f.run, "/repo", "pw", "abc123", dir); err != nil {
		t.Fatal(err)
	}
	args := f.lastArgs()
	found := false
	for i, a := range args {
		if a == "--include" && args[i+1] == KeysPath {
			found = true
		}
	}
	if !found {
		t.Fatalf("--include %s missing from args: %v", KeysPath, args)
	}
}
