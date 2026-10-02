package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
)

// "Forgot your password?" (ADR-067, docs/PHASE1F.md §5): an emailed link
// that works once, for 30 minutes. Asking for one always gets the same
// answer, as fast, whether or not the email has an account. The new
// password only replaces the old one once the person's second step
// (authenticator code, recovery code or passkey) has been checked as well,
// so someone who only got into the mailbox changes nothing. A lost
// authenticator is never reset by email (`sudo linx user reset-2fa`).

// Mailer sends the emails accounts need (ADR-067). The control plane's
// is built on internal/email (which imports this package, hence the
// interface).
type Mailer interface {
	// On reports whether email is set up and turned on: the sign-in page
	// offers "Forgot your password?" only then.
	On(ctx context.Context, tenant uuid.UUID) (bool, error)
	// ResetLink queues the reset link's email to u.
	ResetLink(ctx context.Context, u User, token string) error
	// PasswordChanged queues "Your Linx password was changed" to u.
	PasswordChanged(ctx context.Context, u User, at time.Time, ip netip.Addr, userAgent string) error
}

// Reset limits (docs/PHASE1F.md §5).
const (
	ResetsPerEmail   = 3
	ResetsPerAddress = 10
	resetPeriod      = time.Hour
	// resetWorkTimeout bounds the work done after answering a request.
	resetWorkTimeout = 30 * time.Second
)

// Alert keys (announced, so each is made new with a time and id): one
// address asking for more resets than ResetsPerAddress in an hour (trying
// many accounts), and an admin with no second step resetting by email
// (owner decision 2026-09-30, SCREENS_PHASE1F §16 item 2: the email alone
// was enough, so the other admins hear about it).
const (
	resetManyPrefix  = "password_reset_many:"
	resetAdminPrefix = "password_reset_admin:"
	askAdminPrefix   = "second_step_reset_asked:"
)

// resetLimits are the in-memory reset budgets (like the other limiters:
// per instance, ADR-027).
type resetLimits struct {
	once            sync.Once
	byEmail, byAddr *Limiters
	alerted         *Limiters
	asked           *Limiters
}

func (r *resetLimits) init() {
	r.once.Do(func() {
		r.byEmail = NewLimitersPer(ResetsPerEmail, resetPeriod)
		r.byAddr = NewLimitersPer(ResetsPerAddress, resetPeriod)
		r.alerted = NewLimitersPer(1, resetPeriod)
		r.asked = NewLimitersPer(1, resetPeriod)
	})
}

func resetOff() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusConflict, Code: "password_reset_off",
		Detail: "This Linx can't send email yet, so it can't send a reset link. Ask your admin to reset your password."}
}

func invalidResetLink() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusBadRequest, Code: "reset_link_invalid",
		Detail: "This link has already been used or is older than 30 minutes."}
}

// PasswordResetOffered reports whether the sign-in page shows "Forgot your
// password?": only when email is on.
func (a *Accounts) PasswordResetOffered(ctx context.Context, tenant uuid.UUID) bool {
	if a.Mailer == nil {
		return false
	}
	on, err := a.Mailer.On(ctx, tenant)
	return err == nil && on
}

// RequestPasswordReset is "Send the link". The answer never depends on
// whether the email has an account: the same nil, at once, while the
// lookup and the email happen afterwards (a.Later). Over the limits (3 an
// hour per email, 10 per address) nothing is sent, and still nothing
// different is said.
func (a *Accounts) RequestPasswordReset(ctx context.Context, tenant uuid.UUID, email string, ip netip.Addr) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) {
		return badRequest("email_invalid", "Type the email you sign in with.")
	}
	if !a.PasswordResetOffered(ctx, tenant) {
		return resetOff()
	}
	now := a.Now().UTC()
	a.resets.init()
	ipKey := IPKey(ip)
	if !a.resets.byAddr.Allow(ipKey, now) {
		a.later(ctx, func(ctx context.Context) { a.resetFlood(ctx, tenant, ip, now) })
		return nil
	}
	if !a.resets.byEmail.Allow(email, now) {
		a.later(ctx, func(ctx context.Context) {
			a.auditReset(ctx, tenant, nil, ip, "rate_limited")
		})
		return nil
	}
	a.later(ctx, func(ctx context.Context) { a.sendReset(ctx, tenant, email, ip) })
	return nil
}

