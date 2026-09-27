package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/dbsecret"
)

// Accounts is what the API's session and people-account endpoints do
// (docs/WEB.md §4). Caller mistakes come back as *apihttp.Error.
type Accounts struct {
	Store  UserStore
	Sealer *dbsecret.Sealer
	Alerts AlertFirer
	// Failures is the same per-address limiter Authenticator uses for a bad
	// API key or token (docs/WEB.md §4: "the existing per-address limit"),
	// so failed sign-ins share that budget.
	Failures *Limiters
	Now      func() time.Time
	// SessionsEnded, if set, hears about sessions ended on purpose: one
	// (signing out: session is set) or all of a person's (disabled, or their
	// password or role changed: session is nil). Anything riding on a
	// session, like the /sip relay's phone line, closes at once instead of
	// at its next check (docs/WEB.md §4).
	SessionsEnded func(ctx context.Context, user uuid.UUID, session *uuid.UUID)
	// Log is for audit writes that fail (the response is already decided);
	// nil logs to slog.Default.
	Log *slog.Logger
	// WebAuthn is the passkey relying party (NewWebAuthn); nil turns
	// passkeys off (no domain set). Passkeys is their storage.
	WebAuthn *webauthn.WebAuthn
	Passkeys PasskeyStore
	// CompanyProviders is company sign-in's OpenID Connect side (nil turns
	// it off: no domain set); Company is its storage (ADR-052).
	CompanyProviders CompanyProviders
	Company          CompanyStore

	ceremonies   ceremonies
	companyFlows companyFlows
}

func (a *Accounts) log() *slog.Logger {
	if a.Log == nil {
		return slog.Default()
	}
	return a.Log
}

func (a *Accounts) sessionsEnded(ctx context.Context, user uuid.UUID, session *uuid.UUID) {
	if a.SessionsEnded != nil {
		a.SessionsEnded(ctx, user, session)
	}
}

var errNoPrincipalAuth = errors.New("no principal on the request: authentication middleware is missing")

func notFound(what string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "There is no " + what + " with that id."}
}

func notASession() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusBadRequest, Code: "not_a_session",
		Detail: "This only works for a signed-in browser session."}
}

func secondStepExists() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusConflict, Code: "second_step_exists",
		Detail: "This account already has a passkey or an authenticator app. Sign in with it first."}
}

func invalidLink() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusBadRequest, Code: "setup_link_invalid",
		Detail: "This link has already been used, has expired, or doesn't exist. Ask an admin for a new one."}
}

// beyondCaller refuses changing, disabling or resetting someone whose role
// the caller couldn't have given: an admin must not be able to take over a
// system admin with a set-password link, or demote or disable one.
func beyondCaller(caller Principal, target User) *apihttp.Error {
	if CanGrantRole(caller.Role, target.Role) {
		return nil
	}
	return &apihttp.Error{Status: http.StatusForbidden, Code: "role_exceeds_caller",
		Detail: fmt.Sprintf("You can't change a person with the %s role.", target.Role)}
}

// mfaVerifyFirst refuses account changes from a session still waiting on
// its authenticator code: knowing the password alone must not let anyone
// replace the authenticator app or the password.
func mfaVerifyFirst() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusForbidden, Code: "sign_in_unfinished",
		Detail: "Enter the code from your authenticator app first."}
}

var errSignInInvalid = &apihttp.Error{Status: http.StatusUnauthorized, Code: "sign_in_invalid",
	Detail: "That email or password is incorrect."}

func lockedError(until time.Time) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusTooManyRequests, Code: "account_locked",
		Detail: "Too many failed attempts. Try again after " + until.UTC().Format(time.RFC3339) + "."}
}

func unavailableErr() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusServiceUnavailable, Code: "unavailable",
		Detail: "Linx can't check that right now. Try again shortly."}
}

func tooManyFailuresErr() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusTooManyRequests, Code: "auth_rate_limited",
		Detail: "Too many failed sign-in attempts from your address. Wait a minute and try again."}
}

// unknownUserHash is verified against on an unknown email, so the response
// takes as long as a wrong password does (no timing tell for which emails
// have accounts).
var unknownUserHash = func() string {
	h, err := HashPassword("not-a-real-password-000000000000")
	if err != nil {
		panic(err)
	}
	return h
}()

var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func validEmail(email string) bool { return len(email) <= 200 && emailRE.MatchString(email) }

// ETag is a person's version as an HTTP entity tag (docs/ADMIN.md §9: "the
// 1C gap").
func ETag(version int) string { return `"` + strconv.Itoa(version) + `"` }

func matchETag(ifMatch string, version int) bool {
	for _, tag := range strings.Split(ifMatch, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || tag == ETag(version) {
			return true
		}
	}
	return false
}

var errUserChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "This person was changed since you read them. Fetch them again and retry."}

func userAudit(ctx context.Context, action, target string) (Principal, AuditEntry, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return Principal{}, AuditEntry{}, errNoPrincipalAuth
	}
	return p, AuditEntry{
		TenantID: &p.TenantID, Actor: p.Actor(), IP: ClientIPFromContext(ctx),
		Action: action, Target: target, Result: ResultOK,
	}, nil
}

// UserInput is a new person (docs/WEB.md §4).
type UserInput struct {
	Email, Name string
	Role        string
	ExtensionID *uuid.UUID
}

