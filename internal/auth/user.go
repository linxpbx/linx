package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// User is a person who can sign in (docs/WEB.md §4). PasswordHash is an
// Argon2id hash (password.go); MFASecretEnc is the confirmed, in-use TOTP
// secret sealed with ADR-030's key, set only once a code from it has been
// checked; MFAPendingSecretEnc holds an enrollment in progress separately,
// so starting (or restarting) enrollment never disturbs an
// already-confirmed secret until the new one is confirmed in turn.
// RecoveryCodeHashes are SHA-256, one per unused recovery code.
//
// PasswordHash is empty for a person who set up their account with a
// passkey and hasn't added a password (docs/ADMIN.md §5).
type User struct {
	ID, TenantID uuid.UUID
	Email, Name  string
	Role         string
	// ExtensionID is the extension this person answers on the web client
	// (docs/WEB.md §5); not every account needs one.
	ExtensionID       *uuid.UUID
	PasswordHash      string
	PasswordUpdatedAt time.Time

	MFASecretEnc        []byte
	MFAPendingSecretEnc []byte
	MFAEnabled          bool
	RecoveryCodeHashes  [][]byte
	// MFALastStep is the TOTP step of the last authenticator code accepted
	// (migration 0015): each code works once.
	MFALastStep *int64
	// PasskeyCount is how many passkeys the person has (read-only here:
	// counted from user_passkey). PasswordOnlyAcceptedAt is set when an
	// admin chose to sign in with a password only and accepted the warning
	// (docs/ADMIN.md §5, migration 0022).
	PasskeyCount           int
	PasswordOnlyAcceptedAt *time.Time
	// CompanyLogins names the company sign-in providers the person has
	// linked an account on (read-only here: from user_sso_link, ADR-052).
	CompanyLogins []string

	// FailedAttempts and LockedUntil are per-account lockout (docs/WEB.md
	// §4); FailureWindowStart/-Count are the separate rolling hour the
	// "someone is guessing passwords" alert watches.
	FailedAttempts     int
	LockedUntil        *time.Time
	FailureWindowStart *time.Time
	FailureWindowCount int

	DisabledAt *time.Time
	// Presence is the person's chosen status: available, away or dnd
	// (migration 0014). Read-only here; pbx.Team sets it.
	Presence             string
	Version              int
	CreatedAt, UpdatedAt time.Time
}

// SetupLink is a one-time link a new person uses to pick their password
// (docs/WEB.md §4), or an emailed password-reset link (ADR-067): Purpose
// says which, and neither works as the other.
type SetupLink struct {
	ID, TenantID, UserID uuid.UUID
	TokenHash            []byte
	CreatedAt, ExpiresAt time.Time
	UsedAt               *time.Time
	Purpose              string
}

// Link purposes.
const (
	LinkSetup = "setup"
	LinkReset = "reset"
)

// purpose is l's purpose, a set-password link's when none was stored.
func (l SetupLink) purpose() string {
	if l.Purpose == "" {
		return LinkSetup
	}
	return l.Purpose
}

// SetupLinkTTL is how long a set-password link works before it must be
// reissued (docs/WEB.md §4).
const SetupLinkTTL = 24 * time.Hour

// ResetLinkTTL is how long an emailed password-reset link works (ADR-067).
const ResetLinkTTL = 30 * time.Minute