// later runs work after the request has been answered, so how long it
// takes says nothing about the email.
func (a *Accounts) later(ctx context.Context, work func(context.Context)) {
	run := func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resetWorkTimeout)
		defer cancel()
		work(ctx)
	}
	if a.Later != nil {
		a.Later(run)
		return
	}
	go run()
}

func (a *Accounts) resetFlood(ctx context.Context, tenant uuid.UUID, ip netip.Addr, now time.Time) {
	a.auditReset(ctx, tenant, nil, ip, "rate_limited")
	// One alert an hour per address, however long it keeps trying.
	if a.Alerts == nil || !a.resets.alerted.Allow(IPKey(ip), now) {
		return
	}
	key := fmt.Sprintf("%s%s:%d", resetManyPrefix, IPKey(ip), now.Unix())
	if err := a.Alerts.Announce(ctx, tenant, key, "warning", "Someone is asking for many password resets",
		fmt.Sprintf("More than %d password-reset requests from %s in an hour. Nothing more is sent to that address's requests this hour; "+
			"each account also gets at most %d reset emails an hour.", ResetsPerAddress, ip, ResetsPerEmail),
		"/admin/system/activity"); err != nil {
		a.log().Error("firing the password-reset alert failed", "err", err)
	}
}

// sendReset makes and emails a reset link, if email belongs to an account
// that can use one: not disabled, and not one that must use company
// sign-in (it gets no link and no different answer).
func (a *Accounts) sendReset(ctx context.Context, tenant uuid.UUID, email string, ip netip.Addr) {
	u, err := a.Store.UserByEmail(ctx, tenant, email)
	if errors.Is(err, ErrNotFound) {
		a.auditReset(ctx, tenant, nil, ip, "unknown_email")
		return
	}
	if err != nil {
		a.log().Error("password reset: finding the account failed", "err", err)
		return
	}
	if u.DisabledAt != nil {
		a.auditReset(ctx, tenant, &u, ip, "disabled")
		return
	}
	if required, err := a.companyRequiredFor(ctx, u); err != nil {
		a.log().Error("password reset: checking company sign-in failed", "err", err)
		return
	} else if required {
		a.auditReset(ctx, tenant, &u, ip, "company_sign_in_required")
		return
	}
	id, err := uuid.NewV7()
	if err != nil {
		return
	}
	token := NewSecret()
	now := a.Now().UTC()
	link := SetupLink{ID: id, TenantID: u.TenantID, UserID: u.ID, TokenHash: HashSecret(token), CreatedAt: now,
		ExpiresAt: now.Add(ResetLinkTTL), Purpose: LinkReset}
	if err := a.Store.CreateSetupLink(ctx, link); err != nil {
		a.log().Error("password reset: saving the link failed", "err", err)
		return
	}
	if err := a.Mailer.ResetLink(ctx, u, token); err != nil {
		a.log().Error("password reset: queueing the email failed", "err", err)
		a.auditReset(ctx, tenant, &u, ip, "email_failed")
		return
	}
	a.auditReset(ctx, tenant, &u, ip, "")
}

// auditReset writes user.password_reset_requested: reason is empty when a
// link was emailed. Like sign-in, never an email that matched nobody.
func (a *Accounts) auditReset(ctx context.Context, tenant uuid.UUID, u *User, ip netip.Addr, reason string) {
	e := AuditEntry{TenantID: &tenant, Actor: "anonymous", IP: ip, Action: "user.password_reset_requested", Result: ResultOK}
	if u != nil {
		e.Target = "user:" + u.ID.String()
	}
	if reason != "" {
		e.Result, e.Detail = ResultDenied, map[string]any{"reason": reason}
	}
	if err := a.Store.Audit(ctx, e); err != nil {
		a.log().Error("audit log write failed", "action", e.Action, "err", err)
	}
}

