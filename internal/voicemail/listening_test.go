package voicemail

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/ari"
	"linxpbx.com/linx/internal/auth"
)

// *97 and the message-waiting light (Phase 2 step 9b). The phone engine
// is a stand-in here: it records what Linx asked of it, and answers a
// play with the PlaybackFinished that a real Asterisk sends when the
// sound ends.

// fakeAsterisk is Asterisk's end of the ARI websocket.
type fakeAsterisk struct {
	mu   sync.Mutex
	did  []string
	keys map[string]string // media prefix → the key pressed while it plays
	// listening and channel are how a playback's end is reported back.
	listening *Listening
	channel   string
	fail      map[string]error
	nextID    int
}

func (f *fakeAsterisk) Do(ctx context.Context, method, uri string, out any) error {
	f.mu.Lock()
	f.did = append(f.did, method+" "+uri)
	if err := f.fail[method+" "+uri]; err != nil {
		f.mu.Unlock()
		return err
	}
	play := strings.Contains(uri, "/play?media=")
	var media, id string
	if play {
		f.nextID++
		id = "pb" + string(rune('0'+f.nextID))
		media, _ = url.QueryUnescape(strings.SplitN(uri, "media=", 2)[1])
		if p, ok := out.(*ari.Playback); ok {
			*p = ari.Playback{ID: id, MediaURI: media, State: "playing"}
		}
	}
	key, l, channel := "", f.listening, f.channel
	if play {
		for prefix, k := range f.keys {
			if strings.Contains(media, prefix) {
				key = k
				delete(f.keys, prefix) // pressed once, as a person does
				break
			}
		}
	}
	f.mu.Unlock()
	if !play {
		return nil
	}
	// The sound is playing: a key, or it finishes on its own.
	go func() {
		ch := &ari.Channel{ID: channel}
		if key != "" {
			l.Stasis(context.Background(), f, ari.Event{Type: "ChannelDtmfReceived", Channel: ch, Digit: key})
			return
		}
		l.Stasis(context.Background(), f, ari.Event{Type: "PlaybackFinished",
			Playback: &ari.Playback{ID: id, State: "done", TargetURI: "channel:" + channel}})
	}()
	return nil
}

func (f *fakeAsterisk) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.did)
}

// played is every sound Linx played, in order, without the sound: prefix.
func (f *fakeAsterisk) played() []string {
	var out []string
	for _, d := range f.asked() {
		if !strings.Contains(d, "/play?media=") {
			continue
		}
		media, _ := url.QueryUnescape(strings.SplitN(d, "media=", 2)[1])
		out = append(out, strings.ReplaceAll(media, "sound:", ""))
	}
	return out
}

type fakeListenStore struct {
	mu       sync.Mutex
	box      Box
	owner    *uuid.UUID
	noBox    bool
	messages []Message
	audio    map[uuid.UUID][]byte
	heard    []uuid.UUID
	deleted  []uuid.UUID
}

func (s *fakeListenStore) BoxForEndpoint(ctx context.Context, sip string) (Box, *uuid.UUID, error) {
	if s.noBox {
		return Box{}, nil, ErrNotFound
	}
	return s.box, s.owner, nil
}

func (s *fakeListenStore) UnheardVoicemail(ctx context.Context, box uuid.UUID) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.messages), nil
}

func (s *fakeListenStore) VoicemailMessage(ctx context.Context, tenant, id uuid.UUID) (Message, error) {
	audio, ok := s.audio[id]
	if !ok {
		return Message{}, ErrNotFound
	}
	return Message{ID: id, TenantID: tenant, Audio: audio}, nil
}

func (s *fakeListenStore) HeardVoicemail(ctx context.Context, tenant, id uuid.UUID, by *uuid.UUID, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heard = append(s.heard, id)
	return nil
}

func (s *fakeListenStore) DeleteVoicemail(ctx context.Context, tenant, id uuid.UUID, a auth.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, id)
	return nil
}

// dial is one *97 call, run to its end.
func dial(t *testing.T, store *fakeListenStore, keys map[string]string) (*fakeAsterisk, *Listening) {
	t.Helper()
	l := &Listening{Store: store, Dir: t.TempDir(), DirForAsterisk: "/asterisk-sees-it-here",
		Now: time.Now, AfterMessage: 10 * time.Millisecond, Log: quietLog()}
	f := &fakeAsterisk{keys: keys, listening: l, channel: "PJSIP/d_abcd1234-00000001"}
	l.Stasis(context.Background(), f, ari.Event{Type: "StasisStart", Args: []string{StasisVoicemail},
		Channel: &ari.Channel{ID: f.channel, Name: "PJSIP/d_abcd1234-00000001"}})
	// The call is over when Linx hangs it up.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if slices.Contains(f.asked(), "DELETE channels/"+url.PathEscape(f.channel)) {
			return f, l
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the call never ended; Linx asked: %v", f.asked())
	return nil, nil
}

