package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/safehttp"
	"linxpbx.com/linx/internal/siprelay"
	"linxpbx.com/linx/internal/turn"
	"linxpbx.com/linx/internal/webhook"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

type testEnv struct {
	t        *testing.T
	srv      *httptest.Server
	store    *fakeStore
	authn    *auth.Authenticator
	tokens   *auth.Tokens
	webhooks *webhook.Service
	whStore  *fakeWebhookStore
	alerts   *alert.Service
	alStore  *fakeAlertStore
	pbx      *pbx.Service
	pbxStore *fakePbxStore
	calls    *fakeCalls
	accounts *auth.Accounts
	relay    *siprelay.Relay
	// asterisk is where the /sip relay connects (a websocket URL); tests
	// that use the relay set it.
	asterisk string
}

// testResolver answers the host names the webhook tests use, so no test
// depends on real DNS.
type testResolver map[string][]netip.Addr

func (r testResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return r[host], nil
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewTokens(key)
	if err != nil {
		t.Fatal(err)
	}
	ips, _ := auth.NewClientIPResolver("")
	st := newFakeStore()
	authn := auth.NewAuthenticator(st, tokens, ips, log)

	var encKey [32]byte
	if _, err := rand.Read(encKey[:]); err != nil {
		t.Fatal(err)
	}
	wh := newFakeWebhookStore()
	resolver := testResolver{
		"hooks.example.com": {netip.MustParseAddr("93.184.215.14")},
		"nas.home.arpa":     {netip.MustParseAddr("192.168.1.10")},
	}
	policy := safehttp.Policy{Allowlist: webhook.Allowlist(wh)}
	sender := &webhook.Sender{
		Client: safehttp.NewClient(policy, safehttp.Options{Resolver: resolver}),
		Sealer: dbsecret.NewSealer(encKey),
		Now:    time.Now,
	}
	webhooks := &webhook.Service{Store: wh, Sealer: sender.Sealer, Sender: sender, Policy: policy, Resolver: resolver, Now: time.Now}

	al := newFakeAlertStore()
	alertSender := &alert.Sender{
		Client: safehttp.NewClient(policy, safehttp.Options{Resolver: resolver}),
		Sealer: dbsecret.NewSealer(encKey),
		Now:    time.Now,
	}
	alerts := &alert.Service{Store: al, Sealer: alertSender.Sealer, Sender: alertSender, Policy: policy, Resolver: resolver, Now: time.Now}

	pb := newFakePbxStore()
	pbxSvc := &pbx.Service{Store: pb, Now: time.Now, Domain: "linx.example.com"}

	calls := &fakeCalls{connected: true}
	authn.Sessions = st
	accounts := &auth.Accounts{Store: st, Sealer: sender.Sealer, Alerts: nil, Failures: authn.Failures, Now: time.Now}
	turnIssuer := &turn.Issuer{Secret: []byte("test-turn-secret"), URLs: turn.DefaultURLs("linx.example.com"), Now: time.Now}
	handler, err := newAPIHandler(log, st, authn, webhooks, alerts, pbxSvc, calls, accounts, turnIssuer)
	if err != nil {
		t.Fatalf("newAPIHandler: %v", err)
	}
	env := &testEnv{t: t, store: st, authn: authn, tokens: tokens, webhooks: webhooks, whStore: wh, alerts: alerts, alStore: al,
		pbx: pbxSvc, pbxStore: pb, calls: calls, accounts: accounts}
	sst := testSIPStore{fakeStore: st, fakePbxStore: pb}
	pb.sessionLive = st.sessionLive
	env.relay = &siprelay.Relay{
		Dial: func(ctx context.Context) (*websocket.Conn, error) {
			c, _, err := websocket.Dial(ctx, env.asterisk, &websocket.DialOptions{Subprotocols: []string{siprelay.Subprotocol}})
			return c, err
		},
		Check: func(ctx context.Context, line siprelay.Line) error { return checkLine(ctx, sst, line, time.Now()) },
		Log:   log,
	}
	accounts.SessionsEnded = func(ctx context.Context, user uuid.UUID, session *uuid.UUID) {
		if session != nil {
			env.relay.CloseSession(*session)
		} else {
			env.relay.CloseUser(user)
		}
		revokeDeadWebDevices(ctx, sst, env.relay, log)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", handler)
	mux.Handle(auth.TokenPath, authn.TokenHandler())
	registerSessionHandlers(mux, authn, accounts, st.tenant)
	mux.Handle("GET "+controlplaneapi.SIPPath, sipHandler(authn, sst, env.relay))
	env.srv = httptest.NewServer(mux)
	t.Cleanup(env.srv.Close)
	return env
}

// testSIPStore is the relay's store over the two fakes (the real one is a
// single Postgres store).
type testSIPStore struct {
	*fakeStore
	*fakePbxStore
}

// newCredential stores a key or client made by the server-side CLI and
// returns it with its one-time secret.
func (e *testEnv) newCredential(kind, role string, scopes ...string) (auth.Credential, string) {
	e.t.Helper()
	c, secret, err := auth.NewCredential(auth.SystemPrincipal(e.store.tenant),
		auth.CredentialRequest{Kind: kind, Name: "test", Role: role, Scopes: scopes}, time.Now())
	if err != nil {
		e.t.Fatalf("NewCredential: %v", err)
	}
	if err := e.store.CreateCredential(e.t.Context(), c, auth.AuditEntry{Action: kind + ".create"}); err != nil {
		e.t.Fatal(err)
	}
	return c, secret
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("invalid JSON %q: %v", r.body, err)
	}
}