// UserStore is the database access people accounts and sessions need
// (internal/store implements it).
type UserStore interface {
	CreateUser(ctx context.Context, u User, audit AuditEntry) error
	User(ctx context.Context, tenant, id uuid.UUID) (User, error)
	UserByEmail(ctx context.Context, tenant uuid.UUID, email string) (User, error)
	ListUsers(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]User, error)
	// UpdateUser saves u if its stored version is still u.Version and
	// returns it with the new version (ErrVersionChanged otherwise). Used
	// for admin edits (name, role, extension); not for the system
	// bookkeeping below, which has its own narrower methods so it never
	// collides with a concurrent admin edit's version check.
	UpdateUser(ctx context.Context, u User, audit AuditEntry) (User, error)
	// DisableUser stops a person signing in and revokes every session of
	// theirs, in one transaction (docs/WEB.md §4).
	DisableUser(ctx context.Context, tenant, id uuid.UUID, at time.Time, audit AuditEntry) (User, error)

	// SetPassword changes the password hash and, if revokeSessions, ends
	// every session of the account first (docs/WEB.md §4: changing the
	// password ends the person's sessions).
	SetPassword(ctx context.Context, tenant, user uuid.UUID, passwordHash string, at time.Time, revokeSessions bool, audit AuditEntry) error
	// SetMFASecret starts (or restarts) enrollment: the secret is stored as
	// the *pending* secret, never touching an already-confirmed one.
	SetMFASecret(ctx context.Context, tenant, user uuid.UUID, sealedSecret []byte, at time.Time) error
	// ConfirmMFA promotes the pending secret to the confirmed one, turns MFA
	// on and replaces the recovery codes, once a code from the pending
	// secret has been checked; totpStep is that code's step, so it can't be
	// used again to sign in.
	ConfirmMFA(ctx context.Context, tenant, user uuid.UUID, recoveryHashes [][]byte, totpStep int64, at time.Time, audit AuditEntry) error
	// UseTOTPStep records step as the last authenticator code accepted, only
	// if it's later than the one before; false means the code (or an older
	// one) was already used. One statement, so two requests racing with the
	// same code can't both win.
	UseTOTPStep(ctx context.Context, tenant, user uuid.UUID, step int64) (bool, error)
	// ConsumeRecoveryCode removes one used recovery code; false if it was
	// already gone (two requests racing with the same code: one wins).
	ConsumeRecoveryCode(ctx context.Context, tenant, user uuid.UUID, hash []byte) (bool, error)
	// ResetMFA clears an account's authenticator (confirmed and pending),
	// recovery codes and step memory (docs/ADMIN.md §7 "Reset authenticator"):
	// they enroll again from scratch. Does not end sessions; the caller
	// does that (a plain password change doesn't need to).
	ResetMFA(ctx context.Context, tenant, user uuid.UUID, at time.Time, audit AuditEntry) (User, error)

	// AcceptPasswordOnly records an admin's choice to sign in with a
	// password only, warning accepted (docs/ADMIN.md §5).
	AcceptPasswordOnly(ctx context.Context, tenant, user uuid.UUID, at time.Time, audit AuditEntry) error

	// RecordLoginSuccess clears lockout and the guessing-password window.
	RecordLoginSuccess(ctx context.Context, tenant, user uuid.UUID, at time.Time) error
	// RecordLoginFailure applies lockout and the rolling window, returning
	// the wait the account is now under (nil if none) and whether the
	// window just crossed the alert threshold (docs/WEB.md §4).
	RecordLoginFailure(ctx context.Context, tenant, user uuid.UUID, at time.Time) (lockedUntil *time.Time, alertThreshold bool, err error)
	// RecordLockedAttempt counts a try made during the lockout wait in the
	// guessing-password window only (the wait itself doesn't grow), so the
	// alert still fires: the waits alone allow only about 10 counted
	// failures an hour.
	RecordLockedAttempt(ctx context.Context, tenant, user uuid.UUID, at time.Time) (alertThreshold bool, err error)

	// Audit writes one audit_log row on its own (sign-in attempts, which
	// change nothing else in the same transaction).
	Audit(ctx context.Context, e AuditEntry) error

	CreateSetupLink(ctx context.Context, l SetupLink) error
	SetupLinkByTokenHash(ctx context.Context, hash []byte) (SetupLink, error)
	// ConsumeSetupLink marks the link used, only if it's still unused and
	// unexpired at at; otherwise ErrNotFound (so two uses racing can't both
	// win).
	ConsumeSetupLink(ctx context.Context, id uuid.UUID, at time.Time) error

	CreateSession(ctx context.Context, s UserSession) error
	RevokeSession(ctx context.Context, id uuid.UUID, at time.Time) error
	// RevokeUserSessions ends every session of user (signing out,
	// disabling the person or changing the password all call this).
	RevokeUserSessions(ctx context.Context, user uuid.UUID, at time.Time) error
	// LiveUserSessions is every session of user not revoked or expired at
	// now, newest use first (My account → Signed-in browsers).
	LiveUserSessions(ctx context.Context, user uuid.UUID, now time.Time) ([]UserSession, error)
	// PromoteSession turns a pending session (still waiting on its
	// authenticator code or first-run MFA enrollment) into a full one.
	PromoteSession(ctx context.Context, id uuid.UUID) error

	SessionStore
}

