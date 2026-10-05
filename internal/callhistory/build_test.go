package callhistory

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	tenant  = uuid.MustParse("01a0fb00-0000-7000-8000-000000000001")
	alice   = Person{ExtensionID: uuid.MustParse("01a0fb00-0000-7000-8000-000000000101"), TenantID: tenant, Number: "101", Name: "Alice"}
	bob     = Person{ExtensionID: uuid.MustParse("01a0fb00-0000-7000-8000-000000000102"), TenantID: tenant, Number: "102", Name: "Bob"}
	carol   = Person{ExtensionID: uuid.MustParse("01a0fb00-0000-7000-8000-000000000103"), TenantID: tenant, Number: "103", Name: "Carol"}
	dave    = Person{ExtensionID: uuid.MustParse("01a0fb00-0000-7000-8000-000000000109"), TenantID: tenant, Number: "109", Name: "Dave"}
	sales   = Group{ID: uuid.MustParse("01a0fc00-0000-7000-8000-000000000600"), TenantID: tenant, Name: "Sales"}
	support = Group{ID: uuid.MustParse("01a0fc00-6b16-736e-a597-35c6708b5c0d"), TenantID: tenant, Name: "Support", InTurn: true}
	telnyx  = Line{ID: uuid.MustParse("01a0fc01-db61-7a35-8d42-1c7398106565"), TenantID: tenant, Name: "Telnyx"}
	backup  = Line{ID: uuid.MustParse("01a0fc02-ee03-75b4-89ee-6657b88f75b1"), TenantID: tenant, Name: "Backup line"}
	carolVM = uuid.MustParse("01a0fbff-19a5-7a6f-b114-ef3f024460a6")
)

type fakeLookup struct{}

func (fakeLookup) DeviceOwner(_ context.Context, endpoint string) (Person, error) {
	switch endpoint {
	case "d_alice":
		return alice, nil
	case "d_bob":
		return bob, nil
	case "d_carol":
		return carol, nil
	case "d_dave":
		return dave, nil
	}
	return Person{}, ErrNotFound
}

func (fakeLookup) ExtensionByNumber(_ context.Context, _ uuid.UUID, number string) (Person, error) {
	for _, p := range []Person{alice, bob, carol, dave} {
		if p.Number == number {
			return p, nil
		}
	}
	return Person{}, ErrNotFound
}

func (fakeLookup) RingGroup(_ context.Context, id uuid.UUID) (Group, error) {
	for _, g := range []Group{sales, support} {
		if g.ID == id {
			return g, nil
		}
	}
	return Group{}, ErrNotFound
}

func (fakeLookup) RingGroupByNumber(_ context.Context, _ uuid.UUID, number string) (Group, error) {
	switch number {
	case "600":
		return sales, nil
	case "601":
		return support, nil
	}
	return Group{}, ErrNotFound
}

func (fakeLookup) Trunk(_ context.Context, id uuid.UUID) (Line, error) {
	for _, l := range []Line{telnyx, backup} {
		if l.ID == id {
			return l, nil
		}
	}
	return Line{}, ErrNotFound
}

func (fakeLookup) VoicemailBox(_ context.Context, id uuid.UUID) (Box, error) {
	if id == carolVM {
		return Box{Name: "Carol (103)", ExtensionID: &carol.ExtensionID}, nil
	}
	return Box{}, ErrNotFound
}

func (fakeLookup) Country(context.Context) (string, error)          { return "AE", nil }
func (fakeLookup) DefaultTenant(context.Context) (uuid.UUID, error) { return tenant, nil }

// row is one call record as the call suite's Asterisk wrote it (times as
// seconds after 09:00 UTC; answered -1 for never).
func row(id int64, start, answered, end int, clid, src, dst, dcontext, channel, dstchannel string, billsec int, disposition, uniqueid, dialled, did, step string) Row {
	base := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	r := Row{ID: id, Started: base.Add(time.Duration(start) * time.Second), Ended: base.Add(time.Duration(end) * time.Second),
		CLID: clid, Src: src, Dst: dst, DContext: dcontext, Channel: channel, DstChannel: dstchannel, BillSec: billsec,
		Disposition: disposition, UniqueID: uniqueid, LinkedID: uniqueid, Dialled: dialled, DID: did, Step: step}
	r.Duration = end - start
	if answered >= 0 {
		r.Answered = base.Add(time.Duration(answered) * time.Second)
	} else {
		r.Answered = time.Unix(0, 0).UTC()
	}
	return r
}

const aliceCLID = `"Alice" <101>`

func words(c Call) string {
	var w []string
	for _, s := range c.Steps {
		w = append(w, s.Words())
	}
	return strings.Join(w, " | ")
}

