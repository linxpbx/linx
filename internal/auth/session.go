package auth

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
)

// Cookie and header names for signed-in people (docs/API.md §3, docs/WEB.md
// §4). The __Host- prefix (Secure, Path=/, no Domain) is a browser rule, not
// ours: it stops the cookie being set by any other origin. The session
// cookie is HttpOnly; the CSRF cookie isn't, so client script can put its
// value in the header.
const (
	SessionCookieName = "__Host-linx_session"
	CSRFCookieName    = "__Host-linx_csrf"
	CSRFHeaderName    = "X-CSRF-Token"
)

// Session lifetimes (docs/WEB.md §4). A session also expires after
// SessionIdleTTL with no request, whichever comes first.
const (
	UserSessionTTL  = 30 * 24 * time.Hour
	AdminSessionTTL = 12 * time.Hour
	SessionIdleTTL  = 7 * 24 * time.Hour
	// PendingSessionTTL is how long a session may wait on its authenticator
	// code or first-run enrollment before signing in starts over.
	PendingSessionTTL = 15 * time.Minute
)

// SessionTTL is how long a new session of role lasts before it must be
// signed in again, regardless of activity.
func SessionTTL(role string) time.Duration {
	if role == RoleAdmin || role == RoleSystemAdmin {
		return AdminSessionTTL
	}
	return UserSessionTTL
}

// UserSession is a signed-in person's session. Only hashes of its token and
// CSRF value are stored; the raw values are shown to the browser once, as
// cookies, when the session is created.
type UserSession struct {
	ID, TenantID, UserID uuid.UUID
	Role                 string
	TokenHash, CSRFHash  []byte
	// MFAVerified is false while the session is pending its authenticator
	// code (signing in) or its first-run MFA enrollment: see Principal.
	MFAVerified               bool
	CreatedAt, ExpiresAt      time.Time
	IdleExpiresAt, LastSeenAt time.Time
	LastSeenIP                *netip.Addr
	UserAgent                 string
	RevokedAt                 *time.Time
	// ConfirmedAt is this session's most recent proof of identity: signing
	// in, or POST /session/confirm (docs/ADMIN.md §7 "confirm it's you").
	// nil for a session that has never confirmed (e.g. one from before this
	// column existed).
	ConfirmedAt *time.Time
}

// ConfirmWithin is how long a session's last proof of identity (sign-in, or
// POST /session/confirm) covers "confirm it's you" actions before they ask
// again (docs/ADMIN.md §7).
const ConfirmWithin = 10 * time.Minute

// Confirmed reports whether s proved identity within ConfirmWithin of now.
func (s UserSession) Confirmed(now time.Time) bool {
	return s.ConfirmedAt != nil && now.Sub(*s.ConfirmedAt) <= ConfirmWithin
}

var errConfirmRequired = &apihttp.Error{Status: http.StatusForbidden, Code: "confirm_required",
	Detail: "This needs a fresh confirmation. Enter your password (and code, if you have one) again."}

// RequireConfirmed enforces "confirm it's you" (docs/ADMIN.md §7) for the
// most dangerous actions, even in an already-signed-in session: a stolen
// but still-valid session cookie shouldn't be enough on its own. Only
// session-authenticated callers are checked; an API key or OAuth client
// carries no session and is never subject to this (they're not sessions).
func RequireConfirmed(ctx context.Context, now time.Time) error {
	sess, ok := SessionFromContext(ctx)
	if !ok {
		return nil
	}
	if sess.Confirmed(now) {
		return nil
	}
	return errConfirmRequired
}

// Principal is the caller this session authenticates as (docs/WEB.md §4). A
// pending session holds no scopes: it can reach GET /me, POST /session/mfa,
// DELETE /session and /me/mfa*, which need no scope, and nothing else; those
// handlers use SessionFromContext to see that it's still pending.
func (s UserSession) Principal() Principal {
	p := Principal{Type: TypeUser, ID: s.UserID.String(), TenantID: s.TenantID, Role: s.Role, Pending: !s.MFAVerified}
	if s.MFAVerified {
		p.Scopes = Effective(Scopes, s.Role)
	}
	return p
}

// SessionStore is the database access session authentication needs.
type SessionStore interface {
	SessionByTokenHash(ctx context.Context, hash []byte) (UserSession, error)
	// TouchSession extends a session's idle window and records its last use.
	TouchSession(ctx context.Context, id uuid.UUID, lastSeen, idleExpires time.Time, ip netip.Addr) error
	// ConfirmSession records a fresh proof of identity (docs/ADMIN.md §7).
	ConfirmSession(ctx context.Context, id uuid.UUID, at time.Time) error
}

type sessionKey struct{}

// WithSession returns ctx carrying the authenticated session, for handlers
// that act on the session itself (promoting it past MFA, signing it out).
func WithSession(ctx context.Context, s UserSession) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// SessionFromContext returns the request's session, if it was authenticated
// by cookie rather than by API key or access token.
func SessionFromContext(ctx context.Context) (UserSession, bool) {
	s, ok := ctx.Value(sessionKey{}).(UserSession)
	return s, ok
}

// NewSessionToken and NewCSRFToken are both just NewSecret under a name
// that says what they're for at the call site.
func NewSessionToken() string { return NewSecret() }
func NewCSRFToken() string    { return NewSecret() }
