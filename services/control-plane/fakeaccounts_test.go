package main

import (
	"context"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
)

// addExtension registers an extension for `linx user create --extension`
// tests to find; fakeStore otherwise has no PBX data of its own.
func (f *fakeStore) addExtension(e pbx.Extension) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.extensions == nil {
		f.extensions = map[string]pbx.Extension{}
	}
	f.extensions[e.Number] = e
}

func (f *fakeStore) ExtensionByNumber(_ context.Context, tenant uuid.UUID, number string) (pbx.Extension, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.extensions[number]; ok && e.TenantID == tenant {
		return e, nil
	}
	return pbx.Extension{}, pbx.ErrNotFound
}

// The methods below extend fakeStore to also back auth.Accounts in tests
// (internal/store's real implementation is tested against Postgres in
// make test-docker).

func (f *fakeStore) ensureAccountMaps() {
	if f.users == nil {
		f.users = map[uuid.UUID]auth.User{}
	}
	if f.links == nil {
		f.links = map[string]auth.SetupLink{}
	}
	if f.sessions == nil {
		f.sessions = map[uuid.UUID]auth.UserSession{}
	}
}

func (f *fakeStore) CreateUser(_ context.Context, u auth.User, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureAccountMaps()
	for _, existing := range f.users {
		if existing.TenantID == u.TenantID && existing.Email == u.Email {
			return auth.ErrDuplicate
		}
	}
	f.users[u.ID] = u
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) User(_ context.Context, tenant, id uuid.UUID) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok || u.TenantID != tenant {
		return auth.User{}, auth.ErrNotFound
	}
	return u, nil
}

func (f *fakeStore) UserByEmail(_ context.Context, tenant uuid.UUID, email string) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.TenantID == tenant && u.Email == email {
			return u, nil
		}
	}
	return auth.User{}, auth.ErrNotFound
}

func (f *fakeStore) ListUsers(_ context.Context, tenant uuid.UUID, _ *uuid.UUID, limit int) ([]auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []auth.User{}
	for _, u := range f.users {
		if u.TenantID == tenant {
			out = append(out, u)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) UpdateUser(_ context.Context, u auth.User, a auth.AuditEntry) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.users[u.ID]
	if !ok {
		return auth.User{}, auth.ErrNotFound
	}
	if cur.Version != u.Version {
		return auth.User{}, auth.ErrVersionChanged
	}
	u.Version++
	f.users[u.ID] = u
	f.audits = append(f.audits, a)
	return u, nil
}

func (f *fakeStore) DisableUser(_ context.Context, tenant, id uuid.UUID, at time.Time, a auth.AuditEntry) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok || u.TenantID != tenant {
		return auth.User{}, auth.ErrNotFound
	}
	if u.DisabledAt == nil {
		u.DisabledAt = &at
		u.Version++
	}
	f.users[id] = u
	for sid, s := range f.sessions {
		if s.UserID == id && s.RevokedAt == nil {
			s.RevokedAt = &at
			f.sessions[sid] = s
		}
	}
	f.audits = append(f.audits, a)
	return u, nil
}

func (f *fakeStore) SetPassword(_ context.Context, _, user uuid.UUID, hash string, at time.Time, revoke bool, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[user]
	if !ok {
		return auth.ErrNotFound
	}
	u.PasswordHash, u.PasswordUpdatedAt = hash, at
	f.users[user] = u
	if revoke {
		for sid, s := range f.sessions {
			if s.UserID == user && s.RevokedAt == nil {
				s.RevokedAt = &at
				f.sessions[sid] = s
			}
		}
	}
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) SetMFASecret(_ context.Context, _, user uuid.UUID, sealed []byte, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[user]
	if !ok {
		return auth.ErrNotFound
	}
	u.MFAPendingSecretEnc = sealed
	f.users[user] = u
	return nil
}

func (f *fakeStore) ConfirmMFA(_ context.Context, _, user uuid.UUID, hashes [][]byte, step int64, _ time.Time, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[user]
	if !ok {
		return auth.ErrNotFound
	}
	u.MFASecretEnc, u.MFAPendingSecretEnc = u.MFAPendingSecretEnc, nil
	u.MFAEnabled = true
	u.RecoveryCodeHashes = hashes
	u.MFALastStep = &step
	f.users[user] = u
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) ConsumeRecoveryCode(_ context.Context, _, user uuid.UUID, hash []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[user]
	if !ok {
		return false, auth.ErrNotFound
	}
	out := make([][]byte, 0, len(u.RecoveryCodeHashes))
	for _, h := range u.RecoveryCodeHashes {
		if string(h) != string(hash) {
			out = append(out, h)
		}
	}
	found := len(out) < len(u.RecoveryCodeHashes)
	u.RecoveryCodeHashes = out
	f.users[user] = u
	return found, nil
}

