package callhistory

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// Store is the database access call history needs (internal/store).
type Store interface {
	Lookup
	// UnreadCDR returns every row of the calls that have a row Linx hasn't
	// read yet (at most about batch calls), by linkedid, and the newest
	// row id they were found by.
	UnreadCDR(ctx context.Context, batch int) (map[string][]Row, int64, error)
	// SaveCalls keeps calls (a call seen before is replaced, keeping its
	// id) and marks asterisk.cdr read up to readTo, in one transaction.
	SaveCalls(ctx context.Context, calls []Call, readTo int64, at time.Time) error
	// ExpireCallHistory deletes calls older than the kept days and the
	// rows already read that are a day old, returning the tenants that
	// lost calls.
	ExpireCallHistory(ctx context.Context, now time.Time) ([]uuid.UUID, error)

	ListCalls(ctx context.Context, f Filter) ([]Listed, error)
	// UserExtension is the signed-in person's extension (nil: none).
	UserExtension(ctx context.Context, user uuid.UUID) (*uuid.UUID, error)
	MissedCount(ctx context.Context, user uuid.UUID) (int, error)
	SetCallsSeen(ctx context.Context, user uuid.UUID, at time.Time) error
	CallHistoryKeepDays(ctx context.Context) (int, error)
	SetCallHistoryKeepDays(ctx context.Context, days int, audit auth.AuditEntry) error
	CallHistoryUsage(ctx context.Context, tenant uuid.UUID) (Usage, error)
}

// Usage is how many calls are kept and the space they take.
type Usage struct {
	Calls int64
	Bytes int64
}

const (
	// settle: Asterisk writes a call's rows as its channels go, around
	// the moment the call tracker hears the call ended; wait this long
	// so one pass sees them all. A row that still comes later makes the
	// call be read again.
	settle = 2 * time.Second
	batch  = 500
	// expireEvery: old calls go hourly, like voicemail.
	expireEvery = time.Hour
)

// Builder reads Asterisk's new call records into call history: at start,
// whenever Kick is called (a call ended), and before a list is shown.
type Builder struct {
	Store   Store
	Changed func(tenant uuid.UUID) // the Call history badge's counter
	// Missed, if set, is told about each person who missed a call once the
	// call is in the history: the phone that was asleep hears about it
	// then (the quiet notification, internal/push, docs/PHASE2.md §5).
	Missed func(ctx context.Context, extension uuid.UUID, from string)
	Now    func() time.Time
	Log    *slog.Logger
	// Settle overrides settle (tests).
	Settle time.Duration

	mu   sync.Mutex
	once sync.Once
	kick chan struct{}
}

func (b *Builder) init() { b.once.Do(func() { b.kick = make(chan struct{}, 1) }) }

// Kick asks Run to read new call records soon. Never blocks.
func (b *Builder) Kick() {
	b.init()
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// Run reads what's waiting, then again after every Kick, and deletes
// old calls hourly, until ctx ends.
func (b *Builder) Run(ctx context.Context) {
	b.init()
	wait := b.Settle
	if wait == 0 {
		wait = settle
	}
	expire := time.NewTicker(expireEvery)
	defer expire.Stop()
	b.logged(ctx)
	b.expire(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-expire.C:
			b.expire(ctx)
		case <-b.kick:
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			b.logged(ctx)
		}
	}
}

func (b *Builder) logged(ctx context.Context) {
	if err := b.Read(ctx); err != nil && ctx.Err() == nil {
		b.Log.Error("reading call records failed; trying again with the next call", "err", err)
	}
}

func (b *Builder) expire(ctx context.Context) {
	tenants, err := b.Store.ExpireCallHistory(ctx, b.Now())
	if err != nil && ctx.Err() == nil {
		b.Log.Error("deleting old call history failed; trying again in an hour", "err", err)
	}
	for _, t := range tenants {
		b.changed(t)
	}
}

// missed tells whoever is listening about every person a call went to and
// nobody answered — one notice per person per call, and none at all for a
// call that was answered somewhere.
func (b *Builder) missed(ctx context.Context, calls []Call) {
	if b.Missed == nil {
		return
	}
	for _, c := range calls {
		if c.AnsweredAt != nil {
			continue
		}
		for _, party := range c.Parties {
			if party.Missed {
				b.Missed(ctx, party.ExtensionID, c.FromNumber)
			}
		}
	}
}

func (b *Builder) changed(tenant uuid.UUID) {
	if b.Changed != nil {
		b.Changed(tenant)
	}
}

// Read turns every unread call record into call history now.
func (b *Builder) Read(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		byCall, readTo, err := b.Store.UnreadCDR(ctx, batch)
		if err != nil {
			return err
		}
		if len(byCall) == 0 {
			return nil
		}
		calls := make([]Call, 0, len(byCall))
		tenants := map[uuid.UUID]bool{}
		for _, rows := range byCall {
			c, err := Build(ctx, b.Store, rows)
			if err != nil {
				return err
			}
			calls = append(calls, c)
			tenants[c.TenantID] = true
		}
		if err := b.Store.SaveCalls(ctx, calls, readTo, b.Now()); err != nil {
			return err
		}
		for t := range tenants {
			b.changed(t)
		}
		b.missed(ctx, calls)
	}
}