func (r response) problemCode(t *testing.T) string {
	t.Helper()
	var p struct{ Code string }
	r.json(t, &p)
	return p.Code
}

func (e *testEnv) do(method, path, bearer string, body any) response {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return e.send(req)
}

func (e *testEnv) send(req *http.Request) response {
	e.t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: b}
}

func (e *testEnv) token(form url.Values, basicID, basicSecret string) response {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+auth.TokenPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basicID != "" {
		req.SetBasicAuth(url.QueryEscape(basicID), url.QueryEscape(basicSecret))
	}
	return e.send(req)
}

func TestPublicAndSkeletonEndpoints(t *testing.T) {
	e := newTestEnv(t)
	_, key := e.newCredential(auth.TypeAPIKey, auth.RoleUser, "extensions:read")

	t.Run("openapi.json needs no credentials", func(t *testing.T) {
		r := e.do(http.MethodGet, "/api/v1/openapi.json", "", nil)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d, want 200", r.status)
		}
		var doc map[string]any
		r.json(t, &doc)
		if doc["openapi"] != "3.1.0" {
			t.Fatalf("openapi field = %v, want 3.1.0", doc["openapi"])
		}
	})

	t.Run("event-types needs a credential and paginates", func(t *testing.T) {
		if r := e.do(http.MethodGet, "/api/v1/event-types", "", nil); r.status != http.StatusUnauthorized {
			t.Fatalf("anonymous status = %d, want 401", r.status)
		}
		r := e.do(http.MethodGet, "/api/v1/event-types?limit=3", key, nil)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", r.status, r.body)
		}
		var list struct {
			Items      []map[string]string `json:"items"`
			NextCursor *string             `json:"next_cursor"`
		}
		r.json(t, &list)
		if len(list.Items) != 3 || list.NextCursor == nil {
			t.Fatalf("unexpected page: %+v", list)
		}
	})

	t.Run("an unmatched path is a problem+json 404", func(t *testing.T) {
		r := e.do(http.MethodGet, "/api/v1/does-not-exist", key, nil)
		if ct := r.header.Get("Content-Type"); r.status != http.StatusNotFound || ct != "application/problem+json" {
			t.Fatalf("status %d, Content-Type %q; want 404 problem+json", r.status, ct)
		}
	})

	t.Run("an out-of-range limit is rejected before the handler runs", func(t *testing.T) {
		if r := e.do(http.MethodGet, "/api/v1/event-types?limit=99999", key, nil); r.status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", r.status)
		}
	})
}

