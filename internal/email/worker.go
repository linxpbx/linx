package email

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Worker timings.
const (
	claimBatch = 10
	// lease is how long a claimed email is someone's: longer than a send.
	lease = 2 * SendTimeout
	// heldBack is how soon to look again while the hourly limit holds
	// emails back.
	heldBack = time.Minute
	// idle is the longest the worker sleeps with nothing due (cleanup
	// runs then too).
	idle = 6 * time.Hour
	// keepFinished is how long sent and failed emails stay listed (their
	// content is gone already).
	keepFinished = 30 * 24 * time.Hour
)

// Run sends queued emails until ctx ends. It sleeps until the next one is
// due, or until Enqueue or Update wakes it: no polling.
func (s *Service) Run(ctx context.Context) {
	s.init()
	lastCleanup := time.Time{}
	for {
		now := s.Now()
		n, err := s.sendDue(ctx, now)
		if err != nil && ctx.Err() == nil {
			s.Log.Error("sending queued email failed", "err", err)
		}
		if now.Sub(lastCleanup) > 24*time.Hour {
			if err := s.Store.CleanupEmails(ctx, now.Add(-keepFinished)); err != nil && ctx.Err() == nil {
				s.Log.Error("cleaning up sent email failed", "err", err)
			}
			lastCleanup = now
		}
		wait := idle
		switch next, err := s.Store.NextEmailDue(ctx); {
		case err != nil:
			wait = heldBack
		case n == claimBatch:
			wait = 0 // more may be due right now
		case next != nil && !next.After(now):
			wait = heldBack // due but held back by the hourly limit
		case next != nil:
			wait = min(next.Sub(now), idle)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// sendDue claims and sends what's due now, returning how many it claimed.
func (s *Service) sendDue(ctx context.Context, now time.Time) (int, error) {
	jobs, err := s.Store.ClaimEmails(ctx, now, now.Add(lease), claimBatch)
	if err != nil {
		return 0, err
	}
	configs := map[uuid.UUID]Config{}
	for _, m := range jobs {
		c, ok := configs[m.TenantID]
		if !ok {
			if c, err = s.load(ctx, m.TenantID); err != nil {
				return len(jobs), err
			}
			configs[m.TenantID] = c
		}
		s.sendOne(ctx, c, m)
	}
	return len(jobs), nil
}

func (s *Service) sendOne(ctx context.Context, c Config, m Message) {
	finish := func(status, why string, next *time.Time) {
		if err := s.Store.FinishEmail(ctx, m, status, why, next, s.Now().UTC()); err != nil && ctx.Err() == nil {
			s.Log.Error("recording an email attempt failed", "email", m.ID, "err", err)
		}
	}
	var content Content
	plain, err := s.Sealer.Open(outboxSealID(m.ID), m.ContentEnc)
	if err == nil {
		err = json.Unmarshal(plain, &content)
	}
	if err != nil {
		finish(StatusFailed, "Linx couldn't read this email back.", nil)
		return
	}
	a, err := s.account(c)
	if err != nil {
		finish(StatusFailed, "Linx couldn't read the mail account's password.", nil)
		return
	}
	if content.Voicemail != nil {
		f, found, err := s.attachVoicemail(ctx, m.TenantID, *content.Voicemail)
		switch {
		case err != nil:
			// The database, not the mail account: try again later.
			s.Log.Error("reading a voicemail to email failed", "email", m.ID, "err", err)
			if m.Attempts+1 < MaxAttempts {
				next := s.Now().UTC().Add(retryDelays[m.Attempts])
				finish(StatusPending, "Linx couldn't read the voicemail.", &next)
			} else {
				finish(StatusFailed, "Linx couldn't read the voicemail.", nil)
			}
			return
		case found:
			content.Files = []File{f}
		default:
			content.Text += "\n\n(The message was deleted before this email went out, so it isn't attached.)"
		}
	}
	err = s.Sender.Send(ctx, a, m.To, content)
	if err == nil {
		finish(StatusSent, "", nil)
		if s.Working != nil {
			s.Working(ctx, m.TenantID)
		}
		return
	}
	var se *SendError
	if !errors.As(err, &se) {
		se = &SendError{Detail: "Linx couldn't send this email.", Why: err.Error(), Temporary: true}
	}
	attempts := m.Attempts + 1
	// A refusal for this one recipient fails just this email; anything
	// else (signing in, the server, the certificate) is the account.
	accountProblem := se.Stage != StageSend || se.Temporary
	if se.Temporary && attempts < MaxAttempts {
		next := s.Now().UTC().Add(retryDelays[attempts-1])
		finish(StatusPending, se.Error(), &next)
	} else {
		finish(StatusFailed, se.Error(), nil)
	}
	s.Log.Warn("an email wasn't sent", "email", m.ID, "kind", m.Kind, "attempt", attempts, "err", se.Error())
	if accountProblem && (!se.Temporary || attempts >= MaxAttempts) && s.Broken != nil {
		s.Broken(ctx, m.TenantID, se.Detail)
	}
}

func (s *Service) attachVoicemail(ctx context.Context, tenant, id uuid.UUID) (File, bool, error) {
	if s.Voicemail == nil {
		return File{}, false, nil
	}
	return s.Voicemail(ctx, tenant, id)
}
