// Package emailtest is an in-memory email.Store, for tests.
package emailtest

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/email"
)

// Store keeps the setting and the queue in memory, like internal/store.
type Store struct {
	mu      sync.Mutex
	configs map[uuid.UUID]email.Config
	// Queued is every email queued, in order.
	Queued []email.Message
	Audits []auth.AuditEntry
}

// New returns an empty Store.
func New() *Store { return &Store{configs: map[uuid.UUID]email.Config{}} }

// Put stores c as it is, skipping the service's checks.
func (s *Store) Put(c email.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs[c.TenantID] = c
}

func (s *Store) EmailSettings(_ context.Context, tenant uuid.UUID) (email.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.configs[tenant]
	if !ok {
		return email.Config{}, auth.ErrNotFound
	}
	return c, nil
}

func (s *Store) SaveEmailSettings(_ context.Context, c email.Config, version int, a auth.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.configs[c.TenantID]; (ok && cur.Version != version) || (!ok && version != 0) {
		return auth.ErrVersionChanged
	}
	s.configs[c.TenantID] = c
	s.Audits = append(s.Audits, a)
	return nil
}

func (s *Store) EnqueueEmail(_ context.Context, m email.Message, a auth.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Queued = append(s.Queued, m)
	s.Audits = append(s.Audits, a)
	return nil
}

// ClaimEmails claims nothing: tests read Queued instead of sending.
func (s *Store) ClaimEmails(context.Context, time.Time, time.Time, int) ([]email.Message, error) {
	return nil, nil
}

func (s *Store) FinishEmail(context.Context, email.Message, string, string, *time.Time, time.Time) error {
	return nil
}

func (s *Store) NextEmailDue(context.Context) (*time.Time, error) { return nil, nil }

func (s *Store) EmailStatus(_ context.Context, tenant uuid.UUID, _ time.Time) (email.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st email.Status
	for _, m := range s.Queued {
		if m.TenantID == tenant {
			st.Waiting++
		}
	}
	return st, nil
}

func (s *Store) CleanupEmails(context.Context, time.Time) error { return nil }

// Audit records e in Audits, as the store's audit log.
func (s *Store) Audit(ctx context.Context, e auth.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Audits = append(s.Audits, e)
	return nil
}
