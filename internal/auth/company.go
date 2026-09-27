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

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
)

// Company sign-in (ADR-052, docs/ADMIN.md §6): OpenID Connect providers
// such as Google or Microsoft. It replaces the password, never the second
// step, and never creates people: the first sign-in links a provider
// account to the Linx person whose email the provider has verified, and
// after that only the provider's stable subject id is trusted.

// CompanyCookieName binds a company sign-in to the browser that started it.
// SameSite=Lax, unlike every other Linx cookie: the provider sends the
// browser back with a cross-site top-level navigation, which a Strict
// cookie wouldn't be sent on. It carries nothing but a random value.
const CompanyCookieName = "__Host-linx_sso"

// CompanyFlowTTL is how long the round trip through the provider may take
// (docs/ADMIN.md §10: a 10-minute flow).
const CompanyFlowTTL = 10 * time.Minute

// maxCompanyFlows bounds the waiting flows held in memory.
const maxCompanyFlows = 10000

// What a company flow was started for.
const (
	CompanySignIn  = "sign_in"
	CompanyLink    = "link"
	CompanyConfirm = "confirm"
)

// CompanyProvider is what sign-in needs to know about a provider.
type CompanyProvider struct {
	ID   uuid.UUID
	Name string
}

// CompanyIdentity is the provider account an ID token vouched for, its
// signature, issuer, audience and nonce already checked. Email is the
// address the provider says belongs to it, EmailVerified whether the
// provider has confirmed it (Microsoft's rules differ: internal/sso).
type CompanyIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
}

// CompanyProviders is the OpenID Connect side (internal/sso implements it).
// Both methods refuse a provider that doesn't exist or is turned off with
// ErrNotFound.
type CompanyProviders interface {
	// Start returns where to send the browser: the provider's sign-in page
	// with state, nonce and the PKCE challenge for verifier.
	Start(ctx context.Context, tenant, provider uuid.UUID, state, nonce, verifier string) (string, CompanyProvider, error)
	// Finish exchanges code for tokens and checks the ID token.
	Finish(ctx context.Context, tenant, provider uuid.UUID, code, verifier, nonce string) (CompanyProvider, CompanyIdentity, error)
}

// CompanyLinkInfo is one person's link to one provider account.
type CompanyLinkInfo struct {
	ID, TenantID, UserID, ProviderID uuid.UUID
	ProviderName                     string
	Subject, Email                   string
	CreatedAt                        time.Time
	LastUsedAt                       *time.Time
}

// CompanyStore is the database access company sign-in needs.
type CompanyStore interface {
	CompanyLinkBySubject(ctx context.Context, tenant, provider uuid.UUID, subject string) (CompanyLinkInfo, error)
	CompanyLinks(ctx context.Context, tenant, user uuid.UUID) ([]CompanyLinkInfo, error)
	// AddCompanyLink returns ErrDuplicate if the subject, or this person on
	// this provider, is already linked.
	AddCompanyLink(ctx context.Context, l CompanyLinkInfo, audit AuditEntry) error
	UseCompanyLink(ctx context.Context, id uuid.UUID, at time.Time) error
	RemoveCompanyLink(ctx context.Context, tenant, user, id uuid.UUID, audit AuditEntry) error
	// CompanySignInRequired is "people must use company sign-in".
	CompanySignInRequired(ctx context.Context, tenant uuid.UUID) (bool, error)
}

type companyFlow struct {
	purpose    string
	tenant     uuid.UUID
	provider   uuid.UUID
	cookieHash []byte
	nonce      string
	verifier   string
	// sessionHash is the session a link or confirm flow was started from:
	// the callback can't see the session cookie (it's SameSite=Strict and
	// the provider's redirect is cross-site), so the flow remembers it.
	sessionHash []byte
	expires     time.Time
}

// companyFlows are flows waiting for the provider's answer, keyed by their
// state value; kept in memory like passkey ceremonies. A restart just means
// signing in again.
type companyFlows struct {
	mu sync.Mutex
	m  map[string]companyFlow
	// proofs are sessions that just proved themselves with company sign-in
	// but still owe their second step's code to finish "confirm it's you".
	proofs map[uuid.UUID]time.Time
}

