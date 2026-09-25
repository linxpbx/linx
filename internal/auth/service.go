package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

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
func (a *Accounts) UpdateUser(ctx context.Context, id uuid.UUID, patch UserPatch) (User, error) {
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
	if e := beyondCaller(caller, u); e != nil {
		return User{}, e
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
	if err := a.Store.CreateSession(ctx, s); err != nil {
		return UserSession{}, "", "", err
	}
	return s, token, csrf, nil
}

func statusFor(mfaVerified, mfaEnabled bool) string {
	if mfaVerified {
		return "signed_in"
	}
	if mfaEnabled {
		return "mfa_verify_required"
	}
	return "mfa_setup_required"
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
			return SessionOutcome{}, errSignInInvalid
		}
		return SessionOutcome{}, err
	}
	if u.DisabledAt != nil {
		VerifyPassword(unknownUserHash, password)
		if a.Failures != nil {
			a.Failures.Allow(ipKey, now)
		}
		return SessionOutcome{}, errSignInInvalid
	}
	if u.LockedUntil != nil && now.Before(*u.LockedUntil) {
		a.lockedAttempt(ctx, tenant, u, ipKey, now)
		return SessionOutcome{}, lockedError(*u.LockedUntil)
	}
	if !VerifyPassword(u.PasswordHash, password) {
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
		if lockedUntil != nil {
			return SessionOutcome{}, lockedError(*lockedUntil)
		}
		return SessionOutcome{}, errSignInInvalid
	}
	if err := a.Store.RecordLoginSuccess(ctx, tenant, u.ID, now); err != nil {
		return SessionOutcome{}, err
	}
	if a.Alerts != nil {
		_ = a.Alerts.Resolve(ctx, tenant, loginGuessKey(u.ID))
	}
	mfaVerified := !u.MFAEnabled && !requiresMFA(u.Role)
	sess, token, csrf, err := a.newSession(ctx, u, mfaVerified, ip, userAgent, now)
	if err != nil {
		return SessionOutcome{}, err
	}
	return SessionOutcome{Session: sess, Token: token, CSRF: csrf, Status: statusFor(mfaVerified, u.MFAEnabled)}, nil
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
// the page can say it can't before asking for a password.
func (a *Accounts) CheckSetupLink(ctx context.Context, token string, ip netip.Addr) error {
	_, _, err := a.usableSetupLink(ctx, token, ip, a.Now().UTC())
	return err
}

// CompleteSetup is a new person's first sign-in through their one-time link
// (docs/WEB.md §4), or an existing person's password reset: they pick a
// password, any other session of theirs ends, and they're signed in at once
// (still pending MFA enrollment if their role requires it, or their
// authenticator code if they already have one: a link never skips it).
func (a *Accounts) CompleteSetup(ctx context.Context, linkToken, password string, ip netip.Addr, userAgent string) (SessionOutcome, error) {
	if err := CheckPasswordPolicy(password); err != nil {
		return SessionOutcome{}, badRequest("password_invalid", err.Error())
	}
	now := a.Now().UTC()
	link, u, err := a.usableSetupLink(ctx, linkToken, ip, now)
	if err != nil {
		return SessionOutcome{}, err
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
	mfaVerified := !u.MFAEnabled && !requiresMFA(u.Role)
	sess, token, csrf, err := a.newSession(ctx, u, mfaVerified, ip, userAgent, now)
	if err != nil {
		return SessionOutcome{}, err
	}
	return SessionOutcome{Session: sess, Token: token, CSRF: csrf, Status: statusFor(mfaVerified, u.MFAEnabled)}, nil
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
	if !u.MFAEnabled || len(u.MFASecretEnc) == 0 {
		return badRequest("mfa_not_enrolled", "Set up an authenticator app first: POST /me/mfa.")
	}
	if u.LockedUntil != nil && now.Before(*u.LockedUntil) {
		a.lockedAttempt(ctx, sess.TenantID, u, ipKey, now)
		return lockedError(*u.LockedUntil)
	}
	ok, err = a.checkMFACode(ctx, u, code)
	if err != nil {
		return err
	}
	if !ok {
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
		if lockedUntil != nil {
			return lockedError(*lockedUntil)
		}
		return &apihttp.Error{Status: http.StatusUnauthorized, Code: "mfa_code_invalid", Detail: "That code isn't right."}
	}
	if err := a.Store.RecordLoginSuccess(ctx, sess.TenantID, u.ID, now); err != nil {
		return err
	}
	return a.Store.PromoteSession(ctx, sess.ID)
}

func (a *Accounts) checkMFACode(ctx context.Context, u User, code string) (bool, error) {
	secret, err := a.Sealer.Open(mfaRowID(u.ID), u.MFASecretEnc)
	if err != nil {
		return false, fmt.Errorf("opening MFA secret: %w", err)
	}
	if ValidTOTPCode(secret, code, a.Now()) {
		return true, nil
	}
	norm := normalizeRecoveryCode(code)
	for _, stored := range u.RecoveryCodeHashes {
		if SecretMatches(stored, norm) {
			return true, a.Store.ConsumeRecoveryCode(ctx, u.TenantID, u.ID, stored)
		}
	}
	return false, nil
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
	if caller.Pending && u.MFAEnabled {
		return "", "", mfaVerifyFirst()
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
	if caller.Pending && u.MFAEnabled {
		return nil, mfaVerifyFirst()
	}
	if len(u.MFAPendingSecretEnc) == 0 {
		return nil, badRequest("mfa_not_started", "Start enrollment first: POST /me/mfa.")
	}
	ok2, err := a.checkTOTPOnly(u, code)
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
	if err := a.Store.ConfirmMFA(ctx, u.TenantID, u.ID, hashes, now, audit); err != nil {
		return nil, err
	}
	if sess, ok := SessionFromContext(ctx); ok && !sess.MFAVerified {
		if err := a.Store.PromoteSession(ctx, sess.ID); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

// checkTOTPOnly checks code against the pending (unconfirmed) enrollment
// secret, never the active one: a code from an old, already-confirmed
// authenticator app must not be able to confirm a new enrollment it was
// never shown.
func (a *Accounts) checkTOTPOnly(u User, code string) (bool, error) {
	secret, err := a.Sealer.Open(mfaRowID(u.ID), u.MFAPendingSecretEnc)
	if err != nil {
		return false, fmt.Errorf("opening pending MFA secret: %w", err)
	}
	return ValidTOTPCode(secret, code, a.Now()), nil
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
	// A wrong current password draws on the same per-address budget as a
	// wrong sign-in, so a borrowed session can't be used to guess it.
	now := a.Now().UTC()
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
