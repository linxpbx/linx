package main

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/settings"
)

// fakeStore is an in-memory stand-in for internal/store (which is tested
// against real Postgres in make test-docker).
type fakeStore struct {
	mu         sync.Mutex
	tenant     uuid.UUID
	creds      map[uuid.UUID]auth.Credential
	revoked    map[string]bool
	audits     []auth.AuditEntry
	users      map[uuid.UUID]auth.User
	links      map[string]auth.SetupLink
	sessions   map[uuid.UUID]auth.UserSession
	extensions map[string]pbx.Extension
	auditLog   []auth.AuditLogEntry
	settings   *settings.Settings
	passkeys   []auth.Passkey
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		tenant:  uuid.Must(uuid.NewV7()),
		creds:   map[uuid.UUID]auth.Credential{},
		revoked: map[string]bool{},
	}
}

func (f *fakeStore) DefaultTenant(context.Context) (uuid.UUID, error) { return f.tenant, nil }

func (f *fakeStore) CreateCredential(_ context.Context, c auth.Credential, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds[c.ID] = c
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) CredentialByPublicID(_ context.Context, kind, publicID string) (auth.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.creds {
		if c.Kind == kind && c.PublicID == publicID {
			return c, nil
		}
	}
	return auth.Credential{}, auth.ErrNotFound
}

func (f *fakeStore) CredentialByID(_ context.Context, kind string, id uuid.UUID) (auth.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.creds[id]; ok && c.Kind == kind {
		return c, nil
	}
	return auth.Credential{}, auth.ErrNotFound
}

func (f *fakeStore) TenantCredential(ctx context.Context, kind string, tenant, id uuid.UUID) (auth.Credential, error) {
	c, err := f.CredentialByID(ctx, kind, id)
	if err == nil && c.TenantID != tenant {
		return auth.Credential{}, auth.ErrNotFound
	}
	return c, err
}

func (f *fakeStore) ListCredentials(_ context.Context, kind string, tenant uuid.UUID, before *uuid.UUID, limit int) ([]auth.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []auth.Credential
	for _, c := range f.creds {
		if c.Kind == kind && c.TenantID == tenant && (before == nil || c.ID.String() < before.String()) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b auth.Credential) int { return -compareUUID(a.ID, b.ID) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func compareUUID(a, b uuid.UUID) int {
	switch {
	case a.String() < b.String():
		return -1
	case a.String() > b.String():
		return 1
	}
	return 0
}

func (f *fakeStore) RevokeCredential(_ context.Context, kind string, tenant, id uuid.UUID, at time.Time, a auth.AuditEntry) (auth.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.creds[id]
	if !ok || c.Kind != kind || c.TenantID != tenant {
		return auth.Credential{}, auth.ErrNotFound
	}
	if c.RevokedAt == nil {
		c.RevokedAt = &at
		f.creds[id] = c
	}
	f.audits = append(f.audits, a)
	return c, nil
}

func (f *fakeStore) TouchCredential(_ context.Context, _ string, id uuid.UUID, ip netip.Addr, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.creds[id]
	c.LastUsedAt, c.LastUsedIP = &at, &ip
	f.creds[id] = c
	return nil
}

func (f *fakeStore) TokenRevoked(_ context.Context, jti string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revoked[jti], nil
}

func (f *fakeStore) Audit(_ context.Context, e auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, e)
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	f.auditLog = append(f.auditLog, auth.AuditLogEntry{
		ID: id, At: time.Now(), Actor: e.Actor, IP: e.IP, Action: e.Action, Target: e.Target, Result: e.Result, Detail: e.Detail,
	})
	return nil
}

// ListAuditLog is a simple in-memory stand-in: no cursor pagination (tests
// don't need more than one page), newest first.
func (f *fakeStore) ListAuditLog(_ context.Context, _ uuid.UUID, filter auth.AuditLogFilter, _ *uuid.UUID, limit int) ([]auth.AuditLogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []auth.AuditLogEntry{}
	for i := len(f.auditLog) - 1; i >= 0; i-- {
		e := f.auditLog[i]
		if filter.Actor != "" && e.Actor != filter.Actor {
			continue
		}
		if filter.Action != "" && !strings.HasPrefix(e.Action, filter.Action) {
			continue
		}
		if filter.Target != "" && e.Target != filter.Target {
			continue
		}
		if filter.Since != nil && e.At.Before(*filter.Since) {
			continue
		}
		if filter.Until != nil && e.At.After(*filter.Until) {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) auditActions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.audits {
		out = append(out, a.Action)
	}
	return out
}

func (f *fakeStore) update(id uuid.UUID, change func(*auth.Credential)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.creds[id]
	change(&c)
	f.creds[id] = c
}
