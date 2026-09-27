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
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
)

// Passkeys (ADR-051, docs/ADMIN.md §5). The browser's own
// navigator.credentials does the device side; go-webauthn checks
// everything the server must (challenge, origin, relying party, signature,
// user verification). A passkey is a whole sign-in: the device proves it
// holds the key and unlocks it with Face ID, a fingerprint or a PIN.

const (
	// MaxPasskeys is how many passkeys one person may have.
	MaxPasskeys = 10
	// PasskeyCeremonyTTL is how long the browser has to answer a challenge.
	PasskeyCeremonyTTL = 5 * time.Minute
	// PasskeyCookieName binds a challenge to the browser that asked for it
	// (HttpOnly, SameSite=Strict, 5 minutes): the server keeps only its
	// hash, so a challenge can't be answered from anywhere else.
	PasskeyCookieName = "__Host-linx_passkey"
	// maxCeremonies caps challenges waiting for an answer: asking for one
	// needs no account, so there must be a limit.
	maxCeremonies  = 10000
	maxPasskeyName = 60
)

// Passkey is one of a person's passkeys. Nothing here is secret: the
// private key never leaves their device.
type Passkey struct {
	ID, TenantID, UserID uuid.UUID
	CredentialID         []byte
	PublicKey            []byte
	SignCount            uint32
	AAGUID               []byte
	// BackupEligible is fixed for a credential; BackupState (synced right
	// now, e.g. iCloud Keychain or Google Password Manager) can change.
	BackupEligible, BackupState bool
	Transports                  []string
	AttestationFormat           string
	Name                        string
	CreatedAt                   time.Time
	LastUsedAt                  *time.Time
}

// ErrLimit is returned by PasskeyStore.AddPasskey when the person already
// has MaxPasskeys.
var ErrLimit = errors.New("limit reached")

// PasskeyStore is the database access passkeys need (internal/store
// implements it).
type PasskeyStore interface {
	Passkeys(ctx context.Context, tenant, user uuid.UUID) ([]Passkey, error)
	PasskeyByCredentialID(ctx context.Context, credentialID []byte) (Passkey, error)
	// AddPasskey saves p if its person has fewer than MaxPasskeys (ErrLimit
	// otherwise; ErrDuplicate if the credential is already registered). A
	// non-nil recoveryHashes replaces the person's recovery codes. It clears
	// the person's "password only" choice: they have a second step now.
	AddPasskey(ctx context.Context, p Passkey, recoveryHashes [][]byte, audit AuditEntry) error
	// UsePasskey records a sign-in with the passkey: its new signature
	// counter and backup state.
	UsePasskey(ctx context.Context, id uuid.UUID, signCount uint32, backupState bool, at time.Time) error
	RenamePasskey(ctx context.Context, tenant, user, id uuid.UUID, name string, audit AuditEntry) (Passkey, error)
	// DeletePasskey removes one of user's passkeys. If it was their last
	// second step, their recovery codes go too, and with passwordOnly their
	// "password only" choice is recorded at at.
	DeletePasskey(ctx context.Context, tenant, user, id uuid.UUID, passwordOnly bool, at time.Time, audit AuditEntry) error
}

// NewWebAuthn is Linx's relying party for domain (ADR-051): the ID is the
// base domain, so one passkey works on every Linx hostname under it; the
// web client is served at meet.<domain>. Attestation none (any maker's
// device), discoverable credentials and user verification required.
func NewWebAuthn(domain string) (*webauthn.WebAuthn, error) {
	timeout := webauthn.TimeoutConfig{Enforce: true, Timeout: PasskeyCeremonyTTL, TimeoutUVD: PasskeyCeremonyTTL}
	return webauthn.New(&webauthn.Config{
		RPID:                  domain,
		RPDisplayName:         "Linx",
		RPOrigins:             []string{"https://meet." + domain},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{Login: timeout, Registration: timeout},
	})
}

// passkeyUser adapts a person and their passkeys to go-webauthn. The user
// handle is the person's id: opaque, and never their email.
type passkeyUser struct {
	u    User
	keys []Passkey
}

