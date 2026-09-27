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
