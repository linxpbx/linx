package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"linxpbx.com/linx/internal/apihttp"
)

// touchInterval throttles last_used_at/ip writes to one per credential per
// minute, unless the address changes.
const touchInterval = time.Minute

// Authenticator turns a request's credentials into a Principal, enforces
// rate limits, and audits failures and writes.
type Authenticator struct {
	Store  Store
	Tokens *Tokens
	IPs    *ClientIPResolver
	// Calls limits each key or client; Failures limits failed
	// authentication per address.
	Calls, Failures *Limiters
	Log             *slog.Logger
	Now             func() time.Time
}

// NewAuthenticator builds an Authenticator with the limits from docs/API.md §3.
func NewAuthenticator(store Store, tokens *Tokens, ips *ClientIPResolver, log *slog.Logger) *Authenticator {
	return &Authenticator{
		Store:    store,
		Tokens:   tokens,
		IPs:      ips,
		Calls:    NewLimiters(CallsPerMinute, CallBurst),
		Failures: NewLimiters(FailedAuthPerMinute, FailedAuthPerMinute),
		Log:      log,
		Now:      time.Now,
	}
}

// failure is a refused credential: what the caller is told, and what the
// audit log records.
type failure struct {
	err    *apihttp.Error
	reason string
	cred   *Credential
}

var errInvalid = &apihttp.Error{Status: http.StatusUnauthorized, Code: "auth_invalid",
	Detail: "The API key or access token is not valid."}

// Middleware authenticates requests that carry an Authorization header and
// puts the Principal in the context. Requests without one continue with no
// principal; the spec's security requirements (Authorize) then decide
// whether the operation needs one.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := a.Now()
		ip := a.IPs.ClientIP(r)
		ctx := WithClientIP(r.Context(), ip)

		header := r.Header.Get("Authorization")
		if header == "" {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		ipKey := IPKey(ip)
		if a.Failures.Exhausted(ipKey, now) {
			tooManyFailures(w, a.Failures.Status(ipKey, now))
			return
		}

		p, f := a.authenticate(ctx, header, ip, now)
		if f != nil {
			// A database outage isn't the caller's failure.
			if f.err.Status != http.StatusServiceUnavailable {
				a.Failures.Allow(ipKey, now)
				a.auditFailure(ctx, ip, "auth.failed", f)
			}
			apihttp.WriteError(w, f.err)
			return
		}

		if !a.Calls.Allow(p.Actor(), now) {
			SetRateLimitHeaders(w.Header(), a.Calls.Status(p.Actor(), now))
			apihttp.WriteProblem(w, http.StatusTooManyRequests, "rate_limited",
				"Too many requests from this key. Wait a moment and try again.")
			return
		}
		SetRateLimitHeaders(w.Header(), a.Calls.Status(p.Actor(), now))

		ctx = WithPrincipal(ctx, p)
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r.WithContext(ctx))
		a.audit(ctx, AuditEntry{
			TenantID: &p.TenantID,
			Actor:    p.Actor(),
			IP:       ip,
			Action:   "api.write",
			Target:   r.Method + " " + r.URL.Path,
			Result:   resultForStatus(sw.status),
			Detail:   map[string]any{"status": sw.status},
		})
	})
}

func (a *Authenticator) authenticate(ctx context.Context, header string, ip netip.Addr, now time.Time) (Principal, *failure) {
	scheme, credential, ok := strings.Cut(header, " ")
	credential = strings.TrimSpace(credential)
	if !ok || !strings.EqualFold(scheme, "Bearer") || credential == "" {
		return Principal{}, &failure{
			err: &apihttp.Error{Status: http.StatusUnauthorized, Code: "auth_invalid",
				Detail: "Send the API key or access token as: Authorization: Bearer <key>."},
			reason: "malformed_header",
		}
	}
	if strings.HasPrefix(credential, APIKeyPrefix) {
		return a.authenticateAPIKey(ctx, credential, ip, now)
	}
	return a.authenticateAccessToken(ctx, credential, ip, now)
}