func (p passkeyUser) WebAuthnID() []byte          { return p.u.ID[:] }
func (p passkeyUser) WebAuthnName() string        { return p.u.Email }
func (p passkeyUser) WebAuthnDisplayName() string { return p.u.Name }
func (p passkeyUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(p.keys))
	for _, k := range p.keys {
		transports := make([]protocol.AuthenticatorTransport, 0, len(k.Transports))
		for _, t := range k.Transports {
			transports = append(transports, protocol.AuthenticatorTransport(t))
		}
		out = append(out, webauthn.Credential{
			ID: k.CredentialID, PublicKey: k.PublicKey, AttestationFormat: k.AttestationFormat, Transport: transports,
			Flags: webauthn.CredentialFlags{UserPresent: true, UserVerified: true,
				BackupEligible: k.BackupEligible, BackupState: k.BackupState},
			Authenticator: webauthn.Authenticator{AAGUID: k.AAGUID, SignCount: k.SignCount},
		})
	}
	return out
}

// Ceremony purposes: what answering a challenge is allowed to do.
const (
	purposeSignIn    = "sign_in"
	purposeCheck     = "check" // second step or "confirm it's you", in a session
	purposeRegister  = "register"
	purposeSetupLink = "setup_link"
)

type ceremony struct {
	purpose string
	tenant  uuid.UUID
	user    uuid.UUID // register, check, setup_link
	session uuid.UUID // register, check
	link    uuid.UUID // setup_link
	data    webauthn.SessionData
	expires time.Time
}

// ceremonies are challenges waiting for the browser's answer, kept in
// memory (like the rate limiters): single-use, PasskeyCeremonyTTL, keyed by
// the hash of the PasskeyCookieName cookie. A restart just means asking
// again.
type ceremonies struct {
	mu sync.Mutex
	m  map[string]ceremony
}

func (c *ceremonies) put(token string, cer ceremony, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]ceremony{}
	}
	if len(c.m) >= maxCeremonies {
		for k, v := range c.m {
			if !now.Before(v.expires) {
				delete(c.m, k)
			}
		}
		if len(c.m) >= maxCeremonies {
			return &apihttp.Error{Status: http.StatusServiceUnavailable, Code: "busy",
				Detail: "Linx is handling too many passkey requests. Try again in a minute."}
		}
	}
	c.m[string(HashSecret(token))] = cer
	return nil
}

// take removes and returns the ceremony for token: whatever happens next,
// its challenge can't be answered twice.
func (c *ceremonies) take(token string, now time.Time) (ceremony, bool) {
	if token == "" {
		return ceremony{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := string(HashSecret(token))
	cer, ok := c.m[key]
	delete(c.m, key)
	if !ok || !now.Before(cer.expires) {
		return ceremony{}, false
	}
	return cer, true
}

// PasskeyCeremony is what the browser needs to ask the person's device:
// Options is the JSON for PublicKeyCredential.parse*OptionsFromJSON, Token
// the PasskeyCookieName cookie's value.
type PasskeyCeremony struct {
	Options any
	Token   string
}

func passkeysOff() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusServiceUnavailable, Code: "passkeys_unavailable",
		Detail: "Passkeys aren't available on this server: it has no domain set."}
}

func passkeyExpired() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusBadRequest, Code: "passkey_expired",
		Detail: "That took too long or was already used. Try again."}
}

func passkeyUnknown() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusUnauthorized, Code: "passkey_invalid",
		Detail: "That passkey isn't registered here."}
}

func passkeyRefused() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusUnauthorized, Code: "passkey_refused",
		Detail: "Your device's answer couldn't be checked. Try again."}
}

func (a *Accounts) begin(cer ceremony, options any) (PasskeyCeremony, error) {
	token := NewSecret()
	cer.expires = a.Now().Add(PasskeyCeremonyTTL)
	if err := a.ceremonies.put(token, cer, a.Now()); err != nil {
		return PasskeyCeremony{}, err
	}
	return PasskeyCeremony{Options: options, Token: token}, nil
}