func TestAPIKeyAuthentication(t *testing.T) {
	e := newTestEnv(t)
	cred, key := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")

	t.Run("me without a credential is 401 with a challenge", func(t *testing.T) {
		r := e.do(http.MethodGet, "/api/v1/me", "", nil)
		if r.status != http.StatusUnauthorized || r.problemCode(t) != "auth_required" {
			t.Fatalf("got %d %s, want 401 auth_required", r.status, r.body)
		}
		if r.header.Get("WWW-Authenticate") == "" {
			t.Fatal("no WWW-Authenticate header")
		}
	})

	t.Run("me with a key returns its principal and rate-limit headers", func(t *testing.T) {
		r := e.do(http.MethodGet, "/api/v1/me", key, nil)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d: %s", r.status, r.body)
		}
		var me controlplaneapi.Principal
		r.json(t, &me)
		if me.Id != cred.ID.String() || me.Type != controlplaneapi.PrincipalTypeApiKey || me.Role == nil || *me.Role != auth.RoleAdmin {
			t.Fatalf("unexpected principal %+v", me)
		}
		if slices.Contains(me.Scopes, "api_keys:write") {
			t.Fatal(`"all" included the sensitive api_keys:write scope`)
		}
		if r.header.Get("RateLimit-Limit") != "600" || r.header.Get("RateLimit-Remaining") == "" {
			t.Fatalf("rate-limit headers missing: %v", r.header)
		}
	})

	t.Run("last use is recorded", func(t *testing.T) {
		c, _ := e.store.CredentialByID(t.Context(), auth.TypeAPIKey, cred.ID)
		if c.LastUsedAt == nil || c.LastUsedIP == nil || !c.LastUsedIP.IsLoopback() {
			t.Fatalf("last use not recorded: %+v", c)
		}
	})

	bad := map[string]string{
		"wrong secret":   key[:len(key)-4] + "AAAA",
		"unknown key id": auth.APIKeyPrefix + "aaaaaaaaaaaa" + key[len(auth.APIKeyPrefix)+12:],
		"malformed":      auth.APIKeyPrefix + "short",
		"garbage token":  "not.a.jwt",
	}
	for name, k := range bad {
		t.Run(name+" is 401 auth_invalid", func(t *testing.T) {
			r := e.do(http.MethodGet, "/api/v1/me", k, nil)
			if r.status != http.StatusUnauthorized || r.problemCode(t) != "auth_invalid" {
				t.Fatalf("got %d %s", r.status, r.body)
			}
		})
	}

	t.Run("a non-bearer scheme is refused", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/me", nil)
		req.SetBasicAuth("x", key)
		if r := e.send(req); r.status != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", r.status)
		}
	})

	t.Run("failed attempts are audited", func(t *testing.T) {
		if !slices.Contains(e.store.auditActions(), "auth.failed") {
			t.Fatalf("no auth.failed audit entry: %v", e.store.auditActions())
		}
	})
}

