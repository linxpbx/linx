package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
)

func TestAPIKeyFormat(t *testing.T) {
	publicID, secret, key := NewAPIKey()
	if !strings.HasPrefix(key, "linx_") || len(key) != len("linx_")+12+1+43 {
		t.Fatalf("key %q has the wrong shape", key)
	}
	gotID, gotSecret, ok := ParseAPIKey(key)
	if !ok || gotID != publicID || gotSecret != secret {
		t.Fatalf("ParseAPIKey(%q) = %q, %q, %v", key, gotID, gotSecret, ok)
	}
	if !SecretMatches(HashSecret(secret), secret) || SecretMatches(HashSecret(secret), NewSecret()) {
		t.Fatal("SecretMatches is wrong")
	}
	for _, bad := range []string{
		"", "linx_", key + "x", key[:len(key)-1],
		strings.Replace(key, "linx_", "linq_", 1),
		"linx_" + strings.ToUpper(publicID) + "_" + secret,
		"linx_" + publicID + "-" + secret,
		"linx_" + publicID + "_" + strings.Repeat("!", 43),
	} {
		if _, _, ok := ParseAPIKey(bad); ok {
			t.Errorf("ParseAPIKey(%q) accepted a malformed key", bad)
		}
	}
	a, _, _ := NewAPIKey()
	b, _, _ := NewAPIKey()
	if a == b {
		t.Fatal("two keys share a public id")
	}
}

func TestClientSecretFormat(t *testing.T) {
	raw, full := NewClientSecret()
	got, ok := ParseClientSecret(full)
	if !ok || got != raw || !strings.HasPrefix(full, "linxcs_") {
		t.Fatalf("ParseClientSecret(%q) = %q, %v", full, got, ok)
	}
	if _, ok := ParseClientSecret(raw); ok {
		t.Fatal("accepted a secret without its prefix")
	}
}