// failure counts a refused passkey answer against the caller's address,
// like a wrong password.
func (a *Accounts) passkeyFailure(ctx context.Context, ip netip.Addr) {
	if a.Failures != nil {
		a.Failures.Allow(IPKey(ip), a.Now())
	}
}

func (a *Accounts) addressExhausted(ip netip.Addr) bool {
	return a.Failures != nil && a.Failures.Exhausted(IPKey(ip), a.Now())
}

// BeginPasskeySignIn starts "Sign in with a passkey": no email, the device
// offers whichever of its passkeys are for this server.
func (a *Accounts) BeginPasskeySignIn(ctx context.Context, tenant uuid.UUID, ip netip.Addr) (PasskeyCeremony, error) {
	if a.WebAuthn == nil {
		return PasskeyCeremony{}, passkeysOff()
	}
	if a.addressExhausted(ip) {
		return PasskeyCeremony{}, tooManyFailuresErr()
	}
	opts, data, err := a.WebAuthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return PasskeyCeremony{}, fmt.Errorf("starting passkey sign-in: %w", err)
	}
	return a.begin(ceremony{purpose: purposeSignIn, tenant: tenant, data: *data}, opts.Response)
}

// FinishPasskeySignIn checks the device's answer and signs the person in:
// a whole sign-in, both steps, for admins too (docs/ADMIN.md §5). A lockout
// wait for wrong passwords doesn't apply: a passkey can't be guessed, and
// the account's owner should still get in while someone else is guessing.
func (a *Accounts) FinishPasskeySignIn(ctx context.Context, tenant uuid.UUID, token string, credential []byte, ip netip.Addr, userAgent string) (SessionOutcome, error) {
	if a.WebAuthn == nil {
		return SessionOutcome{}, passkeysOff()
	}
	now := a.Now().UTC()
	if a.addressExhausted(ip) {
		return SessionOutcome{}, tooManyFailuresErr()
	}
	cer, ok := a.ceremonies.take(token, now)
	if !ok || cer.purpose != purposeSignIn || cer.tenant != tenant {
		return SessionOutcome{}, passkeyExpired()
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credential)
	if err != nil {
		a.passkeyFailure(ctx, ip)
		return SessionOutcome{}, passkeyRefused()
	}
	var found *passkeyUser
	var key Passkey
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		k, err := a.Passkeys.PasskeyByCredentialID(ctx, rawID)
		if err != nil {
			return nil, err
		}
		uid, err := uuid.FromBytes(userHandle)
		if err != nil || uid != k.UserID || k.TenantID != tenant {
			return nil, ErrNotFound
		}
		u, err := a.Store.User(ctx, tenant, uid)
		if err != nil {
			return nil, err
		}
		key = k
		found = &passkeyUser{u: u, keys: []Passkey{k}}
		return *found, nil
	}
	cred, err := a.WebAuthn.ValidateDiscoverableLogin(handler, cer.data, parsed)
	attempt := signInAttempt{tenant: tenant, ip: ip, method: "passkey"}
	if found != nil {
		attempt.user = &found.u
	}
	if err != nil {
		a.passkeyFailure(ctx, ip)
		if found == nil {
			attempt.reason = "passkey_unknown"
			a.auditSignIn(ctx, attempt)
			return SessionOutcome{}, passkeyUnknown()
		}
		attempt.reason = "passkey_refused"
		a.auditSignIn(ctx, attempt)
		return SessionOutcome{}, passkeyRefused()
	}
	u := found.u
	if u.DisabledAt != nil {
		attempt.reason = "disabled"
		a.auditSignIn(ctx, attempt)
		return SessionOutcome{}, &apihttp.Error{Status: http.StatusForbidden, Code: "account_disabled",
			Detail: "Your Linx account is disabled. Ask your admin."}
	}
	if err := a.usedPasskey(ctx, u, key, cred, now); err != nil {
		attempt.reason = "sign_count"
		a.auditSignIn(ctx, attempt)
		return SessionOutcome{}, err
	}
	sess, tok, csrf, err := a.newSession(ctx, u, true, ip, userAgent, now)
	if err != nil {
		return SessionOutcome{}, err
	}
	a.auditSignIn(ctx, attempt)
	return SessionOutcome{Session: sess, Token: tok, CSRF: csrf, Status: "signed_in"}, nil
}

