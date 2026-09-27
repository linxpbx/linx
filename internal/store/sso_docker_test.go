package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/sso"
)

// TestSSODocker runs company sign-in's queries (migration 0023) against
// real Postgres: providers with optimistic concurrency and unique names,
// links unique by subject and by person-and-provider, the cascade when a
// provider goes, the names people.company_sign_in shows, and "people must
// use company sign-in". make test-docker.
func TestSSODocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-sso-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	audit := func(action string) auth.AuditEntry {
		return auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: action, Result: auth.ResultOK}
	}
	provider := func(name string, position int) sso.Provider {
		t.Helper()
		p := sso.Provider{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Kind: sso.KindOIDC, Name: name,
			Issuer: "https://idp.example.com", ClientID: "linx", ClientSecretEnc: []byte{1, 2, 3},
			Enabled: true, Shown: true, Position: position, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateSSOProvider(ctx, p, audit("sso_provider.create")); err != nil {
			t.Fatalf("CreateSSOProvider: %v", err)
		}
		return p
	}

	google := provider("Google", 1)
	entra := provider("Microsoft", 0)
	if err := s.CreateSSOProvider(ctx, sso.Provider{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Kind: sso.KindOIDC,
		Name: "Google", Issuer: "https://x.example", ClientID: "c", Version: 1, CreatedAt: now, UpdatedAt: now},
		audit("sso_provider.create")); !errors.Is(err, auth.ErrDuplicate) {
		t.Fatalf("duplicate name = %v", err)
	}
	if err := s.CreateSSOProvider(ctx, sso.Provider{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Kind: sso.KindOIDC,
		Name: "Plain", Issuer: "http://x.example", ClientID: "c", Version: 1, CreatedAt: now, UpdatedAt: now},
		audit("sso_provider.create")); err == nil {
		t.Fatal("an http:// issuer was stored")
	}
	list, err := s.ListSSOProviders(ctx, tenant)
	if err != nil || len(list) != 2 || list[0].ID != entra.ID {
		t.Fatalf("list = %+v %v (want Microsoft first: position 0)", list, err)
	}

	// Optimistic concurrency.
	google.Name = "Google Workspace"
	up, err := s.UpdateSSOProvider(ctx, google, audit("sso_provider.update"))
	if err != nil || up.Version != 2 || up.Name != "Google Workspace" || !slices.Equal(up.ClientSecretEnc, []byte{1, 2, 3}) {
		t.Fatalf("update = %+v %v", up, err)
	}
	if _, err := s.UpdateSSOProvider(ctx, google, audit("sso_provider.update")); !errors.Is(err, auth.ErrVersionChanged) {
		t.Fatalf("stale update = %v", err)
	}
	google = up

	// Links.
	newUser := func(email string) auth.User {
		u := auth.User{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Email: email, Name: "P", Role: auth.RoleUser,
			PasswordHash: "x", PasswordUpdatedAt: now, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateUser(ctx, u, audit("user.create")); err != nil {
			t.Fatal(err)
		}
		return u
	}
	sara, omar := newUser("sara@example.com"), newUser("omar@example.com")
	link := func(u auth.User, p sso.Provider, subject string) error {
		return s.AddCompanyLink(ctx, auth.CompanyLinkInfo{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, UserID: u.ID,
			ProviderID: p.ID, Subject: subject, Email: u.Email, CreatedAt: now}, audit("user.company_linked"))
	}
	if err := link(sara, google, "g-sara"); err != nil {
		t.Fatal(err)
	}
	if err := link(sara, entra, "m-sara"); err != nil {
		t.Fatal(err)
	}
	if err := link(omar, google, "g-sara"); !errors.Is(err, auth.ErrDuplicate) {
		t.Fatalf("same subject twice = %v", err)
	}
	if err := link(sara, google, "g-sara-2"); !errors.Is(err, auth.ErrDuplicate) {
		t.Fatalf("second account on one provider = %v", err)
	}
	got, err := s.CompanyLinkBySubject(ctx, tenant, google.ID, "g-sara")
	if err != nil || got.UserID != sara.ID || got.ProviderName != "Google Workspace" {
		t.Fatalf("by subject = %+v %v", got, err)
	}
	if _, err := s.CompanyLinkBySubject(ctx, tenant, entra.ID, "g-sara"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("subject on the other provider = %v", err)
	}
	if err := s.UseCompanyLink(ctx, got.ID, now); err != nil {
		t.Fatal(err)
	}
	u, err := s.User(ctx, tenant, sara.ID)
	if err != nil || !slices.Equal(u.CompanyLogins, []string{"Microsoft", "Google Workspace"}) {
		t.Fatalf("company_sign_in = %v %v", u.CompanyLogins, err)
	}

	// Removing a link, then a provider (its links go with it).
	links, _ := s.CompanyLinks(ctx, tenant, sara.ID)
	if len(links) != 2 {
		t.Fatalf("links = %+v", links)
	}
	if err := s.RemoveCompanyLink(ctx, tenant, omar.ID, links[0].ID, audit("user.company_unlinked")); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("removing someone else's link = %v", err)
	}
	if err := s.RemoveCompanyLink(ctx, tenant, sara.ID, links[0].ID, audit("user.company_unlinked")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSSOProvider(ctx, tenant, google.ID, audit("sso_provider.delete")); err != nil {
		t.Fatal(err)
	}
	if links, _ := s.CompanyLinks(ctx, tenant, sara.ID); len(links) != 0 {
		t.Fatalf("links after the provider went = %+v", links)
	}
	if err := s.DeleteSSOProvider(ctx, tenant, google.ID, audit("sso_provider.delete")); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}

	// "People must use company sign-in".
	if req, err := s.CompanySignInRequired(ctx, tenant); err != nil || req {
		t.Fatalf("default = %v %v", req, err)
	}
	cur, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cur.CompanySignInRequired = true
	if _, err := s.UpdateSettings(ctx, cur, audit("settings.update")); err != nil {
		t.Fatal(err)
	}
	if req, err := s.CompanySignInRequired(ctx, tenant); err != nil || !req {
		t.Fatalf("after turning on = %v %v", req, err)
	}
}