func TestGrantScopes(t *testing.T) {
	admin := roleCeilings[RoleAdmin]
	tests := []struct {
		name      string
		requested []string
		role      string
		caller    []string
		want      []string
		code      string
	}{
		{"all leaves out sensitive scopes", []string{"all"}, RoleAdmin, admin, nonSensitive(admin), ""},
		{"sensitive scope named explicitly", []string{"all", "api_keys:write"}, RoleAdmin, admin, sorted(append(nonSensitive(admin), "api_keys:write")), ""},
		{"all is limited to what the caller holds", []string{"all"}, RoleAdmin, []string{"webhooks:read"}, []string{"webhooks:read"}, ""},
		{"all for a reporter", []string{"all"}, RoleReporter, admin, roleCeilings[RoleReporter], ""},
		{"duplicates collapse", []string{"webhooks:read", "webhooks:read"}, RoleAdmin, admin, []string{"webhooks:read"}, ""},
		{"empty", nil, RoleAdmin, admin, nil, "scopes_required"},
		{"unknown", []string{"root"}, RoleAdmin, admin, nil, "scope_unknown"},
		{"above the role", []string{"webhooks:write"}, RoleReporter, admin, nil, "scope_exceeds_role"},
		{"above the caller", []string{"webhooks:write"}, RoleAdmin, []string{"webhooks:read"}, nil, "scope_exceeds_caller"},
		{"all but nothing left", []string{"all"}, RoleUser, []string{"webhooks:read"}, nil, "scopes_required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GrantScopes(tt.requested, tt.role, tt.caller)
			var se *ScopeError
			switch {
			case tt.code == "" && err != nil:
				t.Fatalf("unexpected error %v", err)
			case tt.code != "" && (!errors.As(err, &se) || se.Code != tt.code):
				t.Fatalf("error = %v, want code %s", err, tt.code)
			case !slices.Equal(got, tt.want):
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func nonSensitive(s []string) []string {
	var out []string
	for _, v := range s {
		if !Sensitive(v) {
			out = append(out, v)
		}
	}
	return sorted(out)
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func TestRoles(t *testing.T) {
	for _, r := range Roles {
		if _, ok := roleCeilings[r]; !ok {
			t.Errorf("role %s has no ceiling", r)
		}
		if !CanGrantRole(r, r) {
			t.Errorf("role %s can't grant itself", r)
		}
		for _, s := range roleCeilings[r] {
			if !ValidScope(s) {
				t.Errorf("role %s ceiling has unknown scope %s", r, s)
			}
		}
	}
	for _, s := range sensitiveScopes {
		if !ValidScope(s) {
			t.Errorf("sensitive scope %s is unknown", s)
		}
	}
	if CanGrantRole(RoleAdmin, RoleSystemAdmin) || CanGrantRole(RoleReporter, RoleAdmin) || CanGrantRole(RoleUser, RoleReporter) {
		t.Fatal("a role can grant a higher one")
	}
	if got := Effective([]string{"webhooks:write", "extensions:read", "nope"}, RoleUser); !slices.Equal(got, []string{"extensions:read"}) {
		t.Fatalf("Effective = %v", got)
	}
}

func testTokens(t *testing.T) *Tokens {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := NewTokens(key)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestTokenRoundTrip(t *testing.T) {
	tk := testTokens(t)
	client, tenant := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := time.Now()
	raw, issued, err := tk.Issue(client, tenant, []string{"alerts:read", "webhooks:read"}, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tk.Verify(raw, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.ClientID != client || got.TenantID != tenant || got.JTI != issued.JTI || !slices.Equal(got.Scopes, issued.Scopes) {
		t.Fatalf("claims %+v, want %+v", got, issued)
	}
	if _, err := tk.Verify(raw, now.Add(AccessTokenTTL+time.Minute)); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired token: err = %v", err)
	}
	if _, err := tk.Verify(raw, now.Add(-time.Minute)); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("not-yet-valid token: err = %v", err)
	}
}

// signWith signs arbitrary claims with the Tokens' own key, to check that
// Verify refuses well-signed tokens that aren't shaped like ours.
func signWith(t *testing.T, tk *Tokens, claims any, kid string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: tk.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestTokenVerifyRefuses(t *testing.T) {
	tk := testTokens(t)
	now := time.Now()
	good := func() tokenClaims {
		return tokenClaims{
			Claims: jwt.Claims{
				Issuer: TokenIssuer, Subject: uuid.NewString(), Audience: jwt.Audience{TokenAudience},
				Expiry: jwt.NewNumericDate(now.Add(10 * time.Minute)), NotBefore: jwt.NewNumericDate(now),
				IssuedAt: jwt.NewNumericDate(now), ID: uuid.NewString(),
			},
			TenantID: uuid.NewString(), Scope: "alerts:read",
		}
	}
	if _, err := tk.Verify(signWith(t, tk, good(), tk.kid), now); err != nil {
		t.Fatalf("control token refused: %v", err)
	}
	cases := map[string]func(*tokenClaims){
		"no expiry":         func(c *tokenClaims) { c.Expiry = nil },
		"no issued-at":      func(c *tokenClaims) { c.IssuedAt = nil },
		"no not-before":     func(c *tokenClaims) { c.NotBefore = nil },
		"no jti":            func(c *tokenClaims) { c.ID = "" },
		"wrong audience":    func(c *tokenClaims) { c.Audience = jwt.Audience{"livekit"} },
		"wrong issuer":      func(c *tokenClaims) { c.Issuer = "someone" },
		"lifetime too long": func(c *tokenClaims) { c.Expiry = jwt.NewNumericDate(now.Add(24 * time.Hour)) },
		"subject not uuid":  func(c *tokenClaims) { c.Subject = "admin" },
		"tenant not uuid":   func(c *tokenClaims) { c.TenantID = "" },
	}
	for name, mutate := range cases {
		c := good()
		mutate(&c)
		if _, err := tk.Verify(signWith(t, tk, c, tk.kid), now); !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("%s: err = %v, want ErrTokenInvalid", name, err)
		}
	}
	if _, err := tk.Verify(signWith(t, tk, good(), "other-kid"), now); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("wrong kid accepted: %v", err)
	}
	// An HMAC token keyed with the public key (the classic alg-confusion attack).
	hs, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte(tk.pub)}, nil)
	raw, _ := jwt.Signed(hs).Claims(good()).Serialize()
	if _, err := tk.Verify(raw, now); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("HS256 token accepted: %v", err)
	}
}

func TestLoadSigningKey(t *testing.T) {
	dir := t.TempDir()
	good, bad := dir+"/good", dir+"/bad"
	seed := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(seed)
	writeFile(t, good, seed)
	writeFile(t, bad, seed[:10])
	if _, err := LoadSigningKey(good); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigningKey(bad); err == nil {
		t.Fatal("short key accepted")
	}
	if got := SigningKeyPathFromEnv(func(string) string { return "" }); got != "/run/secrets/linx_jwt_signing_key" {
		t.Fatalf("default path %q", got)
	}
}

func TestClientIP(t *testing.T) {
	r, err := NewClientIPResolver("10.0.0.0/8, 192.168.1.1")
	if err != nil {
		t.Fatal(err)
	}
	req := func(remote string, xff ...string) *http.Request {
		q := httptest.NewRequest(http.MethodGet, "/", nil)
		q.RemoteAddr = remote
		for _, v := range xff {
			q.Header.Add("X-Forwarded-For", v)
		}
		return q
	}
	tests := []struct {
		name string
		req  *http.Request
		want string
	}{
		{"direct peer, header ignored", req("203.0.113.5:1234", "1.2.3.4"), "203.0.113.5"},
		{"trusted proxy", req("10.1.2.3:1234", "198.51.100.7"), "198.51.100.7"},
		{"spoofed left entry ignored", req("10.1.2.3:1234", "1.2.3.4, 198.51.100.7"), "198.51.100.7"},
		{"chain of trusted proxies", req("10.1.2.3:1234", "198.51.100.7, 192.168.1.1"), "198.51.100.7"},
		{"multiple headers", req("10.1.2.3:1234", "1.2.3.4", "198.51.100.7"), "198.51.100.7"},
		{"garbage falls back to peer", req("10.1.2.3:1234", "nonsense"), "10.1.2.3"},
		{"trusted proxy without header", req("10.1.2.3:1234"), "10.1.2.3"},
		{"IPv4-mapped IPv6", req("[::ffff:203.0.113.5]:1234"), "203.0.113.5"},
	}
	for _, tt := range tests {
		if got := r.ClientIP(tt.req).String(); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}
	for _, bad := range []string{"not an ip", "10.0.0.0/33", "-linx", "linx_sni"} {
		if _, err := NewClientIPResolver(bad); err == nil {
			t.Errorf("bad trusted proxy %q accepted", bad)
		}
	}
}

// A trusted proxy given by name (the Linx-takes-443 HAProxy container) is
// trusted at the addresses its name resolves to, and only after Refresh.
func TestClientIPTrustedByName(t *testing.T) {
	r, err := NewClientIPResolver("linx-sni, 192.168.1.30")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Names(); len(got) != 1 || got[0] != "linx-sni" {
		t.Fatalf("Names() = %v", got)
	}
	haproxy := netip.MustParseAddr("172.18.0.9")
	if r.Trusted(haproxy) {
		t.Fatal("trusted a name before looking it up")
	}
	addr := haproxy
	lookup := func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "linx-sni" {
			t.Errorf("looked up %q", host)
		}
		if !addr.IsValid() {
			return nil, errors.New("no such host")
		}
		return []netip.Addr{addr}, nil
	}
	if err := r.Refresh(context.Background(), lookup); err != nil {
		t.Fatal(err)
	}
	if !r.Trusted(haproxy) || !r.Trusted(netip.MustParseAddr("192.168.1.30")) || r.Trusted(netip.MustParseAddr("172.18.0.10")) {
		t.Fatal("wrong addresses trusted after Refresh")
	}
	// The container went away: its old address is no longer trusted.
	addr = netip.Addr{}
	if err := r.Refresh(context.Background(), lookup); err == nil {
		t.Error("a failed lookup wasn't reported")
	}
	if r.Trusted(haproxy) {
		t.Error("still trusting the address of a name that no longer resolves")
	}
}

func TestIPAllowed(t *testing.T) {
	p, _ := ParseAllowedIP("203.0.113.0/24")
	one, _ := ParseAllowedIP("198.51.100.7")
	allowed := []netip.Prefix{p, one}
	for ip, want := range map[string]bool{"203.0.113.9": true, "198.51.100.7": true, "198.51.100.8": false} {
		if got := IPAllowed(allowed, netip.MustParseAddr(ip)); got != want {
			t.Errorf("IPAllowed(%s) = %v", ip, got)
		}
	}
	if !IPAllowed(nil, netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("empty list should allow everything")
	}
}

func TestLimiters(t *testing.T) {
	l := NewLimiters(60, 2)
	now := time.Now()
	if !l.Allow("k", now) || !l.Allow("k", now) || l.Allow("k", now) {
		t.Fatal("burst of 2 not enforced")
	}
	if !l.Exhausted("k", now) {
		t.Fatal("Exhausted should be true")
	}
	s := l.Status("k", now)
	if s.Remaining != 0 || s.RetryAfter <= 0 || s.Limit != 60 {
		t.Fatalf("status %+v", s)
	}
	if !l.Allow("other", now) {
		t.Fatal("keys should be independent")
	}
	if !l.Allow("k", now.Add(1100*time.Millisecond)) {
		t.Fatal("token not refilled after a second")
	}
	l.Allow("k", now.Add(time.Hour))
	if _, ok := l.buckets["other"]; ok {
		t.Fatal("idle bucket not swept")
	}
	if IPKey(netip.MustParseAddr("2001:db8::1")) != IPKey(netip.MustParseAddr("2001:db8::ffff")) {
		t.Fatal("IPv6 addresses in one /64 should share a bucket")
	}
}

func TestNewCredential(t *testing.T) {
	tenant := uuid.Must(uuid.NewV7())
	now := time.Now()
	admin := Principal{Type: TypeAPIKey, ID: "k", TenantID: tenant, Role: RoleAdmin, Scopes: roleCeilings[RoleAdmin]}

	c, key, err := NewCredential(admin, CredentialRequest{Kind: TypeAPIKey, Name: " CRM ", Scopes: []string{"all"}, AllowedIPs: []string{"203.0.113.7"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	id, secret, ok := ParseAPIKey(key)
	if !ok || id != c.PublicID || !SecretMatches(c.SecretHash, secret) {
		t.Fatal("returned key doesn't match the stored hash")
	}
	if c.Name != "CRM" || c.Role != RoleAdmin || c.TenantID != tenant || c.CreatedBy != "api_key:k" {
		t.Fatalf("unexpected credential %+v", c)
	}
	if d := c.ExpiresAt.Sub(now); d < DefaultLifetime-time.Second || d > DefaultLifetime+time.Second {
		t.Fatalf("default expiry %v", d)
	}
	if strings.Contains(string(c.SecretHash), secret) {
		t.Fatal("secret stored in the clear")
	}

	oc, clientSecret, err := NewCredential(admin, CredentialRequest{Kind: TypeOAuthClient, Name: "x", Scopes: []string{"alerts:read"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := ParseClientSecret(clientSecret)
	if !ok || !SecretMatches(oc.SecretHash, raw) || !ValidPublicID(oc.PublicID) {
		t.Fatal("client secret doesn't match the stored hash")
	}

	past, far := now.Add(-time.Hour), now.Add(MaxLifetime+time.Hour)
	for name, tc := range map[string]struct {
		req  CredentialRequest
		code string
	}{
		"empty name":      {CredentialRequest{Kind: TypeAPIKey, Name: " ", Scopes: []string{"all"}}, "name_invalid"},
		"newline in name": {CredentialRequest{Kind: TypeAPIKey, Name: "a\nb", Scopes: []string{"all"}}, "name_invalid"},
		"bad role":        {CredentialRequest{Kind: TypeAPIKey, Name: "x", Role: "root", Scopes: []string{"all"}}, "role_invalid"},
		"higher role":     {CredentialRequest{Kind: TypeAPIKey, Name: "x", Role: RoleSystemAdmin, Scopes: []string{"all"}}, "role_exceeds_caller"},
		"past expiry":     {CredentialRequest{Kind: TypeAPIKey, Name: "x", Scopes: []string{"all"}, ExpiresAt: &past}, "expiry_invalid"},
		"far expiry":      {CredentialRequest{Kind: TypeAPIKey, Name: "x", Scopes: []string{"all"}, ExpiresAt: &far}, "expiry_invalid"},
		"bad ip":          {CredentialRequest{Kind: TypeAPIKey, Name: "x", Scopes: []string{"all"}, AllowedIPs: []string{"10.0.0.0/33"}}, "allowed_ips_invalid"},
	} {
		_, _, err := NewCredential(admin, tc.req, now)
		var e *apihttp.Error
		if !errors.As(err, &e) || e.Code != tc.code {
			t.Errorf("%s: err = %v, want %s", name, err, tc.code)
		}
	}
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
