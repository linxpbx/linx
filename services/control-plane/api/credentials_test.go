package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// fakeCredentialStore is a minimal in-memory CredentialStore, just enough
// to exercise revokeCredentialsCreatedBy.
type fakeCredentialStore struct {
	mu    sync.Mutex
	creds map[uuid.UUID]auth.Credential
}

func newFakeCredentialStore() *fakeCredentialStore {
	return &fakeCredentialStore{creds: map[uuid.UUID]auth.Credential{}}
}

func (f *fakeCredentialStore) CreateCredential(_ context.Context, c auth.Credential, _ auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds[c.ID] = c
	return nil
}

func (f *fakeCredentialStore) TenantCredential(_ context.Context, kind string, tenant, id uuid.UUID) (auth.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.creds[id]
	if !ok || c.Kind != kind || c.TenantID != tenant {
		return auth.Credential{}, auth.ErrNotFound
	}
	return c, nil
}

func (f *fakeCredentialStore) ListCredentials(_ context.Context, kind string, tenant uuid.UUID, before *uuid.UUID, limit int) ([]auth.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []auth.Credential
	for _, c := range f.creds {
		if c.Kind == kind && c.TenantID == tenant {
			out = append(out, c)
		}
	}
	_ = before
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeCredentialStore) RevokeCredential(_ context.Context, kind string, tenant, id uuid.UUID, at time.Time, _ auth.AuditEntry) (auth.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.creds[id]
	if !ok || c.Kind != kind || c.TenantID != tenant {
		return auth.Credential{}, auth.ErrNotFound
	}
	c.RevokedAt = &at
	f.creds[id] = c
	return c, nil
}

// TestRevokeCredentialsCreatedByDisabledUser is the fix for a pre-launch
// audit finding: disabling a person left every API key or OAuth client
// they'd created still working at its original scope. Now DisableUser and
// UpdateUser(disabled: true) both revoke everything that person created,
// and leave everyone else's credentials alone.
func TestRevokeCredentialsCreatedByDisabledUser(t *testing.T) {
	store := newFakeCredentialStore()
	s := &Server{store: store, now: time.Now}
	tenant := uuid.New()
	disabledUser := uuid.New()
	otherUser := uuid.New()

	mkCred := func(kind string, creator uuid.UUID) auth.Credential {
		caller := auth.Principal{Type: auth.TypeUser, ID: creator.String(), TenantID: tenant, Role: auth.RoleAdmin, Scopes: auth.Scopes}
		c, _, err := auth.NewCredential(caller, auth.CredentialRequest{Kind: kind, Name: "test", Role: auth.RoleUser, Scopes: []string{"team:read"}}, time.Now())
		if err != nil {
			t.Fatalf("NewCredential: %v", err)
		}
		if err := store.CreateCredential(context.Background(), c, auth.AuditEntry{}); err != nil {
			t.Fatal(err)
		}
		return c
	}

	byDisabledKey := mkCred(auth.TypeAPIKey, disabledUser)
	byDisabledClient := mkCred(auth.TypeOAuthClient, disabledUser)
	byOther := mkCred(auth.TypeAPIKey, otherUser)

	actingAdmin := auth.Principal{Type: auth.TypeUser, ID: uuid.New().String(), TenantID: tenant, Role: auth.RoleSystemAdmin, Scopes: auth.Scopes}
	ctx := auth.WithPrincipal(context.Background(), actingAdmin)

	s.revokeCredentialsCreatedBy(ctx, disabledUser)

	got, err := store.TenantCredential(ctx, auth.TypeAPIKey, tenant, byDisabledKey.ID)
	if err != nil || got.RevokedAt == nil {
		t.Errorf("API key created by the disabled user should be revoked: %+v, %v", got, err)
	}
	got2, err := store.TenantCredential(ctx, auth.TypeOAuthClient, tenant, byDisabledClient.ID)
	if err != nil || got2.RevokedAt == nil {
		t.Errorf("OAuth client created by the disabled user should be revoked: %+v, %v", got2, err)
	}
	got3, err := store.TenantCredential(ctx, auth.TypeAPIKey, tenant, byOther.ID)
	if err != nil || got3.RevokedAt != nil {
		t.Errorf("another person's credential must not be touched: %+v, %v", got3, err)
	}
}
