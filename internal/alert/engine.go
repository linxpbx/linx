package alert

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// StableFor: an alert must stay open this long before it notifies
	// anyone, so a flapping problem doesn't spam every channel
	// (docs/API.md §5).
	StableFor = 5 * time.Minute
	// ReminderInterval: how often an open alert reminds its channels.
	ReminderInterval = 24 * time.Hour
	// Retention of the delivery log.
	Retention = 30 * 24 * time.Hour
	// lease is how long a claimed delivery belongs to one worker.
	lease = 2 * time.Minute
)

// Engine decides when a reported problem becomes a real notification and
// sends it. Reporting (Fire/Resolve) is separate and cheap — feature code
// calls it as often as it likes; the Engine's tick is what looks at
// timing, severity and quiet hours.
type Engine struct {
	Store  Store
	Sender *Sender
	Log    *slog.Logger
	// Concurrency is how many deliveries are sent at once (default 8).
	Concurrency int
	// Poll is how often to tick when idle (default 5 s: alerts are less
	// time-critical than webhooks, so this can be coarser).
	Poll time.Duration
}

// Fire reports that a problem is happening (or still is): key identifies
// it, severity/title/message/link describe it in plain language. Safe to
// call often (e.g. every health check); only a state change matters.
func (e *Engine) Fire(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string) error {
	_, _, err := e.Store.Fire(ctx, tenant, key, severity, title, message, link, e.Sender.Now())
	return err
}

// FireAfter is Fire for a problem that should notify after holdBack
// rather than the usual StableFor (never sooner than now).
func (e *Engine) FireAfter(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string, holdBack time.Duration) error {
	now := e.Sender.Now()
	_, _, err := e.Store.FireWith(ctx, tenant, key, severity, title, message, link, now,
		FireOptions{StableSince: now.Add(holdBack - StableFor)})
	return err
}

// Announce tells the alert channels about something that happened (an
// emergency call, a first call to a country) at the engine's next tick,
// once: key must be new for each occurrence. The alert closes itself as
// it's sent.
func (e *Engine) Announce(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string) error {
	now := e.Sender.Now()
	_, _, err := e.Store.FireWith(ctx, tenant, key, severity, title, message, link, now,
		FireOptions{StableSince: now.Add(-StableFor), OneShot: true})
	return err
}

// Resolve reports that the problem behind key has cleared.
func (e *Engine) Resolve(ctx context.Context, tenant uuid.UUID, key string) error {
	_, _, err := e.Store.Resolve(ctx, tenant, key, e.Sender.Now())
	return err
}

