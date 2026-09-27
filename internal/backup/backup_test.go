package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
)

// fakeRestic simulates restic's process behaviour for one call: whatever
// stdout it's given is written to the runner's writer, and if err is set,
// the runner returns it (as if it already folded restic's stderr in, per
// Runner's contract).
type fakeRestic struct {
	calls   [][]string
	envs    [][]string // one entry per call, in order
	stdout  []string   // one entry per call, in order
	err     []error    // one entry per call, in order (nil = success)
	callNum int
}

func (f *fakeRestic) run(_ context.Context, w io.Writer, env []string, name string, args ...string) error {
	if name != "restic" {
		return errors.New("unexpected command: " + name)
	}
	f.calls = append(f.calls, args)
	f.envs = append(f.envs, env)
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

func TestInitRepoLeavesAnExistingRepositoryAlone(t *testing.T) {
	f := &fakeRestic{} // cat config succeeds: a repository is there
	if err := InitRepo(context.Background(), f.run, Target{Repo: "/repo", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0][len(f.calls[0])-1] != "config" {
		t.Fatalf("calls = %v, want only cat config", f.calls)
	}
}

func TestInitRepoCreatesAMissingRepository(t *testing.T) {
	f := &fakeRestic{err: []error{errors.New("Fatal: unable to open config file: stat /repo/config: no such file or directory")}}
	if err := InitRepo(context.Background(), f.run, Target{Repo: "/repo", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	args := f.lastArgs()
	if len(f.calls) != 2 || args[0] != "-r" || args[1] != "/repo" || args[2] != "--password-file" || args[len(args)-1] != "init" {
		t.Fatalf("unexpected calls: %v", f.calls)
	}
}

func TestInitRepoRealFailure(t *testing.T) {
	f := &fakeRestic{err: []error{errors.New("no config"), errors.New("permission denied")}}
	if err := InitRepo(context.Background(), f.run, Target{Repo: "/repo", Password: "pw"}); err == nil {
		t.Fatal("InitRepo() succeeded, want an error")
	}
}

func TestInitRepoUsesAPasswordFileNotAnArgument(t *testing.T) {
	f := &fakeRestic{}
	if err := InitRepo(context.Background(), f.run, Target{Repo: "/repo", Password: "super-secret-password"}); err != nil {
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
			`{"message_type":"summary","snapshot_id":"abc123","total_bytes_processed":401234}` + "\n",
	}}
	id, size, err := Backup(context.Background(), f.run, Target{Repo: "/repo", Password: "pw"}, "pair-1", "/tmp/dump.sql")
	if err != nil {
		t.Fatal(err)
	}
	if id != "abc123" || size != 401234 {
		t.Fatalf("snapshot id, size = %q, %d; want abc123, 401234", id, size)
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
	if _, _, err := Backup(context.Background(), f.run, Target{Repo: "/repo", Password: "pw"}, "pair-1", "/tmp/dump.sql"); err == nil {
		t.Fatal("Backup() succeeded with no summary line, want an error")
	}
}

func TestBackupCommandFailure(t *testing.T) {
	f := &fakeRestic{err: []error{errors.New("no space left on device")}}
	if _, _, err := Backup(context.Background(), f.run, Target{Repo: "/repo", Password: "pw"}, "pair-1", "/tmp/dump.sql"); err == nil {
		t.Fatal("Backup() succeeded, want an error")
	}
}

func TestForgetKeepsLast(t *testing.T) {
	f := &fakeRestic{}
	if err := Forget(context.Background(), f.run, Target{Repo: "/repo", Password: "pw"}, 14); err != nil {
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

func TestFindSnapshot(t *testing.T) {
	f := &fakeRestic{stdout: []string{`[{"id":"abc123full","short_id":"abc123","time":"2026-09-20T03:00:00Z","tags":["other:x","pair:pair-42"]}]`}}
	snap, err := FindSnapshot(context.Background(), f.run, Target{Repo: "/repo", Password: "pw"}, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if snap.PairID != "pair-42" || snap.ID != "abc123full" || snap.Time.IsZero() {
		t.Fatalf("snapshot = %+v", snap)
	}
	if args := f.lastArgs(); !slices.Contains(args, "latest") {
		t.Fatalf("args = %v, want the snapshot asked for", args)
	}
}

func TestFindSnapshotMissingTag(t *testing.T) {
	f := &fakeRestic{stdout: []string{`[{"id":"abc123","tags":["other:x"]}]`}}
	if _, err := FindSnapshot(context.Background(), f.run, Target{Repo: "/repo", Password: "pw"}, "abc123"); err == nil {
		t.Fatal("FindSnapshot() succeeded for a snapshot with no pair tag, want an error")
	}
}

func TestFindSnapshotNotFound(t *testing.T) {
	f := &fakeRestic{stdout: []string{`[]`}}
	if _, err := FindSnapshot(context.Background(), f.run, Target{Repo: "/repo", Password: "pw"}, "nope"); err == nil {
		t.Fatal("FindSnapshot() succeeded for an unknown snapshot, want an error")
	}
}

func TestRestoreFilesIncludesOnlyTheStagingDir(t *testing.T) {
	f := &fakeRestic{}
	dir := t.TempDir()
	if err := RestoreFiles(context.Background(), f.run, Target{Repo: "/repo", Password: "pw"}, "abc123", dir); err != nil {
		t.Fatal(err)
	}
	args := f.lastArgs()
	i := slices.Index(args, "--include")
	if i < 0 || args[i+1] != StagingDir || !slices.Contains(args, dir) {
		t.Fatalf("args = %v, want --include %s and --target %s", args, StagingDir, dir)
	}
}

func TestCheckRestoreSource(t *testing.T) {
	for _, tc := range []struct {
		source, location string
		ok               bool
	}{
		{SourceFolder, "/var/backups/linx", true},
		{SourceFolder, "/", true},
		{SourceFolder, "var/backups", false},
		{SourceFolder, "sftp:user@host:/x", false},
		{SourceFolder, "rclone:remote:x", false},
		{SourceFolder, "/var/../etc", false},
		{SourceFolder, "/var/backups/", false},
		{SourceFolder, "/var/back\nups", false},
		{SourceFolder, "/var/back\x00ups", false},
		{SourceDestination, "nas", true},
		{SourceDestination, "office-nas_2", true},
		{SourceDestination, "../x", false},
		{SourceDestination, "", false},
		{SourceDestination, "-x", false},
		{"s3", "x", false},
	} {
		if err := CheckRestoreSource(tc.source, tc.location); (err == nil) != tc.ok {
			t.Errorf("CheckRestoreSource(%q, %q) = %v, want ok=%v", tc.source, tc.location, err, tc.ok)
		}
	}
}

func TestCheckSnapshot(t *testing.T) {
	for s, ok := range map[string]bool{"latest": true, "abc12345": true, "ABC12345": false, "abc": false, "latest;rm": false, "": false} {
		if err := CheckSnapshot(s); (err == nil) != ok {
			t.Errorf("CheckSnapshot(%q) = %v, want ok=%v", s, err, ok)
		}
	}
}

func TestTargetExtraAndEnvAreThreadedThrough(t *testing.T) {
	f := &fakeRestic{}
	target := Target{
		Repo:     "sftp:user@host:path",
		Password: "pw",
		Env:      []string{"AWS_ACCESS_KEY_ID=id", "AWS_SECRET_ACCESS_KEY=secret"},
		Extra:    []string{"-o", "sftp.command=ssh -i key user@host -s sftp"},
	}
	if err := InitRepo(context.Background(), f.run, target); err != nil {
		t.Fatal(err)
	}
	args := f.lastArgs()
	found := false
	for i, a := range args {
		if a == "-o" && args[i+1] == "sftp.command=ssh -i key user@host -s sftp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Extra options missing from args: %v", args)
	}
	env := f.envs[len(f.envs)-1]
	if !slices.Contains(env, "AWS_ACCESS_KEY_ID=id") || !slices.Contains(env, "AWS_SECRET_ACCESS_KEY=secret") {
		t.Fatalf("Env missing from the call: %v", env)
	}
}