func (f *companyFlows) put(state string, fl companyFlow, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]companyFlow{}
	}
	if len(f.m) >= maxCompanyFlows {
		for k, v := range f.m {
			if !now.Before(v.expires) {
				delete(f.m, k)
			}
		}
		if len(f.m) >= maxCompanyFlows {
			return &apihttp.Error{Status: http.StatusServiceUnavailable, Code: "busy",
				Detail: "Linx is handling too many sign-ins. Try again in a minute."}
		}
	}
	f.m[state] = fl
	return nil
}

// take removes the flow for state: a provider's answer is used once.
func (f *companyFlows) take(state string, now time.Time) (companyFlow, bool) {
	if state == "" {
		return companyFlow{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.m[state]
	delete(f.m, state)
	if !ok || !now.Before(fl.expires) {
		return companyFlow{}, false
	}
	return fl, true
}

func (f *companyFlows) prove(session uuid.UUID, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.proofs == nil {
		f.proofs = map[uuid.UUID]time.Time{}
	}
	for k, at := range f.proofs {
		if now.Sub(at) > CompanyFlowTTL {
			delete(f.proofs, k)
		}
	}
	f.proofs[session] = now
}

// proven reports whether session proved itself with company sign-in in the
// last CompanyFlowTTL; take removes the proof (it finishes one confirm).
func (f *companyFlows) proven(session uuid.UUID, now time.Time, take bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	at, ok := f.proofs[session]
	if take {
		delete(f.proofs, session)
	}
	return ok && now.Sub(at) <= CompanyFlowTTL
}

// CompanyStart is where to send the browser, and the CompanyCookieName
// cookie's value.
type CompanyStart struct {
	URL    string
	Cookie string
}

// CompanyResult is how a company flow ended. Status is, for sign_in, the
// session's status (SessionOutcome.Status); for link "linked"; for confirm
// "confirmed", or "code_required" when the person still owes their second
// step's code (POST /session/confirm with just the code).
type CompanyResult struct {
	Purpose string
	Status  string
	Session *SessionOutcome
}

func companyOff() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusServiceUnavailable, Code: "company_sign_in_unavailable",
		Detail: "Company sign-in isn't available on this server: it has no domain set."}
}

// Company refusals: the codes the sign-in page turns into its plain
// sentences (docs/ui/ADMIN_SCREENS_PHASE1E.md §12.1). Never more detail.
func companyRefusal(code, detail string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusForbidden, Code: code, Detail: detail}
}

var (
	errCompanyExpired = &apihttp.Error{Status: http.StatusBadRequest, Code: "company_expired",
		Detail: "That sign-in took too long or was already used. Try again."}
	errCompanyCancelled = &apihttp.Error{Status: http.StatusBadRequest, Code: "company_cancelled",
		Detail: "Signing in with your company account was cancelled."}
	errCompanyFailed = &apihttp.Error{Status: http.StatusBadGateway, Code: "company_failed",
		Detail: "Your company account's sign-in didn't work. Try again, or ask your admin."}
	errNoAccount = companyRefusal("no_account",
		"No Linx account uses this company account. Ask your admin to add you.")
	errEmailUnverified = companyRefusal("email_unverified",
		"Your company account hasn't confirmed this email address.")
	errNotLinked = companyRefusal("not_linked",
		"Your Linx account is linked to a different company account.")
	errLinkedElsewhere = companyRefusal("linked_elsewhere",
		"That company account is already linked to another Linx person.")
	errEmailMismatch = companyRefusal("email_mismatch",
		"That company account's email doesn't match your Linx email.")
	errAccountDisabled = &apihttp.Error{Status: http.StatusForbidden, Code: "account_disabled",
		Detail: "Your Linx account is disabled. Ask your admin."}
	errCompanyRequired = &apihttp.Error{Status: http.StatusForbidden, Code: "company_sign_in_required",
		Detail: "Sign in with your company account (or a passkey): passwords are turned off here."}
)

// companyRequiredFor reports whether "people must use company sign-in"
// stops u using a password. System admins are never stopped, so a broken
// provider can't lock the owner out (docs/ADMIN.md §6).
func (a *Accounts) companyRequiredFor(ctx context.Context, u User) (bool, error) {
	if a.Company == nil || u.Role == RoleSystemAdmin {
		return false, nil
	}
	return a.Company.CompanySignInRequired(ctx, u.TenantID)
}