func (f *fakeStore) UseTOTPStep(_ context.Context, _, user uuid.UUID, step int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[user]
	if !ok {
		return false, auth.ErrNotFound
	}
	if u.MFALastStep != nil && *u.MFALastStep >= step {
		return false, nil
	}
	u.MFALastStep = &step
	f.users[user] = u
	return true, nil
}

func (f *fakeStore) RecordLoginSuccess(_ context.Context, _, user uuid.UUID, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[user]
	if !ok {
		return auth.ErrNotFound
	}
	u.FailedAttempts, u.LockedUntil, u.FailureWindowStart, u.FailureWindowCount = 0, nil, nil, 0
	f.users[user] = u
	return nil
}

func (f *fakeStore) RecordLoginFailure(_ context.Context, _, user uuid.UUID, at time.Time) (*time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[user]
	if !ok {
		return nil, false, auth.ErrNotFound
	}
	u.FailedAttempts++
	if u.FailureWindowStart == nil || at.Sub(*u.FailureWindowStart) > time.Hour {
		u.FailureWindowStart = &at
		u.FailureWindowCount = 1
	} else {
		u.FailureWindowCount++
	}
	var lockedUntil *time.Time
	if u.FailedAttempts >= 5 {
		wait := time.Minute * time.Duration(1<<uint(u.FailedAttempts-5))
		if wait > time.Hour {
			wait = time.Hour
		}
		until := at.Add(wait)
		lockedUntil = &until
		u.LockedUntil = &until
	}
	alert := u.FailureWindowCount == 20
	f.users[user] = u
	return lockedUntil, alert, nil
}

func (f *fakeStore) CreateSetupLink(_ context.Context, l auth.SetupLink) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureAccountMaps()
	f.links[string(l.TokenHash)] = l
	return nil
}

func (f *fakeStore) SetupLinkByTokenHash(_ context.Context, hash []byte) (auth.SetupLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.links[string(hash)]
	if !ok {
		return auth.SetupLink{}, auth.ErrNotFound
	}
	return l, nil
}

func (f *fakeStore) ConsumeSetupLink(_ context.Context, id uuid.UUID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, l := range f.links {
		if l.ID == id {
			if l.UsedAt != nil || !at.Before(l.ExpiresAt) {
				break
			}
			l.UsedAt = &at
			f.links[k] = l
			return nil
		}
	}
	return auth.ErrNotFound
}

func (f *fakeStore) CreateSession(_ context.Context, s auth.UserSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureAccountMaps()
	f.sessions[s.ID] = s
	return nil
}

func (f *fakeStore) RevokeSession(_ context.Context, id uuid.UUID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if ok && s.RevokedAt == nil {
		s.RevokedAt = &at
		f.sessions[id] = s
	}
	return nil
}

func (f *fakeStore) RevokeUserSessions(_ context.Context, user uuid.UUID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, s := range f.sessions {
		if s.UserID == user && s.RevokedAt == nil {
			s.RevokedAt = &at
			f.sessions[id] = s
		}
	}
	return nil
}

func (f *fakeStore) PromoteSession(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return auth.ErrNotFound
	}
	s.MFAVerified = true
	f.sessions[id] = s
	return nil
}

func (f *fakeStore) SessionByTokenHash(_ context.Context, hash []byte) (auth.UserSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if string(s.TokenHash) == string(hash) {
			return s, nil
		}
	}
	return auth.UserSession{}, auth.ErrNotFound
}

func (f *fakeStore) TouchSession(_ context.Context, id uuid.UUID, lastSeen, idleExpires time.Time, ip netip.Addr) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return auth.ErrNotFound
	}
	s.LastSeenAt, s.IdleExpiresAt = lastSeen, idleExpires
	if ip.IsValid() {
		s.LastSeenIP = &ip
	}
	f.sessions[id] = s
	return nil
}

// sessionLive is the fake's device_live rule (migration 0013) for a web
// device's session: live, past MFA, and its person not disabled.
func (f *fakeStore) sessionLive(id uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok || s.RevokedAt != nil || !s.MFAVerified || !time.Now().Before(s.ExpiresAt) {
		return false
	}
	u, ok := f.users[s.UserID]
	return ok && u.DisabledAt == nil
}

func (f *fakeStore) RecordLockedAttempt(_ context.Context, _, user uuid.UUID, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[user]
	if !ok {
		return false, auth.ErrNotFound
	}
	if u.FailureWindowStart == nil || at.Sub(*u.FailureWindowStart) > time.Hour {
		u.FailureWindowStart = &at
		u.FailureWindowCount = 1
	} else {
		u.FailureWindowCount++
	}
	f.users[user] = u
	return u.FailureWindowCount == 20, nil
}
