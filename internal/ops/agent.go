package ops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Exec runs a command without a shell and returns what it wrote to
// standard output and standard error.
type Exec func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

// Agent is linx-ops-agent's side of the link: it runs on the host, as
// root (docker needs it), and does only what Services allows.
type Agent struct {
	Exec Exec
	// Dial opens the link: in real use, docker exec -i into the control
	// plane's ops-bridge (cmd/linx-ops-agent).
	Dial func(ctx context.Context) (io.ReadWriteCloser, error)
	Now  func() time.Time
	Log  *slog.Logger
	// RestartGap is the least time between two restarts of one service
	// (default 30 s): a script, or a control plane someone broke into,
	// can't keep a service restarting.
	RestartGap time.Duration
	// Retry is how long to wait before opening the link again after it
	// ends (default 5 s): the control plane restarting, say.
	Retry time.Duration

	mu          sync.Mutex
	lastRestart map[string]time.Time
	busy        int
}

// maxBusy is how many requests the agent works on at once; more are
// refused, so the control plane can never make it start a pile of docker
// commands.
const maxBusy = 4

// dockerRestartSeconds is how long docker restart lets a service stop by
// itself before it's killed: Asterisk hangs up calls cleanly well within it.
const dockerRestartSeconds = "20"

func (a *Agent) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