// usedPasskey records a checked answer: a signature counter that didn't go
// up (other than a synced passkey's constant 0) means the key may have been
// copied, so it's refused and the admins are told (docs/ADMIN.md §10).
func (a *Accounts) usedPasskey(ctx context.Context, u User, key Passkey, cred *webauthn.Credential, now time.Time) error {
	if cred.Authenticator.CloneWarning {
		if a.Alerts != nil {
			_ = a.Alerts.Fire(ctx, u.TenantID, "passkey_cloned:"+key.ID.String(), "critical",
				"A passkey may have been copied",
				fmt.Sprintf("A sign-in with %s's passkey %q was refused: its device counter went backwards. Remove that passkey in People if they don't recognise this.", u.Email, key.Name), "")
		}
		return &apihttp.Error{Status: http.StatusUnauthorized, Code: "passkey_refused",
			Detail: "This passkey was refused for your safety. Use another way to sign in, or ask your admin."}
	}
	return a.Passkeys.UsePasskey(ctx, key.ID, cred.Authenticator.SignCount, cred.Flags.BackupState, now)
}

// sessionUser returns the request's session and its person.
func (a *Accounts) sessionUser(ctx context.Context) (UserSession, User, error) {
	sess, ok := SessionFromContext(ctx)
	if !ok {
		return UserSession{}, User{}, notASession()
	}
	u, err := a.Store.User(ctx, sess.TenantID, sess.UserID)
	if err != nil {
		return UserSession{}, User{}, err
	}
	return sess, u, nil
}

// BeginPasskeyCheck asks for one of the signed-in person's own passkeys:
// the second step of a password sign-in (a pending session), or "confirm
// it's you" (a whole one, docs/ADMIN.md §7).
func (a *Accounts) BeginPasskeyCheck(ctx context.Context) (PasskeyCeremony, error) {
	if a.WebAuthn == nil {
		return PasskeyCeremony{}, passkeysOff()
	}
	sess, u, err := a.sessionUser(ctx)
	if err != nil {
		return PasskeyCeremony{}, err
	}
	if a.addressExhausted(ClientIPFromContext(ctx)) {
		return PasskeyCeremony{}, tooManyFailuresErr()
	}
	keys, err := a.Passkeys.Passkeys(ctx, u.TenantID, u.ID)
	if err != nil {
		return PasskeyCeremony{}, err
	}
	if len(keys) == 0 {
		return PasskeyCeremony{}, badRequest("no_passkeys", "You don't have a passkey yet.")
	}
	pu := passkeyUser{u: u, keys: keys}
	opts, data, err := a.WebAuthn.BeginLogin(pu, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return PasskeyCeremony{}, fmt.Errorf("starting passkey check: %w", err)
	}
	return a.begin(ceremony{purpose: purposeCheck, tenant: u.TenantID, user: u.ID, session: sess.ID, data: *data}, opts.Response)
}