func TestCredentialState(t *testing.T) {
	e := newTestEnv(t)

	t.Run("expired", func(t *testing.T) {
		c, key := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
		e.store.update(c.ID, func(c *auth.Credential) { c.ExpiresAt = time.Now().Add(-time.Minute) })
		r := e.do(http.MethodGet, "/api/v1/me", key, nil)
		if r.status != http.StatusUnauthorized || r.problemCode(t) != "credential_expired" {
			t.Fatalf("got %d %s", r.status, r.body)
		}
	})

	t.Run("IP allowlist", func(t *testing.T) {
		c, key := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
		e.store.update(c.ID, func(c *auth.Credential) { c.AllowedIPs = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")} })
		r := e.do(http.MethodGet, "/api/v1/me", key, nil)
		if r.status != http.StatusForbidden || r.problemCode(t) != "ip_not_allowed" {
			t.Fatalf("got %d %s", r.status, r.body)
		}
		e.store.update(c.ID, func(c *auth.Credential) { c.AllowedIPs = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")} })
		if r := e.do(http.MethodGet, "/api/v1/me", key, nil); r.status != http.StatusOK {
			t.Fatalf("allowed address got %d", r.status)
		}
	})

	t.Run("a role ceiling narrowed later still limits the key", func(t *testing.T) {
		c, key := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all", "api_keys:write")
		e.store.update(c.ID, func(c *auth.Credential) { c.Role = auth.RoleReporter })
		r := e.do(http.MethodGet, "/api/v1/api-keys", key, nil)
		if r.status != http.StatusForbidden || r.problemCode(t) != "scope_missing" {
			t.Fatalf("got %d %s", r.status, r.body)
		}
	})
}

func TestAPIKeyManagement(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all", "api_keys:write")
	_, reader := e.newCredential(auth.TypeAPIKey, auth.RoleReporter, "all")

	t.Run("a key without the scope is refused and audited", func(t *testing.T) {
		r := e.do(http.MethodGet, "/api/v1/api-keys", reader, nil)
		if r.status != http.StatusForbidden || r.problemCode(t) != "scope_missing" {
			t.Fatalf("got %d %s", r.status, r.body)
		}
		if !slices.Contains(e.store.auditActions(), "auth.scope_denied") {
			t.Fatal("scope denial not audited")
		}
	})

	var created controlplaneapi.ApiKeyCreated
	t.Run("create returns the key once, and it works", func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/api-keys", admin, map[string]any{
			"name": "CRM sync", "scopes": []string{"webhooks:read", "webhooks:write"}, "allowed_ips": []string{"127.0.0.1"},
		})
		if r.status != http.StatusCreated {
			t.Fatalf("status = %d: %s", r.status, r.body)
		}
		r.json(t, &created)
		if !strings.HasPrefix(created.Key, created.ApiKey.Prefix+"_") || created.ApiKey.Role != controlplaneapi.Role(auth.RoleAdmin) {
			t.Fatalf("unexpected response %+v", created)
		}
		if got := e.do(http.MethodGet, "/api/v1/me", created.Key, nil); got.status != http.StatusOK {
			t.Fatalf("new key: status %d", got.status)
		}
		if !slices.Contains(e.store.auditActions(), "api_key.create") || !slices.Contains(e.store.auditActions(), "api.write") {
			t.Fatalf("create not audited: %v", e.store.auditActions())
		}
	})

	t.Run("get and list never include the secret", func(t *testing.T) {
		r := e.do(http.MethodGet, "/api/v1/api-keys/"+created.ApiKey.Id.String(), admin, nil)
		if r.status != http.StatusOK || bytes.Contains(r.body, []byte(created.Key)) {
			t.Fatalf("get: %d %s", r.status, r.body)
		}
		r = e.do(http.MethodGet, "/api/v1/api-keys?limit=2", admin, nil)
		var list controlplaneapi.ApiKeyList
		r.json(t, &list)
		if r.status != http.StatusOK || len(list.Items) != 2 || list.NextCursor == nil {
			t.Fatalf("list page 1: %d %s", r.status, r.body)
		}
		r = e.do(http.MethodGet, "/api/v1/api-keys?limit=2&cursor="+*list.NextCursor, admin, nil)
		var page2 controlplaneapi.ApiKeyList
		r.json(t, &page2)
		if len(page2.Items) != 1 || page2.NextCursor != nil {
			t.Fatalf("list page 2: %s", r.body)
		}
	})

	refusals := []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{"a scope the caller lacks", map[string]any{"name": "x", "scopes": []string{"recordings:read"}}, 403, "scope_exceeds_caller"},
		{"an unknown scope", map[string]any{"name": "x", "scopes": []string{"everything"}}, 400, "scope_unknown"},
		{"a higher role", map[string]any{"name": "x", "role": "system_admin", "scopes": []string{"all"}}, 403, "role_exceeds_caller"},
		{"a scope above the role", map[string]any{"name": "x", "role": "user", "scopes": []string{"webhooks:write"}}, 400, "scope_exceeds_role"},
		{"an expiry over two years", map[string]any{"name": "x", "scopes": []string{"all"}, "expires_at": time.Now().AddDate(3, 0, 0)}, 400, "expiry_invalid"},
		{"a bad address", map[string]any{"name": "x", "scopes": []string{"all"}, "allowed_ips": []string{"example.com"}}, 400, "allowed_ips_invalid"},
		{"an unknown field", map[string]any{"name": "x", "scopes": []string{"all"}, "admin": true}, 400, "request_invalid"},
	}
	for _, tc := range refusals {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			r := e.do(http.MethodPost, "/api/v1/api-keys", admin, tc.body)
			if r.status != tc.status || r.problemCode(t) != tc.code {
				t.Fatalf("got %d %s, want %d %s", r.status, r.body, tc.status, tc.code)
			}
		})
	}

	t.Run("a key can't mint a key with more than it holds", func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/api-keys", created.Key, map[string]any{"name": "x", "scopes": []string{"all"}})
		if r.status != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (no api_keys:write)", r.status)
		}
	})

	t.Run("revoke stops the key at once", func(t *testing.T) {
		r := e.do(http.MethodDelete, "/api/v1/api-keys/"+created.ApiKey.Id.String(), admin, nil)
		if r.status != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", r.status, r.body)
		}
		r = e.do(http.MethodGet, "/api/v1/me", created.Key, nil)
		if r.status != http.StatusUnauthorized || r.problemCode(t) != "credential_revoked" {
			t.Fatalf("revoked key: %d %s", r.status, r.body)
		}
		if r := e.do(http.MethodDelete, "/api/v1/api-keys/"+created.ApiKey.Id.String(), admin, nil); r.status != http.StatusNoContent {
			t.Fatalf("second revoke: %d", r.status)
		}
	})

	t.Run("an unknown id is 404", func(t *testing.T) {
		r := e.do(http.MethodGet, "/api/v1/api-keys/0199a000-0000-7000-8000-000000000000", admin, nil)
		if r.status != http.StatusNotFound {
			t.Fatalf("status = %d", r.status)
		}
	})
}