// CompanySignInRequired is "people must use company sign-in", for the
// sign-in page.
func (a *Accounts) CompanySignInRequired(ctx context.Context, tenant uuid.UUID) (bool, error) {
	if a.Company == nil {
		return false, nil
	}
	return a.Company.CompanySignInRequired(ctx, tenant)
}

// BeginCompany starts a company flow: sign_in (no session), link (add a
// company account to mine) or confirm ("confirm it's you"). Link and
// confirm need a whole signed-in session.
func (a *Accounts) BeginCompany(ctx context.Context, tenant, provider uuid.UUID, purpose string, ip netip.Addr) (CompanyStart, error) {
	if a.CompanyProviders == nil {
		return CompanyStart{}, companyOff()
	}
	now := a.Now().UTC()
	if a.addressExhausted(ip) {
		return CompanyStart{}, tooManyFailuresErr()
	}
	fl := companyFlow{purpose: purpose, tenant: tenant, provider: provider, expires: now.Add(CompanyFlowTTL)}
	switch purpose {
	case CompanySignIn:
	case CompanyLink, CompanyConfirm:
		sess, ok := SessionFromContext(ctx)
		if !ok {
			return CompanyStart{}, notASession()
		}
		if !sess.MFAVerified {
			return CompanyStart{}, mfaVerifyFirst()
		}
		fl.tenant, fl.sessionHash = sess.TenantID, sess.TokenHash
	default:
		return CompanyStart{}, fmt.Errorf("unknown company flow purpose %q", purpose)
	}
	state, cookie := NewSecret(), NewSecret()
	fl.cookieHash, fl.nonce, fl.verifier = HashSecret(cookie), NewSecret(), NewSecret()
	url, _, err := a.CompanyProviders.Start(ctx, fl.tenant, provider, state, fl.nonce, fl.verifier)
	if errors.Is(err, ErrNotFound) {
		return CompanyStart{}, notFound("company sign-in provider")
	}
	if err != nil {
		return CompanyStart{}, err
	}
	if err := a.companyFlows.put(state, fl, now); err != nil {
		return CompanyStart{}, err
	}
	return CompanyStart{URL: url, Cookie: cookie}, nil
}

// FinishCompany is the provider sending the browser back: state and code
// (or providerError, when the person cancelled or the provider refused)
// from the query, cookie from CompanyCookieName. The purpose is returned
// even with an error, when it's known, so the caller knows where to send
// the browser.
func (a *Accounts) FinishCompany(ctx context.Context, state, cookie, code, providerError string, ip netip.Addr, userAgent string) (string, CompanyResult, error) {
	if a.CompanyProviders == nil {
		return "", CompanyResult{}, companyOff()
	}
	now := a.Now().UTC()
	fl, ok := a.companyFlows.take(state, now)
	if !ok || cookie == "" || !SecretMatches(fl.cookieHash, cookie) {
		// A stale or replayed answer, or another browser's: a link someone
		// was sent to finish their sign-in in this browser.
		a.passkeyFailure(ctx, ip)
		return "", CompanyResult{}, errCompanyExpired
	}
	res, err := a.finishCompany(ctx, fl, code, providerError, ip, userAgent, now)
	res.Purpose = fl.purpose
	return fl.purpose, res, err
}

