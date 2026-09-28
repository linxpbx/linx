package install

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeApplier struct {
	mu        sync.Mutex
	token     string
	ran       *ApplyInput
	switched  bool
	release   chan struct{}
	failFirst bool
	// failAfterSwitch: Run fails once the full stack has replaced the
	// installer's.
	failAfterSwitch bool
}

func (f *fakeApplier) Steps(context.Context, ApplyInput) ([]string, error) {
	return []string{"Firewall", "Phone system", "Start the full Linx", "First admin"}, nil
}
func (f *fakeApplier) Run(_ context.Context, in ApplyInput, report func(int, string, string), keep func(KeepItem), switching func()) error {
	f.mu.Lock()
	f.ran = &in
	fail := f.failFirst
	f.failFirst = false
	f.mu.Unlock()
	report(0, StageRunning, "")
	if fail {
		// Something to write down was made before it failed.
		keep(KeepItem{Title: "Certificate authority backup passphrase", Value: "ABCD-EFGH"})
		report(0, StageFailed, "nftables isn't installed")
		return errNotYet
	}
	report(0, StageOK, "")
	keep(KeepItem{Title: "Certificate authority backup passphrase", Value: "ABCD-EFGH"})
	<-f.release
	switching()
	f.mu.Lock()
	f.switched = true
	after := f.failAfterSwitch
	f.mu.Unlock()
	if after {
		return errNotYet
	}
	return nil
}
func (f *fakeApplier) SaveToken(_ context.Context, token string) (string, error) {
	if len(token) < 20 {
		return "That doesn't look like a DNS token.", nil
	}
	f.mu.Lock()
	f.token = token
	f.mu.Unlock()
	return "", nil
}
func (f *fakeApplier) Profiles(context.Context) ([]ProfileOption, string, string) {
	return []ProfileOption{{Name: "lite"}, {Name: "standard"}, {Name: "performance"}}, "standard", "4 processor cores"
}

// secureRig is a rig whose browser has moved to the secure page.
func secureRig(t *testing.T, ap *fakeApplier) (*rig, string) {
	t.Helper()
	fake := &fakeCert{mode: CertPort443, dns: []string{"203.0.113.5"}}
	r, cookie := certRig(t, fake)
	r.host.Apply, r.host.SwitchPause = ap, time.Millisecond
	waitFor(t, func() bool { v, _ := r.srv.Snapshot(); return v.Cert.Ready() })
	rec := r.do("POST", "/install/api/handoff", cookie, "{}", jsonFromPage...)
	var h struct{ Handoff string }
	_ = json.Unmarshal(rec.Body.Bytes(), &h)
	rec = r.secureDo("POST", "example.com", "/install/api/redeem", "", `{"handoff":"`+h.Handoff+`"}`, secureJSONHeaders...)
	for _, c := range rec.Result().Cookies() {
		if c.Name == SecureCookieName {
			waitFor(t, func() bool { v, _ := r.srv.Snapshot(); return v.Finish != nil })
			return r, c.Value
		}
	}
	t.Fatalf("redeem: %d", rec.Code)
	return nil, ""
}

var secureJSONHeaders = []string{"Content-Type", "application/json", "Origin", "https://example.com"}

func (r *rig) secureState(t *testing.T, cookie string) pageState {
	t.Helper()
	var ps pageState
	rec := r.secureDo("GET", "example.com", "/install/api/state", cookie, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &ps); err != nil {
		t.Fatalf("state: %d %s", rec.Code, rec.Body.String())
	}
	return ps
}

