package install

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSettings struct {
	mu      sync.Mutex
	view    ServerView
	ran     []ServerChange
	release chan struct{}
	fail    bool
}

func (f *fakeSettings) View(context.Context) (ServerView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view, nil
}
func (f *fakeSettings) CheckToken(_ context.Context, token string) (string, error) {
	if len(token) < 20 {
		return "That token can't see example.com at Cloudflare.", nil
	}
	return "", nil
}
func (f *fakeSettings) Steps(context.Context, ServerChange) ([]string, error) {
	return []string{"Save your settings", "Restart Linx with the new settings"}, nil
}
func (f *fakeSettings) Run(_ context.Context, c ServerChange, report func(int, string, string), keep func(KeepItem)) error {
	report(0, StageOK, "")
	report(1, StageRunning, "")
	if f.release != nil {
		<-f.release
	}
	if f.fail {
		report(1, StageFailed, "docker: no space left")
		return errors.New("Restart Linx with the new settings: no space left")
	}
	if c.Portainer {
		keep(KeepItem{Title: "Portainer password", Value: "pw"})
	}
	report(1, StageOK, "")
	f.mu.Lock()
	f.ran = append(f.ran, c)
	f.view.Profile, f.view.Portainer = c.Profile, c.Portainer
	if c.Token != "" {
		f.view.Token = "saved"
	}
	f.mu.Unlock()
	return nil
}

// settingsRig is the full control plane's relay and a settings-mode host,
// joined through a real socket, as docker exec joins them.
func settingsRig(t *testing.T, fake *fakeSettings) (*Server, *SettingsHost) {
	t.Helper()
	dir, err := os.MkdirTemp("", "linxset")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := &Server{}
	go srv.ServeBridge(ctx, ln)
	h := &SettingsHost{Apply: fake, Retry: 10 * time.Millisecond,
		Dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}
	go func() { _ = h.Run(ctx) }()
	waitFor(t, func() bool { return srv.ServerSettings() != nil })
	return srv, h
}

var homeSettings = ServerView{Where: WhereHome, FrontDoor: "pangolin", Domain: "example.com", Provider: "cloudflare", Token: "saved",
	Profile: "lite", Profiles: []ProfileOption{{Name: "lite"}, {Name: "standard"}}, ProfilePick: "lite", PortainerAllowed: true}

func TestServerSettingsChange(t *testing.T) {
	fake := &fakeSettings{view: homeSettings}
	srv, _ := settingsRig(t, fake)
	ctx := context.Background()
	v := srv.ServerSettings()
	if v.Domain != "example.com" || v.ExpiresAt.IsZero() {
		t.Fatalf("view %+v", v)
	}
	refused := func(c ServerChange, want string) {
		t.Helper()
		errs, err := srv.ChangeServerSettings(ctx, c)
		var r *Refused
		if want == "field" {
			if len(errs) != 1 || errs[0].Field != "token" {
				t.Errorf("%+v: %v %v", c, errs, err)
			}
			return
		}
		if !errors.As(err, &r) || !strings.Contains(r.Detail, want) {
			t.Errorf("%+v: %v %v", c, errs, err)
		}
	}
	refused(ServerChange{Profile: "lite"}, "Nothing to change")
	refused(ServerChange{Profile: "huge"}, "isn't one of the choices")
	refused(ServerChange{Profile: "lite", Token: "short"}, "field")

	if errs, err := srv.ChangeServerSettings(ctx, ServerChange{Profile: "standard", Portainer: true}); err != nil || errs != nil {
		t.Fatalf("change: %v %v", errs, err)
	}
	waitFor(t, func() bool { v := srv.ServerSettings(); return v != nil && v.Apply.State == StageOK })
	v = srv.ServerSettings()
	if v.Profile != "standard" || !v.Portainer || len(v.Keep) != 1 || v.Steps[1].State != StageOK {
		t.Errorf("after: %+v", v)
	}
}

func TestServerSettingsRules(t *testing.T) {
	rented := homeSettings
	rented.Where, rented.PortainerAllowed = WhereRented, false
	fake := &fakeSettings{view: rented, release: make(chan struct{}), fail: true}
	srv, _ := settingsRig(t, fake)
	ctx := context.Background()
	if _, err := srv.ChangeServerSettings(ctx, ServerChange{Profile: "lite", Portainer: true}); err == nil || !strings.Contains(err.Error(), "only offered on a server at home") {
		t.Errorf("Portainer on a rented server: %v", err)
	}
	if _, err := srv.ChangeServerSettings(ctx, ServerChange{Profile: "standard"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { v := srv.ServerSettings(); return v != nil && v.Apply.State == StageRunning })
	// One change at a time.
	if _, err := srv.ChangeServerSettings(ctx, ServerChange{Profile: "lite", Token: strings.Repeat("t", 40)}); err == nil || !strings.Contains(err.Error(), "still being made") {
		t.Errorf("second change: %v", err)
	}
	close(fake.release)
	waitFor(t, func() bool { v := srv.ServerSettings(); return v != nil && v.Apply.State == StageFailed })
	if v := srv.ServerSettings(); v.Profile != "lite" || !strings.Contains(v.Apply.Detail, "no space left") {
		t.Errorf("failed: %+v", v)
	}
}

// The page closes after its time, and nothing is relayed after that.
func TestServerSettingsExpire(t *testing.T) {
	fake := &fakeSettings{view: homeSettings}
	dir, _ := os.MkdirTemp("", "linxset")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, _ := Listen(sock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &Server{}
	go srv.ServeBridge(ctx, ln)
	h := &SettingsHost{Apply: fake, Lifetime: 200 * time.Millisecond, Retry: 10 * time.Millisecond,
		Dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}
	done := make(chan struct{})
	go func() { _ = h.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("still open")
	}
	waitFor(t, func() bool { return srv.ServerSettings() == nil })
}