// CreateUser registers a person and returns a one-time set-password link
// token (docs/WEB.md §4). Their role can't exceed the caller's.
func (a *Accounts) CreateUser(ctx context.Context, in UserInput) (User, string, error) {
	caller, ok := PrincipalFromContext(ctx)
	if !ok {
		return User{}, "", errNoPrincipalAuth
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if !validEmail(email) {
		return User{}, "", badRequest("email_invalid", "Give a valid email address.")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 100 {
		return User{}, "", badRequest("name_invalid", "Give a name of 1 to 100 characters.")
	}
	role := in.Role
	if role == "" {
		return User{}, "", badRequest("role_required", "Choose a role.")
	}
	if !ValidRole(role) {
		return User{}, "", badRequest("role_invalid", fmt.Sprintf("%q is not a role. Roles: %s.", role, strings.Join(Roles, ", ")))
	}
	if !CanGrantRole(caller.Role, role) {
		return User{}, "", &apihttp.Error{Status: http.StatusForbidden, Code: "role_exceeds_caller",
			Detail: fmt.Sprintf("You can't create a person with the %s role.", role)}
	}
	if role == RoleAdmin || role == RoleSystemAdmin {
		// Creating an admin is one of "confirm it's you"'s actions, even in
		// an already-signed-in session (docs/ADMIN.md §7).
		if err := RequireConfirmed(ctx, a.Now()); err != nil {
			return User{}, "", err
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return User{}, "", err
	}
	now := a.Now().UTC()
	// A random, unusable password until the person picks their own through
	// the setup link: never shown, never meant to be guessed.
	placeholder, err := HashPassword(NewSecret())
	if err != nil {
		return User{}, "", err
	}
	u := User{
		ID: id, TenantID: caller.TenantID, Email: email, Name: name, Role: role, ExtensionID: in.ExtensionID,
		PasswordHash: placeholder, PasswordUpdatedAt: now, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	audit := AuditEntry{TenantID: &caller.TenantID, Actor: caller.Actor(), IP: ClientIPFromContext(ctx),
		Action: "user.create", Target: "user:" + id.String(), Result: ResultOK,
		Detail: map[string]any{"email": email, "role": role}}
	if err := a.Store.CreateUser(ctx, u, audit); err != nil {
		if errors.Is(err, ErrDuplicate) {
			return User{}, "", &apihttp.Error{Status: http.StatusConflict, Code: "email_taken",
				Detail: "Someone already has an account with that email."}
		}
		return User{}, "", err
	}
	token, err := a.createSetupLink(ctx, u)
	if err != nil {
		return User{}, "", err
	}
	return u, token, nil
}

func (a *Accounts) createSetupLink(ctx context.Context, u User) (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	token := NewSecret()
	now := a.Now().UTC()
	l := SetupLink{ID: id, TenantID: u.TenantID, UserID: u.ID, TokenHash: HashSecret(token), CreatedAt: now, ExpiresAt: now.Add(SetupLinkTTL)}
	if err := a.Store.CreateSetupLink(ctx, l); err != nil {
		return "", err
	}
	return token, nil
}

// CreateSetupLink issues a fresh set-password link for an existing person
// (docs/WEB.md §4: "admins use ... POST /users/{id}/setup-link").
func (a *Accounts) CreateSetupLink(ctx context.Context, userID uuid.UUID) (string, error) {
	caller, ok := PrincipalFromContext(ctx)
	if !ok {
		return "", errNoPrincipalAuth
	}
	u, err := a.Store.User(ctx, caller.TenantID, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", notFound("person")
		}
		return "", err
	}
	if e := beyondCaller(caller, u); e != nil {
		return "", e
	}
	return a.createSetupLink(ctx, u)
}

// ListUsers returns a page of the caller's tenant's people, newest first.
func (a *Accounts) ListUsers(ctx context.Context, before *uuid.UUID, limit int) ([]User, error) {
	caller, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipalAuth
	}
	return a.Store.ListUsers(ctx, caller.TenantID, before, limit)
}

// GetUser returns one of the caller's tenant's people.
func (a *Accounts) GetUser(ctx context.Context, id uuid.UUID) (User, error) {
	caller, ok := PrincipalFromContext(ctx)
	if !ok {
		return User{}, errNoPrincipalAuth
	}
	u, err := a.Store.User(ctx, caller.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return User{}, notFound("person")
	}
	return u, err
}

// UserPatch is a JSON Merge Patch of a person; nil fields stay as they are.
type UserPatch struct {
	Name        *string
	Role        *string
	ExtensionID *uuid.UUID
	Disabled    *bool
}

// UpdateUser applies patch to a person. Raising their role can't exceed the
// caller's own (like creating them); disabling them ends their sessions.
// ifMatch, when not empty, must match the person's current ETag (412
// otherwise; docs/ADMIN.md §9, the 1C gap).
func (a *Accounts) UpdateUser(ctx context.Context, id uuid.UUID, patch UserPatch, ifMatch string) (User, error) {
	caller, ok := PrincipalFromContext(ctx)
	if !ok {
		return User{}, errNoPrincipalAuth
	}
	u, err := a.Store.User(ctx, caller.TenantID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return User{}, notFound("person")
		}
		return User{}, err
	}
	if ifMatch != "" && !matchETag(ifMatch, u.Version) {
		return User{}, errUserChanged
	}
	if e := beyondCaller(caller, u); e != nil {
		return User{}, e
	}
	if patch.Role != nil && (*patch.Role == RoleAdmin || *patch.Role == RoleSystemAdmin) && *patch.Role != u.Role {
		// Giving someone an admin role is one of "confirm it's you"'s
		// actions, even in an already-signed-in session (docs/ADMIN.md §7).
		if err := RequireConfirmed(ctx, a.Now()); err != nil {
			return User{}, err
		}
	}
	now := a.Now().UTC()
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" || len([]rune(name)) > 100 {
			return User{}, badRequest("name_invalid", "Give a name of 1 to 100 characters.")
		}
		u.Name = name
	}
	roleChanging := false
	if patch.Role != nil {
		if !ValidRole(*patch.Role) {
			return User{}, badRequest("role_invalid", fmt.Sprintf("%q is not a role. Roles: %s.", *patch.Role, strings.Join(Roles, ", ")))
		}
		if !CanGrantRole(caller.Role, *patch.Role) {
			return User{}, &apihttp.Error{Status: http.StatusForbidden, Code: "role_exceeds_caller",
				Detail: fmt.Sprintf("You can't give the %s role.", *patch.Role)}
		}
		roleChanging = *patch.Role != u.Role
		u.Role = *patch.Role
	}
	if patch.ExtensionID != nil {
		u.ExtensionID = patch.ExtensionID
	}
	disabling := patch.Disabled != nil && *patch.Disabled && u.DisabledAt == nil
	enabling := patch.Disabled != nil && !*patch.Disabled
	if enabling {
		u.DisabledAt = nil
	} else if disabling {
		u.DisabledAt = &now
	}
	u.UpdatedAt = now
	_, audit, err := userAudit(ctx, "user.update", "user:"+id.String())
	if err != nil {
		return User{}, err
	}
	out, err := a.Store.UpdateUser(ctx, u, audit)
	if errors.Is(err, ErrVersionChanged) {
		return User{}, &apihttp.Error{Status: http.StatusConflict, Code: "version_changed",
			Detail: "Someone else changed this person first. Reload and try again."}
	}
	if errors.Is(err, ErrNotFound) {
		return User{}, notFound("person")
	}
	if err != nil {
		return User{}, err
	}
	// A session carries the role it signed in with (Principal's scopes come
	// from it); ending every session on a role change is what makes a
	// demotion (or promotion) take effect at once instead of up to
	// AdminSessionTTL/UserSessionTTL later.
	if disabling || roleChanging {
		if err := a.Store.RevokeUserSessions(ctx, id, now); err != nil {
			return out, err
		}
		a.sessionsEnded(ctx, id, nil)
	}
	return out, nil
}

