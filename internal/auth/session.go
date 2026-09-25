package auth

import (
	"context"
	"net/netip"
	"time"

	"github.com/google/uuid"
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