// FinishPasskeyCheck checks the answer: a pending session becomes a whole
// one; a whole one is confirmed for the next ConfirmWithin.
func (a *Accounts) FinishPasskeyCheck(ctx context.Context, token string, credential []byte) error {
	if a.WebAuthn == nil {
		return passkeysOff()
	}
	sess, u, err := a.sessionUser(ctx)
	if err != nil {
		return err
	}
	now := a.Now().UTC()
	ip := ClientIPFromContext(ctx)
	if a.addressExhausted(ip) {
		return tooManyFailuresErr()
	}
	cer, ok := a.ceremonies.take(token, now)
	if !ok || cer.purpose != purposeCheck || cer.session != sess.ID || cer.user != u.ID {
		return passkeyExpired()
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credential)
	if err != nil {
		a.passkeyFailure(ctx, ip)
		return passkeyRefused()
	}
	keys, err := a.Passkeys.Passkeys(ctx, u.TenantID, u.ID)
	if err != nil {
		return err
	}
	attempt := signInAttempt{tenant: u.TenantID, user: &u, ip: ip, code: true, method: "passkey"}
	cred, err := a.WebAuthn.ValidateLogin(passkeyUser{u: u, keys: keys}, cer.data, parsed)
	if err != nil {
		a.passkeyFailure(ctx, ip)
		if !sess.MFAVerified {
			attempt.reason = "passkey_refused"
			a.auditSignIn(ctx, attempt)
		}
		return passkeyRefused()
	}
	var key Passkey
	for _, k := range keys {
		if string(k.CredentialID) == string(cred.ID) {
			key = k
		}
	}
	if err := a.usedPasskey(ctx, u, key, cred, now); err != nil {
		return err
	}
	if !sess.MFAVerified {
		if err := a.Store.PromoteSession(ctx, sess.ID); err != nil {
			return err
		}
		a.auditSignIn(ctx, attempt)
	}
	return a.Store.ConfirmSession(ctx, sess.ID, now)
}

// registrationOptions starts adding a passkey for u: the device must make a
// discoverable one and unlock it, and mustn't reuse one already registered.
func (a *Accounts) registrationOptions(u User, keys []Passkey) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	pu := passkeyUser{u: u, keys: keys}
	exclude := make([]protocol.CredentialDescriptor, 0, len(keys))
	for _, c := range pu.WebAuthnCredentials() {
		exclude = append(exclude, c.Descriptor())
	}
	return a.WebAuthn.BeginRegistration(pu, webauthn.WithExclusions(exclude))
}

func passkeyLimit() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusConflict, Code: "passkey_limit",
		Detail: fmt.Sprintf("You already have %d passkeys, the most allowed. Remove one first.", MaxPasskeys)}
}

// BeginPasskeyRegistration starts adding a passkey to the caller's own
// account (My account, or setting up a second step while a sign-in waits
// for one). In a whole session it needs a fresh "confirm it's you": a new
// passkey is a new way in (docs/ADMIN.md §7).
func (a *Accounts) BeginPasskeyRegistration(ctx context.Context) (PasskeyCeremony, error) {
	if a.WebAuthn == nil {
		return PasskeyCeremony{}, passkeysOff()
	}
	sess, u, err := a.sessionUser(ctx)
	if err != nil {
		return PasskeyCeremony{}, err
	}
	if err := a.mayAddSecondStep(ctx, sess, u); err != nil {
		return PasskeyCeremony{}, err
	}
	keys, err := a.Passkeys.Passkeys(ctx, u.TenantID, u.ID)
	if err != nil {
		return PasskeyCeremony{}, err
	}
	if len(keys) >= MaxPasskeys {
		return PasskeyCeremony{}, passkeyLimit()
	}
	opts, data, err := a.registrationOptions(u, keys)
	if err != nil {
		return PasskeyCeremony{}, fmt.Errorf("starting passkey registration: %w", err)
	}
	return a.begin(ceremony{purpose: purposeRegister, tenant: u.TenantID, user: u.ID, session: sess.ID, data: *data}, opts.Response)
}

// mayAddSecondStep refuses adding a passkey from a session still waiting on
// its second step (knowing the password mustn't be enough to add one), and
// from a whole session without a fresh confirmation.
func (a *Accounts) mayAddSecondStep(ctx context.Context, sess UserSession, u User) error {
	if !sess.MFAVerified {
		if u.HasSecondStep() {
			return mfaVerifyFirst()
		}
		return nil
	}
	return RequireConfirmed(ctx, a.Now())
}

// PasskeyName checks a passkey's name: 1 to 60 characters, no control
// characters.
func PasskeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxPasskeyName || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", badRequest("name_invalid", fmt.Sprintf("Give the passkey a name of 1 to %d characters.", maxPasskeyName))
	}
	return name, nil
}