// DisableUser stops a person signing in and ends every session of theirs
// (docs/WEB.md §4). Disabling a disabled person succeeds and changes nothing.
func (a *Accounts) DisableUser(ctx context.Context, id uuid.UUID) (User, error) {
	caller, audit, err := userAudit(ctx, "user.disable", "user:"+id.String())
	if err != nil {
		return User{}, err
	}
	target, err := a.Store.User(ctx, caller.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return User{}, notFound("person")
	}
	if err != nil {
		return User{}, err
	}
	if e := beyondCaller(caller, target); e != nil {
		return User{}, e
	}
	u, err := a.Store.DisableUser(ctx, caller.TenantID, id, a.Now().UTC(), audit)
	if errors.Is(err, ErrNotFound) {
		return User{}, notFound("person")
	}
	if err == nil {
		a.sessionsEnded(ctx, id, nil)
	}
	return u, err
}

func (a *Accounts) newSession(ctx context.Context, u User, mfaVerified bool, ip netip.Addr, userAgent string, now time.Time) (UserSession, string, string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return UserSession{}, "", "", err
	}
	token, csrf := NewSessionToken(), NewCSRFToken()
	ttl := SessionTTL(u.Role)
	expires := now.Add(ttl)
	idle := now.Add(SessionIdleTTL)
	if idle.After(expires) {
		idle = expires
	}
	var lastIP *netip.Addr
	if ip.IsValid() {
		lastIP = &ip
	}
	s := UserSession{
		ID: id, TenantID: u.TenantID, UserID: u.ID, Role: u.Role,
		TokenHash: HashSecret(token), CSRFHash: HashSecret(csrf), MFAVerified: mfaVerified,
		CreatedAt: now, ExpiresAt: expires, IdleExpiresAt: idle, LastSeenAt: now,
		LastSeenIP: lastIP, UserAgent: trimUserAgent(userAgent),
	}
	if mfaVerified {
		// Signing in already proved identity; a session still pending its
		// authenticator code confirms once VerifyMFA promotes it instead
		// (docs/ADMIN.md §7).
		s.ConfirmedAt = &now
	}
	if err := a.Store.CreateSession(ctx, s); err != nil {
		return UserSession{}, "", "", err
	}
	return s, token, csrf, nil
}

// passwordSignInDone reports whether a password (or a setup link's new
// password) is a whole sign-in for u: only when u has no second step and
// isn't an admin who still has to set one up (docs/ADMIN.md §5).
func passwordSignInDone(u User) bool { return !u.HasSecondStep() && !u.needsSecondStepSetup() }

func statusFor(u User, done bool) string {
	if done {
		return "signed_in"
	}
	if u.HasSecondStep() {
		return "mfa_verify_required"
	}
	return "mfa_setup_required"
}

// passwordOutcome is the session a correct password gets u: whole, or
// pending the second step (or setting one up).
func (a *Accounts) passwordOutcome(ctx context.Context, u User, ip netip.Addr, userAgent string, now time.Time) (SessionOutcome, error) {
	done := passwordSignInDone(u)
	sess, token, csrf, err := a.newSession(ctx, u, done, ip, userAgent, now)
	if err != nil {
		return SessionOutcome{}, err
	}
	out := SessionOutcome{Session: sess, Token: token, CSRF: csrf, Status: statusFor(u, done)}
	if out.Status == "mfa_verify_required" {
		out.Methods = u.SecondStepMethods()
	}
	return out, nil
}