func boxWith(n int) (*fakeListenStore, []uuid.UUID) {
	tenant, ext, owner := uuid.New(), uuid.New(), uuid.New()
	s := &fakeListenStore{
		box:   Box{ID: ext, TenantID: tenant, ExtensionID: &ext, Owner: "Sara Haddad (101)", Enabled: true},
		owner: &owner, audio: map[uuid.UUID][]byte{},
	}
	var ids []uuid.UUID
	for i := 0; i < n; i++ {
		id := uuid.New()
		ids = append(ids, id)
		s.messages = append(s.messages, Message{ID: id, TenantID: tenant, BoxID: ext,
			CallerNumber: "0501", ReceivedAt: time.Now(), Duration: 3 * time.Second})
		s.audio[id] = make([]byte, 8000)
	}
	return s, ids
}

func TestListeningNoMessages(t *testing.T) {
	s, _ := boxWith(0)
	f, _ := dial(t, s, nil)
	if got := f.played(); len(got) != 1 || got[0] != promptNone {
		t.Errorf("played %v, want just %q", got, promptNone)
	}
	if !slices.Contains(f.asked(), "POST channels/PJSIP%2Fd_abcd1234-00000001/answer") {
		t.Errorf("the call wasn't answered: %v", f.asked())
	}
}

func TestListeningNoBox(t *testing.T) {
	s, _ := boxWith(0)
	s.noBox = true
	f, _ := dial(t, s, nil)
	if got := f.played(); len(got) != 1 || got[0] != promptNoBox {
		t.Errorf("played %v, want just %q", got, promptNoBox)
	}
}

// A box that is switched off is the same as not having one: callers were
// never sent to it, so there is nothing to hear.
func TestListeningBoxOff(t *testing.T) {
	s, _ := boxWith(2)
	s.box.Enabled = false
	f, _ := dial(t, s, nil)
	if got := f.played(); len(got) != 1 || got[0] != promptNoBox {
		t.Errorf("played %v, want just %q", got, promptNoBox)
	}
}

func TestListeningPlaysEveryMessage(t *testing.T) {
	s, ids := boxWith(2)
	f, l := dial(t, s, nil)
	played := f.played()
	want := []string{promptIntro,
		promptFrom + ",linx/digit-0,linx/digit-5,linx/digit-0,linx/digit-1",
		"/asterisk-sees-it-here/" + ids[0].String(),
		promptFrom + ",linx/digit-0,linx/digit-5,linx/digit-0,linx/digit-1",
		"/asterisk-sees-it-here/" + ids[1].String(),
		promptEnd}
	if !slices.Equal(played, want) {
		t.Errorf("played\n%v\nwant\n%v", played, want)
	}
	// Both were heard to the end, so neither is new any more.
	if !slices.Equal(s.heard, ids) {
		t.Errorf("heard %v, want %v", s.heard, ids)
	}
	if len(s.deleted) != 0 {
		t.Errorf("deleted %v", s.deleted)
	}
	// Nothing of anybody's voice is left behind.
	left, _ := os.ReadDir(l.Dir)
	if len(left) != 0 {
		t.Errorf("%d files left in the folder Asterisk plays from", len(left))
	}
}

// 3 deletes the message being played, says so, and goes on. A deleted
// message is never also marked heard.
func TestListeningDelete(t *testing.T) {
	s, ids := boxWith(1)
	f, _ := dial(t, s, map[string]string{ids[0].String(): keyDelete})
	if !slices.Equal(s.deleted, ids) {
		t.Errorf("deleted %v, want %v", s.deleted, ids)
	}
	if len(s.heard) != 0 {
		t.Errorf("a deleted message was marked heard: %v", s.heard)
	}
	if !slices.Contains(f.played(), promptDeleted) {
		t.Errorf("nothing said it was deleted: %v", f.played())
	}
}

// 2 pressed in the middle of a message leaves it new: it wasn't heard.
// Pressed after it has played through, it is heard like any other.
func TestListeningSkipKeepsItNew(t *testing.T) {
	s, ids := boxWith(2)
	// The key lands while the first message's own audio is playing.
	f, _ := dial(t, s, map[string]string{ids[0].String(): keyNext})
	if slices.Contains(s.heard, ids[0]) {
		t.Errorf("a message skipped partway was marked heard: %v", s.heard)
	}
	if !slices.Contains(s.heard, ids[1]) {
		t.Errorf("the message that did play through is still new: %v", s.heard)
	}
	if !slices.Contains(f.played(), promptEnd) {
		t.Errorf("the call didn't finish: %v", f.played())
	}
}

// 1 plays the same message again.
func TestListeningAgain(t *testing.T) {
	s, ids := boxWith(1)
	// The key is pressed while the caller's number is read out, which is
	// the moment a person realises they want it again.
	f, _ := dial(t, s, map[string]string{promptFrom: keyAgain})
	// Pressed once while the caller's number is read out: that message
	// starts again, and this time plays through.
	if n := strings.Count(strings.Join(f.played(), "|"), promptFrom); n != 2 {
		t.Errorf("1 didn't play the message again exactly once: %v", f.played())
	}
	if !slices.Equal(s.heard, ids) {
		t.Errorf("heard %v, want %v", s.heard, ids)
	}
}

