package webhook

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// FailingLimit: an endpoint whose every delivery has failed for this
	// long is turned off (docs/API.md §4).
	FailingLimit = 5 * 24 * time.Hour
	// Retention of the delivery log and sent events.
	Retention = 30 * 24 * time.Hour
	// lease is how long a claimed delivery belongs to one worker. It's well
	// above the 10 s request timeout; if the worker dies, another takes the
	// delivery over after this.
	lease = 2 * time.Minute
)

// Worker fans outbox events out to endpoints and delivers them. Several
// control-plane instances can each run one: rows are claimed with
// FOR UPDATE SKIP LOCKED, so a delivery is sent by one worker at a time.
type Worker struct {
	Store  Store
	Sender *Sender
	Log    *slog.Logger
	// Concurrency is how many deliveries are sent at once (default 8).
	Concurrency int
	// Poll is how often to look for new work when idle (default 1 s).
	Poll time.Duration
	// OnDisabled is called when the worker turns an endpoint off. Admin
	// alerts hook in here (docs/API.md §8 step 5).
	OnDisabled func(ctx context.Context, tenant, endpoint uuid.UUID, reason string)
}

// Run works until ctx is cancelled, then waits for sends in flight. A send
// cut short by shutdown isn't recorded: its lease runs out and it's retried.
func (w *Worker) Run(ctx context.Context) {
	concurrency := w.Concurrency
	if concurrency <= 0 {
		concurrency = 8
	}
	poll := w.Poll
	if poll <= 0 {
		poll = time.Second
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()

	var lastCleanup time.Time
	for ctx.Err() == nil {
		now := w.Sender.Now()
		if now.Sub(lastCleanup) >= time.Hour {
			if err := w.Store.Cleanup(ctx, now.Add(-Retention), now); err != nil && ctx.Err() == nil {
				w.Log.Error("webhook log cleanup failed", "err", err)
			}
			lastCleanup = now
		}
		if _, err := w.Store.DispatchEvents(ctx, now, MaxAttempts, 100); err != nil && ctx.Err() == nil {
			w.Log.Error("webhook dispatch failed", "err", err)
		}

		free := cap(sem) - len(sem)
		var jobs []Job
		if free > 0 {
			var err error
			jobs, err = w.Store.ClaimDeliveries(ctx, now, now.Add(lease), free)
			if err != nil && ctx.Err() == nil {
				w.Log.Error("webhook claim failed", "err", err)
			}
		}
		for _, job := range jobs {
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				w.deliver(ctx, job)
			}()
		}
		// More work may be waiting if every free slot was filled.
		if free > 0 && len(jobs) == free {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}

func (w *Worker) deliver(ctx context.Context, job Job) {
	a, ok, gone := w.Sender.Send(ctx, job)
	if ctx.Err() != nil {
		return
	}
	o := outcome(job, a, ok, gone)
	reason, err := w.Store.RecordAttempt(ctx, o, w.Sender.Now().Add(-FailingLimit))
	if err != nil {
		w.Log.Error("recording webhook attempt failed", "delivery", job.ID, "err", err)
		return
	}
	if reason != "" {
		w.Log.Warn("webhook endpoint turned off", "endpoint", job.EndpointID, "reason", reason)
		if w.OnDisabled != nil {
			w.OnDisabled(ctx, job.TenantID, job.EndpointID, reason)
		}
	}
}