// SignIn is step one of signing in: email and password (docs/WEB.md §4).
// The right password during a lockout wait still fails, and a wrong email
// takes as long as a wrong password so neither reveals whether an account
// exists (docs/WEB.md §4).
func (a *Accounts) SignIn(ctx context.Context, tenant uuid.UUID, email, password string, ip netip.Addr, userAgent string) (SessionOutcome, error) {
	now := a.Now().UTC()
	// The same per-address budget a bad API key or token draws on
	// (docs/WEB.md §4: "the existing per-address limit").
	ipKey := IPKey(ip)
	if a.Failures != nil && a.Failures.Exhausted(ipKey, now) {
		return SessionOutcome{}, tooManyFailuresErr()
	}
	email = strings.ToLower(strings.TrimSpace(email))
	u, err := a.Store.UserByEmail(ctx, tenant, email)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			VerifyPassword(unknownUserHash, password)
			if a.Failures != nil {
				a.Failures.Allow(ipKey, now)
			}
			a.auditSignIn(ctx, signInAttempt{tenant: tenant, ip: ip, reason: "unknown_email"})
			return SessionOutcome{}, errSignInInvalid
		}
		return SessionOutcome{}, err
	}
	attempt := signInAttempt{tenant: tenant, user: &u, ip: ip}
	if u.DisabledAt != nil {
		VerifyPassword(unknownUserHash, password)
		if a.Failures != nil {
			a.Failures.Allow(ipKey, now)
		}
		attempt.reason = "disabled"
		a.auditSignIn(ctx, attempt)
		return SessionOutcome{}, errSignInInvalid
	}
	if u.LockedUntil != nil && now.Before(*u.LockedUntil) {
		a.lockedAttempt(ctx, tenant, u, ipKey, now)
		attempt.reason, attempt.lockedUntil = "locked", u.LockedUntil
		a.auditSignIn(ctx, attempt)
		return SessionOutcome{}, lockedError(*u.LockedUntil)
	}
	if !u.HasPassword() {
		// A passkey-only account: no password can match, but take as long as
		// checking one would, and count the try like any wrong password.
		VerifyPassword(unknownUserHash, password)
	}
	if !u.HasPassword() || !VerifyPassword(u.PasswordHash, password) {
		if a.Failures != nil {
			a.Failures.Allow(ipKey, now)
		}
		lockedUntil, alertThreshold, ferr := a.Store.RecordLoginFailure(ctx, tenant, u.ID, now)
		if ferr != nil {
			return SessionOutcome{}, unavailableErr()
		}
		if alertThreshold {
			a.fireGuessing(ctx, tenant, u)
		}
		attempt.reason, attempt.lockedUntil = "wrong_password", lockedUntil
		a.auditSignIn(ctx, attempt)
		if lockedUntil != nil {
			return SessionOutcome{}, lockedError(*lockedUntil)
		}
		return SessionOutcome{}, errSignInInvalid
	}
	// "People must use company sign-in": checked only once the password is
	// right, so the answer never tells a guesser anything.
	if required, err := a.companyRequiredFor(ctx, u); err != nil {
		return SessionOutcome{}, err
	} else if required {
		attempt.reason = "company_sign_in_required"
		a.auditSignIn(ctx, attempt)
		return SessionOutcome{}, errCompanyRequired
	}
	if err := a.Store.RecordLoginSuccess(ctx, tenant, u.ID, now); err != nil {
		return SessionOutcome{}, err
	}
	if a.Alerts != nil {
		_ = a.Alerts.Resolve(ctx, tenant, loginGuessKey(u.ID))
	}
	out, err := a.passwordOutcome(ctx, u, ip, userAgent, now)
	if err != nil {
		return SessionOutcome{}, err
	}
	attempt.method = "password"
	a.auditSignIn(ctx, attempt)
	return out, nil
}

// signInAttempt is one sign-in step's outcome for the audit log: reason is
// empty for a success. Never the password or code typed, and never an
// email that matched nobody (people type passwords into the email box).
type signInAttempt struct {
	tenant      uuid.UUID
	user        *User
	ip          netip.Addr
	code        bool // the authenticator-code step, not the password one
	method      string
	reason      string
	lockedUntil *time.Time
}

