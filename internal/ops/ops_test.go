package ops

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDocker records every command and answers docker inspect/logs/restart.
type fakeDocker struct {
	mu       sync.Mutex
	calls    [][]string
	inspect  string
	stdout   string
	stderr   string
	restart  error
	block    chan struct{}
	restarts chan string
}

func (f *fakeDocker) exec(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.mu.Unlock()
	switch args[0] {
	case "container":
		return []byte(f.inspect), []byte("Error: No such container: linx-sni"), errors.New("exit status 1")
	case "logs":
		if f.block != nil {
			<-f.block
		}
		return []byte(f.stdout), []byte(f.stderr), nil
	case "restart":
		if f.restarts != nil {
			f.restarts <- args[len(args)-1]
		}
		return nil, nil, f.restart
	}
	return nil, nil, errors.New("unexpected command")
}

func (f *fakeDocker) commands() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

const inspectJSON = `[
 {"Name":"/linx-control-plane","RestartCount":0,"State":{"Status":"running","StartedAt":"2026-09-28T08:00:00.5Z","Health":{"Status":"healthy"}}},
 {"Name":"/linx-asterisk","RestartCount":3,"State":{"Status":"running","StartedAt":"2026-09-28T09:00:00Z","Health":{"Status":"unhealthy"}}},
 {"Name":"/linx-postgres","RestartCount":0,"State":{"Status":"exited","StartedAt":"2026-09-28T07:00:00Z"}},
 {"Name":"/something-else","RestartCount":0,"State":{"Status":"running","StartedAt":"2026-09-28T07:00:00Z"}}
]`

// link connects a Hub and an Agent over an in-memory connection and
// waits for the agent's first status.
func link(t *testing.T, d *fakeDocker, now func() time.Time) (*Hub, *Agent) {
	t.Helper()
	h := &Hub{}
	a := &Agent{Exec: d.exec, Now: now}
	hc, ac := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go h.handle(hc)
	go func() { _ = a.Serve(ctx, ac); close(done) }()
	t.Cleanup(func() { cancel(); ac.Close(); <-done })
	deadline := time.Now().Add(2 * time.Second)
	for h.Snapshot().CheckedAt.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("no status from the agent")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return h, a
}

func TestStatus(t *testing.T) {
	d := &fakeDocker{inspect: inspectJSON}
	h, _ := link(t, d, time.Now)
	s := h.Snapshot()
	if !s.Connected || len(s.Containers) != len(Services) {
		t.Fatalf("snapshot = %+v", s)
	}
	byName := map[string]Container{}
	for _, c := range s.Containers {
		byName[c.Service] = c
	}
	if c := byName["control-plane"]; c.Summary() != StateRunning || c.StartedAt.IsZero() {
		t.Errorf("control-plane = %+v", c)
	}
	if c := byName["asterisk"]; c.Summary() != StateUnhealthy || c.Restarts != 3 {
		t.Errorf("asterisk = %+v", c)
	}
	if c := byName["postgres"]; c.Summary() != StateStopped || !c.StartedAt.IsZero() {
		t.Errorf("postgres = %+v (a stopped service has no start time)", c)
	}
	if c := byName["sni"]; c.Summary() != StateMissing {
		t.Errorf("sni = %+v", c)
	}
	// One docker inspect, of exactly the listed containers.
	cmd := d.commands()[0]
	want := []string{"docker", "container", "inspect"}
	for _, s := range Services {
		want = append(want, s.Container)
	}
	if !slices.Equal(cmd, want) {
		t.Errorf("command = %q, want %q", cmd, want)
	}
}

func TestLogs(t *testing.T) {
	d := &fakeDocker{
		inspect: "[]",
		stdout:  "2026-09-28T10:00:01Z first\n2026-09-28T10:00:03Z \x1b[31mthird\x1b[0m\tred\n",
		stderr:  "2026-09-28T10:00:02Z second\x07\n",
	}
	h, _ := link(t, d, time.Now)
	lines, err := h.Logs(context.Background(), "asterisk", 0)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, l := range lines {
		texts = append(texts, l.Text)
	}
	if want := []string{"first", "second", "third red"}; !slices.Equal(texts, want) {
		t.Errorf("lines = %q, want %q (merged in time order, escapes and control characters gone)", texts, want)
	}
	cmd := d.commands()[len(d.commands())-1]
	if want := []string{"docker", "logs", "--tail", "200", "--timestamps", "linx-asterisk"}; !slices.Equal(cmd, want) {
		t.Errorf("command = %q, want %q", cmd, want)
	}
	if _, err := h.Logs(context.Background(), "postgres", 10_000); err != nil {
		t.Fatal(err)
	}
	if cmd := d.commands()[len(d.commands())-1]; cmd[3] != "500" {
		t.Errorf("--tail %s, want at most 500", cmd[3])
	}
}

func TestRestart(t *testing.T) {
	d := &fakeDocker{inspect: "[]"}
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	h, _ := link(t, d, clock)
	ctx := context.Background()

	got, err := h.Restart(ctx, "asterisk")
	if err != nil || got != "restarted" {
		t.Fatalf("Restart = %q, %v", got, err)
	}
	cmd := d.commands()[len(d.commands())-1]
	if want := []string{"docker", "restart", "--time", "20", "linx-asterisk"}; !slices.Equal(cmd, want) {
		t.Errorf("command = %q, want %q", cmd, want)
	}
	if _, err := h.Restart(ctx, "asterisk"); !errors.Is(err, ErrTooSoon) {
		t.Errorf("second restart at once: %v, want ErrTooSoon", err)
	}
	mu.Lock()
	now = now.Add(31 * time.Second)
	mu.Unlock()
	if _, err := h.Restart(ctx, "asterisk"); err != nil {
		t.Errorf("restart after the gap: %v", err)
	}
	if _, err := h.Restart(ctx, "postgres"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("postgres: %v, want ErrNotAllowed", err)
	}
	if _, err := h.Restart(ctx, "portainer"); !errors.Is(err, ErrUnknownService) {
		t.Errorf("portainer: %v, want ErrUnknownService", err)
	}
	d.restart = errors.New("exit status 1")
	if _, err := h.Restart(ctx, "certd"); err == nil || !strings.Contains(err.Error(), "couldn't restart") {
		t.Errorf("failed docker restart: %v", err)
	}
}