func TestRateLimits(t *testing.T) {
	e := newTestEnv(t)
	_, key := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")

	t.Run("failed authentication is limited per address", func(t *testing.T) {
		bad := auth.APIKeyPrefix + "aaaaaaaaaaaa_" + strings.Repeat("A", 43)
		for i := 0; i < auth.FailedAuthPerMinute; i++ {
			if r := e.do(http.MethodGet, "/api/v1/me", bad, nil); r.status != http.StatusUnauthorized {
				t.Fatalf("attempt %d: status %d", i+1, r.status)
			}
		}
		r := e.do(http.MethodGet, "/api/v1/me", bad, nil)
		if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
			t.Fatalf("after %d failures: %d %v", auth.FailedAuthPerMinute, r.status, r.header)
		}
		// Even a good key is held back from that address until it cools down.
		if r := e.do(http.MethodGet, "/api/v1/me", key, nil); r.status != http.StatusTooManyRequests {
			t.Fatalf("good key during lockout: %d", r.status)
		}
		// Anonymous public requests aren't affected.
		if r := e.do(http.MethodGet, "/api/v1/openapi.json", "", nil); r.status != http.StatusOK {
			t.Fatalf("public endpoint during lockout: %d", r.status)
		}
	})

	t.Run("calls are limited per key", func(t *testing.T) {
		e := newTestEnv(t)
		_, key := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
		e.authn.Calls = auth.NewLimiters(60, 3)
		for i := 0; i < 3; i++ {
			if r := e.do(http.MethodGet, "/api/v1/me", key, nil); r.status != http.StatusOK {
				t.Fatalf("call %d: %d", i+1, r.status)
			}
		}
		r := e.do(http.MethodGet, "/api/v1/me", key, nil)
		if r.status != http.StatusTooManyRequests || r.problemCode(t) != "rate_limited" || r.header.Get("Retry-After") == "" {
			t.Fatalf("got %d %s %v", r.status, r.body, r.header)
		}
	})
}