func TestSecurePageInstall(t *testing.T) {
	ap := &fakeApplier{release: make(chan struct{})}
	r, cookie := secureRig(t, ap)
	post := func(path, body string) int {
		return r.secureDo("POST", "example.com", path, cookie, body, secureJSONHeaders...).Code
	}
	ps := r.secureState(t, cookie)
	// A rented server where Linx takes 443: Skip is offered.
	if ps.Finish == nil || !ps.Finish.SkipAllowed || ps.Finish.Token != "" || len(ps.Finish.Profiles) != 3 ||
		ps.Finish.ProfilePick != "standard" || ps.Finish.PortainerAllowed {
		t.Fatalf("finish: %+v", ps.Finish)
	}
	if code := post("/install/api/install", "{}"); code != http.StatusConflict {
		t.Errorf("install before the token: %d", code)
	}
	if code := post("/install/api/token", `{"token":"short"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("short token: %d", code)
	}
	token := strings.Repeat("t", 40)
	if code := post("/install/api/token", `{"token":"`+token+`"}`); code != http.StatusNoContent {
		t.Fatalf("token: %d", code)
	}
	if code := post("/install/api/extras", `{"profile":"huge","portainer":false}`); code != http.StatusConflict {
		t.Errorf("unknown size: %d", code)
	}
	// Portainer is only for a server at home.
	if code := post("/install/api/extras", `{"profile":"standard","portainer":true}`); code != http.StatusConflict {
		t.Errorf("Portainer on a rented server: %d", code)
	}
	if code := post("/install/api/extras", `{"profile":"standard","portainer":false}`); code != http.StatusNoContent {
		t.Fatalf("extras: %d", code)
	}
	if code := post("/install/api/install", "{}"); code != http.StatusNoContent {
		t.Fatalf("install: %d", code)
	}
	if code := post("/install/api/install", "{}"); code != http.StatusConflict {
		t.Errorf("install twice: %d", code)
	}
	waitFor(t, func() bool { v, _ := r.srv.Snapshot(); return v.Finish != nil && len(v.Finish.Keep) == 1 })
	ps = r.secureState(t, cookie)
	f := ps.Finish
	if f.Install.State != StageRunning || f.Steps[0].State != StageOK || !strings.HasPrefix(f.SignInPath, "/setup/") || f.Keep[0].Value != "ABCD-EFGH" {
		t.Errorf("while installing: %+v", f)
	}
	ap.mu.Lock()
	ran := *ap.ran
	ap.mu.Unlock()
	if ran.SkipToken || ran.Extras.Profile != "standard" || ran.SetupToken != strings.TrimPrefix(f.SignInPath, "/setup/") || ap.token != token {
		t.Errorf("apply input: %+v", ran)
	}

	close(ap.release)
	waitFor(t, func() bool { st := r.host.State(); return st.View.Ended == EndedFinished })
	st := r.host.State()
	if !st.View.Finish.Switching || st.View.Finish.Install.State != StageOK || len(st.View.Finish.Keep) != 0 {
		t.Errorf("finished: %+v", st.View.Finish)
	}
	if b, _ := json.Marshal(st); strings.Contains(string(b), "ABCD-EFGH") {
		t.Error("the passphrase is still in the install state")
	}
	// The plain page never sees any of it.
	if rec := r.do("GET", "/install/api/state", "", ""); strings.Contains(rec.Body.String(), "setup/") {
		t.Error("plain page shows the sign-in link")
	}
}

func TestInstallFailureCanBeTriedAgain(t *testing.T) {
	ap := &fakeApplier{release: make(chan struct{}), failFirst: true}
	r, cookie := secureRig(t, ap)
	post := func(path, body string) int {
		return r.secureDo("POST", "example.com", path, cookie, body, secureJSONHeaders...).Code
	}
	if code := post("/install/api/skip-token", "{}"); code != http.StatusNoContent {
		t.Fatalf("skip: %d", code)
	}
	post("/install/api/extras", `{"profile":"","portainer":false}`)
	post("/install/api/install", "{}")
	waitFor(t, func() bool { st := r.host.State(); return st.View.Finish.Install.State == StageFailed })
	first := r.host.State().View.Finish.SignInPath
	if code := post("/install/api/install", "{}"); code != http.StatusNoContent {
		t.Fatalf("try again: %d", code)
	}
	// What the failed try made to write down is still there.
	waitFor(t, func() bool { return len(r.host.State().View.Finish.Keep) == 2 })
	close(ap.release)
	waitFor(t, func() bool { st := r.host.State(); return st.View.Ended == EndedFinished })
	ap.mu.Lock()
	defer ap.mu.Unlock()
	if !ap.ran.SkipToken || r.host.State().View.Finish.SignInPath != first {
		t.Errorf("second try: %+v, link %s vs %s", ap.ran, r.host.State().View.Finish.SignInPath, first)
	}
}

func TestInstallStoppedAfterSwitch(t *testing.T) {
	ap := &fakeApplier{release: make(chan struct{}), failAfterSwitch: true}
	close(ap.release)
	r, cookie := secureRig(t, ap)
	post := func(path, body string) int {
		return r.secureDo("POST", "example.com", path, cookie, body, secureJSONHeaders...).Code
	}
	post("/install/api/skip-token", "{}")
	post("/install/api/extras", `{"profile":"","portainer":false}`)
	if code := post("/install/api/install", "{}"); code != http.StatusNoContent {
		t.Fatalf("install: %d", code)
	}
	waitFor(t, func() bool { return r.host.State().View.Ended == EndedStopped })
	st := r.host.State()
	if !st.Switched || st.View.Finish.Install.State != StageFailed {
		t.Errorf("stopped: switched %v, %+v", st.Switched, st.View.Finish.Install)
	}
}
