// Package helpanswerstest is an in-memory helpanswers.Store, for tests.
package helpanswerstest

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/helpanswers"
)

// Store keeps the setting and counts in memory, like internal/store.
type Store struct {
	mu      sync.Mutex
	configs map[uuid.UUID]helpanswers.Config
	counts  map[string]int // tenant/day/user
	Audit   []auth.AuditEntry
}

// New returns an empty Store.
func New() *Store {
	return &Store{configs: map[uuid.UUID]helpanswers.Config{}, counts: map[string]int{}}
}

// Put stores c as it is, skipping the service's checks (a provider on a
// test server's loopback address, say).
func (s *Store) Put(c helpanswers.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Version == 0 {
		c.Version = 1
	}
	s.configs[c.TenantID] = c
}

func (s *Store) HelpAnswers(_ context.Context, tenant uuid.UUID) (helpanswers.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.configs[tenant]
	if !ok {
		return helpanswers.Config{}, auth.ErrNotFound
	}
	return c, nil
}

func (s *Store) SaveHelpAnswers(_ context.Context, c helpanswers.Config, version int, audit auth.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.configs[c.TenantID]; cur.Version != version {
		return auth.ErrVersionChanged
	}
	s.configs[c.TenantID] = c
	s.Audit = append(s.Audit, audit)
	return nil
}

func key(tenant uuid.UUID, day time.Time, user uuid.UUID) string {
	return tenant.String() + "/" + day.UTC().Format(time.DateOnly) + "/" + user.String()
}

func (s *Store) used(tenant uuid.UUID, day time.Time) int {
	prefix := tenant.String() + "/" + day.UTC().Format(time.DateOnly) + "/"
	n := 0
	for k, c := range s.counts {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			n += c
		}
	}
	return n
}

func (s *Store) TakeHelpAnswer(_ context.Context, tenant, user uuid.UUID, day time.Time, personMax, serverMax int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tenant, day, user)
	switch {
	case s.counts[k] >= personMax:
		return "person", nil
	case s.used(tenant, day) >= serverMax:
		return "server", nil
	}
	s.counts[k]++
	return "", nil
}

func (s *Store) HelpAnswersUsedToday(_ context.Context, tenant uuid.UUID, day time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used(tenant, day), nil
}