// Run ticks until ctx is cancelled, then waits for sends in flight.
func (e *Engine) Run(ctx context.Context) {
	concurrency := e.Concurrency
	if concurrency <= 0 {
		concurrency = 8
	}
	poll := e.Poll
	if poll <= 0 {
		poll = 5 * time.Second
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()

	var lastCleanup time.Time
	for ctx.Err() == nil {
		now := e.Sender.Now()
		if now.Sub(lastCleanup) >= time.Hour {
			if err := e.Store.CleanupAlerts(ctx, now.Add(-Retention)); err != nil && ctx.Err() == nil {
				e.Log.Error("alert log cleanup failed", "err", err)
			}
			lastCleanup = now
		}

		e.scan(ctx, now)
		e.flushHeld(ctx, now)

		free := cap(sem) - len(sem)
		var jobs []Job
		if free > 0 {
			var err error
			jobs, err = e.Store.ClaimAlertDeliveries(ctx, now, now.Add(lease), free)
			if err != nil && ctx.Err() == nil {
				e.Log.Error("alert claim failed", "err", err)
			}
		}
		for _, job := range jobs {
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				e.deliver(ctx, job)
			}()
		}
		if free > 0 && len(jobs) == free {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}

// scan fans out newly-stable alerts, reminders and resolved notices to
// matching channels.
func (e *Engine) scan(ctx context.Context, now time.Time) {
	if due, err := e.Store.DueToNotify(ctx, now.Add(-StableFor), 100); err != nil {
		if ctx.Err() == nil {
			e.Log.Error("alert scan (notify) failed", "err", err)
		}
	} else {
		for _, a := range due {
			e.notify(ctx, a, DeliveryFired, now)
		}
	}
	if due, err := e.Store.DueForReminder(ctx, now.Add(-ReminderInterval), 100); err != nil {
		if ctx.Err() == nil {
			e.Log.Error("alert scan (reminder) failed", "err", err)
		}
	} else {
		for _, a := range due {
			e.notify(ctx, a, DeliveryReminder, now)
		}
	}
	if due, err := e.Store.DueForResolvedNotice(ctx, 100); err != nil {
		if ctx.Err() == nil {
			e.Log.Error("alert scan (resolved) failed", "err", err)
		}
	} else {
		for _, a := range due {
			e.notify(ctx, a, DeliveryResolved, now)
		}
	}
}

// notify decides which of the tenant's enabled channels get a's kind of
// message right now (due) versus held for quiet hours (held), then tells
// the store to fan it out and mark a so it isn't picked up again.
func (e *Engine) notify(ctx context.Context, a Alert, kind string, now time.Time) {
	channels, err := e.Store.EnabledChannels(ctx, a.TenantID)
	if err != nil {
		if ctx.Err() == nil {
			e.Log.Error("listing alert channels failed", "err", err)
		}
		return
	}
	var due, held []uuid.UUID
	for _, c := range channels {
		if !meetsSeverity(a.Severity, c.MinSeverity) {
			continue
		}
		quiet, err := c.QuietHours.Active(now)
		if err != nil {
			e.Log.Warn("alert channel has an unusable time zone", "channel", c.ID, "err", err)
			quiet = false
		}
		bypass := a.Severity == SeverityCritical && c.QuietHours != nil && c.QuietHours.BypassCritical
		if quiet && !bypass {
			held = append(held, c.ID)
		} else {
			due = append(due, c.ID)
		}
	}
	var ev *WebhookEvent
	if kind == DeliveryFired || kind == DeliveryResolved {
		ev = &WebhookEvent{
			Type: "alert." + kind,
			Data: map[string]any{
				"alert_id": a.ID, "key": a.Key, "severity": a.Severity,
				"title": a.Title, "message": a.Message, "link": a.Link,
			},
		}
	}
	if err := e.Store.Notify(ctx, a, kind, due, held, now, MaxAttempts, ev); err != nil && ctx.Err() == nil {
		e.Log.Error("queueing alert notification failed", "alert", a.ID, "err", err)
	}
}

// flushHeld merges each channel's held deliveries into one digest once
// it's no longer in quiet hours (or has none — a channel turned off or
// with quiet hours removed while holding messages).
func (e *Engine) flushHeld(ctx context.Context, now time.Time) {
	channels, err := e.Store.HeldChannels(ctx)
	if err != nil {
		if ctx.Err() == nil {
			e.Log.Error("listing channels with held alerts failed", "err", err)
		}
		return
	}
	for _, c := range channels {
		quiet, err := c.QuietHours.Active(now)
		if err != nil {
			quiet = false
		}
		if quiet {
			continue
		}
		if err := e.Store.FlushHeld(ctx, c.ID, now, MaxAttempts); err != nil && ctx.Err() == nil {
			e.Log.Error("flushing held alerts failed", "channel", c.ID, "err", err)
		}
	}
}

func (e *Engine) deliver(ctx context.Context, job Job) {
	a, ok := e.Sender.Send(ctx, job)
	if ctx.Err() != nil {
		return
	}
	o := outcome(job, a, ok)
	if err := e.Store.RecordAlertAttempt(ctx, o); err != nil {
		e.Log.Error("recording alert attempt failed", "delivery", job.ID, "err", err)
	}
}