// CheckResetLink reports whether a reset link can still be used, and its
// person: the page shows their email and asks for their second step.
func (a *Accounts) CheckResetLink(ctx context.Context, token string, ip netip.Addr) (User, error) {
	_, u, err := a.usableSetupLink(ctx, token, LinkReset, ip, a.Now().UTC())
	return u, err
}

// ResetProof is how the person finishes a reset: an authenticator or
// recovery code, or a passkey's answer (Ceremony is the cookie from
// BeginResetPasskey). Nothing for an account with no second step.
type ResetProof struct {
	Code       string
	Ceremony   string
	Credential []byte
}

// BeginResetPasskey asks for one of the link's person's own passkeys, as
// the second step of a reset.
func (a *Accounts) BeginResetPasskey(ctx context.Context, linkToken string, ip netip.Addr) (PasskeyCeremony, error) {
	if a.WebAuthn == nil {
		return PasskeyCeremony{}, passkeysOff()
	}
	link, u, err := a.usableSetupLink(ctx, linkToken, LinkReset, ip, a.Now().UTC())
	if err != nil {
		return PasskeyCeremony{}, err
	}
	keys, err := a.Passkeys.Passkeys(ctx, u.TenantID, u.ID)
	if err != nil {
		return PasskeyCeremony{}, err
	}
	if len(keys) == 0 {
		return PasskeyCeremony{}, badRequest("no_passkeys", "You don't have a passkey yet.")
	}
	opts, data, err := a.WebAuthn.BeginLogin(passkeyUser{u: u, keys: keys}, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return PasskeyCeremony{}, fmt.Errorf("starting passkey check: %w", err)
	}
	return a.begin(ceremony{purpose: purposeReset, tenant: u.TenantID, user: u.ID, link: link.ID, data: *data}, opts.Response)
}

