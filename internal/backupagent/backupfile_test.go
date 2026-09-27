package backupagent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// scriptedHost answers each command by what it is, not by call order, and
// plays both the control plane's transfer folder and linx.
type scriptedHost struct {
	t       *testing.T
	pending string
	take    string            // export-take's or restore-take's output
	answers map[string]string // by the command's last word ("report", "--json", "export-done", ...)
	calls   []call
	put     []byte // what export-put received
	upload  []byte // what upload-read sends
	fileArg string // linx restore's --file, and whether it existed then
}

func (h *scriptedHost) exec(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	h.calls = append(h.calls, call{stdin, name, args})
	last := args[len(args)-1]
	switch {
	case last == "pending":
		return []byte(h.pending), nil
	case last == "export-take" || last == "restore-take":
		return []byte(h.take), nil
	case name == "linx" && args[0] == "backup" && args[1] == "export":
		out := args[slices.Index(args, "--out")+1]
		if err := os.WriteFile(out, []byte("THE-TAR"), 0o600); err != nil {
			h.t.Fatal(err)
		}
		return []byte(`{"ok":true,"snapshot_id":"abc","snapshot_time":"2026-09-27T03:00:00Z","password":"repo-pw"}`), nil
	case name == "linx" && args[0] == "restore":
		if i := slices.Index(args, "--file"); i >= 0 {
			b, _ := os.ReadFile(args[i+1])
			h.fileArg = args[i+1] + " " + string(b)
		}
		return []byte(h.answers["restore"]), nil
	}
	return []byte(h.answers[last]), nil
}

func (h *scriptedHost) stream(_ context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	h.calls = append(h.calls, call{nil, name, args})
	switch args[len(args)-2] {
	case "export-put":
		h.put, _ = io.ReadAll(stdin)
	case "upload-read":
		_, _ = stdout.Write(h.upload)
	}
	return nil
}

func (h *scriptedHost) called(word string) []call {
	var out []call
	for _, c := range h.calls {
		if slices.Contains(c.args, word) {
			out = append(out, c)
		}
	}
	return out
}

func TestExportBacksUpThenHandsTheFileOver(t *testing.T) {
	h := &scriptedHost{t: t, pending: "run export\n", take: `{"id":"0192a4b0-0000-7000-8000-000000000001"}`,
		answers: map[string]string{"--json": `{"destinations":[{"name":"local","ok":true,"snapshot_id":"abc"}]}`}}
	work := t.TempDir()
	env := Env{Exec: h.exec, Stream: h.stream, LinxPath: "linx", WorkDir: work, Now: time.Now}
	if err := env.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r := h.called("report"); len(r) != 1 || !strings.Contains(string(r[0].stdin), `"trigger":"manual"`) {
		t.Fatalf("the backup before the file wasn't reported as a manual run: %+v", r)
	}
	if string(h.put) != "THE-TAR" {
		t.Fatalf("export-put got %q", h.put)
	}
	done := h.called("export-done")
	if len(done) != 1 {
		t.Fatalf("export-done calls %+v", h.calls)
	}
	var got map[string]any
	_ = json.Unmarshal(done[0].stdin, &got)
	if got["ok"] != true || got["password"] != "repo-pw" || got["id"] != "0192a4b0-0000-7000-8000-000000000001" {
		t.Fatalf("export-done got %s", done[0].stdin)
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Errorf("left %d things in the work folder", len(entries))
	}
}

func TestExportReportsAFailure(t *testing.T) {
	h := &scriptedHost{t: t, pending: "run export\n", take: `{"id":"0192a4b0-0000-7000-8000-000000000001"}`,
		answers: map[string]string{"--json": `{"destinations":[]}`}}
	env := Env{Exec: func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		if name == "linx" && len(args) > 1 && args[1] == "export" {
			return []byte(`{"ok":false,"error":"nothing here to download"}`), nil
		}
		return h.exec(ctx, stdin, name, args...)
	}, Stream: h.stream, LinxPath: "linx", WorkDir: t.TempDir()}
	if err := env.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	done := h.called("export-done")
	if len(done) != 1 || !bytes.Contains(done[0].stdin, []byte(`"ok":false`)) || !bytes.Contains(done[0].stdin, []byte("nothing here")) {
		t.Fatalf("export-done %+v", done)
	}
	if h.put != nil {
		t.Error("handed over a file that was never made")
	}
}

func TestExportNothingWaiting(t *testing.T) {
	h := &scriptedHost{t: t, pending: "run export\n", take: ""}
	env := Env{Exec: h.exec, Stream: h.stream, LinxPath: "linx", WorkDir: t.TempDir()}
	if err := env.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 2 {
		t.Fatalf("calls %+v, want pending and export-take only", h.calls)
	}
}

func TestRestoreFromAnUploadedFile(t *testing.T) {
	upload := "0192a4b0-0000-7000-8000-0000000000aa"
	h := &scriptedHost{t: t, pending: "run restore\n", upload: []byte("UPLOADED-TAR"),
		take:    `{"id":"0192a4b0-0000-7000-8000-000000000002","source":"upload","location":"` + upload + `","snapshot":"latest","password":"pw"}`,
		answers: map[string]string{"restore": `{"ok":true,"changed":true,"snapshot_id":"abc"}`}}
	work := t.TempDir()
	env := Env{Exec: h.exec, Stream: h.stream, LinxPath: "linx", WorkDir: work}
	if err := env.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h.fileArg, work) || !strings.HasSuffix(h.fileArg, " UPLOADED-TAR") {
		t.Fatalf("linx restore --file saw %q", h.fileArg)
	}
	if d := h.called("upload-delete"); len(d) != 1 || d[0].args[len(d[0].args)-1] != upload {
		t.Fatalf("upload-delete %+v", d)
	}
	if len(h.called("restore-done")) != 1 {
		t.Fatal("restore-done not reported")
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Errorf("left %d things in the work folder", len(entries))
	}
}

func TestRestoreRefusesABadUploadID(t *testing.T) {
	h := &scriptedHost{t: t, pending: "run restore\n",
		take: `{"id":"0192a4b0-0000-7000-8000-000000000002","source":"upload","location":"../../etc/passwd","snapshot":"latest","password":"pw"}`}
	env := Env{Exec: h.exec, Stream: h.stream, LinxPath: "linx", WorkDir: t.TempDir()}
	if err := env.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(h.called("upload-read")) != 0 || len(h.called("restore-failed")) != 1 {
		t.Fatalf("calls %+v", h.calls)
	}
}

func TestCapWriter(t *testing.T) {
	var b bytes.Buffer
	c := &capWriter{w: &b, left: 5}
	if _, err := c.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("def")); err == nil {
		t.Fatal("wrote past the cap")
	}
	if b.String() != "abc" {
		t.Fatalf("%q", b.String())
	}
}