func missedBy(c Call) map[string]bool {
	out := map[string]bool{}
	for _, p := range c.Parties {
		for _, x := range []Person{alice, bob, carol, dave} {
			if x.ExtensionID == p.ExtensionID {
				out[x.Name] = p.Missed
			}
		}
	}
	return out
}

func TestBuild(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rows    []Row
		check   func(t *testing.T, c Call)
		result  string
		dir     string
		words   string
		parties map[string]bool
	}{
		{
			name:   "answered",
			rows:   []Row{row(1, 0, 1, 3, aliceCLID, "101", "s", "linx-route", "PJSIP/d_alice-00000000", "PJSIP/d_bob-00000001", 2, "ANSWERED", "1.0", "102", "", "n:102")},
			result: ResultAnswered, dir: DirectionInternal, words: "Rang Bob (102) · answered",
			parties: map[string]bool{"Alice": false, "Bob": false},
			check: func(t *testing.T, c Call) {
				if c.TalkSeconds != 2 || c.AnsweredByName != "Bob (102)" || c.ToNumber != "102" || c.ToName != "Bob" || c.FromName != "Alice" {
					t.Errorf("%+v", c)
				}
				if c.AnsweredAt == nil || !c.AnsweredAt.Equal(time.Date(2026, 10, 2, 9, 0, 1, 0, time.UTC)) {
					t.Errorf("answered at %v", c.AnsweredAt)
				}
			},
		},
		{
			name:   "a number not in use",
			rows:   []Row{row(3, 0, 0, 5, aliceCLID, "101", "not-in-use", "linx-messages", "PJSIP/d_alice-00000003", "", 5, "ANSWERED", "1.3", "555", "", "")},
			result: ResultNotInUse, dir: DirectionInternal, words: `Heard "That number isn't in use"`,
			parties: map[string]bool{"Alice": false},
			check: func(t *testing.T, c Call) {
				if c.ToNumber != "555" || c.RangUnanswered {
					t.Errorf("%+v", c)
				}
			},
		},
		{
			name:   "nobody can ring",
			rows:   []Row{row(5, 0, 0, 6, aliceCLID, "101", "not-available", "linx-messages", "PJSIP/d_alice-00000005", "", 5, "ANSWERED", "1.5", "103", "", "n:103")},
			result: ResultNotAvailable, dir: DirectionInternal,
			words:   `Carol (103) has no phone or browser connected | Heard "Nobody can take your call right now"`,
			parties: map[string]bool{"Alice": false, "Carol": true},
		},
		{
			name: "nobody answers",
			rows: []Row{
				row(8, 0, -1, 30, aliceCLID, "101", "s", "linx-route", "PJSIP/d_alice-00000008", "PJSIP/d_bob-00000009", 0, "NO ANSWER", "1.8", "102", "", "n:102"),
				row(9, 30, 30, 36, aliceCLID, "101", "not-available", "linx-messages", "PJSIP/d_alice-00000008", "", 5, "ANSWERED", "1.8", "102", "", "n:102"),
			},
			result: ResultMissed, dir: DirectionInternal,
			words:   `Rang Bob (102) · nobody answered in 30 s | Heard "Nobody can take your call right now"`,
			parties: map[string]bool{"Alice": false, "Bob": true},
			check: func(t *testing.T, c Call) {
				if !c.RangUnanswered || c.AnsweredAt != nil || c.EndedAt.Sub(c.StartedAt) != 36*time.Second {
					t.Errorf("%+v", c)
				}
			},
		},
		{
			name:   "voicemail",
			rows:   []Row{row(10, 0, 0, 9, aliceCLID, "101", carolVM.String(), "linx-voicemail", "PJSIP/d_alice-0000000a", "", 9, "ANSWERED", "1.10", "103", "", "n:103")},
			result: ResultVoicemail, dir: DirectionInternal,
			words:   "Carol (103) has no phone or browser connected | Went to the voicemail for Carol (103)",
			parties: map[string]bool{"Alice": false, "Carol": true},
			check: func(t *testing.T, c Call) {
				if c.VoicemailSource != "1.10" || c.VoicemailBoxName != "Carol (103)" || c.RangUnanswered {
					t.Errorf("%+v", c)
				}
			},
		},
		{
			name: "a ring group, all at once",
			rows: []Row{
				// Asterisk writes rows rung together in no set order.
				row(12, 0, 0, 1, aliceCLID, "101", "s", "linx-route", "PJSIP/d_alice-0000000c", "PJSIP/d_dave-0000000e", 1, "ANSWERED", "1.12", "600", "", "n:600"),
				row(13, 0, -1, 0, aliceCLID, "101", "s", "linx-route", "PJSIP/d_alice-0000000c", "PJSIP/d_bob-0000000d", 0, "NO ANSWER", "1.12", "600", "", "n:600"),
			},
			result: ResultAnswered, dir: DirectionInternal,
			words:   "Rang Sales (all at once): Bob (102), Dave (109) · Dave (109) answered",
			parties: map[string]bool{"Alice": false, "Bob": false, "Dave": false},
			check: func(t *testing.T, c Call) {
				if c.RingGroupID == nil || *c.RingGroupID != sales.ID || c.ToName != "Sales" || c.AnsweredByName != "Dave (109)" {
					t.Errorf("%+v", c)
				}
			},
		},
		{
			name: "a ring group, one after another",
			rows: []Row{
				row(14, 0, -1, 5, aliceCLID, "101", "s", "linx-route", "PJSIP/d_alice-0000000f", "PJSIP/d_bob-00000010", 0, "NO ANSWER", "1.15", "601", "", "n:601"),
				row(15, 5, 6, 7, aliceCLID, "101", "s", "linx-route", "PJSIP/d_alice-0000000f", "PJSIP/d_dave-00000011", 1, "ANSWERED", "1.15", "601", "", "g:"+support.ID.String()+":1"),
			},
			result: ResultAnswered, dir: DirectionInternal,
			words:   "Rang Support (one after another): Bob (102), Dave (109) · Dave (109) answered",
			parties: map[string]bool{"Alice": false, "Bob": false, "Dave": false},
		},
		{
			name: "busy",
			rows: []Row{
				row(19, 0, -1, 0, aliceCLID, "101", "s", "linx-route", "PJSIP/d_alice-00000016", "PJSIP/d_bob-00000017", 0, "BUSY", "1.22", "102", "", "n:102"),
				row(20, 0, 0, 5, aliceCLID, "101", "not-available", "linx-messages", "PJSIP/d_alice-00000016", "", 5, "ANSWERED", "1.22", "102", "", "n:102"),
			},
			result: ResultBusy, dir: DirectionInternal,
			words:   `Rang Bob (102) · busy | Heard "Nobody can take your call right now"`,
			parties: map[string]bool{"Alice": false, "Bob": true},
		},
		{
			name: "from a line",
			rows: []Row{row(1, 0, 1, 2, `"Evil Caller" <+971501112222>`, "+971501112222", "s", "linx-route",
				"PJSIP/trunk-"+telnyx.ID.String()+"-00000000", "PJSIP/d_bob-00000001", 1, "ANSWERED", "2.0", "", "+97142000102", "e:102")},
			result: ResultAnswered, dir: DirectionInbound, words: "Rang Bob (102) · answered",
			parties: map[string]bool{"Bob": false},
			check: func(t *testing.T, c Call) {
				if c.FromNumber != "+971501112222" || c.FromName != "Evil Caller" || c.ToNumber != "+97142000102" ||
					c.TrunkName != "Telnyx" || c.FromExtensionID != nil {
					t.Errorf("%+v", c)
				}
			},
		},
		{
			name: "from a line, local number format, nobody connected",
			rows: []Row{row(24, 0, 0, 1, `"" <0501112233>`, "0501112233", "not-available", "linx-messages",
				"PJSIP/trunk-"+telnyx.ID.String()+"-00000021", "", 1, "ANSWERED", "2.33", "", "+97142000105", "e:102")},
			result: ResultNotAvailable, dir: DirectionInbound,
			words:   `Bob (102) has no phone or browser connected | Heard "Nobody can take your call right now"`,
			parties: map[string]bool{"Bob": true},
			check: func(t *testing.T, c Call) {
				if c.FromNumber != "+971501112233" || c.FromName != "" {
					t.Errorf("%+v", c)
				}
			},
		},
		{
			// A line that times out is NO ANSWER in Asterisk's records
			// (CONGESTION when it refuses at once); either way the call
			// went on, so it couldn't take it.
			name: "out on the second line",
			rows: []Row{
				row(15, 0, -1, 0, aliceCLID, "101", "+971501234569", "linx-outbound", "PJSIP/d_alice-00000015",
					"PJSIP/trunk-01a0fc02-edfc-71b9-b6cc-ca047c70376a-00000016", 0, "NO ANSWER", "1.21", "+971501234569", "", ""),
				row(16, 0, 0, 1, aliceCLID, "101", "+971501234569", "linx-outbound", "PJSIP/d_alice-00000015",
					"PJSIP/trunk-"+backup.ID.String()+"-00000017", 1, "ANSWERED", "1.21", "+971501234569", "", ""),
			},
			result: ResultAnswered, dir: DirectionOutbound,
			words:   "a removed line couldn't take it (down or full) | Went out on Backup line · answered",
			parties: map[string]bool{"Alice": false},
			check: func(t *testing.T, c Call) {
				if c.ToNumber != "+971501234569" || c.TrunkName != "Backup line" || c.AnsweredByName != "+971501234569" {
					t.Errorf("%+v", c)
				}
			},
		},
		{
			name:   "out, a local number",
			rows:   []Row{row(2, 0, 0, 3, aliceCLID, "101", "0501234567", "linx-outbound", "PJSIP/d_alice-00000002", "PJSIP/trunk-"+telnyx.ID.String()+"-00000003", 3, "ANSWERED", "1.2", "0501234567", "", "")},
			result: ResultAnswered, dir: DirectionOutbound, words: "Went out on Telnyx · answered",
			parties: map[string]bool{"Alice": false},
			check: func(t *testing.T, c Call) {
				if c.ToNumber != "+971501234567" {
					t.Errorf("to %q", c.ToNumber)
				}
			},
		},
		{
			name: "no lines",
			rows: []Row{
				row(18, 0, -1, 0, aliceCLID, "101", "0503333333", "linx-outbound", "PJSIP/d_alice-0000001a", "PJSIP/trunk-"+telnyx.ID.String()+"-0000001b", 0, "NO ANSWER", "1.26", "0503333333", "", ""),
				row(19, 0, 0, 6, aliceCLID, "101", "no-lines", "linx-messages", "PJSIP/d_alice-0000001a", "", 6, "ANSWERED", "1.26", "0503333333", "", ""),
			},
			result: ResultNoLines, dir: DirectionOutbound,
			words:   `Telnyx couldn't take it (down or full) | Heard "Calls outside the company can't be made right now"`,
			parties: map[string]bool{"Alice": false},
		},
		{
			name:   "not permitted",
			rows:   []Row{row(3, 0, 0, 6, aliceCLID, "101", "not-permitted", "linx-messages", "PJSIP/d_alice-00000004", "", 6, "ANSWERED", "1.4", "00441234567", "", "")},
			result: ResultNotPermitted, dir: DirectionOutbound, words: `Heard "This phone isn't allowed to call that number"`,
			parties: map[string]bool{"Alice": false},
		},
		{
			name:   "listened to voicemail from a phone",
			rows:   []Row{row(2, 0, 0, 1, aliceCLID, "101", "*97", "linx-extensions", "PJSIP/d_alice-00000002", "", 1, "ANSWERED", "1.2", "", "", "")},
			result: ResultAnswered, dir: DirectionInternal, words: "Listened to voicemail",
			parties: map[string]bool{"Alice": false},
		},
		{
			name:   "echo test",
			rows:   []Row{row(2, 0, 0, 1, aliceCLID, "101", "*43", "linx-extensions", "PJSIP/d_alice-00000002", "", 1, "ANSWERED", "1.2", "", "", "")},
			result: ResultEchoTest, dir: DirectionInternal, words: "Echo test",
			parties: map[string]bool{"Alice": false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Build(context.Background(), fakeLookup{}, tc.rows)
			if err != nil {
				t.Fatal(err)
			}
			if c.Result != tc.result || c.Direction != tc.dir || c.TenantID != tenant {
				t.Errorf("result %s, direction %s, tenant %s; want %s, %s", c.Result, c.Direction, c.TenantID, tc.result, tc.dir)
			}
			if got := words(c); got != tc.words {
				t.Errorf("steps:\n got %s\nwant %s", got, tc.words)
			}
			if got := missedBy(c); !reflect.DeepEqual(got, tc.parties) {
				t.Errorf("parties %v, want %v", got, tc.parties)
			}
			if tc.check != nil {
				tc.check(t, c)
			}
		})
	}
}

func TestCSVSafe(t *testing.T) {
	for in, want := range map[string]string{
		"+971501234567": "+971501234567", "=HYPERLINK(1)": "'=HYPERLINK(1)", "+1+2": "'+1+2", "-5": "'-5", "@SUM": "'@SUM",
		"Ahmed Ali": "Ahmed Ali", "": "",
	} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCursor(t *testing.T) {
	c := Cursor{StartedAt: time.Date(2026, 10, 2, 9, 0, 0, 123000, time.UTC), ID: uuid.MustParse("01a0fc00-0000-7000-8000-000000000001")}
	got, err := ParseCursor(c.String())
	if err != nil || !got.StartedAt.Equal(c.StartedAt) || got.ID != c.ID {
		t.Errorf("%v, %v", got, err)
	}
	for _, bad := range []string{"", "x", "1.notauuid", "x." + c.ID.String()} {
		if _, err := ParseCursor(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