func TestOAuthClientCredentials(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all", "oauth_clients:write")

	r := e.do(http.MethodPost, "/api/v1/oauth-clients", admin, map[string]any{
		"name": "Reporting", "scopes": []string{"webhooks:read", "alerts:read"},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("create client: %d %s", r.status, r.body)
	}
	var client controlplaneapi.OAuthClientCreated
	r.json(t, &client)
	id, secret := client.OauthClient.ClientId, client.ClientSecret

	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		Scope       string `json:"scope"`
	}
	t.Run("Basic auth gets a 15-minute bearer token", func(t *testing.T) {
		r := e.token(url.Values{"grant_type": {"client_credentials"}}, id, secret)
		if r.status != http.StatusOK || r.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("token: %d %v %s", r.status, r.header, r.body)
		}
		r.json(t, &tok)
		if tok.TokenType != "Bearer" || tok.ExpiresIn != 900 || tok.Scope != "alerts:read webhooks:read" {
			t.Fatalf("unexpected token response %+v", tok)
		}
	})

	t.Run("the token authenticates as the client", func(t *testing.T) {
		r := e.do(http.MethodGet, "/api/v1/me", tok.AccessToken, nil)
		var me controlplaneapi.Principal
		r.json(t, &me)
		if r.status != http.StatusOK || me.Type != controlplaneapi.PrincipalTypeOauthClient || me.Id != client.OauthClient.Id.String() {
			t.Fatalf("me: %d %s", r.status, r.body)
		}
	})

	t.Run("form credentials and a narrowed scope", func(t *testing.T) {
		r := e.token(url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {secret}, "scope": {"alerts:read"}}, "", "")
		var narrow struct {
			AccessToken string `json:"access_token"`
		}
		r.json(t, &narrow)
		if r.status != http.StatusOK {
			t.Fatalf("token: %d %s", r.status, r.body)
		}
		me := e.do(http.MethodGet, "/api/v1/me", narrow.AccessToken, nil)
		var p controlplaneapi.Principal
		me.json(t, &p)
		if !slices.Equal(p.Scopes, []string{"alerts:read"}) {
			t.Fatalf("scopes = %v, want [alerts:read]", p.Scopes)
		}
	})

	oauthErrors := []struct {
		name       string
		form       url.Values
		basicID    string
		basicSec   string
		status     int
		wantErrStr string
	}{
		{"wrong secret", url.Values{"grant_type": {"client_credentials"}}, id, auth.ClientSecretPrefix + strings.Repeat("A", 43), 401, "invalid_client"},
		{"no credentials", url.Values{"grant_type": {"client_credentials"}}, "", "", 401, "invalid_client"},
		{"both Basic and form", url.Values{"grant_type": {"client_credentials"}, "client_id": {id}}, id, secret, 400, "invalid_request"},
		{"another grant type", url.Values{"grant_type": {"password"}}, id, secret, 400, "unsupported_grant_type"},
		{"a scope the client lacks", url.Values{"grant_type": {"client_credentials"}, "scope": {"webhooks:write"}}, id, secret, 400, "invalid_scope"},
	}
	for _, tc := range oauthErrors {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			r := e.token(tc.form, tc.basicID, tc.basicSec)
			var oe struct{ Error string }
			r.json(t, &oe)
			if r.status != tc.status || oe.Error != tc.wantErrStr {
				t.Fatalf("got %d %s, want %d %s", r.status, r.body, tc.status, tc.wantErrStr)
			}
		})
	}

	t.Run("tampered and forged tokens are refused", func(t *testing.T) {
		parts := strings.Split(tok.AccessToken, ".")
		payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
		forgedPayload := base64.RawURLEncoding.EncodeToString(bytes.Replace(payload, []byte("alerts:read"), []byte("audit:read!"), 1))
		none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
		_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
		other, _ := auth.NewTokens(otherKey)
		otherTok, _, _ := other.Issue(client.OauthClient.Id, e.store.tenant, []string{"alerts:read"}, time.Now())
		for name, bad := range map[string]string{
			"payload changed": parts[0] + "." + forgedPayload + "." + parts[2],
			"alg none":        none + "." + parts[1] + ".",
			"other key":       otherTok,
		} {
			if r := e.do(http.MethodGet, "/api/v1/me", bad, nil); r.status != http.StatusUnauthorized {
				t.Errorf("%s: status %d, want 401", name, r.status)
			}
		}
	})

	t.Run("a revoked jti is refused", func(t *testing.T) {
		fresh := e.token(url.Values{"grant_type": {"client_credentials"}}, id, secret)
		var ft struct {
			AccessToken string `json:"access_token"`
		}
		fresh.json(t, &ft)
		claims, err := e.tokens.Verify(ft.AccessToken, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		e.store.revoked[claims.JTI] = true
		if r := e.do(http.MethodGet, "/api/v1/me", ft.AccessToken, nil); r.status != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", r.status)
		}
	})

	t.Run("revoking the client stops its tokens at once", func(t *testing.T) {
		r := e.do(http.MethodDelete, "/api/v1/oauth-clients/"+client.OauthClient.Id.String(), admin, nil)
		if r.status != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", r.status, r.body)
		}
		if r := e.do(http.MethodGet, "/api/v1/me", tok.AccessToken, nil); r.status != http.StatusUnauthorized {
			t.Fatalf("token after revoke: %d", r.status)
		}
		r = e.token(url.Values{"grant_type": {"client_credentials"}}, id, secret)
		if r.status != http.StatusUnauthorized {
			t.Fatalf("new token after revoke: %d %s", r.status, r.body)
		}
	})
}