// auditSignIn writes attempt as user.sign_in (password step) or
// user.sign_in_code (code step), so an admin can see who tried, from where,
// and why it was refused (docs/WEB.md §4). Tries refused by the address's
// failure budget aren't written: they're bounded only by the caller.
func (a *Accounts) auditSignIn(ctx context.Context, at signInAttempt) {
	e := AuditEntry{TenantID: &at.tenant, Actor: "anonymous", IP: at.ip, Action: "user.sign_in", Result: ResultOK}
	if at.code {
		e.Action = "user.sign_in_code"
	}
	if at.user != nil {
		e.Target = "user:" + at.user.ID.String()
		if at.code {
			e.Actor = e.Target
		}
	}
	detail := map[string]any{}
	if at.reason != "" {
		e.Result = ResultDenied
		detail["reason"] = at.reason
	}
	if at.method != "" {
		detail["method"] = at.method
	}
	if at.lockedUntil != nil {
		detail["locked_until"] = at.lockedUntil.UTC().Format(time.RFC3339)
	}
	if len(detail) > 0 {
		e.Detail = detail
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.Store.Audit(ctx, e); err != nil {
		log := a.Log
		if log == nil {
			log = slog.Default()
		}
		log.Error("audit log write failed", "action", e.Action, "err", err)
	}
}

// UnlockUser clears a person's lockout wait and failure count, so they can
// sign in again at once (`linx user unlock`). It doesn't change their
// password: if someone else was guessing it, that's a separate decision.
func (a *Accounts) UnlockUser(ctx context.Context, id uuid.UUID) (User, error) {
	caller, audit, err := userAudit(ctx, "user.unlock", "user:"+id.String())
	if err != nil {
		return User{}, err
	}
	u, err := a.Store.User(ctx, caller.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return User{}, notFound("person")
	}
	if err != nil {
		return User{}, err
	}
	if e := beyondCaller(caller, u); e != nil {
		return User{}, e
	}
	now := a.Now().UTC()
	if u.LockedUntil != nil {
		audit.Detail = map[string]any{"locked_until": u.LockedUntil.UTC().Format(time.RFC3339)}
	}
	if err := a.Store.RecordLoginSuccess(ctx, u.TenantID, u.ID, now); err != nil {
		return User{}, err
	}
	if err := a.Store.Audit(ctx, audit); err != nil {
		return User{}, err
	}
	if a.Alerts != nil {
		_ = a.Alerts.Resolve(ctx, u.TenantID, loginGuessKey(u.ID))
	}
	u.FailedAttempts, u.LockedUntil, u.FailureWindowStart, u.FailureWindowCount = 0, nil, nil, 0
	return u, nil
}

// lockedAttempt counts a try made during the lockout wait: against the
// address's budget, and toward the guessing-password alert.
func (a *Accounts) lockedAttempt(ctx context.Context, tenant uuid.UUID, u User, ipKey string, now time.Time) {
	if a.Failures != nil {
		a.Failures.Allow(ipKey, now)
	}
	if alert, err := a.Store.RecordLockedAttempt(ctx, tenant, u.ID, now); err == nil && alert {
		a.fireGuessing(ctx, tenant, u)
	}
}

func (a *Accounts) fireGuessing(ctx context.Context, tenant uuid.UUID, u User) {
	if a.Alerts != nil {
		_ = a.Alerts.Fire(ctx, tenant, loginGuessKey(u.ID), "warning", "Someone is guessing a password",
			fmt.Sprintf("20 or more failed sign-ins for %s in the last hour.", u.Email), "")
	}
}

// usableSetupLink returns the link for token if it can still be used. A
// token that can't counts against ip's failure budget, like a wrong
// password: links are 256-bit, but nobody gets to try them at speed.
func (a *Accounts) usableSetupLink(ctx context.Context, token string, ip netip.Addr, now time.Time) (SetupLink, User, error) {
	ipKey := IPKey(ip)
	if a.Failures != nil && a.Failures.Exhausted(ipKey, now) {
		return SetupLink{}, User{}, tooManyFailuresErr()
	}
	invalid := func() (SetupLink, User, error) {
		if a.Failures != nil {
			a.Failures.Allow(ipKey, now)
		}
		return SetupLink{}, User{}, invalidLink()
	}
	link, err := a.Store.SetupLinkByTokenHash(ctx, HashSecret(token))
	if errors.Is(err, ErrNotFound) {
		return invalid()
	}
	if err != nil {
		return SetupLink{}, User{}, err
	}
	if link.UsedAt != nil || !now.Before(link.ExpiresAt) {
		return invalid()
	}
	u, err := a.Store.User(ctx, link.TenantID, link.UserID)
	if err != nil {
		return SetupLink{}, User{}, err
	}
	if u.DisabledAt != nil {
		return invalid()
	}
	return link, u, nil
}

// CheckSetupLink reports whether a set-password link can still be used, so
// the page can say it can't before asking for a password, and returns its
// person: the page shows their email and offers the passkey and
// password-only choices only to someone with no second step yet.
func (a *Accounts) CheckSetupLink(ctx context.Context, token string, ip netip.Addr) (User, error) {
	_, u, err := a.usableSetupLink(ctx, token, ip, a.Now().UTC())
	return u, err
}

// CompleteSetup is a new person's first sign-in through their one-time link
// (docs/WEB.md §4), or an existing person's password reset: they pick a
// password, any other session of theirs ends, and they're signed in at once
// (still pending a second step's setup if their role requires one, or their
// second step if they already have one: a link never skips it).
// passwordOnly is the "Password only" choice (docs/ADMIN.md §5): for an
// admin it means they accepted the "not recommended" warning, so no second
// step is asked for; it's refused for someone who already has one.
func (a *Accounts) CompleteSetup(ctx context.Context, linkToken, password string, passwordOnly bool, ip netip.Addr, userAgent string) (SessionOutcome, error) {
	if err := CheckPasswordPolicy(password); err != nil {
		return SessionOutcome{}, badRequest("password_invalid", err.Error())
	}
	now := a.Now().UTC()
	link, u, err := a.usableSetupLink(ctx, linkToken, ip, now)
	if err != nil {
		return SessionOutcome{}, err
	}
	if passwordOnly && u.HasSecondStep() {
		return SessionOutcome{}, secondStepExists()
	}
	hash, err := HashPassword(password)
	if err != nil {
		return SessionOutcome{}, err
	}
	// Claimed first, and only if still unused: two requests racing with the
	// same link can't both set a password.
	if err := a.Store.ConsumeSetupLink(ctx, link.ID, now); err != nil {
		if errors.Is(err, ErrNotFound) {
			return SessionOutcome{}, invalidLink()
		}
		return SessionOutcome{}, err
	}
	if err := a.Store.SetPassword(ctx, u.TenantID, u.ID, hash, now, true, AuditEntry{
		TenantID: &u.TenantID, Actor: "user:" + u.ID.String(), IP: ip, Action: "user.password_set",
		Target: "user:" + u.ID.String(), Result: ResultOK,
	}); err != nil {
		return SessionOutcome{}, err
	}
	a.sessionsEnded(ctx, u.ID, nil)
	if passwordOnly && requiresMFA(u.Role) && u.PasswordOnlyAcceptedAt == nil {
		if err := a.Store.AcceptPasswordOnly(ctx, u.TenantID, u.ID, now, AuditEntry{
			TenantID: &u.TenantID, Actor: "user:" + u.ID.String(), IP: ip, Action: "user.password_only_accepted",
			Target: "user:" + u.ID.String(), Result: ResultOK,
		}); err != nil {
			return SessionOutcome{}, err
		}
		u.PasswordOnlyAcceptedAt = &now
	}
	return a.passwordOutcome(ctx, u, ip, userAgent, now)
}

// VerifyMFA is step two of signing in: the authenticator code (or a
// recovery code) for an account that already has MFA enabled (docs/WEB.md
// §4). It promotes the request's pending session to a full one.
func (a *Accounts) VerifyMFA(ctx context.Context, code string) error {
	sess, ok := SessionFromContext(ctx)
	if !ok {
		return notASession()
	}
	if sess.MFAVerified {
		return nil
	}
	now := a.Now().UTC()
	// Wrong codes share the same per-address budget and per-account lockout
	// as a wrong password (docs/WEB.md §4): without this, a stolen pending
	// session cookie would let an attacker brute-force a 6-digit code with
	// no limit.
	ipKey := IPKey(ClientIPFromContext(ctx))
	if a.Failures != nil && a.Failures.Exhausted(ipKey, now) {
		return tooManyFailuresErr()
	}
	u, err := a.Store.User(ctx, sess.TenantID, sess.UserID)
	if err != nil {
		return err
	}
	if !u.HasSecondStep() {
		return badRequest("mfa_not_enrolled", "Set up a passkey or an authenticator app first.")
	}
	attempt := signInAttempt{tenant: sess.TenantID, user: &u, ip: ClientIPFromContext(ctx), code: true}
	if u.LockedUntil != nil && now.Before(*u.LockedUntil) {
		a.lockedAttempt(ctx, sess.TenantID, u, ipKey, now)
		attempt.reason, attempt.lockedUntil = "locked", u.LockedUntil
		a.auditSignIn(ctx, attempt)
		return lockedError(*u.LockedUntil)
	}
	method, err := a.checkMFACode(ctx, u, code, now)
	if err != nil {
		return err
	}
	if method == codeInvalid || method == codeUsed {
		if a.Failures != nil {
			a.Failures.Allow(ipKey, now)
		}
		lockedUntil, alertThreshold, ferr := a.Store.RecordLoginFailure(ctx, sess.TenantID, u.ID, now)
		if ferr != nil {
			return unavailableErr()
		}
		if alertThreshold {
			a.fireGuessing(ctx, sess.TenantID, u)
		}
		attempt.reason, attempt.lockedUntil = string(method), lockedUntil
		a.auditSignIn(ctx, attempt)
		if lockedUntil != nil {
			return lockedError(*lockedUntil)
		}
		if method == codeUsed {
			return &apihttp.Error{Status: http.StatusUnauthorized, Code: "mfa_code_used",
				Detail: "That code was already used. Wait for the next one in your app."}
		}
		return &apihttp.Error{Status: http.StatusUnauthorized, Code: "mfa_code_invalid", Detail: "That code isn't right."}
	}
	if err := a.Store.RecordLoginSuccess(ctx, sess.TenantID, u.ID, now); err != nil {
		return err
	}
	if err := a.Store.PromoteSession(ctx, sess.ID); err != nil {
		return err
	}
	if err := a.Store.ConfirmSession(ctx, sess.ID, now); err != nil {
		return err
	}
	attempt.method = string(method)
	a.auditSignIn(ctx, attempt)
	return nil
}

// mfaCodeResult is what checkMFACode found: which kind of code worked, or
// why none did (these double as the audit log's method and reason).
type mfaCodeResult string

const (
	codeAuthenticator mfaCodeResult = "authenticator"
	codeRecovery      mfaCodeResult = "recovery_code"
	codeInvalid       mfaCodeResult = "code_invalid"
	codeUsed          mfaCodeResult = "code_used"
)

// checkMFACode checks code as an authenticator code, then as a recovery
// code. Either works once (docs/WEB.md §4): an authenticator code only for
// a later step than the last one accepted, a recovery code only while it's
// still stored.
func (a *Accounts) checkMFACode(ctx context.Context, u User, code string, now time.Time) (mfaCodeResult, error) {
	if u.MFAEnabled && len(u.MFASecretEnc) > 0 {
		secret, err := a.Sealer.Open(mfaRowID(u.ID), u.MFASecretEnc)
		if err != nil {
			return "", fmt.Errorf("opening MFA secret: %w", err)
		}
		if step, ok := MatchTOTPCode(secret, code, now); ok {
			fresh, err := a.Store.UseTOTPStep(ctx, u.TenantID, u.ID, step)
			if err != nil {
				return "", err
			}
			if !fresh {
				return codeUsed, nil
			}
			return codeAuthenticator, nil
		}
	}
	norm := normalizeRecoveryCode(code)
	for _, stored := range u.RecoveryCodeHashes {
		if SecretMatches(stored, norm) {
			fresh, err := a.Store.ConsumeRecoveryCode(ctx, u.TenantID, u.ID, stored)
			if err != nil {
				return "", err
			}
			if !fresh {
				return codeUsed, nil
			}
			return codeRecovery, nil
		}
	}
	return codeInvalid, nil
}

// BeginMFAEnrollment starts (or restarts) turning on MFA for the caller's
// own account: an authenticator app secret to scan, unconfirmed until
// ConfirmMFAEnrollment (docs/WEB.md §4).
func (a *Accounts) BeginMFAEnrollment(ctx context.Context) (secret, otpauthURL string, err error) {
	caller, ok := PrincipalFromContext(ctx)
	if !ok {
		return "", "", errNoPrincipalAuth
	}
	if caller.Type != TypeUser {
		return "", "", notASession()
	}
	uid, err := uuid.Parse(caller.ID)
	if err != nil {
		return "", "", err
	}
	u, err := a.Store.User(ctx, caller.TenantID, uid)
	if err != nil {
		return "", "", err
	}
	if caller.Pending && u.HasSecondStep() {
		return "", "", mfaVerifyFirst()
	}
	if !caller.Pending {
		// A new authenticator is a new way in: a borrowed session mustn't be
		// able to add one without a fresh proof (docs/ADMIN.md §7).
		if err := RequireConfirmed(ctx, a.Now()); err != nil {
			return "", "", err
		}
	}
	raw, err := NewTOTPSecret()
	if err != nil {
		return "", "", err
	}
	enc, err := a.Sealer.Seal(mfaRowID(u.ID), raw)
	if err != nil {
		return "", "", err
	}
	if err := a.Store.SetMFASecret(ctx, u.TenantID, u.ID, enc, a.Now().UTC()); err != nil {
		return "", "", err
	}
	return totpSecretEncoding.EncodeToString(raw), TOTPProvisioningURI(raw, "Linx", u.Email), nil
}

// ConfirmMFAEnrollment checks a code from the pending secret, turns MFA on,
// issues recovery codes (shown once), and promotes the caller's session
// past MFA if it was still pending (docs/WEB.md §4).
func (a *Accounts) ConfirmMFAEnrollment(ctx context.Context, code string) ([]string, error) {
	caller, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipalAuth
	}
	if caller.Type != TypeUser {
		return nil, notASession()
	}
	uid, err := uuid.Parse(caller.ID)
	if err != nil {
		return nil, err
	}
	u, err := a.Store.User(ctx, caller.TenantID, uid)
	if err != nil {
		return nil, err
	}
	if caller.Pending && u.HasSecondStep() {
		return nil, mfaVerifyFirst()
	}
	if len(u.MFAPendingSecretEnc) == 0 {
		return nil, badRequest("mfa_not_started", "Start enrollment first: POST /me/mfa.")
	}
	step, ok2, err := a.checkTOTPOnly(u, code)
	if err != nil {
		return nil, err
	}
	if !ok2 {
		return nil, &apihttp.Error{Status: http.StatusUnauthorized, Code: "mfa_code_invalid", Detail: "That code isn't right."}
	}
	codes, hashes, err := NewRecoveryCodes()
	if err != nil {
		return nil, err
	}
	now := a.Now().UTC()
	_, audit, err := userAudit(ctx, "user.mfa_enabled", "user:"+u.ID.String())
	if err != nil {
		return nil, err
	}
	if err := a.Store.ConfirmMFA(ctx, u.TenantID, u.ID, hashes, step, now, audit); err != nil {
		return nil, err
	}
	if err := a.promotePending(ctx, now); err != nil {
		return nil, err
	}
	return codes, nil
}