// newPasskey checks the device's answer to a registration ceremony.
func (a *Accounts) newPasskey(u User, keys []Passkey, data webauthn.SessionData, credential []byte, name string, now time.Time) (Passkey, error) {
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credential)
	if err != nil {
		return Passkey{}, passkeyRefused()
	}
	cred, err := a.WebAuthn.CreateCredential(passkeyUser{u: u, keys: keys}, data, parsed)
	if err != nil {
		return Passkey{}, passkeyRefused()
	}
	if !cred.Flags.UserVerified {
		// go-webauthn already refuses this (user verification required);
		// checked again because a passkey that didn't unlock is only half a
		// sign-in.
		return Passkey{}, passkeyRefused()
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Passkey{}, err
	}
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	return Passkey{
		ID: id, TenantID: u.TenantID, UserID: u.ID, CredentialID: cred.ID, PublicKey: cred.PublicKey,
		SignCount: cred.Authenticator.SignCount, AAGUID: cred.Authenticator.AAGUID,
		BackupEligible: cred.Flags.BackupEligible, BackupState: cred.Flags.BackupState,
		Transports: transports, AttestationFormat: cred.AttestationFormat, Name: name, CreatedAt: now,
	}, nil
}

// savePasskey stores p; if it's the person's first second step, it also
// issues their recovery codes (returned once): with a password, each one
// can stand in for the passkey when the device is lost.
func (a *Accounts) savePasskey(ctx context.Context, u User, p Passkey, audit AuditEntry) ([]string, error) {
	var codes []string
	var hashes [][]byte
	if !u.MFAEnabled && len(u.RecoveryCodeHashes) == 0 {
		var err error
		if codes, hashes, err = NewRecoveryCodes(); err != nil {
			return nil, err
		}
	}
	audit.Detail = map[string]any{"passkey": p.ID.String(), "name": p.Name}
	switch err := a.Passkeys.AddPasskey(ctx, p, hashes, audit); {
	case errors.Is(err, ErrLimit):
		return nil, passkeyLimit()
	case errors.Is(err, ErrDuplicate):
		return nil, &apihttp.Error{Status: http.StatusConflict, Code: "passkey_exists",
			Detail: "That passkey is already registered."}
	case err != nil:
		return nil, err
	}
	return codes, nil
}

// FinishPasskeyRegistration checks the device's answer and adds the
// passkey. A session that was waiting for its second step's setup becomes
// a whole one. Returns the new passkey and, if it's the person's first
// second step, their recovery codes (shown once).
func (a *Accounts) FinishPasskeyRegistration(ctx context.Context, token, name string, credential []byte) (Passkey, []string, error) {
	if a.WebAuthn == nil {
		return Passkey{}, nil, passkeysOff()
	}
	name, err := PasskeyName(name)
	if err != nil {
		return Passkey{}, nil, err
	}
	sess, u, err := a.sessionUser(ctx)
	if err != nil {
		return Passkey{}, nil, err
	}
	now := a.Now().UTC()
	cer, ok := a.ceremonies.take(token, now)
	if !ok || cer.purpose != purposeRegister || cer.session != sess.ID || cer.user != u.ID {
		return Passkey{}, nil, passkeyExpired()
	}
	if err := a.mayAddSecondStep(ctx, sess, u); err != nil {
		return Passkey{}, nil, err
	}
	keys, err := a.Passkeys.Passkeys(ctx, u.TenantID, u.ID)
	if err != nil {
		return Passkey{}, nil, err
	}
	p, err := a.newPasskey(u, keys, cer.data, credential, name, now)
	if err != nil {
		return Passkey{}, nil, err
	}
	_, audit, err := userAudit(ctx, "user.passkey_added", "user:"+u.ID.String())
	if err != nil {
		return Passkey{}, nil, err
	}
	codes, err := a.savePasskey(ctx, u, p, audit)
	if err != nil {
		return Passkey{}, nil, err
	}
	if err := a.promotePending(ctx, now); err != nil {
		return Passkey{}, nil, err
	}
	return p, codes, nil
}

