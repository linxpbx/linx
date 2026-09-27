package sso

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/safehttp"
)

// Service is what the API's /sso-providers endpoints do (docs/ADMIN.md §6,
// §9). Every change needs a fresh "confirm it's you" (§7). Caller mistakes
// come back as *apihttp.Error.
type Service struct {
	Store  Store
	Sealer *dbsecret.Sealer
	// Client checks a new issuer can be discovered, through the same
	// guarded HTTP client sign-in uses. nil (no domain set) turns company
	// sign-in off: providers can't be added.
	Client *Client
	// Policy and Resolver refuse issuers on private addresses that aren't
	// on the outbound allowlist, with a plain reason, before trying them.
	Policy   safehttp.Policy
	Resolver safehttp.Resolver
	Now      func() time.Time
}

var errNoPrincipal = errors.New("no principal on the request: authentication middleware is missing")

func invalid(code, detail string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: code, Detail: detail}
}

func notFound() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "There is no company sign-in provider with that id."}
}

var errNameTaken = &apihttp.Error{Status: http.StatusConflict, Code: "name_taken",
	Detail: "Another company sign-in provider already has that name."}

var errChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "Someone changed this provider since you loaded it. Reload and try again."}

func audit(ctx context.Context, action, target string) (auth.Principal, auth.AuditEntry, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Principal{}, auth.AuditEntry{}, errNoPrincipal
	}
	return p, auth.AuditEntry{
		TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: action, Target: target, Result: auth.ResultOK,
	}, nil
}

// defaultNames are each kind's button name when none is given.
var defaultNames = map[string]string{
	KindGoogle: "Google", KindMicrosoft: "Microsoft", KindAuthentik: "Authentik", KindKeycloak: "Keycloak",
}

// ProviderInput is a new provider. Issuer may be empty for Google (it has
// one issuer for everyone); Name may be empty except for kind "oidc".
type ProviderInput struct {
	Kind, Name, Issuer, ClientID, ClientSecret string
	Enabled, Shown                             *bool
	Position                                   *int
}

// ProviderPatch is a JSON Merge Patch of a provider; nil fields stay as
// they are. An empty ClientSecret removes the secret.
type ProviderPatch struct {
	Name, Issuer, ClientID, ClientSecret *string
	Enabled, Shown                       *bool
	Position                             *int
}

func checkName(name string) error {
	if n := len([]rune(name)); n < 1 || n > 60 {
		return invalid("name_invalid", "The name is 1 to 60 characters: it's shown as \"Continue with <name>\".")
	}
	return nil
}

func checkClient(id, secret string) error {
	if strings.TrimSpace(id) == "" || len(id) > 500 {
		return invalid("client_id_invalid", "Paste the client ID the provider gave you.")
	}
	if len(secret) > 1000 {
		return invalid("client_secret_invalid", "That client secret is too long.")
	}
	return nil
}

// checkIssuer validates an issuer address for kind, and that Linx can
// reach and discover it (docs/ADMIN.md §6).
func (s *Service) checkIssuer(ctx context.Context, kind, issuer string) error {
	if kind == KindGoogle && issuer != GoogleIssuer {
		return invalid("issuer_invalid", "Google's issuer is always "+GoogleIssuer+".")
	}
	if len(issuer) > 500 {
		return invalid("issuer_invalid", "That issuer address is too long.")
	}
	u, err := safehttp.CheckURL(issuer)
	if err != nil {
		return invalid("issuer_invalid", err.Error())
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return invalid("issuer_invalid", "The issuer address has no ? or # part.")
	}
	if kind == KindMicrosoft {
		// The shared "common" and "organizations" issuers would let any
		// Microsoft organisation's accounts in; the discovery document's
		// issuer wouldn't even match. Linx needs the organisation's own.
		for _, shared := range []string{"/common/", "/organizations/", "/consumers/"} {
			if strings.Contains(strings.ToLower(u.Path)+"/", shared) {
				return invalid("issuer_invalid",
					"Use your organisation's own issuer: https://login.microsoftonline.com/<your tenant ID>/v2.0.")
			}
		}
	}
	if err := s.Policy.CheckHost(ctx, s.Resolver, u.Hostname()); err != nil {
		var b *safehttp.BlockedError
		if errors.As(err, &b) {
			return invalid("issuer_blocked", b.Error()+
				" If the provider runs on your own network, add its address to the outbound allowlist first.")
		}
		return err
	}
	if _, err := s.Client.Discover(ctx, issuer); err != nil {
		return invalid("issuer_unreachable", "Linx couldn't read the provider's sign-in settings at that address: "+safehttp.Describe(err))
	}
	return nil
}