func (a *Authenticator) authenticateAPIKey(ctx context.Context, key string, ip netip.Addr, now time.Time) (Principal, *failure) {
	publicID, secret, ok := ParseAPIKey(key)
	if !ok {
		return Principal{}, &failure{err: errInvalid, reason: "malformed_key"}
	}
	cred, err := a.Store.CredentialByPublicID(ctx, TypeAPIKey, publicID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			a.Log.Error("api key lookup failed", "err", err)
			return Principal{}, &failure{err: unavailable(), reason: "lookup_error"}
		}
		// Hash anyway so an unknown id takes as long as a wrong secret.
		SecretMatches(make([]byte, 32), secret)
		return Principal{}, &failure{err: errInvalid, reason: "unknown_key"}
	}
	if f := checkCredential(&cred, secret, ip, now); f != nil {
		return Principal{}, f
	}
	a.touch(ctx, cred, ip, now)
	return cred.Principal(), nil
}

// checkCredential verifies a secret against a stored credential, then its
// state. Revoked/expired/address reasons are only revealed to a caller who
// has proved they hold the secret.
func checkCredential(cred *Credential, secret string, ip netip.Addr, now time.Time) *failure {
	if !SecretMatches(cred.SecretHash, secret) {
		return &failure{err: errInvalid, reason: "wrong_secret", cred: cred}
	}
	what := "API key"
	if cred.Kind == TypeOAuthClient {
		what = "OAuth client"
	}
	switch {
	case cred.RevokedAt != nil:
		return &failure{err: &apihttp.Error{Status: http.StatusUnauthorized, Code: "credential_revoked",
			Detail: "This " + what + " has been revoked. Create a new one."}, reason: "revoked", cred: cred}
	case !now.Before(cred.ExpiresAt):
		return &failure{err: &apihttp.Error{Status: http.StatusUnauthorized, Code: "credential_expired",
			Detail: "This " + what + " has expired. Create a new one."}, reason: "expired", cred: cred}
	case !IPAllowed(cred.AllowedIPs, ip):
		return &failure{err: &apihttp.Error{Status: http.StatusForbidden, Code: "ip_not_allowed",
			Detail: "This " + what + " can't be used from your address (" + ip.String() + ")."}, reason: "ip_not_allowed", cred: cred}
	}
	return nil
}

func (a *Authenticator) authenticateAccessToken(ctx context.Context, raw string, ip netip.Addr, now time.Time) (Principal, *failure) {
	claims, err := a.Tokens.Verify(raw, now)
	if errors.Is(err, ErrTokenExpired) {
		return Principal{}, &failure{err: &apihttp.Error{Status: http.StatusUnauthorized, Code: "token_expired",
			Detail: "The access token has expired. Get a new one from /oauth/token."}, reason: "token_expired"}
	}
	if err != nil {
		return Principal{}, &failure{err: errInvalid, reason: "bad_token"}
	}
	revoked, err := a.Store.TokenRevoked(ctx, claims.JTI)
	if err != nil {
		a.Log.Error("token revocation lookup failed", "err", err)
		return Principal{}, &failure{err: unavailable(), reason: "lookup_error"}
	}
	if revoked {
		return Principal{}, &failure{err: errInvalid, reason: "token_revoked"}
	}
	// Look the client up on every call, so revoking a client stops its
	// tokens at once rather than after up to 15 minutes.
	cred, err := a.Store.CredentialByID(ctx, TypeOAuthClient, claims.ClientID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			a.Log.Error("oauth client lookup failed", "err", err)
			return Principal{}, &failure{err: unavailable(), reason: "lookup_error"}
		}
		return Principal{}, &failure{err: errInvalid, reason: "unknown_client"}
	}
	if cred.TenantID != claims.TenantID {
		return Principal{}, &failure{err: errInvalid, reason: "tenant_mismatch", cred: &cred}
	}
	switch {
	case cred.RevokedAt != nil, !now.Before(cred.ExpiresAt):
		return Principal{}, &failure{err: errInvalid, reason: "client_inactive", cred: &cred}
	case !IPAllowed(cred.AllowedIPs, ip):
		return Principal{}, &failure{err: &apihttp.Error{Status: http.StatusForbidden, Code: "ip_not_allowed",
			Detail: "This OAuth client can't be used from your address (" + ip.String() + ")."}, reason: "ip_not_allowed", cred: &cred}
	}
	p := cred.Principal()
	// The token can only narrow what the client currently holds.
	var scopes []string
	for _, s := range claims.Scopes {
		if p.Has(s) {
			scopes = append(scopes, s)
		}
	}
	p.Scopes = Effective(scopes, p.Role)
	return p, nil
}