// promotePending turns the request's session, if it was pending its
// second step's setup, into a whole one that has just proved identity.
func (a *Accounts) promotePending(ctx context.Context, now time.Time) error {
	sess, ok := SessionFromContext(ctx)
	if !ok || sess.MFAVerified {
		return nil
	}
	if err := a.Store.PromoteSession(ctx, sess.ID); err != nil {
		return err
	}
	return a.Store.ConfirmSession(ctx, sess.ID, now)
}

// checkTOTPOnly checks code against the pending (unconfirmed) enrollment
// secret, never the active one: a code from an old, already-confirmed
// authenticator app must not be able to confirm a new enrollment it was
// never shown.
func (a *Accounts) checkTOTPOnly(u User, code string) (int64, bool, error) {
	secret, err := a.Sealer.Open(mfaRowID(u.ID), u.MFAPendingSecretEnc)
	if err != nil {
		return 0, false, fmt.Errorf("opening pending MFA secret: %w", err)
	}
	step, ok := MatchTOTPCode(secret, code, a.Now())
	return step, ok, nil
}

// Confirm is POST /session/confirm: a fresh proof of identity for "confirm
// it's you" actions (docs/ADMIN.md §7), without ending this or any other
// session. Requires the current password and, for an account with an
// authenticator enrolled, its code too (a password-only admin isn't asked
// for one: that's their own choice, §5).
func (a *Accounts) Confirm(ctx context.Context, password, code string) error {
	caller, ok := PrincipalFromContext(ctx)
	if !ok {
		return errNoPrincipalAuth
	}
	if caller.Type != TypeUser {
		return notASession()
	}
	if caller.Pending {
		return mfaVerifyFirst()
	}
	sess, ok := SessionFromContext(ctx)
	if !ok {
		return notASession()
	}
	uid, err := uuid.Parse(caller.ID)
	if err != nil {
		return err
	}
	u, err := a.Store.User(ctx, caller.TenantID, uid)
	if err != nil {
		return err
	}
	now := a.Now().UTC()
	ipKey := IPKey(ClientIPFromContext(ctx))
	if a.Failures != nil && a.Failures.Exhausted(ipKey, now) {
		return tooManyFailuresErr()
	}
	// Company sign-in (ADR-052) stands in for the password: a session that
	// just came back from its provider only owes the second step's code.
	company := password == "" && a.companyFlows.proven(sess.ID, now, false)
	if !company {
		if !u.HasPassword() {
			return &apihttp.Error{Status: http.StatusBadRequest, Code: "password_not_set",
				Detail: "You don't have a password. Use your passkey or company account to confirm."}
		}
		if required, err := a.companyRequiredFor(ctx, u); err != nil {
			return err
		} else if required {
			return errCompanyRequired
		}
		if !VerifyPassword(u.PasswordHash, password) {
			if a.Failures != nil {
				a.Failures.Allow(ipKey, now)
			}
			return &apihttp.Error{Status: http.StatusUnauthorized, Code: "password_invalid", Detail: "Your password is incorrect."}
		}
	}
	if !u.MFAEnabled && u.PasskeyCount > 0 && strings.TrimSpace(code) == "" {
		// Signing in takes the password and the passkey; confirming mustn't
		// take less. A recovery code in code still works.
		return &apihttp.Error{Status: http.StatusBadRequest, Code: "passkey_required",
			Detail: "Use your passkey to confirm, or type one of your recovery codes."}
	}
	if u.HasSecondStep() {
		method, err := a.checkMFACode(ctx, u, code, now)
		if err != nil {
			return err
		}
		if method == codeInvalid || method == codeUsed {
			if a.Failures != nil {
				a.Failures.Allow(ipKey, now)
			}
			if method == codeUsed {
				return &apihttp.Error{Status: http.StatusUnauthorized, Code: "mfa_code_used",
					Detail: "That code was already used. Wait for the next one in your app."}
			}
			return &apihttp.Error{Status: http.StatusUnauthorized, Code: "mfa_code_invalid", Detail: "That code isn't right."}
		}
	}
	if company {
		a.companyFlows.proven(sess.ID, now, true)
	}
	return a.Store.ConfirmSession(ctx, sess.ID, now)
}

