package main

import (
	"context"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// fakeStore as an auth.PasskeyStore; it keeps User.PasskeyCount in step,
// as the real store's count does.

func (f *fakeStore) Passkeys(_ context.Context, tenant, user uuid.UUID) ([]auth.Passkey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []auth.Passkey{}
	for _, p := range f.passkeys {
		if p.TenantID == tenant && p.UserID == user {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) PasskeyByCredentialID(_ context.Context, id []byte) (auth.Passkey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.passkeys {
		if string(p.CredentialID) == string(id) {
			return p, nil
		}
	}
	return auth.Passkey{}, auth.ErrNotFound
}

func (f *fakeStore) AddPasskey(_ context.Context, p auth.Passkey, recovery [][]byte, _ auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.users[p.UserID]
	if u.PasskeyCount >= auth.MaxPasskeys {
		return auth.ErrLimit
	}
	f.passkeys = append(f.passkeys, p)
	u.PasskeyCount++
	u.PasswordOnlyAcceptedAt = nil
	if recovery != nil {
		u.RecoveryCodeHashes = recovery
	}
	f.users[p.UserID] = u
	return nil
}

func (f *fakeStore) UsePasskey(_ context.Context, id uuid.UUID, count uint32, backup bool, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.passkeys {
		if p.ID == id {
			f.passkeys[i].SignCount, f.passkeys[i].BackupState, f.passkeys[i].LastUsedAt = count, backup, &at
		}
	}
	return nil
}

func (f *fakeStore) RenamePasskey(_ context.Context, tenant, user, id uuid.UUID, name string, _ auth.AuditEntry) (auth.Passkey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.passkeys {
		if p.ID == id && p.TenantID == tenant && p.UserID == user {
			f.passkeys[i].Name = name
			return f.passkeys[i], nil
		}
	}
	return auth.Passkey{}, auth.ErrNotFound
}

func (f *fakeStore) DeletePasskey(_ context.Context, tenant, user, id uuid.UUID, passwordOnly bool, at time.Time, _ auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.passkeys {
		if p.ID == id && p.TenantID == tenant && p.UserID == user {
			f.passkeys = append(f.passkeys[:i], f.passkeys[i+1:]...)
			u := f.users[user]
			u.PasskeyCount--
			if passwordOnly {
				u.PasswordOnlyAcceptedAt = &at
			}
			f.users[user] = u
			return nil
		}
	}
	return auth.ErrNotFound
}