// Authorize enforces an operation's security requirement: an authenticated
// caller holding every listed scope. The request validator calls it with
// the scopes api/openapi.yaml declares for the operation.
func (a *Authenticator) Authorize(r *http.Request, scopes []string) error {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		return &apihttp.Error{Status: http.StatusUnauthorized, Code: "auth_required",
			Detail: "This needs an API key or access token: Authorization: Bearer <key>."}
	}
	for _, s := range scopes {
		if !p.Has(s) {
			a.audit(r.Context(), AuditEntry{
				TenantID: &p.TenantID,
				Actor:    p.Actor(),
				IP:       ClientIPFromContext(r.Context()),
				Action:   "auth.scope_denied",
				Target:   r.Method + " " + r.URL.Path,
				Result:   ResultDenied,
				Detail:   map[string]any{"scope": s},
			})
			return &apihttp.Error{Status: http.StatusForbidden, Code: "scope_missing",
				Detail: "Your API key or token doesn't have the " + s + " scope."}
		}
	}
	return nil
}

func (a *Authenticator) touch(ctx context.Context, cred Credential, ip netip.Addr, now time.Time) {
	if cred.LastUsedAt != nil && now.Sub(*cred.LastUsedAt) < touchInterval &&
		cred.LastUsedIP != nil && *cred.LastUsedIP == ip {
		return
	}
	if err := a.Store.TouchCredential(ctx, cred.Kind, cred.ID, ip, now); err != nil {
		a.Log.Warn("recording credential use failed", "err", err)
	}
}

func (a *Authenticator) auditFailure(ctx context.Context, ip netip.Addr, action string, f *failure) {
	e := AuditEntry{
		Actor:  "anonymous",
		IP:     ip,
		Action: action,
		Result: ResultDenied,
		Detail: map[string]any{"reason": f.reason},
	}
	if f.cred != nil {
		e.Actor = f.cred.Kind + ":" + f.cred.ID.String()
		e.TenantID = &f.cred.TenantID
	}
	a.audit(ctx, e)
}

// audit writes an entry even if the client has gone, and never fails the
// request: the response is already decided.
func (a *Authenticator) audit(ctx context.Context, e AuditEntry) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.Store.Audit(ctx, e); err != nil {
		a.Log.Error("audit log write failed", "action", e.Action, "err", err)
	}
}

func tooManyFailures(w http.ResponseWriter, s Status) {
	SetRateLimitHeaders(w.Header(), s)
	apihttp.WriteProblem(w, http.StatusTooManyRequests, "auth_rate_limited",
		"Too many failed sign-in attempts from your address. Wait a minute and try again.")
}

func unavailable() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusServiceUnavailable, Code: "unavailable",
		Detail: "Linx can't check credentials right now. Try again shortly."}
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func resultForStatus(status int) string {
	switch {
	case status < 400:
		return ResultOK
	case status < 500:
		return ResultDenied
	default:
		return ResultFailed
	}
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
