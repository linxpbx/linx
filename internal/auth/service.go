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
	now := a.Now().UTC()
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" || len([]rune(name)) > 100 {
			return User{}, badRequest("name_invalid", "Give a name of 1 to 100 characters.")
		}
		u.Name = name
	}
	if patch.Role != nil {
		if !ValidRole(*patch.Role) {
			return User{}, badRequest("role_invalid", fmt.Sprintf("%q is not a role. Roles: %s.", *patch.Role, strings.Join(Roles, ", ")))
		}
		if !CanGrantRole(caller.Role, *patch.Role) {
			return User{}, &apihttp.Error{Status: http.StatusForbidden, Code: "role_exceeds_caller",
				Detail: fmt.Sprintf("You can't give the %s role.", *patch.Role)}
		}
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
	if disabling {
		if err := a.Store.RevokeUserSessions(ctx, id, now); err != nil {
			return out, err
		}
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
	u, err := a.Store.DisableUser(ctx, caller.TenantID, id, a.Now().UTC(), audit)
	if errors.Is(err, ErrNotFound) {
		return User{}, notFound("person")
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
		if alertThreshold && a.Alerts != nil {
			_ = a.Alerts.Fire(ctx, tenant, loginGuessKey(u.ID), "warning", "Someone is guessing a password",
				fmt.Sprintf("More than 20 failed sign-ins for %s in the last hour.", u.Email), "")
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

// CompleteSetup is a new person's first sign-in through their one-time link
// (docs/WEB.md §4): they pick a password, and are signed in at once (still
// pending MFA enrollment if their role requires it).
func (a *Accounts) CompleteSetup(ctx context.Context, linkToken, password string, ip netip.Addr, userAgent string) (SessionOutcome, error) {
	if err := CheckPasswordPolicy(password); err != nil {
		return SessionOutcome{}, badRequest("password_invalid", err.Error())
	}
	link, err := a.Store.SetupLinkByTokenHash(ctx, HashSecret(linkToken))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return SessionOutcome{}, invalidLink()
		}
		return SessionOutcome{}, err
	}
	now := a.Now().UTC()
	if link.UsedAt != nil || !now.Before(link.ExpiresAt) {
		return SessionOutcome{}, invalidLink()
	}
	u, err := a.Store.User(ctx, link.TenantID, link.UserID)
	if err != nil {
		return SessionOutcome{}, err
	}
	if u.DisabledAt != nil {
		return SessionOutcome{}, invalidLink()
	}
	hash, err := HashPassword(password)
	if err != nil {
		return SessionOutcome{}, err
	}
	if err := a.Store.SetPassword(ctx, u.TenantID, u.ID, hash, now, false, AuditEntry{
		TenantID: &u.TenantID, Actor: "user:" + u.ID.String(), IP: ip, Action: "user.password_set",
		Target: "user:" + u.ID.String(), Result: ResultOK,
	}); err != nil {
		return SessionOutcome{}, err
	}
	if err := a.Store.ConsumeSetupLink(ctx, link.ID, now); err != nil {
		return SessionOutcome{}, err
	}
	mfaVerified := !requiresMFA(u.Role)
	sess, token, csrf, err := a.newSession(ctx, u, mfaVerified, ip, userAgent, now)
	if err != nil {
		return SessionOutcome{}, err
	}
	status := "signed_in"
	if !mfaVerified {
		status = "mfa_setup_required"
	}
	return SessionOutcome{Session: sess, Token: token, CSRF: csrf, Status: status}, nil
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
	u, err := a.Store.User(ctx, sess.TenantID, sess.UserID)
	if err != nil {
		return err
	}
	if !u.MFAEnabled || len(u.MFASecretEnc) == 0 {
		return badRequest("mfa_not_enrolled", "Set up an authenticator app first: POST /me/mfa.")
	}
	ok, err = a.checkMFACode(ctx, u, code)
	if err != nil {
		return err
	}
	if !ok {
		return &apihttp.Error{Status: http.StatusUnauthorized, Code: "mfa_code_invalid", Detail: "That code isn't right."}
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
	if len(u.MFASecretEnc) == 0 {
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

func (a *Accounts) checkTOTPOnly(u User, code string) (bool, error) {
	secret, err := a.Sealer.Open(mfaRowID(u.ID), u.MFASecretEnc)
	if err != nil {
		return false, fmt.Errorf("opening MFA secret: %w", err)
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
	uid, err := uuid.Parse(caller.ID)
	if err != nil {
		return err
	}
	u, err := a.Store.User(ctx, caller.TenantID, uid)
	if err != nil {
		return err
	}
	if !VerifyPassword(u.PasswordHash, currentPassword) {
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
	return a.Store.SetPassword(ctx, u.TenantID, u.ID, hash, a.Now().UTC(), true, audit)
}

// SignOut revokes the caller's session (docs/WEB.md §4).
func (a *Accounts) SignOut(ctx context.Context) error {
	sess, ok := SessionFromContext(ctx)
	if !ok {
		return notASession()
	}
	return a.Store.RevokeSession(ctx, sess.ID, a.Now().UTC())
}