func (s *Service) available() error {
	if s.Client == nil {
		return &apihttp.Error{Status: http.StatusServiceUnavailable, Code: "company_sign_in_unavailable",
			Detail: "Company sign-in isn't available on this server: it has no domain set."}
	}
	return nil
}

// RedirectURI is the address to register at every provider ("" when
// company sign-in is off).
func (s *Service) RedirectURI() string {
	if s.Client == nil {
		return ""
	}
	return s.Client.RedirectURI
}

// Create adds a provider.
func (s *Service) Create(ctx context.Context, in ProviderInput) (Provider, error) {
	if err := s.available(); err != nil {
		return Provider{}, err
	}
	if err := auth.RequireConfirmed(ctx, s.Now()); err != nil {
		return Provider{}, err
	}
	p, a, err := audit(ctx, "sso_provider.create", "")
	if err != nil {
		return Provider{}, err
	}
	switch in.Kind {
	case KindGoogle, KindMicrosoft, KindAuthentik, KindKeycloak, KindOIDC:
	default:
		return Provider{}, invalid("kind_invalid", "kind is google, microsoft, authentik, keycloak or oidc.")
	}
	if in.Name == "" {
		in.Name = defaultNames[in.Kind]
	}
	if in.Kind == KindGoogle && in.Issuer == "" {
		in.Issuer = GoogleIssuer
	}
	in.Name, in.Issuer, in.ClientID = strings.TrimSpace(in.Name), strings.TrimSpace(in.Issuer), strings.TrimSpace(in.ClientID)
	if err := checkName(in.Name); err != nil {
		return Provider{}, err
	}
	if err := checkClient(in.ClientID, in.ClientSecret); err != nil {
		return Provider{}, err
	}
	if in.Issuer == "" {
		return Provider{}, invalid("issuer_invalid", "Paste the provider's issuer address (it starts with https://).")
	}
	if err := s.checkIssuer(ctx, in.Kind, in.Issuer); err != nil {
		return Provider{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Provider{}, err
	}
	now := s.Now().UTC()
	prov := Provider{ID: id, TenantID: p.TenantID, Kind: in.Kind, Name: in.Name, Issuer: in.Issuer, ClientID: in.ClientID,
		Enabled: true, Shown: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	if in.Enabled != nil {
		prov.Enabled = *in.Enabled
	}
	if in.Shown != nil {
		prov.Shown = *in.Shown
	}
	if in.Position != nil {
		prov.Position = *in.Position
	}
	if in.ClientSecret != "" {
		if prov.ClientSecretEnc, err = s.Sealer.Seal(SealID(id), []byte(in.ClientSecret)); err != nil {
			return Provider{}, err
		}
	}
	a.Target = "sso_provider:" + id.String()
	a.Detail = map[string]any{"kind": prov.Kind, "name": prov.Name, "issuer": prov.Issuer, "enabled": prov.Enabled, "shown": prov.Shown}
	if err := s.Store.CreateSSOProvider(ctx, prov, a); err != nil {
		if errors.Is(err, auth.ErrDuplicate) {
			return Provider{}, errNameTaken
		}
		return Provider{}, err
	}
	return prov, nil
}

// List lists providers in button order.
func (s *Service) List(ctx context.Context) ([]Provider, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	return s.Store.ListSSOProviders(ctx, p.TenantID)
}

// Get returns one provider.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Provider, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Provider{}, errNoPrincipal
	}
	prov, err := s.Store.SSOProvider(ctx, p.TenantID, id)
	if errors.Is(err, auth.ErrNotFound) {
		return Provider{}, notFound()
	}
	return prov, err
}