// BeginSetupLinkPasskey starts the "Passkey" choice on a set-password link
// (docs/ADMIN.md §5): the person makes a passkey instead of choosing a
// password. Only for an account with no second step yet: a link never
// skips an existing one (like CompleteSetup).
func (a *Accounts) BeginSetupLinkPasskey(ctx context.Context, linkToken string, ip netip.Addr) (PasskeyCeremony, error) {
	if a.WebAuthn == nil {
		return PasskeyCeremony{}, passkeysOff()
	}
	link, u, err := a.usableSetupLink(ctx, linkToken, ip, a.Now().UTC())
	if err != nil {
		return PasskeyCeremony{}, err
	}
	if u.HasSecondStep() {
		return PasskeyCeremony{}, secondStepExists()
	}
	opts, data, err := a.registrationOptions(u, nil)
	if err != nil {
		return PasskeyCeremony{}, fmt.Errorf("starting passkey registration: %w", err)
	}
	return a.begin(ceremony{purpose: purposeSetupLink, tenant: u.TenantID, user: u.ID, link: link.ID, data: *data}, opts.Response)
}

// FinishSetupLinkPasskey checks the device's answer, uses up the link, and
// signs the person in with their new passkey. Whatever password the account
// had goes (a link replaces how you sign in; they can add a password in My
// account), and so does every other session of theirs.
func (a *Accounts) FinishSetupLinkPasskey(ctx context.Context, linkToken, token, name string, credential []byte, ip netip.Addr, userAgent string) (SessionOutcome, error) {
	if a.WebAuthn == nil {
		return SessionOutcome{}, passkeysOff()
	}
	name, err := PasskeyName(name)
	if err != nil {
		return SessionOutcome{}, err
	}
	now := a.Now().UTC()
	link, u, err := a.usableSetupLink(ctx, linkToken, ip, now)
	if err != nil {
		return SessionOutcome{}, err
	}
	cer, ok := a.ceremonies.take(token, now)
	if !ok || cer.purpose != purposeSetupLink || cer.link != link.ID || cer.user != u.ID {
		return SessionOutcome{}, passkeyExpired()
	}
	if u.HasSecondStep() {
		return SessionOutcome{}, secondStepExists()
	}
	p, err := a.newPasskey(u, nil, cer.data, credential, name, now)
	if err != nil {
		a.passkeyFailure(ctx, ip)
		return SessionOutcome{}, err
	}
	if err := a.Store.ConsumeSetupLink(ctx, link.ID, now); err != nil {
		if errors.Is(err, ErrNotFound) {
			return SessionOutcome{}, invalidLink()
		}
		return SessionOutcome{}, err
	}
	actor := "user:" + u.ID.String()
	audit := AuditEntry{TenantID: &u.TenantID, Actor: actor, IP: ip, Action: "user.passkey_added", Target: actor, Result: ResultOK}
	codes, err := a.savePasskey(ctx, u, p, audit)
	if err != nil {
		return SessionOutcome{}, err
	}
	audit.Action, audit.Detail = "user.password_set", map[string]any{"method": "passkey"}
	if err := a.Store.SetPassword(ctx, u.TenantID, u.ID, "", now, true, audit); err != nil {
		return SessionOutcome{}, err
	}
	a.sessionsEnded(ctx, u.ID, nil)
	sess, tok, csrf, err := a.newSession(ctx, u, true, ip, userAgent, now)
	if err != nil {
		return SessionOutcome{}, err
	}
	return SessionOutcome{Session: sess, Token: tok, CSRF: csrf, Status: "signed_in", RecoveryCodes: codes}, nil
}

// ListPasskeys returns the caller's own passkeys.
func (a *Accounts) ListPasskeys(ctx context.Context) ([]Passkey, error) {
	_, u, err := a.sessionUser(ctx)
	if err != nil {
		return nil, err
	}
	return a.Passkeys.Passkeys(ctx, u.TenantID, u.ID)
}

