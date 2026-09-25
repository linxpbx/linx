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
// (docs/WEB.md §4).
type SetupLink struct {
	ID, TenantID, UserID uuid.UUID
	TokenHash            []byte
	CreatedAt, ExpiresAt time.Time
	UsedAt               *time.Time
}

// SetupLinkTTL is how long a set-password link works before it must be
// reissued (docs/WEB.md §4).
const SetupLinkTTL = 24 * time.Hour

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
	// secret has been checked.
	ConfirmMFA(ctx context.Context, tenant, user uuid.UUID, recoveryHashes [][]byte, at time.Time, audit AuditEntry) error
	// ConsumeRecoveryCode removes one used recovery code.
	ConsumeRecoveryCode(ctx context.Context, tenant, user uuid.UUID, hash []byte) error

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
	// PromoteSession turns a pending session (still waiting on its
	// authenticator code or first-run MFA enrollment) into a full one.
	PromoteSession(ctx context.Context, id uuid.UUID) error

	SessionStore
}

// AlertFirer is the admin-alert access sign-in needs: the "someone is
// guessing a password" alert (docs/WEB.md §4, ADR-036). *alert.Engine
// implements this.
type AlertFirer interface {
	Fire(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string) error
	Resolve(ctx context.Context, tenant uuid.UUID, key string) error
}

func loginGuessKey(user uuid.UUID) string { return "login_guessing:" + user.String() }

func mfaRowID(user uuid.UUID) string { return "app_user:mfa:" + user.String() }

// requiresMFA reports whether role must have MFA turned on to sign in
// (ADR-036: required for admin/system_admin, optional otherwise).
func requiresMFA(role string) bool { return role == RoleAdmin || role == RoleSystemAdmin }

// SessionOutcome is a new or promoted session, and the raw cookie values
// shown to the browser once.
type SessionOutcome struct {
	Session UserSession
	Token   string
	CSRF    string
	// Status is "signed_in", "mfa_verify_required" (an authenticator code
	// is needed for an already-enrolled account) or "mfa_setup_required"
	// (the account must enroll before it can do anything else).
	Status string
}

func trimUserAgent(s string) string {
	const max = 200
	if len(s) > max {
		return s[:max]
	}
	return s
}