// TestEverySecuredOperationDeclaresScopes guards against an endpoint that
// only needs "any credential" by accident: that is allowed for a short,
// reviewed list; everything else must name its scopes (docs/API.md §3).
func TestEverySecuredOperationDeclaresScopes(t *testing.T) {
	spec, err := controlplaneapi.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	// The embedded spec's operation ids are capitalised by the generator.
	// The four session endpoints authenticate a cookie themselves (or are
	// unauthenticated, signing in), never the bearer scheme (docs/WEB.md §4).
	public := []string{"GetOpenapiSpec", "OauthToken", "CreateSession", "VerifySessionMfa", "CompleteSetupLink", "DeleteSession"}
	// /me/mfa* and /me/password act on the caller's own account, whatever
	// kind of credential it is signed in with (docs/WEB.md §4), like GetMe.
	// /me/web-phone and /me/turn-credentials are the signed-in person's own
	// phone line and relay access, refused to anything but a finished
	// sign-in (docs/WEB.md §5).
	anyCredential := []string{"GetMe", "ListEventTypes", "BeginMyMfaEnrollment", "ConfirmMyMfaEnrollment", "ChangeMyPassword",
		"IssueMyWebPhone", "GetMyTurnCredentials"}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			sec := spec.Security
			if op.Security != nil {
				sec = *op.Security
			}
			switch {
			case slices.Contains(public, op.OperationID):
				if len(sec) != 0 {
					t.Errorf("%s %s should be public", method, path)
				}
			case len(sec) != 1 || len(sec[0]) != 1 || sec[0]["bearer"] == nil:
				t.Errorf("%s %s: want exactly one bearer requirement, got %v", method, path, sec)
			case len(sec[0]["bearer"]) == 0 && !slices.Contains(anyCredential, op.OperationID):
				t.Errorf("%s %s (%s) lists no scopes", method, path, op.OperationID)
			default:
				for _, s := range sec[0]["bearer"] {
					if !auth.ValidScope(s) {
						t.Errorf("%s %s uses unknown scope %q (add it to auth.Scopes)", method, path, s)
					}
				}
			}
		}
	}
}
