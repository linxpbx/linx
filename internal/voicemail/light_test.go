package voicemail

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// The message-waiting light (Phase 2 step 9b): what Asterisk is told, and
// what it isn't told twice.

type lightStore struct {
	mu     sync.Mutex
	counts map[uuid.UUID]Counts
	err    error
}

func (s *lightStore) VoicemailCounts(ctx context.Context) (map[uuid.UUID]Counts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := map[uuid.UUID]Counts{}
	for k, v := range s.counts {
		out[k] = v
	}
	return out, nil
}

func (s *lightStore) set(box uuid.UUID, c Counts) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[box] = c
}

type lightAsterisk struct {
	mu   sync.Mutex
	did  []string
	fail error
}

func (a *lightAsterisk) Do(ctx context.Context, method, uri string, out any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail != nil {
		return a.fail
	}
	a.did = append(a.did, method+" "+uri)
	return nil
}

func (a *lightAsterisk) sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := slices.Clone(a.did)
	a.did = nil
	return out
}

func TestLight(t *testing.T) {
	sara, omar := uuid.New(), uuid.New()
	store := &lightStore{counts: map[uuid.UUID]Counts{sara: {New: 2, Old: 1}, omar: {}}}
	l := &Light{Store: store, Log: quietLog()}
	ast := &lightAsterisk{}

	// Nothing is sent before Asterisk is there to hear it.
	if err := l.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ast.sent(); len(got) != 0 {
		t.Errorf("something was sent with no phone engine connected: %v", got)
	}

	l.Connected(ast)
	if err := l.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := ast.sent()
	slices.Sort(got)
	want := []string{
		"PUT mailboxes/" + omar.String() + "?oldMessages=0&newMessages=0",
		"PUT mailboxes/" + sara.String() + "?oldMessages=1&newMessages=2",
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("sent\n%v\nwant\n%v", got, want)
	}

	// Nothing changed: nothing is sent. A quiet system is quiet.
	if err := l.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ast.sent(); len(got) != 0 {
		t.Errorf("unchanged counts were sent again: %v", got)
	}

	// Sara hears one: only her light is set, and only the new count moved.
	store.set(sara, Counts{New: 1, Old: 2})
	if err := l.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ast.sent(); len(got) != 1 ||
		got[0] != "PUT mailboxes/"+sara.String()+"?oldMessages=2&newMessages=1" {
		t.Errorf("after hearing one: %v", got)
	}

	// Her person is removed: the box goes, and the light goes out with it
	// rather than staying on for ever.
	store.mu.Lock()
	delete(store.counts, sara)
	store.mu.Unlock()
	if err := l.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ast.sent(); len(got) != 1 ||
		got[0] != "PUT mailboxes/"+sara.String()+"?oldMessages=0&newMessages=0" {
		t.Errorf("a box that went away left its light on: %v", got)
	}

	// Asterisk restarts and knows nothing: everything is sent again.
	l.Connected(ast)
	if err := l.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ast.sent(); len(got) != 1 || !strings.Contains(got[0], omar.String()) {
		t.Errorf("after Asterisk reconnected: %v", got)
	}

	// Asterisk goes away: nothing is sent into the void.
	l.Disconnected()
	store.set(omar, Counts{New: 3})
	if err := l.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ast.sent(); len(got) != 0 {
		t.Errorf("sent with Asterisk gone: %v", got)
	}
}

// A mailbox Asterisk refused is tried again at the next change, not
// remembered as sent.
func TestLightRetries(t *testing.T) {
	box := uuid.New()
	store := &lightStore{counts: map[uuid.UUID]Counts{box: {New: 1}}}
	l := &Light{Store: store, Log: quietLog()}
	ast := &lightAsterisk{fail: errors.New("no")}
	l.Connected(ast)
	if err := l.Update(context.Background()); err == nil {
		t.Error("a refused mailbox looked like success")
	}
	ast.mu.Lock()
	ast.fail = nil
	ast.mu.Unlock()
	if err := l.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ast.sent(); len(got) != 1 {
		t.Errorf("the refused mailbox wasn't tried again: %v", got)
	}
}

// A database that can't be read leaves the lights exactly as they were.
func TestLightDatabaseDown(t *testing.T) {
	store := &lightStore{counts: map[uuid.UUID]Counts{}, err: errors.New("down")}
	l := &Light{Store: store, Log: quietLog()}
	ast := &lightAsterisk{}
	l.Connected(ast)
	if err := l.Update(context.Background()); err == nil {
		t.Error("a database that can't be read looked like success")
	}
	if got := ast.sent(); len(got) != 0 {
		t.Errorf("something was sent anyway: %v", got)
	}
}