func (a *Agent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Run keeps the link open until ctx ends, opening it again whenever it
// drops.
func (a *Agent) Run(ctx context.Context) {
	retry := a.Retry
	if retry <= 0 {
		retry = 5 * time.Second
	}
	for ctx.Err() == nil {
		conn, err := a.Dial(ctx)
		if err != nil {
			a.log().Warn("can't reach the control plane yet", "err", err)
		} else {
			a.log().Info("connected to the control plane")
			if err := a.Serve(ctx, conn); err != nil && ctx.Err() == nil {
				a.log().Warn("link to the control plane ended", "err", err)
			}
			conn.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
}

// Serve answers one link's requests and reports every container's state
// on it until the link ends.
func (a *Agent) Serve(ctx context.Context, rw io.ReadWriter) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A failed write (or ctx ending) must also end the read below.
	if c, ok := rw.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { c.Close() })
		defer stop()
	}
	var wmu sync.Mutex
	enc := json.NewEncoder(rw)
	send := func(m Message) error {
		wmu.Lock()
		defer wmu.Unlock()
		return enc.Encode(m)
	}

	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(StatusInterval)
		defer t.Stop()
		for {
			if err := send(a.status(ctx)); err != nil {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	sc := bufio.NewScanner(rw)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil || m.Type != TypeRequest {
			continue
		}
		if m.ID == "" || len(m.ID) > 64 {
			continue
		}
		if !a.take() {
			_ = send(Message{Type: TypeReply, ID: m.ID, Code: CodeBusy, Error: "the server helper is busy; try again in a moment"})
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer a.release()
			a.handle(ctx, m, send)
		}()
	}
	cancel()
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

func (a *Agent) take() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy >= maxBusy {
		return false
	}
	a.busy++
	return true
}

func (a *Agent) release() {
	a.mu.Lock()
	a.busy--
	a.mu.Unlock()
}

func (a *Agent) handle(ctx context.Context, m Message, send func(Message) error) {
	reply := Message{Type: TypeReply, ID: m.ID}
	fail := func(code, msg string) {
		reply.Code, reply.Error = code, msg
		_ = send(reply)
	}
	if m.Op == OpStatus {
		_ = send(a.status(ctx))
		reply.OK = true
		_ = send(reply)
		return
	}
	svc, ok := Lookup(m.Service)
	if !ok {
		fail(CodeUnknownService, "not a Linx service")
		return
	}
	switch m.Op {
	case OpLogs:
		lines, err := a.logs(ctx, svc, ClampLines(m.Lines))
		if err != nil {
			fail(CodeFailed, err.Error())
			return
		}
		reply.OK, reply.Log = true, lines
		_ = send(reply)
	case OpRestart:
		a.restart(ctx, svc, reply, send)
	default:
		fail(CodeBadRequest, "unknown request")
	}
}

// inspected is the part of docker container inspect's output the status
// needs.
type inspected struct {
	Name         string `json:"Name"`
	RestartCount int    `json:"RestartCount"`
	State        struct {
		Status    string    `json:"Status"`
		StartedAt time.Time `json:"StartedAt"`
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

// status inspects every container in Services at once. A missing one
// makes docker exit non-zero but still print the others, so the exit
// status is ignored when the output parses.
func (a *Agent) status(ctx context.Context) Message {
	args := []string{"container", "inspect"}
	for _, s := range Services {
		args = append(args, s.Container)
	}
	out, stderr, err := a.Exec(ctx, "docker", args...)
	var found []inspected
	if jerr := json.Unmarshal(out, &found); jerr != nil {
		a.log().Warn("docker container inspect", "err", errors.Join(err, jerr), "stderr", strings.TrimSpace(string(stderr)))
		return Message{Type: TypeStatus, CheckedAt: a.now().UTC()}
	}
	msg := Message{Type: TypeStatus, CheckedAt: a.now().UTC(), Containers: make([]Container, 0, len(Services))}
	for _, s := range Services {
		c := Container{Service: s.Name, State: StateMissing}
		if i := slices.IndexFunc(found, func(f inspected) bool { return strings.TrimPrefix(f.Name, "/") == s.Container }); i >= 0 {
			f := found[i]
			c.State, c.Restarts = f.State.Status, f.RestartCount
			if f.State.Status == "running" || f.State.Status == "restarting" {
				c.StartedAt = f.State.StartedAt.UTC()
			}
			if f.State.Health != nil {
				c.Health = f.State.Health.Status
			}
		}
		msg.Containers = append(msg.Containers, c)
	}
	return msg
}

// logs reads a service's last lines, both of its output streams merged in
// time order (docker keeps each line's time).
func (a *Agent) logs(ctx context.Context, svc Service, n int) ([]LogLine, error) {
	out, stderr, err := a.Exec(ctx, "docker", "logs", "--tail", fmt.Sprint(n), "--timestamps", svc.Container)
	if err != nil {
		if msg := strings.TrimSpace(string(stderr)); strings.Contains(msg, "No such container") {
			return nil, errors.New("the service isn't there")
		}
		return nil, errors.New("docker couldn't read the log")
	}
	lines := append(parseLogLines(out), parseLogLines(stderr)...)
	slices.SortStableFunc(lines, func(x, y LogLine) int { return x.Time.Compare(y.Time) })
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func parseLogLines(b []byte) []LogLine {
	var out []LogLine
	for _, raw := range bytes.Split(b, []byte("\n")) {
		s := strings.TrimSuffix(string(raw), "\r")
		if s == "" {
			continue
		}
		var l LogLine
		if ts, rest, ok := strings.Cut(s, " "); ok {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				l.Time, s = t.UTC(), rest
			}
		}
		l.Text = CleanLine(s)
		out = append(out, l)
	}
	return out
}

func (a *Agent) restart(ctx context.Context, svc Service, reply Message, send func(Message) error) {
	if !svc.Restart {
		reply.Code, reply.Error = CodeNotAllowed, "this service can't be restarted from the browser"
		_ = send(reply)
		return
	}
	gap := a.RestartGap
	if gap <= 0 {
		gap = 30 * time.Second
	}
	a.mu.Lock()
	if last, ok := a.lastRestart[svc.Name]; ok && a.now().Sub(last) < gap {
		a.mu.Unlock()
		reply.Code, reply.Error = CodeTooSoon, "it was restarted moments ago"
		_ = send(reply)
		return
	}
	if a.lastRestart == nil {
		a.lastRestart = map[string]time.Time{}
	}
	a.lastRestart[svc.Name] = a.now()
	a.mu.Unlock()

	a.log().Info("restarting a service, as an admin asked", "service", svc.Name)
	// The control plane is the other end of this link: say yes first.
	self := svc.Name == "control-plane"
	if self {
		reply.OK, reply.Result = true, "restarting"
		_ = send(reply)
	}
	// Not ctx: the link ends when the control plane restarts, and the
	// restart must still finish.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	_, stderr, err := a.Exec(rctx, "docker", "restart", "--time", dockerRestartSeconds, svc.Container)
	if err != nil {
		a.log().Error("restarting a service", "service", svc.Name, "err", err, "stderr", strings.TrimSpace(string(stderr)))
		if !self {
			reply.Code, reply.Error = CodeFailed, "docker couldn't restart it"
			_ = send(reply)
		}
		return
	}
	if !self {
		reply.OK, reply.Result = true, "restarted"
		_ = send(reply)
	}
}