// ResetMFA turns a person's authenticator and passkeys off and ends every
// session of theirs, for when they've lost their second step (docs/ADMIN.md
// §7): "Reset authenticator" in People. Needs a fresh "confirm it's you"
// and users:write.
func (a *Accounts) ResetMFA(ctx context.Context, id uuid.UUID) (User, error) {
	if err := RequireConfirmed(ctx, a.Now()); err != nil {
		return User{}, err
	}
	caller, audit, err := userAudit(ctx, "user.mfa_reset", "user:"+id.String())
	if err != nil {
		return User{}, err
	}
	target, err := a.Store.User(ctx, caller.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		return User{}, notFound("person")
	}
	if err != nil {
		return User{}, err
	}
	if e := beyondCaller(caller, target); e != nil {
		return User{}, e
	}
	now := a.Now().UTC()
	out, err := a.Store.ResetMFA(ctx, caller.TenantID, id, now, audit)
	if errors.Is(err, ErrNotFound) {
		return User{}, notFound("person")
	}
	if err != nil {
		return User{}, err
	}
	if err := a.Store.RevokeUserSessions(ctx, id, now); err != nil {
		return out, err
	}
	a.sessionsEnded(ctx, id, nil)
	return out, nil
}

// ChangePassword changes the caller's own password after checking their
// current one, and ends every session of theirs, including this one
// (docs/WEB.md §4).
func (a *Accounts) ChangePassword(ctx context.Context, currentPassword, newPassword string) error {
	caller, ok := PrincipalFromContext(ctx)
	if !ok {
		return errNoPrincipalAuth
	}
	if caller.Type != TypeUser {
		return notASession()
	}
	if caller.Pending {
		return mfaVerifyFirst()
	}
	uid, err := uuid.Parse(caller.ID)
	if err != nil {
		return err
	}
	u, err := a.Store.User(ctx, caller.TenantID, uid)
	if err != nil {
		return err
	}
	now := a.Now().UTC()
	if !u.HasPassword() {
		return a.addPassword(ctx, u, newPassword, now)
	}
	// A wrong current password draws on the same per-address budget as a
	// wrong sign-in, so a borrowed session can't be used to guess it.
	ipKey := IPKey(ClientIPFromContext(ctx))
	if a.Failures != nil && a.Failures.Exhausted(ipKey, now) {
		return tooManyFailuresErr()
	}
	if !VerifyPassword(u.PasswordHash, currentPassword) {
		if a.Failures != nil {
			a.Failures.Allow(ipKey, now)
		}
		return &apihttp.Error{Status: http.StatusUnauthorized, Code: "password_invalid", Detail: "Your current password is incorrect."}
	}
	if err := CheckPasswordPolicy(newPassword); err != nil {
		return badRequest("password_invalid", err.Error())
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	_, audit, err := userAudit(ctx, "user.password_changed", "user:"+u.ID.String())
	if err != nil {
		return err
	}
	if err := a.Store.SetPassword(ctx, u.TenantID, u.ID, hash, now, true, audit); err != nil {
		return err
	}
	a.sessionsEnded(ctx, u.ID, nil)
	return nil
}

// addPassword gives a passkey-only account a password ("Add a password" in
// My account, docs/ui/ADMIN_SCREENS_PHASE1E.md §11). There's no current
// password to check, so it needs a fresh "confirm it's you" (their
// passkey) instead. Nothing that worked stops working, so no session ends.
func (a *Accounts) addPassword(ctx context.Context, u User, newPassword string, now time.Time) error {
	if err := RequireConfirmed(ctx, now); err != nil {
		return err
	}
	if err := CheckPasswordPolicy(newPassword); err != nil {
		return badRequest("password_invalid", err.Error())
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	_, audit, err := userAudit(ctx, "user.password_added", "user:"+u.ID.String())
	if err != nil {
		return err
	}
	return a.Store.SetPassword(ctx, u.TenantID, u.ID, hash, now, false, audit)
}

// SignOut revokes the caller's session (docs/WEB.md §4).
func (a *Accounts) SignOut(ctx context.Context) error {
	sess, ok := SessionFromContext(ctx)
	if !ok {
		return notASession()
	}
	if err := a.Store.RevokeSession(ctx, sess.ID, a.Now().UTC()); err != nil {
		return err
	}
	a.sessionsEnded(ctx, sess.UserID, &sess.ID)
	return nil
}
