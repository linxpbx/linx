package helpanswers_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/help"
	"linxpbx.com/linx/internal/helpanswers"
	"linxpbx.com/linx/internal/helpanswers/helpanswerstest"
	"linxpbx.com/linx/internal/safehttp"
)

func library(t *testing.T) *help.Library {
	t.Helper()
	guide := func(title, audience, section, body string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("---\ntitle: " + title + "\naudience: " + audience + "\nsection: " + section +
			"\nkeywords: []\nscreens: []\n---\n# " + title + "\n\n" + body + "\n")}
	}
	lib, err := help.Open(fstest.MapFS{
		"desk-phones.md": guide("Desk phones", "everyone", "everyday", "## Adding one\n\nPress **Add a phone** and scan the code on the desk phone."),
		"phone-lines.md": guide("Phone lines", "admin", "admin", "## Adding a line\n\nOnly admins read quokka: paste the provider's details."),
	})
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

type env struct {
	svc    *helpanswers.Service
	store  *helpanswerstest.Store
	sealer *dbsecret.Sealer
	tenant uuid.UUID
	user   uuid.UUID
	now    time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	e := &env{store: helpanswerstest.New(), sealer: dbsecret.NewSealer(key), tenant: uuid.New(), user: uuid.New(),
		now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	e.svc = &helpanswers.Service{Store: e.store, Sealer: e.sealer, Help: library(t), Now: func() time.Time { return e.now },
		Policy:   safehttp.Policy{},
		Resolver: resolver{"ollama.home.arpa": {netip.MustParseAddr("192.168.1.20")}, "api.example.com": {netip.MustParseAddr("93.184.215.14")}}}
	return e
}

type resolver map[string][]netip.Addr

func (r resolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := r[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

// provider serves one provider's streaming API and records what it got.
type provider struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
	auth   []string
}

func (p *provider) last() (body, authz string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bodies[len(p.bodies)-1], p.auth[len(p.auth)-1]
}

// newProvider answers every request with pieces, in kind's stream format.
func newProvider(t *testing.T, kind string, status int, pieces ...string) *provider {
	t.Helper()
	p := &provider{}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, string(b))
		a := r.Header.Get("Authorization")
		if a == "" {
			a = r.Header.Get("X-Api-Key")
		}
		p.auth = append(p.auth, a)
		p.mu.Unlock()
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		switch kind {
		case helpanswers.ProviderAnthropic:
			if r.URL.Path != "/v1/messages" || r.Header.Get("Anthropic-Version") != "2023-06-01" {
				t.Errorf("anthropic path %s, version %q", r.URL.Path, r.Header.Get("Anthropic-Version"))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-4-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			for _, piece := range pieces {
				d, _ := json.Marshal(piece)
				fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", d)
			}
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
			fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		case helpanswers.ProviderOpenAI:
			if r.URL.Path != "/v1/chat/completions" {
				t.Errorf("openai path %s", r.URL.Path)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, piece := range pieces {
				d, _ := json.Marshal(piece)
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":null}]}\n\n", d)
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case helpanswers.ProviderOllama:
			if r.URL.Path != "/api/chat" {
				t.Errorf("ollama path %s", r.URL.Path)
			}
			for _, piece := range pieces {
				d, _ := json.Marshal(piece)
				fmt.Fprintf(w, "{\"message\":{\"role\":\"assistant\",\"content\":%s},\"done\":false}\n", d)
			}
			fmt.Fprint(w, "{\"message\":{\"role\":\"assistant\",\"content\":\"\"},\"done\":true,\"done_reason\":\"stop\"}\n")
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (e *env) turnOn(t *testing.T, p *provider, kind, key string) {
	t.Helper()
	e.svc.Client = p.Client()
	c := helpanswers.Defaults(e.tenant)
	c.Enabled, c.Provider = true, kind
	switch kind {
	case helpanswers.ProviderAnthropic:
		t.Cleanup(helpanswers.SetAnthropicURL(p.URL + "/"))
	case helpanswers.ProviderOpenAI:
		c.BaseURL = p.URL + "/v1"
	case helpanswers.ProviderOllama:
		c.BaseURL = p.URL
	}
	if key != "" {
		enc, err := e.sealer.Seal(helpanswers.SealID(e.tenant), []byte(key))
		if err != nil {
			t.Fatal(err)
		}
		c.APIKeyEnc = enc
	}
	e.store.Put(c)
}

func (e *env) ask(role, question string) (string, helpanswers.Answer, error) {
	var b strings.Builder
	a, err := e.svc.Ask(context.Background(), e.tenant, helpanswers.Question{Text: question, Role: role, UserID: e.user},
		func(s string) error { b.WriteString(s); return nil })
	return b.String(), a, err
}

func TestEveryProviderStreamsAnAnswerFromTheGuides(t *testing.T) {
	answer := []string{"1. Open **Desk phones**.\n2. Press Add a phone", ".\n", "Gui", "des: [desk-phones], [phone-lines], [nope]"}
	for _, tc := range []struct{ kind, key, wantAuth string }{
		{helpanswers.ProviderAnthropic, "sk-ant-test", "sk-ant-test"},
		{helpanswers.ProviderOpenAI, "sk-open", "Bearer sk-open"},
		{helpanswers.ProviderOllama, "", ""},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			e := newEnv(t)
			p := newProvider(t, tc.kind, http.StatusOK, answer...)
			e.turnOn(t, p, tc.kind, tc.key)
			text, a, err := e.ask(auth.RoleUser, "how do I add a desk phone?")
			if err != nil {
				t.Fatal(err)
			}
			if want := "1. Open **Desk phones**.\n2. Press Add a phone.\n"; text != want {
				t.Errorf("answer %q, want %q (the guides line held back)", text, want)
			}
			// Only guides this person may read, among those sent.
			if len(a.Guides) != 1 || a.Guides[0].Name != "desk-phones" || a.Guides[0].Title != "Desk phones" {
				t.Errorf("guides %+v", a.Guides)
			}
			body, authz := p.last()
			if authz != tc.wantAuth {
				t.Errorf("authorization %q, want %q", authz, tc.wantAuth)
			}
			if !strings.Contains(body, "scan the code on the desk phone") || !strings.Contains(body, "how do I add a desk phone?") {
				t.Errorf("the request lacks the section or the question: %s", body)
			}
			if strings.Contains(body, "quokka") {
				t.Errorf("an admin guide went out for a person: %s", body)
			}
		})
	}
}

func TestAnAdminsAnswerMayUseAdminGuides(t *testing.T) {
	e := newEnv(t)
	p := newProvider(t, helpanswers.ProviderOllama, http.StatusOK, "Paste the details.\nGuides: [phone-lines]")
	e.turnOn(t, p, helpanswers.ProviderOllama, "")
	_, a, err := e.ask(auth.RoleAdmin, "quokka")
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := p.last(); !strings.Contains(body, "quokka") {
		t.Errorf("the admin guide wasn't sent: %s", body)
	}
	if len(a.Guides) != 1 || a.Guides[0].Name != "phone-lines" {
		t.Errorf("guides %+v", a.Guides)
	}
}

func TestNothingFoundSendsTheGuideTitles(t *testing.T) {
	e := newEnv(t)
	p := newProvider(t, helpanswers.ProviderOllama, http.StatusOK, "The guides don't cover that.\nGuides: none")
	e.turnOn(t, p, helpanswers.ProviderOllama, "")
	text, a, err := e.ask(auth.RoleUser, "xyzzy plugh")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := p.last()
	if !strings.Contains(body, "[desk-phones] Desk phones") || strings.Contains(body, "Phone lines") {
		t.Errorf("want only the titles this person may read: %s", body)
	}
	if text != "The guides don't cover that.\n" || len(a.Guides) != 0 {
		t.Errorf("%q %+v", text, a.Guides)
	}
}

func TestAnswersNeedToBeOnAndASession(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.ask(auth.RoleUser, "desk phone"); !isCode(err, "answers_off") {
		t.Errorf("off: %v", err)
	}
	p := newProvider(t, helpanswers.ProviderOllama, http.StatusOK, "x")
	e.turnOn(t, p, helpanswers.ProviderOllama, "")
	if _, _, err := e.ask("", "desk phone"); !isCode(err, "sign_in_required") {
		t.Errorf("no session: %v", err)
	}
	if _, _, err := e.ask(auth.RoleUser, strings.Repeat("a", 501)); !isCode(err, "question_too_long") {
		t.Errorf("too long: %v", err)
	}
	if _, _, err := e.ask(auth.RoleUser, "  "); !isCode(err, "question_empty") {
		t.Errorf("empty: %v", err)
	}
	if n := len(p.bodies); n != 0 {
		t.Errorf("%d requests went out for refused questions", n)
	}
}

func isCode(err error, code string) bool {
	var ae *apihttp.Error
	return errors.As(err, &ae) && ae.Code == code
}

func TestLimits(t *testing.T) {
	e := newEnv(t)
	p := newProvider(t, helpanswers.ProviderOllama, http.StatusOK, "ok")
	e.turnOn(t, p, helpanswers.ProviderOllama, "")
	for i := range helpanswers.PersonPerMinute {
		if _, _, err := e.ask(auth.RoleUser, "desk phone"); err != nil {
			t.Fatalf("question %d: %v", i+1, err)
		}
	}
	if _, _, err := e.ask(auth.RoleUser, "desk phone"); !isCode(err, "rate_limited") {
		t.Errorf("11th in a minute: %v", err)
	}

	// The daily limits, a person's and the server's.
	c, _ := e.store.HelpAnswers(context.Background(), e.tenant)
	c.PersonDailyLimit, c.ServerDailyLimit = 12, 13
	e.store.Put(c)
	e.now = e.now.Add(time.Hour)
	for range 2 {
		if _, _, err := e.ask(auth.RoleUser, "desk phone"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.ask(auth.RoleUser, "desk phone"); !isCode(err, "daily_limit") {
		t.Errorf("person's daily limit: %v", err)
	}
	e.user = uuid.New()
	if _, _, err := e.ask(auth.RoleUser, "desk phone"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.ask(auth.RoleUser, "desk phone"); !isCode(err, "server_daily_limit") {
		t.Errorf("server's daily limit: %v", err)
	}
}

func TestProviderFailuresInPlainWords(t *testing.T) {
	e := newEnv(t)
	p := newProvider(t, helpanswers.ProviderAnthropic, http.StatusUnauthorized)
	e.turnOn(t, p, helpanswers.ProviderAnthropic, "sk-wrong")
	_, _, err := e.ask(auth.RoleUser, "desk phone")
	var f *helpanswers.Failure
	if !errors.As(err, &f) || !strings.Contains(f.Detail, "didn't accept the API key") {
		t.Errorf("%v", err)
	}
	if len(p.bodies) != 1 {
		t.Errorf("tried %d times, want once", len(p.bodies))
	}
}

// confirmed is a system admin's session, confirmed now.
func (e *env) confirmed() context.Context {
	now := e.now
	sess := auth.UserSession{ID: uuid.New(), TenantID: e.tenant, UserID: e.user, Role: auth.RoleSystemAdmin, MFAVerified: true, ConfirmedAt: &now}
	return auth.WithSession(auth.WithPrincipal(context.Background(), sess.Principal()), sess)
}

func ptr[T any](v T) *T { return &v }

func TestUpdate(t *testing.T) {
	e := newEnv(t)
	ctx := e.confirmed()

	// Not confirmed lately: refused.
	old := e.now.Add(-time.Hour)
	sess := auth.UserSession{ID: uuid.New(), TenantID: e.tenant, UserID: e.user, Role: auth.RoleSystemAdmin, MFAVerified: true, ConfirmedAt: &old}
	stale := auth.WithSession(auth.WithPrincipal(context.Background(), sess.Principal()), sess)
	if _, err := e.svc.Update(stale, helpanswers.Patch{Enabled: ptr(true)}, ""); err == nil {
		t.Error("an unconfirmed session changed it")
	}

	// Anthropic can't be turned on without a key.
	if _, err := e.svc.Update(ctx, helpanswers.Patch{Enabled: ptr(true)}, ""); !isCode(err, "api_key_required") {
		t.Errorf("on without a key: %v", err)
	}
	c, err := e.svc.Update(ctx, helpanswers.Patch{Enabled: ptr(true), APIKey: ptr("sk-ant-secret")}, `"0"`)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.Model != helpanswers.DefaultModel || c.BaseURL != "" || c.Version != 1 {
		t.Errorf("%+v", c)
	}
	if b, err := e.sealer.Open(helpanswers.SealID(e.tenant), c.APIKeyEnc); err != nil || string(b) != "sk-ant-secret" {
		t.Errorf("key sealed as %q, %v", b, err)
	}

	// A stale ETag is refused.
	if _, err := e.svc.Update(ctx, helpanswers.Patch{Model: ptr("claude-sonnet-5-5")}, `"0"`); !isCode(err, "etag_mismatch") {
		t.Errorf("stale etag: %v", err)
	}
	// A new provider or address needs the key again.
	if _, err := e.svc.Update(ctx, helpanswers.Patch{Provider: ptr("openai"), BaseURL: ptr("https://api.example.com/v1")}, ""); !isCode(err, "api_key_required") {
		t.Errorf("moved without the key: %v", err)
	}
	// Private addresses need the allowlist; plain http never works.
	if _, err := e.svc.Update(ctx, helpanswers.Patch{Provider: ptr("ollama"), BaseURL: ptr("https://ollama.home.arpa"), APIKey: ptr("")}, ""); !isCode(err, "base_url_blocked") {
		t.Errorf("private address: %v", err)
	}
	if _, err := e.svc.Update(ctx, helpanswers.Patch{Provider: ptr("ollama"), BaseURL: ptr("http://api.example.com"), APIKey: ptr("")}, ""); !isCode(err, "base_url_invalid") {
		t.Errorf("plain http: %v", err)
	}
	c, err = e.svc.Update(ctx, helpanswers.Patch{Provider: ptr("openai"), BaseURL: ptr("https://api.example.com/v1"), APIKey: ptr("sk-open"), Model: ptr("gpt-x")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider != "openai" || c.BaseURL != "https://api.example.com/v1" || helpanswers.By(c) != "api.example.com" {
		t.Errorf("%+v", c)
	}
	if _, err := e.svc.Update(ctx, helpanswers.Patch{Model: ptr("two words")}, ""); !isCode(err, "model_invalid") {
		t.Errorf("model: %v", err)
	}
	if _, err := e.svc.Update(ctx, helpanswers.Patch{ServerDailyLimit: ptr(0)}, ""); !isCode(err, "limit_invalid") {
		t.Errorf("limit: %v", err)
	}

	// Audited, never with the key.
	if len(e.store.Audit) != 2 {
		t.Fatalf("%d audit entries", len(e.store.Audit))
	}
	for _, a := range e.store.Audit {
		b, _ := json.Marshal(a.Detail)
		if a.Action != "help_answers.update" || strings.Contains(string(b), "sk-") {
			t.Errorf("audit %s %s", a.Action, b)
		}
	}
}
