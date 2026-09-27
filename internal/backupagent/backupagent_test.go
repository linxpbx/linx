package backupagent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type call struct {
	stdin []byte
	name  string
	args  []string
}

type fakeExec struct {
	calls   []call
	outputs [][]byte
	errs    []error
	n       int
}

func (f *fakeExec) run(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{stdin, name, args})
	i := f.n
	f.n++
	var out []byte
	var err error
	if i < len(f.outputs) {
		out = f.outputs[i]
	}
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return out, err
}

func TestOnceSkipsWhenNotDue(t *testing.T) {
	f := &fakeExec{outputs: [][]byte{[]byte("skip\n")}}
	env := Env{Exec: f.run, LinxPath: "linx", Now: func() time.Time { return time.Unix(0, 0) }}
	if err := env.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want 1 (pending only)", len(f.calls))
	}
}

func TestOnceRunsAndReportsOnDue(t *testing.T) {
	f := &fakeExec{outputs: [][]byte{
		[]byte("run scheduled\n"),
		[]byte(`{"destinations":[{"name":"local","ok":true,"snapshot_id":"abc"}]}`),
		[]byte("Recorded backup ... (success).\n"),
	}}
	env := Env{Exec: f.run, LinxPath: "/usr/local/bin/linx", Now: func() time.Time { return time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC) }}
	if err := env.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(f.calls))
	}
	if f.calls[1].name != "/usr/local/bin/linx" || f.calls[1].args[0] != "backup" || f.calls[1].args[1] != "--json" {
		t.Fatalf("second call = %+v", f.calls[1])
	}
	reportCall := f.calls[2]
	if reportCall.args[len(reportCall.args)-1] != "report" {
		t.Fatalf("third call = %+v", reportCall)
	}
	var sent report
	if err := json.Unmarshal(reportCall.stdin, &sent); err != nil {
		t.Fatalf("report stdin wasn't valid JSON: %v: %s", err, reportCall.stdin)
	}
	if sent.Trigger != "scheduled" || len(sent.Destinations) != 1 || sent.Destinations[0].SnapshotID != "abc" {
		t.Fatalf("sent report = %+v", sent)
	}
}