// CompleteReset sets the new password from a reset link. For an account
// with a second step, proof must pass first: until it does, nothing
// changes and the link still works (wrong codes count towards the
// account's lockout, like signing in). Then the link is used up, every
// other session ends, the person is signed in and emailed that the
// password changed. An admin with no second step (a password only, by
// choice) is let in on the link alone, and the other admins are alerted.
func (a *Accounts) CompleteReset(ctx context.Context, linkToken, password string, proof ResetProof, ip netip.Addr, userAgent string) (SessionOutcome, error) {
	if err := CheckPasswordPolicy(password); err != nil {
		return SessionOutcome{}, badRequest("password_invalid", err.Error())
	}
	now := a.Now().UTC()
	link, u, err := a.usableSetupLink(ctx, linkToken, LinkReset, ip, now)
	if err != nil {
		return SessionOutcome{}, err
	}
	if required, err := a.companyRequiredFor(ctx, u); err != nil {
		return SessionOutcome{}, err
	} else if required {
		return SessionOutcome{}, errCompanyRequired
	}
	attempt := signInAttempt{tenant: u.TenantID, user: &u, ip: ip, code: true}
	if u.LockedUntil != nil && now.Before(*u.LockedUntil) {
		a.lockedAttempt(ctx, u.TenantID, u, IPKey(ip), now)
		attempt.reason, attempt.lockedUntil = "locked", u.LockedUntil
		a.auditSignIn(ctx, attempt)
		return SessionOutcome{}, lockedError(*u.LockedUntil)
	}
	method := "email_link"
	if u.HasSecondStep() {
		if method, err = a.resetProof(ctx, u, link, proof, attempt, now); err != nil {
			return SessionOutcome{}, err
		}
	}
	hash, err := HashPassword(password)
	if err != nil {
		return SessionOutcome{}, err
	}
	// Claimed first, and only if still unused: two requests racing with the
	// same link can't both set a password.
	if err := a.Store.ConsumeSetupLink(ctx, link.ID, now); err != nil {
		if errors.Is(err, ErrNotFound) {
			return SessionOutcome{}, invalidResetLink()
		}
		return SessionOutcome{}, err
	}
	actor := "user:" + u.ID.String()
	if err := a.Store.SetPassword(ctx, u.TenantID, u.ID, hash, now, true, AuditEntry{
		TenantID: &u.TenantID, Actor: actor, IP: ip, Action: "user.password_reset", Target: actor, Result: ResultOK,
		Detail: map[string]any{"method": method},
	}); err != nil {
		return SessionOutcome{}, err
	}
	a.sessionsEnded(ctx, u.ID, nil)
	if err := a.Store.RecordLoginSuccess(ctx, u.TenantID, u.ID, now); err != nil {
		return SessionOutcome{}, err
	}
	if a.Alerts != nil {
		_ = a.Alerts.Resolve(ctx, u.TenantID, loginGuessKey(u.ID))
	}
	var out SessionOutcome
	if u.HasSecondStep() {
		sess, token, csrf, err := a.newSession(ctx, u, true, ip, userAgent, now)
		if err != nil {
			return SessionOutcome{}, err
		}
		out = SessionOutcome{Session: sess, Token: token, CSRF: csrf, Status: "signed_in"}
		attempt.method = method
		a.auditSignIn(ctx, attempt)
	} else if out, err = a.passwordOutcome(ctx, u, ip, userAgent, now); err != nil {
		return SessionOutcome{}, err
	}
	a.passwordChangedEmail(ctx, u, now, ip, userAgent)
	if requiresMFA(u.Role) && !u.HasSecondStep() && a.Alerts != nil {
		if err := a.Alerts.Announce(ctx, u.TenantID, fmt.Sprintf("%s%s:%d", resetAdminPrefix, u.ID, now.Unix()), "warning",
			"An admin reset their password by email",
			fmt.Sprintf("%s (%s) reset their password with an emailed link from %s. Their account has no passkey or authenticator app, "+
				"so the email alone was enough. If that wasn't them, disable their account in People now.", u.Name, u.Email, ip),
			"/admin/people"); err != nil {
			a.log().Error("firing the admin password-reset alert failed", "err", err)
		}
	}
	return out, nil
}

// resetProof checks a reset's second step and returns how it was done.
func (a *Accounts) resetProof(ctx context.Context, u User, link SetupLink, proof ResetProof, attempt signInAttempt, now time.Time) (string, error) {
	ipKey := IPKey(attempt.ip)
	if a.Failures != nil && a.Failures.Exhausted(ipKey, now) {
		return "", tooManyFailuresErr()
	}
	if len(proof.Credential) > 0 {
		if a.WebAuthn == nil {
			return "", passkeysOff()
		}
		cer, ok := a.ceremonies.take(proof.Ceremony, now)
		if !ok || cer.purpose != purposeReset || cer.link != link.ID || cer.user != u.ID {
			return "", passkeyExpired()
		}
		parsed, err := protocol.ParseCredentialRequestResponseBytes(proof.Credential)
		if err != nil {
			a.passkeyFailure(ctx, attempt.ip)
			return "", passkeyRefused()
		}
		keys, err := a.Passkeys.Passkeys(ctx, u.TenantID, u.ID)
		if err != nil {
			return "", err
		}
		cred, err := a.WebAuthn.ValidateLogin(passkeyUser{u: u, keys: keys}, cer.data, parsed)
		if err != nil {
			a.passkeyFailure(ctx, attempt.ip)
			attempt.reason = "passkey_refused"
			a.auditSignIn(ctx, attempt)
			return "", passkeyRefused()
		}
		for _, k := range keys {
			if string(k.CredentialID) == string(cred.ID) {
				if err := a.usedPasskey(ctx, u, k, cred, now); err != nil {
					return "", err
				}
			}
		}
		return "passkey", nil
	}
	if strings.TrimSpace(proof.Code) == "" {
		return "", &apihttp.Error{Status: http.StatusBadRequest, Code: "second_step_required",
			Detail: "To finish, confirm with your authenticator app or passkey."}
	}
	method, err := a.checkMFACode(ctx, u, proof.Code, now)
	if err != nil {
		return "", err
	}
	if method == codeInvalid || method == codeUsed {
		if a.Failures != nil {
			a.Failures.Allow(ipKey, now)
		}
		lockedUntil, alertThreshold, ferr := a.Store.RecordLoginFailure(ctx, u.TenantID, u.ID, now)
		if ferr != nil {
			return "", unavailableErr()
		}
		if alertThreshold {
			a.fireGuessing(ctx, u.TenantID, u)
		}
		attempt.reason, attempt.lockedUntil = string(method), lockedUntil
		a.auditSignIn(ctx, attempt)
		if lockedUntil != nil {
			return "", lockedError(*lockedUntil)
		}
		if method == codeUsed {
			return "", &apihttp.Error{Status: http.StatusUnauthorized, Code: "mfa_code_used",
				Detail: "That code was already used. Wait for the next one in your app."}
		}
		return "", &apihttp.Error{Status: http.StatusUnauthorized, Code: "mfa_code_invalid", Detail: "That code isn't right."}
	}
	return string(method), nil
}