// RenamePasskey renames one of the caller's own passkeys.
func (a *Accounts) RenamePasskey(ctx context.Context, id uuid.UUID, name string) (Passkey, error) {
	sess, u, err := a.sessionUser(ctx)
	if err != nil {
		return Passkey{}, err
	}
	if !sess.MFAVerified {
		return Passkey{}, mfaVerifyFirst()
	}
	name, err = PasskeyName(name)
	if err != nil {
		return Passkey{}, err
	}
	_, audit, err := userAudit(ctx, "user.passkey_renamed", "user:"+u.ID.String())
	if err != nil {
		return Passkey{}, err
	}
	audit.Detail = map[string]any{"passkey": id.String(), "name": name}
	p, err := a.Passkeys.RenamePasskey(ctx, u.TenantID, u.ID, id, name, audit)
	if errors.Is(err, ErrNotFound) {
		return Passkey{}, notFound("passkey")
	}
	return p, err
}

// RemovePasskey removes one of the caller's own passkeys, after a fresh
// "confirm it's you" (docs/ADMIN.md §7). Removing the last way past the
// password is refused for someone with no password (they couldn't sign in
// at all), and for an admin needs acceptPasswordOnly: the same "not
// recommended" warning as choosing a password only (§5).
func (a *Accounts) RemovePasskey(ctx context.Context, id uuid.UUID, acceptPasswordOnly bool) error {
	sess, u, err := a.sessionUser(ctx)
	if err != nil {
		return err
	}
	if !sess.MFAVerified {
		return mfaVerifyFirst()
	}
	now := a.Now().UTC()
	if err := RequireConfirmed(ctx, now); err != nil {
		return err
	}
	keys, err := a.Passkeys.Passkeys(ctx, u.TenantID, u.ID)
	if err != nil {
		return err
	}
	var key *Passkey
	for i := range keys {
		if keys[i].ID == id {
			key = &keys[i]
		}
	}
	if key == nil {
		return notFound("passkey")
	}
	last := len(keys) == 1
	if last && !u.HasPassword() && len(u.CompanyLogins) == 0 {
		return &apihttp.Error{Status: http.StatusConflict, Code: "last_sign_in_method",
			Detail: "This is your only way to sign in. Add a password, another passkey or a company account first."}
	}
	passwordOnly := last && !u.MFAEnabled && requiresMFA(u.Role) && u.PasswordOnlyAcceptedAt == nil
	if passwordOnly && !acceptPasswordOnly {
		return &apihttp.Error{Status: http.StatusConflict, Code: "password_only_warning",
			Detail: "After this you'd sign in with a password only. Anyone who learns or guesses it could control your whole phone system. Accept the warning to go ahead."}
	}
	_, audit, err := userAudit(ctx, "user.passkey_removed", "user:"+u.ID.String())
	if err != nil {
		return err
	}
	audit.Detail = map[string]any{"passkey": key.ID.String(), "name": key.Name}
	if passwordOnly {
		audit.Detail["password_only_accepted"] = true
	}
	err = a.Passkeys.DeletePasskey(ctx, u.TenantID, u.ID, id, passwordOnly, now, audit)
	if errors.Is(err, ErrNotFound) {
		return notFound("passkey")
	}
	return err
}

// AcceptPasswordOnly is an admin choosing to sign in with a password only,
// having accepted the "not recommended" warning (docs/ADMIN.md §5), while
// their sign-in waits for a second step's setup. Refused once they have a
// passkey or authenticator: it can't be used to skip one.
func (a *Accounts) AcceptPasswordOnly(ctx context.Context) error {
	_, u, err := a.sessionUser(ctx)
	if err != nil {
		return err
	}
	if u.HasSecondStep() {
		return secondStepExists()
	}
	now := a.Now().UTC()
	if requiresMFA(u.Role) && u.PasswordOnlyAcceptedAt == nil {
		_, audit, err := userAudit(ctx, "user.password_only_accepted", "user:"+u.ID.String())
		if err != nil {
			return err
		}
		if err := a.Store.AcceptPasswordOnly(ctx, u.TenantID, u.ID, now, audit); err != nil {
			return err
		}
	}
	return a.promotePending(ctx, now)
}