// AlertFirer is the admin-alert access sign-in needs: the "someone is
// guessing a password" alert (docs/WEB.md §4, ADR-036), and the password
// reset ones (ADR-067), which are events, so announced once. *alert.Engine
// implements this.
type AlertFirer interface {
	Fire(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string) error
	Resolve(ctx context.Context, tenant uuid.UUID, key string) error
	// Announce tells the alert channels once; key must be new each time.
	Announce(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string) error
}

func loginGuessKey(user uuid.UUID) string { return "login_guessing:" + user.String() }

func mfaRowID(user uuid.UUID) string { return "app_user:mfa:" + user.String() }

// requiresMFA reports whether role must have a second step (a passkey or
// an authenticator app) to sign in, unless the person chose a password only
// (ADR-036: required for admin/system_admin, optional otherwise; password
// only allowed with a warning since 2026-09-27, docs/ADMIN.md §5).
func requiresMFA(role string) bool { return role == RoleAdmin || role == RoleSystemAdmin }

// HasSecondStep reports whether u signs in with more than a password: an
// authenticator app or at least one passkey. With one, a password alone is
// never enough to sign in (docs/ADMIN.md §5).
func (u User) HasSecondStep() bool { return u.MFAEnabled || u.PasskeyCount > 0 }

// HasPassword reports whether u has a password at all (a passkey-only
// account doesn't).
func (u User) HasPassword() bool { return u.PasswordHash != "" }

// needsSecondStepSetup reports whether u must set up a passkey or
// authenticator (or accept signing in with a password only) before a
// password sign-in counts: admins who have done neither.
func (u User) needsSecondStepSetup() bool {
	return requiresMFA(u.Role) && !u.HasSecondStep() && u.PasswordOnlyAcceptedAt == nil
}

// SecondStepMethods lists what can finish u's sign-in after the password:
// "authenticator", "recovery_code", "passkey".
func (u User) SecondStepMethods() []string {
	var m []string
	if u.MFAEnabled {
		m = append(m, "authenticator")
	}
	if u.PasskeyCount > 0 {
		m = append(m, "passkey")
	}
	if len(u.RecoveryCodeHashes) > 0 {
		m = append(m, "recovery_code")
	}
	return m
}

// SessionOutcome is a new or promoted session, and the raw cookie values
// shown to the browser once.
type SessionOutcome struct {
	Session UserSession
	Token   string
	CSRF    string
	// Status is "signed_in", "mfa_verify_required" (a second step is needed
	// for an account that has one) or "mfa_setup_required" (an admin must
	// set one up, or choose a password only, before doing anything else).
	Status string
	// Methods is what can finish a "mfa_verify_required" sign-in
	// (User.SecondStepMethods).
	Methods []string
	// RecoveryCodes are shown once, when a passkey set up through a setup
	// link is the account's first second step.
	RecoveryCodes []string
}

func trimUserAgent(s string) string {
	const max = 200
	if len(s) > max {
		return s[:max]
	}
	return s
}