// passwordChangedEmail tells u their password changed, when email is on.
func (a *Accounts) passwordChangedEmail(ctx context.Context, u User, at time.Time, ip netip.Addr, userAgent string) {
	if !a.PasswordResetOffered(ctx, u.TenantID) {
		return
	}
	if err := a.Mailer.PasswordChanged(ctx, u, at, ip, userAgent); err != nil {
		a.log().Error("queueing the password-changed email failed", "err", err)
	}
}

// sessionUserAgent is the browser the request's session signed in from.
func sessionUserAgent(ctx context.Context) string {
	sess, _ := SessionFromContext(ctx)
	return sess.UserAgent
}

// AskSecondStepReset is "Ask my admin to reset it" on a reset link's second
// step (owner, 2026-10-02): someone who has lost their authenticator app,
// passkeys and recovery codes asks the admins, who check it's really them
// and choose Reset authenticator in People. Only the emailed link can ask,
// so nobody can ask for someone else without their mailbox; nothing about
// the account changes, and the link still works. The admins hear about it
// at most once an hour per person.
func (a *Accounts) AskSecondStepReset(ctx context.Context, linkToken string, ip netip.Addr) error {
	now := a.Now().UTC()
	_, u, err := a.usableSetupLink(ctx, linkToken, LinkReset, ip, now)
	if err != nil {
		return err
	}
	if !u.HasSecondStep() {
		return badRequest("no_second_step", "Your account has no second step to reset: choose a new password instead.")
	}
	a.resets.init()
	if !a.resets.asked.Allow(u.ID.String(), now) {
		return nil
	}
	actor := "user:" + u.ID.String()
	if err := a.Store.Audit(ctx, AuditEntry{TenantID: &u.TenantID, Actor: actor, IP: ip,
		Action: "user.second_step_reset_asked", Target: actor, Result: ResultOK}); err != nil {
		return err
	}
	if a.Alerts == nil {
		return nil
	}
	return a.Alerts.Announce(ctx, u.TenantID, fmt.Sprintf("%s%s:%d", askAdminPrefix, u.ID, now.Unix()), "warning",
		fmt.Sprintf("%s asked for their second step to be reset", u.Name),
		fmt.Sprintf("%s (%s) says they've lost their authenticator app, passkeys and recovery codes, and asked from %s, "+
			"using a password-reset link emailed to them. Check it's really them (call them, or ask in person), "+
			"then choose Reset authenticator on their row in People. If it wasn't them, do nothing and tell them.",
			u.Name, u.Email, ip), "/admin/people")
}