// The agent checks the list itself, whatever the control plane sends: a
// control plane someone broke into can't name another container.
func TestAgentRefusesWhatIsntListed(t *testing.T) {
	d := &fakeDocker{inspect: "[]"}
	a := &Agent{Exec: d.exec}
	for _, m := range []Message{
		{Type: TypeRequest, ID: "1", Op: OpRestart, Service: "linx-portainer"},
		{Type: TypeRequest, ID: "2", Op: OpRestart, Service: "postgres"},
		{Type: TypeRequest, ID: "3", Op: OpRestart, Service: "step-ca"},
		{Type: TypeRequest, ID: "4", Op: OpLogs, Service: "../../etc"},
		{Type: TypeRequest, ID: "5", Op: "exec", Service: "asterisk"},
	} {
		var got Message
		a.handle(context.Background(), m, func(r Message) error { got = r; return nil })
		if got.OK || got.ID != m.ID {
			t.Errorf("%+v answered %+v", m, got)
		}
	}
	if cmds := d.commands(); len(cmds) != 0 {
		t.Errorf("ran %q", cmds)
	}
}

// Restarting the control plane ends the link, so the agent answers first.
func TestRestartControlPlaneAnswersFirst(t *testing.T) {
	d := &fakeDocker{inspect: "[]", restarts: make(chan string, 1)}
	a := &Agent{Exec: d.exec}
	var replies []Message
	a.restart(context.Background(), Services[0], Message{Type: TypeReply, ID: "x"}, func(r Message) error {
		if len(d.restarts) != 0 {
			t.Error("replied after restarting")
		}
		replies = append(replies, r)
		return nil
	})
	if len(replies) != 1 || !replies[0].OK || replies[0].Result != "restarting" {
		t.Errorf("replies = %+v", replies)
	}
	if got := <-d.restarts; got != "linx-control-plane" {
		t.Errorf("restarted %q", got)
	}
}

func TestBusy(t *testing.T) {
	d := &fakeDocker{inspect: "[]", block: make(chan struct{})}
	h, _ := link(t, d, time.Now)
	var wg sync.WaitGroup
	for range maxBusy {
		wg.Go(func() { _, _ = h.Logs(context.Background(), "asterisk", 5) })
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		n := 0
		for _, c := range d.commands() {
			if c[1] == "logs" {
				n++
			}
		}
		if n == maxBusy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("requests didn't start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := h.Logs(context.Background(), "asterisk", 5); !errors.Is(err, ErrBusy) {
		t.Errorf("one more: %v, want ErrBusy", err)
	}
	close(d.block)
	wg.Wait()
}

func TestNotConnected(t *testing.T) {
	h := &Hub{}
	if _, err := h.Logs(context.Background(), "asterisk", 5); !errors.Is(err, ErrNotConnected) {
		t.Errorf("Logs: %v", err)
	}
	if h.Snapshot().Connected {
		t.Error("connected with no agent")
	}
}

// The agent going away mid-request answers at once, not after the timeout.
func TestDisconnectEndsRequests(t *testing.T) {
	h := &Hub{}
	hc, ac := net.Pipe()
	go h.handle(hc)
	for !h.Snapshot().Connected {
		time.Sleep(time.Millisecond)
	}
	go func() {
		buf := make([]byte, 4096)
		_, _ = ac.Read(buf) // the request
		ac.Close()
	}()
	start := time.Now()
	if _, err := h.Logs(context.Background(), "asterisk", 5); !errors.Is(err, ErrNotConnected) {
		t.Errorf("Logs: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("waited for the timeout")
	}
	for h.Snapshot().Connected {
		time.Sleep(time.Millisecond)
	}
}

func TestCleanLine(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                     "plain",
		"\x1b[1;32mgreen\x1b[0m":    "green",
		"a\tb":                      "a b",
		"bad \xff byte":             "bad � byte",
		"bell\x07 and \x1b]0;t\x07": "bell and ",
		"trailing   ":               "trailing",
	} {
		if got := CleanLine(in); got != strings.TrimRight(want, " ") {
			t.Errorf("CleanLine(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("x", 3000)
	if got := CleanLine(long); len([]rune(got)) != maxLineRunes+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("long line kept %d runes", len([]rune(got)))
	}
}

func TestSummary(t *testing.T) {
	for _, tc := range []struct {
		c    Container
		want string
	}{
		{Container{State: "running"}, StateRunning},
		{Container{State: "running", Health: "healthy"}, StateRunning},
		{Container{State: "running", Health: "starting"}, StateStarting},
		{Container{State: "running", Health: "unhealthy"}, StateUnhealthy},
		{Container{State: "restarting"}, StateRestarting},
		{Container{State: "exited"}, StateStopped},
		{Container{State: "dead"}, StateStopped},
		{Container{State: "missing"}, StateMissing},
	} {
		if got := tc.c.Summary(); got != tc.want {
			t.Errorf("%+v = %q, want %q", tc.c, got, tc.want)
		}
	}
}
