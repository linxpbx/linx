package voicemail

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Asterisk is the one thing Linx asks of a phone engine from here: a REST
// request over the ARI websocket it is already connected on
// (internal/ari's Conn).
type Asterisk interface {
	Do(ctx context.Context, method, uri string, out any) error
}

// The message-waiting light (Phase 2 step 9b, owner 2026-10-04): the lamp
// or envelope on a desk phone that says there is a new message.
//
// Asterisk is what tells the phone (a SIP NOTIFY, migration 0046's
// mailboxes on its endpoint), but it has no mailbox of its own to count:
// Linx keeps voicemail itself (ADR-069). So the counts come from here,
// over ARI, whenever anything about a person's messages changes — a new
// one arrives, one is heard or marked new again, one is deleted, the
// clean-up removes old ones — and again whenever Asterisk reconnects,
// because a phone engine that has just restarted knows nothing.
//
// Every box is counted in one query and only what changed is sent, so a
// quiet system sends nothing at all, and a Linx that drifted out of step
// (a push that failed, a restart in the middle) puts itself right at the
// next change.

// Counts is what a mailbox holds: messages nobody has heard, and the rest.
type Counts struct{ New, Old int }

// LightStore is the database access the light needs (internal/store).
type LightStore interface {
	// VoicemailCounts is every person's box with its unheard and heard
	// messages. A ring group's box is not in it: no phone belongs to one,
	// so there is no light to turn on.
	VoicemailCounts(ctx context.Context) (map[uuid.UUID]Counts, error)
}

// Light keeps Asterisk's mailboxes the same as the database.
type Light struct {
	Store LightStore
	Log   *slog.Logger

	mu sync.Mutex
	// conn is Asterisk, while it is connected.
	conn Asterisk
	// sent is what each mailbox was last told, so an unchanged count
	// costs nothing.
	sent map[uuid.UUID]Counts
	poke chan struct{}
}

func (l *Light) log() *slog.Logger {
	if l.Log == nil {
		return slog.Default()
	}
	return l.Log
}

// Changed says something about somebody's messages has changed. It never
// blocks: the update happens on Run's own goroutine, and several changes
// close together cost one round of it.
func (l *Light) Changed() {
	if l == nil {
		return
	}
	l.mu.Lock()
	ch := l.poke
	l.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Connected is called when Asterisk's ARI application registers: it is a
// phone engine that may have just started, so everything is sent again.
func (l *Light) Connected(c Asterisk) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.conn, l.sent = c, nil
	l.mu.Unlock()
	l.Changed()
}

// Disconnected is Asterisk going away: nothing is sent until it is back.
func (l *Light) Disconnected() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.conn, l.sent = nil, nil
	l.mu.Unlock()
}

// Run keeps the lights up to date until ctx ends.
func (l *Light) Run(ctx context.Context) {
	l.mu.Lock()
	if l.poke == nil {
		l.poke = make(chan struct{}, 1)
	}
	poke := l.poke
	l.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poke:
			// A moment's wait so a burst of changes — a call that leaves
			// a message also writes its call record — is one update.
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			if err := l.Update(ctx); err != nil && ctx.Err() == nil {
				l.log().Warn("the message-waiting lights couldn't be set; they follow at the next change", "err", err)
			}
		}
	}
}

// Update counts every box and tells Asterisk what changed.
func (l *Light) Update(ctx context.Context) error {
	l.mu.Lock()
	c, sent := l.conn, l.sent
	l.mu.Unlock()
	if c == nil {
		return nil // no phone engine to tell; Connected sends it all later
	}
	want, err := l.Store.VoicemailCounts(ctx)
	if err != nil {
		return err
	}
	now := make(map[uuid.UUID]Counts, len(want))
	var firstErr error
	for box, n := range want {
		now[box] = n
		if was, ok := sent[box]; ok && was == n {
			continue
		}
		if err := setMailbox(ctx, c, box, n); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			delete(now, box) // try again next time
		}
	}
	// A box that is gone (its person was removed) leaves a light on
	// otherwise.
	for box := range sent {
		if _, ok := want[box]; ok {
			continue
		}
		if err := setMailbox(ctx, c, box, Counts{}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	l.mu.Lock()
	if l.conn == c {
		l.sent = now
	}
	l.mu.Unlock()
	return firstErr
}

// setMailbox is ARI's "this mailbox holds this much". The name is the
// box's id, which for a person is their extension's (migration 0046 puts
// the same name on their phones' endpoints).
func setMailbox(ctx context.Context, c Asterisk, box uuid.UUID, n Counts) error {
	uri := fmt.Sprintf("mailboxes/%s?oldMessages=%d&newMessages=%d", box, n.Old, n.New)
	return c.Do(ctx, "PUT", uri, nil)
}