func (a *Accounts) finishCompany(ctx context.Context, fl companyFlow, code, providerError string, ip netip.Addr, userAgent string, now time.Time) (CompanyResult, error) {
	attempt := signInAttempt{tenant: fl.tenant, ip: ip, method: "company"}
	refuse := func(u *User, reason string, err error) (CompanyResult, error) {
		if fl.purpose == CompanySignIn {
			attempt.user, attempt.reason = u, reason
			a.auditSignIn(ctx, attempt)
		}
		return CompanyResult{}, err
	}

	// A link or confirm flow acts on the session it was started from, which
	// must still be live.
	var sess UserSession
	if fl.purpose != CompanySignIn {
		s, err := a.Store.SessionByTokenHash(ctx, fl.sessionHash)
		if err != nil || s.RevokedAt != nil || !now.Before(s.ExpiresAt) || !now.Before(s.IdleExpiresAt) || !s.MFAVerified {
			return CompanyResult{}, errCompanyExpired
		}
		sess = s
	}
	if providerError != "" {
		return refuse(nil, "cancelled", errCompanyCancelled)
	}
	prov, id, err := a.CompanyProviders.Finish(ctx, fl.tenant, fl.provider, code, fl.verifier, fl.nonce)
	if err != nil {
		a.passkeyFailure(ctx, ip)
		a.log().Warn("company sign-in failed", "provider", fl.provider, "err", err)
		return refuse(nil, "provider_error", errCompanyFailed)
	}
	attempt.method = "company:" + prov.Name

	link, err := a.Company.CompanyLinkBySubject(ctx, fl.tenant, fl.provider, id.Subject)
	linked := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return CompanyResult{}, err
	}

	switch fl.purpose {
	case CompanyConfirm:
		if !linked || link.UserID != sess.UserID {
			return CompanyResult{}, errNotLinked
		}
		u, err := a.Store.User(ctx, sess.TenantID, sess.UserID)
		if err != nil {
			return CompanyResult{}, err
		}
		if err := a.Company.UseCompanyLink(ctx, link.ID, now); err != nil {
			return CompanyResult{}, err
		}
		// Company sign-in replaces the password, not the second step
		// (docs/ADMIN.md §7: "company sign-in + code").
		if u.HasSecondStep() {
			a.companyFlows.prove(sess.ID, now)
			return CompanyResult{Status: "code_required"}, nil
		}
		if err := a.Store.ConfirmSession(ctx, sess.ID, now); err != nil {
			return CompanyResult{}, err
		}
		return CompanyResult{Status: "confirmed"}, nil

	case CompanyLink:
		u, err := a.Store.User(ctx, sess.TenantID, sess.UserID)
		if err != nil {
			return CompanyResult{}, err
		}
		if linked {
			if link.UserID != u.ID {
				return CompanyResult{}, errLinkedElsewhere
			}
			return CompanyResult{Status: "linked"}, nil
		}
		if err := a.linkByEmail(ctx, u, prov, id, "user:"+u.ID.String(), ip, now); err != nil {
			return CompanyResult{}, err
		}
		return CompanyResult{Status: "linked"}, nil
	}

	// Signing in.
	var u User
	if linked {
		u, err = a.Store.User(ctx, fl.tenant, link.UserID)
		if err != nil {
			return CompanyResult{}, err
		}
		if err := a.Company.UseCompanyLink(ctx, link.ID, now); err != nil {
			return CompanyResult{}, err
		}
	} else {
		// First use: only an email the provider has verified, matching a
		// person who already exists, and who has no other account on this
		// provider (docs/ADMIN.md §6).
		if !id.EmailVerified {
			return refuse(nil, "email_unverified", errEmailUnverified)
		}
		u, err = a.Store.UserByEmail(ctx, fl.tenant, strings.ToLower(strings.TrimSpace(id.Email)))
		if errors.Is(err, ErrNotFound) {
			return refuse(nil, "no_account", errNoAccount)
		}
		if err != nil {
			return CompanyResult{}, err
		}
		if u.DisabledAt != nil {
			return refuse(&u, "disabled", errAccountDisabled)
		}
		if err := a.linkByEmail(ctx, u, prov, id, "anonymous", ip, now); err != nil {
			var e *apihttp.Error
			if errors.As(err, &e) {
				return refuse(&u, e.Code, err)
			}
			return CompanyResult{}, err
		}
	}
	if u.DisabledAt != nil {
		return refuse(&u, "disabled", errAccountDisabled)
	}
	// Like a passkey, company sign-in ignores a lockout wait for wrong
	// passwords: it can't be guessed, and the owner should still get in
	// while someone else guesses.
	out, err := a.passwordOutcome(ctx, u, ip, userAgent, now)
	if err != nil {
		return CompanyResult{}, err
	}
	attempt.user = &u
	a.auditSignIn(ctx, attempt)
	return CompanyResult{Status: out.Status, Session: &out}, nil
}

