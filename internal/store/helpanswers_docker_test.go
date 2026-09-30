package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/helpanswers"
)

// TestHelpAnswersDocker runs written answers' queries (migration 0029)
// against real Postgres: the setting with optimistic concurrency and its
// checks, and the daily counts, including many questions at once against
// the server's limit and last week's counts going. make test-docker.
func TestHelpAnswersDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-help-answers-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "help_answers.update", Result: auth.ResultOK}

	if _, err := s.HelpAnswers(ctx, tenant); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("before saving: %v", err)
	}
	c := helpanswers.Defaults(tenant)
	c.Enabled, c.APIKeyEnc, c.Version, c.UpdatedAt = true, []byte{1, 2, 3}, 1, now
	if err := s.SaveHelpAnswers(ctx, c, 0, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHelpAnswers(ctx, c, 0, audit); !errors.Is(err, auth.ErrVersionChanged) {
		t.Fatalf("a second first save: %v", err)
	}
	c.Provider, c.BaseURL, c.Model, c.APIKeyEnc, c.Version = helpanswers.ProviderOllama, "https://ollama.example.com", "llama3.2", nil, 2
	if err := s.SaveHelpAnswers(ctx, c, 1, audit); err != nil {
		t.Fatal(err)
	}
	got, err := s.HelpAnswers(ctx, tenant)
	if err != nil || got.Provider != "ollama" || got.BaseURL != c.BaseURL || got.APIKeyEnc != nil || got.Version != 2 || !got.UpdatedAt.Equal(now) {
		t.Fatalf("%+v %v", got, err)
	}
	c.BaseURL, c.Version = "http://ollama.example.com", 3
	if err := s.SaveHelpAnswers(ctx, c, 2, audit); err == nil {
		t.Fatal("an http:// address was stored")
	}

	// Counts.
	newUser := func(email string) uuid.UUID {
		u := auth.User{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Email: email, Name: "P", Role: auth.RoleUser,
			PasswordHash: "x", PasswordUpdatedAt: now, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateUser(ctx, u, auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "user.create", Result: auth.ResultOK}); err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	sara, omar := newUser("sara@example.com"), newUser("omar@example.com")
	day := time.Date(2026, 9, 30, 23, 30, 0, 0, time.FixedZone("+03", 3*3600)) // 20:30 UTC
	take := func(user uuid.UUID, at time.Time) string {
		t.Helper()
		which, err := s.TakeHelpAnswer(ctx, tenant, user, at, 2, 3)
		if err != nil {
			t.Fatal(err)
		}
		return which
	}
	if take(sara, day) != "" || take(sara, day) != "" || take(sara, day) != "person" {
		t.Fatal("a person's limit")
	}
	if take(omar, day) != "" || take(omar, day) != "server" {
		t.Fatal("the server's limit")
	}
	if n, err := s.HelpAnswersUsedToday(ctx, tenant, day); err != nil || n != 3 {
		t.Fatalf("used today %d %v", n, err)
	}

	// Many at once can't pass the server's limit.
	next := day.Add(24 * time.Hour)
	var wg sync.WaitGroup
	results := make(chan string, 20)
	for i := range 20 {
		user := sara
		if i%2 == 1 {
			user = omar
		}
		wg.Go(func() {
			which, err := s.TakeHelpAnswer(ctx, tenant, user, next, 100, 5)
			if err != nil {
				t.Error(err)
			}
			results <- which
		})
	}
	wg.Wait()
	close(results)
	taken := 0
	for r := range results {
		if r == "" {
			taken++
		}
	}
	if taken != 5 {
		t.Errorf("%d questions counted against a limit of 5", taken)
	}

	// A new day more than a week later forgets the old counts.
	if take(sara, day.Add(9*24*time.Hour)) != "" {
		t.Fatal("a new day")
	}
	var old int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM help_answer_usage WHERE day < '2026-10-02'`).Scan(&old); err != nil || old != 0 {
		t.Errorf("%d old rows left, %v", old, err)
	}
}