// A caller who hangs up stops everything at once: no more sounds, and
// nothing of theirs is left in the folder.
func TestListeningHangUp(t *testing.T) {
	s, _ := boxWith(2)
	l := &Listening{Store: s, Dir: t.TempDir(), Now: time.Now, Log: quietLog()}
	f := &fakeAsterisk{listening: l, channel: "c1"}
	// Nothing answers the playbacks, so the session waits.
	f.keys = map[string]string{}
	stop := &fakeAsterisk{listening: l, channel: "c1"}
	_ = stop
	l.Stasis(context.Background(), f, ari.Event{Type: "StasisStart", Args: []string{StasisVoicemail},
		Channel: &ari.Channel{ID: "c1", Name: "PJSIP/d_abcd1234-00000001"}})
	time.Sleep(50 * time.Millisecond)
	l.Stasis(context.Background(), f, ari.Event{Type: "ChannelDestroyed", Channel: &ari.Channel{ID: "c1"}})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		left, _ := os.ReadDir(l.Dir)
		l.mu.Lock()
		running := len(l.calls)
		l.mu.Unlock()
		if running == 0 && len(left) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("the session didn't stop when the caller hung up: %v", f.asked())
}

// A call that isn't *97 is left exactly where it is: Linx has no other
// Stasis application, and answering a call it doesn't understand would
// take it away from whatever does.
func TestListeningIgnoresOtherApps(t *testing.T) {
	s, _ := boxWith(1)
	l := &Listening{Store: s, Dir: t.TempDir(), Log: quietLog()}
	f := &fakeAsterisk{listening: l, channel: "c9"}
	l.Stasis(context.Background(), f, ari.Event{Type: "StasisStart", Args: []string{"something-else"},
		Channel: &ari.Channel{ID: "c9"}})
	time.Sleep(50 * time.Millisecond)
	if got := f.asked(); len(got) != 0 {
		t.Errorf("Linx touched a call that wasn't its own: %v", got)
	}
}

// Asterisk runs as its own user and reads the folder over a mount, so a
// message written for it has to be readable whatever uid that is. The
// folder is what keeps it private, not the file (compose's
// voicemail-play is 2750).
func TestMessageIsReadableByAsterisk(t *testing.T) {
	store, ids := boxWith(1)
	l := &Listening{Store: store, Dir: t.TempDir(), Log: quietLog()}
	s := &session{l: l}
	file, play, err := s.write(context.Background(), store.box.TenantID, store.messages[0])
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o044 == 0 {
		t.Errorf("a message written for Asterisk is %o: it can't read it", mode)
	}
	if want := l.Dir + "/" + ids[0].String(); play != want {
		t.Errorf("played as %q, want %q", play, want)
	}
}

func TestDigits(t *testing.T) {
	for _, c := range []struct {
		number string
		want   []string
	}{
		{"", nil},
		{"101", []string{"linx/digit-1", "linx/digit-0", "linx/digit-1"}},
		{"+9715", []string{"linx/digit-plus", "linx/digit-9", "linx/digit-7", "linx/digit-1", "linx/digit-5"}},
		// Nothing but digits ever names a file, and a + only leads.
		{"05 50/../x1", []string{"linx/digit-0", "linx/digit-5", "linx/digit-5", "linx/digit-0", "linx/digit-1"}},
		{"9+9", []string{"linx/digit-9", "linx/digit-9"}},
		{strings.Repeat("7", 40), slices.Repeat([]string{"linx/digit-7"}, maxDigits)},
	} {
		if got := Digits(c.number); !slices.Equal(got, c.want) {
			t.Errorf("Digits(%q) = %v, want %v", c.number, got, c.want)
		}
	}
}

// Every message *97 can play is a file in the Asterisk image, the same
// rule the dialplan's messages follow (TestPlaybackPromptsExist).
func TestListeningPrompts(t *testing.T) {
	sums, err := os.ReadFile("../../deploy/docker/asterisk/prompts/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	has := func(name string) bool {
		return strings.Contains(string(sums), " ./"+strings.TrimPrefix(name, "linx/")+".g722")
	}
	names := []string{promptNoBox, promptNone, promptIntro, promptFrom, promptUnknown, promptDeleted, promptEnd}
	names = append(names, Digits("+0123456789")...)
	for _, name := range names {
		if !has(name) {
			t.Errorf("%s isn't in the Asterisk image: add it to tools/prompts/prompts.tsv and run make prompts", name)
		}
	}
}

func TestWhoFromUnknown(t *testing.T) {
	s := &session{l: &Listening{}}
	if got := s.whoFrom(Message{CallerNumber: ""}); got != promptUnknown {
		t.Errorf("a withheld number says %q", got)
	}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