// linkByEmail links id to u, only if the provider verified an email equal
// to u's and u has no other account on this provider.
func (a *Accounts) linkByEmail(ctx context.Context, u User, prov CompanyProvider, id CompanyIdentity, actor string, ip netip.Addr, now time.Time) error {
	if !id.EmailVerified {
		return errEmailUnverified
	}
	if !strings.EqualFold(strings.TrimSpace(id.Email), u.Email) {
		return errEmailMismatch
	}
	linkID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	tenant := u.TenantID
	audit := AuditEntry{TenantID: &tenant, Actor: actor, IP: ip, Action: "user.company_linked",
		Target: "user:" + u.ID.String(), Result: ResultOK,
		Detail: map[string]any{"provider": prov.ID.String(), "provider_name": prov.Name}}
	err = a.Company.AddCompanyLink(ctx, CompanyLinkInfo{ID: linkID, TenantID: u.TenantID, UserID: u.ID,
		ProviderID: prov.ID, Subject: id.Subject, Email: strings.ToLower(strings.TrimSpace(id.Email)),
		CreatedAt: now, LastUsedAt: &now}, audit)
	if errors.Is(err, ErrDuplicate) {
		// This person already has a different account on this provider (or
		// the subject was linked by a concurrent request to someone else).
		return errNotLinked
	}
	return err
}

// MyCompanyLinks lists the caller's own company accounts.
func (a *Accounts) MyCompanyLinks(ctx context.Context) ([]CompanyLinkInfo, error) {
	_, u, err := a.sessionUser(ctx)
	if err != nil {
		return nil, err
	}
	if a.Company == nil {
		return []CompanyLinkInfo{}, nil
	}
	return a.Company.CompanyLinks(ctx, u.TenantID, u.ID)
}

// UnlinkMyCompany removes one of the caller's company accounts, unless it's
// their only way left to sign in.
func (a *Accounts) UnlinkMyCompany(ctx context.Context, id uuid.UUID) error {
	sess, u, err := a.sessionUser(ctx)
	if err != nil {
		return err
	}
	if !sess.MFAVerified {
		return mfaVerifyFirst()
	}
	return a.unlinkCompany(ctx, u, id)
}

// UserCompanyLinks lists a person's company accounts (People; users:read).
func (a *Accounts) UserCompanyLinks(ctx context.Context, user uuid.UUID) ([]CompanyLinkInfo, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipalAuth
	}
	if _, err := a.Store.User(ctx, p.TenantID, user); errors.Is(err, ErrNotFound) {
		return nil, notFound("person")
	} else if err != nil {
		return nil, err
	}
	if a.Company == nil {
		return []CompanyLinkInfo{}, nil
	}
	return a.Company.CompanyLinks(ctx, p.TenantID, user)
}

// UnlinkUserCompany removes a person's company account (People;
// users:write), e.g. when it was linked to the wrong account.
func (a *Accounts) UnlinkUserCompany(ctx context.Context, user, id uuid.UUID) error {
	caller, ok := PrincipalFromContext(ctx)
	if !ok {
		return errNoPrincipalAuth
	}
	u, err := a.Store.User(ctx, caller.TenantID, user)
	if errors.Is(err, ErrNotFound) {
		return notFound("person")
	}
	if err != nil {
		return err
	}
	if e := beyondCaller(caller, u); e != nil {
		return e
	}
	return a.unlinkCompany(ctx, u, id)
}

func (a *Accounts) unlinkCompany(ctx context.Context, u User, id uuid.UUID) error {
	if a.Company == nil {
		return notFound("company account")
	}
	links, err := a.Company.CompanyLinks(ctx, u.TenantID, u.ID)
	if err != nil {
		return err
	}
	var link *CompanyLinkInfo
	for i := range links {
		if links[i].ID == id {
			link = &links[i]
		}
	}
	if link == nil {
		return notFound("company account")
	}
	required, err := a.companyRequiredFor(ctx, u)
	if err != nil {
		return err
	}
	passwordWorks := u.HasPassword() && !required
	if len(links) == 1 && u.PasskeyCount == 0 && !passwordWorks {
		return &apihttp.Error{Status: http.StatusConflict, Code: "last_sign_in_method",
			Detail: "This is the only way left to sign in to this account. Add a passkey or a password first."}
	}
	_, audit, err := userAudit(ctx, "user.company_unlinked", "user:"+u.ID.String())
	if err != nil {
		return err
	}
	audit.Detail = map[string]any{"provider": link.ProviderID.String(), "provider_name": link.ProviderName}
	err = a.Company.RemoveCompanyLink(ctx, u.TenantID, u.ID, id, audit)
	if errors.Is(err, ErrNotFound) {
		return notFound("company account")
	}
	return err
}