// Update applies patch if ifMatch (an ETag, or "") matches.
func (s *Service) Update(ctx context.Context, id uuid.UUID, patch ProviderPatch, ifMatch string) (Provider, error) {
	if err := auth.RequireConfirmed(ctx, s.Now()); err != nil {
		return Provider{}, err
	}
	_, a, err := audit(ctx, "sso_provider.update", "sso_provider:"+id.String())
	if err != nil {
		return Provider{}, err
	}
	prov, err := s.Get(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	if ifMatch != "" && ifMatch != auth.ETag(prov.Version) {
		return Provider{}, errChanged
	}
	changes := map[string]any{}
	if patch.Name != nil {
		prov.Name = strings.TrimSpace(*patch.Name)
		if err := checkName(prov.Name); err != nil {
			return Provider{}, err
		}
		changes["name"] = prov.Name
	}
	if patch.ClientID != nil {
		prov.ClientID = strings.TrimSpace(*patch.ClientID)
		changes["client_id"] = "changed"
	}
	secret := ""
	if patch.ClientSecret != nil {
		secret = *patch.ClientSecret
		changes["client_secret"] = "changed"
	}
	if err := checkClient(prov.ClientID, secret); err != nil {
		return Provider{}, err
	}
	if patch.ClientSecret != nil {
		prov.ClientSecretEnc = nil
		if secret != "" {
			if prov.ClientSecretEnc, err = s.Sealer.Seal(SealID(id), []byte(secret)); err != nil {
				return Provider{}, err
			}
		}
	}
	if patch.Issuer != nil && strings.TrimSpace(*patch.Issuer) != prov.Issuer {
		if err := s.available(); err != nil {
			return Provider{}, err
		}
		issuer := strings.TrimSpace(*patch.Issuer)
		if err := s.checkIssuer(ctx, prov.Kind, issuer); err != nil {
			return Provider{}, err
		}
		prov.Issuer = issuer
		changes["issuer"] = issuer
	}
	if patch.Enabled != nil {
		prov.Enabled = *patch.Enabled
		changes["enabled"] = prov.Enabled
	}
	if patch.Shown != nil {
		prov.Shown = *patch.Shown
		changes["shown"] = prov.Shown
	}
	if patch.Position != nil {
		prov.Position = *patch.Position
		changes["position"] = prov.Position
	}
	prov.UpdatedAt = s.Now().UTC()
	a.Detail = changes
	out, err := s.Store.UpdateSSOProvider(ctx, prov, a)
	switch {
	case errors.Is(err, auth.ErrVersionChanged):
		return Provider{}, errChanged
	case errors.Is(err, auth.ErrDuplicate):
		return Provider{}, errNameTaken
	case errors.Is(err, auth.ErrNotFound):
		return Provider{}, notFound()
	}
	return out, err
}

// Delete removes a provider; everyone's links to it go too.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if err := auth.RequireConfirmed(ctx, s.Now()); err != nil {
		return err
	}
	p, a, err := audit(ctx, "sso_provider.delete", "sso_provider:"+id.String())
	if err != nil {
		return err
	}
	err = s.Store.DeleteSSOProvider(ctx, p.TenantID, id, a)
	if errors.Is(err, auth.ErrNotFound) {
		return notFound()
	}
	return err
}

// SignInButton is one "Continue with ..." button on the sign-in page.
type SignInButton struct {
	ID         uuid.UUID
	Kind, Name string
}

// Buttons lists the enabled, shown providers for the sign-in page (no
// caller: the page is public).
func (s *Service) Buttons(ctx context.Context, tenant uuid.UUID) ([]SignInButton, error) {
	if s.Client == nil {
		return []SignInButton{}, nil
	}
	all, err := s.Store.ListSSOProviders(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := []SignInButton{}
	for _, p := range all {
		if p.Enabled && p.Shown {
			out = append(out, SignInButton{ID: p.ID, Kind: p.Kind, Name: p.Name})
		}
	}
	return out, nil
}

// Linkable lists the enabled providers a signed-in person may link an
// account on (My account, "confirm it's you").
func (s *Service) Linkable(ctx context.Context) ([]SignInButton, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	if s.Client == nil {
		return []SignInButton{}, nil
	}
	all, err := s.Store.ListSSOProviders(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	out := []SignInButton{}
	for _, prov := range all {
		if prov.Enabled {
			out = append(out, SignInButton{ID: prov.ID, Kind: prov.Kind, Name: prov.Name})
		}
	}
	return out, nil
}