func TestOnceStillReportsWhenLinxBackupExitsNonZero(t *testing.T) {
	// linx backup --json exits 1 when every destination failed, but still
	// prints a valid JSON summary — Once must trust that, not the exit code.
	f := &fakeExec{
		outputs: [][]byte{
			[]byte("run manual\n"),
			[]byte(`{"destinations":[{"name":"local","ok":false,"error":"no space left on device"}]}`),
			[]byte(""),
		},
		errs: []error{nil, errors.New("exit status 1"), nil},
	}
	env := Env{Exec: f.run, LinxPath: "linx", Now: time.Now}
	if err := env.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 {
		t.Fatalf("calls = %d, want 3 (it should still report)", len(f.calls))
	}
	var sent report
	if err := json.Unmarshal(f.calls[2].stdin, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Destinations[0].OK {
		t.Fatal("the failed destination should be reported as not ok")
	}
}

func TestOnceFailsWhenPendingCantBeRead(t *testing.T) {
	f := &fakeExec{outputs: [][]byte{[]byte("")}, errs: []error{errors.New("docker: command not found")}}
	env := Env{Exec: f.run, LinxPath: "linx", Now: time.Now}
	if err := env.Once(context.Background()); err == nil {
		t.Fatal("Once() should fail when the control plane can't be reached")
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want 1 (nothing else attempted)", len(f.calls))
	}
}

func TestOnceFailsWhenLinxBackupOutputIsUnparseable(t *testing.T) {
	f := &fakeExec{outputs: [][]byte{[]byte("run manual\n"), []byte("not json")}}
	env := Env{Exec: f.run, LinxPath: "linx", Now: time.Now}
	if err := env.Once(context.Background()); err == nil {
		t.Fatal("Once() should fail when linx backup's output can't be parsed")
	}
	if len(f.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (report never attempted)", len(f.calls))
	}
}

func TestParsePending(t *testing.T) {
	cases := []struct {
		in          string
		wantTrigger string
		wantDue     bool
	}{
		{"skip\n", "", false},
		{"run manual\n", "manual", true},
		{"run scheduled\n", "scheduled", true},
		{"", "", false},
	}
	for _, c := range cases {
		trigger, due := parsePending([]byte(c.in))
		if trigger != c.wantTrigger || due != c.wantDue {
			t.Errorf("parsePending(%q) = %q, %v, want %q, %v", c.in, trigger, due, c.wantTrigger, c.wantDue)
		}
	}
}

const takenRestore = `{"id":"0192f000-0000-7000-8000-000000000001","source":"folder","location":"/var/backups/linx","snapshot":"latest","password":"pw-123","requested_by":"user:u1"}`

func TestOnceRestoresAndRecordsIt(t *testing.T) {
	f := &fakeExec{outputs: [][]byte{
		[]byte("run restore\n"),
		[]byte(takenRestore + "\n"),
		[]byte(`{"ok":true,"changed":true,"snapshot_id":"abcdef0123","snapshot_time":"2026-09-20T03:00:00Z"}` + "\n"),
		[]byte("Recorded the restore.\n"),
	}}
	env := Env{Exec: f.run, LinxPath: "/usr/local/bin/linx"}
	if err := env.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 4 {
		t.Fatalf("calls = %+v", f.calls)
	}
	run := f.calls[2]
	if run.name != "/usr/local/bin/linx" || string(run.stdin) != "pw-123" {
		t.Errorf("linx restore call = %+v, want the password on stdin", run)
	}
	want := []string{"restore", "--yes", "--json", "--password-stdin", "--path", "/var/backups/linx", "latest"}
	if len(run.args) != len(want) {
		t.Fatalf("args = %v, want %v", run.args, want)
	}
	for i := range want {
		if run.args[i] != want[i] {
			t.Fatalf("args = %v, want %v", run.args, want)
		}
	}
	for _, a := range run.args {
		if a == "pw-123" {
			t.Error("the password is a command-line argument")
		}
	}
	done := f.calls[3]
	if done.args[len(done.args)-1] != "restore-done" {
		t.Fatalf("last call = %v", done.args)
	}
	var rec map[string]any
	if err := json.Unmarshal(done.stdin, &rec); err != nil || rec["snapshot_id"] != "abcdef0123" || rec["requested_by"] != "user:u1" {
		t.Errorf("restore-done got %s", done.stdin)
	}
	if _, leaked := rec["password"]; leaked {
		t.Error("the password was reported back")
	}
}

func TestOnceReportsAFailedRestore(t *testing.T) {
	f := &fakeExec{
		outputs: [][]byte{
			[]byte("run restore\n"),
			[]byte(takenRestore + "\n"),
			[]byte(`{"ok":false,"changed":false,"error":"couldn't read that backup: wrong password"}` + "\n"),
			[]byte("Recorded the failed restore.\n"),
		},
		errs: []error{nil, nil, errors.New("exit status 1")},
	}
	env := Env{Exec: f.run, LinxPath: "linx"}
	if err := env.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed := f.calls[3]
	var rec map[string]string
	if failed.args[len(failed.args)-1] != "restore-failed" || json.Unmarshal(failed.stdin, &rec) != nil ||
		rec["error"] != "couldn't read that backup: wrong password" || rec["id"] == "" {
		t.Fatalf("restore-failed call = %v %s", failed.args, failed.stdin)
	}
}

func TestOnceRefusesAnUnsafeRestoreLocation(t *testing.T) {
	bad := `{"id":"0192f000-0000-7000-8000-000000000001","source":"folder","location":"rclone:x:y","snapshot":"latest","password":"pw"}`
	f := &fakeExec{outputs: [][]byte{[]byte("run restore\n"), []byte(bad), []byte("ok")}}
	env := Env{Exec: f.run, LinxPath: "linx"}
	if err := env.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 || f.calls[2].args[len(f.calls[2].args)-1] != "restore-failed" {
		t.Fatalf("calls = %+v, want take then restore-failed, never linx restore", f.calls)
	}
}
