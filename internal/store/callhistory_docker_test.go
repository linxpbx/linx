package store

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/callhistory"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/voicemail"
)

// TestCallHistoryDocker checks call history against real Postgres
// (migration 0037): rows Asterisk adds become one line per call, read
// once and read again when a late row comes; each person sees their own
// calls and what they missed; the badge counts since they last looked;
// filters, pages and a voicemail left; and old calls go. It needs Docker:
// make test-docker.
func TestCallHistoryDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-callhistory-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "test", Result: auth.ResultOK}
	person := func(number, name, username string) (pbx.Extension, uuid.UUID) {
		e := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: number, DisplayName: name, Enabled: true,
			Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateExtension(ctx, e, audit); err != nil {
			t.Fatal(err)
		}
		d := pbx.Device{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, ExtensionID: e.ID, Name: "Phone", Kind: pbx.KindSoftphone,
			SIPUsername: username, DigestHash: pbx.DigestHash(username, "password"), Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateDevice(ctx, d, audit); err != nil {
			t.Fatal(err)
		}
		u := auth.User{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Email: username + "@example.com", Name: name, Role: auth.RoleUser,
			ExtensionID: &e.ID, PasswordHash: "x", PasswordUpdatedAt: now, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateUser(ctx, u, audit); err != nil {
			t.Fatal(err)
		}
		return e, u.ID
	}
	sara, saraUser := person("101", "Sara", "d_sara0001")
	omar, omarUser := person("102", "Omar", "d_omar0002")

	// Asterisk's own role may only add rows (ADR-070).
	astPool := asteriskPool(t, ctx, pool)
	add := func(id int, start time.Time, secs int, dst, dcontext, channel, dstchannel, disposition, uniqueid, dialled, step string) {
		t.Helper()
		answered := "1970-01-01 00:00:00"
		if disposition == "ANSWERED" {
			answered = start.Add(time.Second).Format(time.DateTime)
		}
		_, err := astPool.Exec(ctx, `INSERT INTO asterisk.cdr (started, answered, ended, clid, src, dst, dcontext, channel, dstchannel,
				lastapp, duration, billsec, disposition, uniqueid, linkedid, sequence, linx_dialled, linx_step)
			VALUES ($1, $2, $3, '"Sara" <101>', '101', $4, $5, $6, $7, 'Dial', $8, $8, $9, $10, $10, $11, $12, $13)`,
			start.Format(time.DateTime), answered, start.Add(time.Duration(secs)*time.Second).Format(time.DateTime),
			dst, dcontext, channel, dstchannel, secs, disposition, uniqueid, id, dialled, step)
		if err != nil {
			t.Fatalf("Asterisk adding a call record: %v", err)
		}
	}
	for _, q := range []string{"SELECT count(*) FROM asterisk.cdr", "UPDATE asterisk.cdr SET src = ''", "DELETE FROM asterisk.cdr",
		"SELECT count(*) FROM call_record"} {
		if _, err := astPool.Exec(ctx, q); err == nil {
			t.Errorf("linx_asterisk could run %q; want permission denied", q)
		}
	}

	changed := 0
	b := &callhistory.Builder{Store: CallHistory{s}, Now: func() time.Time { return now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Changed: func(uuid.UUID) { changed++ }}
	svc := &callhistory.Service{Store: CallHistory{s}, Builder: b, Now: func() time.Time { return now }}

	// Sara calls Omar, who answers; an hour later, Omar doesn't answer.
	t0 := now.Add(-2 * time.Hour)
	add(1, t0, 60, "s", "linx-route", "PJSIP/d_sara0001-00000001", "PJSIP/d_omar0002-00000002", "ANSWERED", "100.1", "102", "n:102")
	t1 := now.Add(-time.Hour)
	add(2, t1, 30, "s", "linx-route", "PJSIP/d_sara0001-00000003", "PJSIP/d_omar0002-00000004", "NO ANSWER", "100.3", "102", "n:102")
	if err := b.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if changed == 0 {
		t.Error("the badge wasn't told")
	}
	mine := func(user uuid.UUID, f callhistory.Filter) []callhistory.Listed {
		t.Helper()
		items, _, err := svc.Mine(ctx, tenant, user, f)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	got := mine(omarUser, callhistory.Filter{})
	if len(got) != 2 || got[0].Result != callhistory.ResultMissed || !got[0].Missed || got[1].Result != callhistory.ResultAnswered || got[1].Missed {
		t.Fatalf("Omar's calls: %+v", got)
	}
	if got[1].TalkSeconds != 60 || got[1].AnsweredByName != "Omar (102)" || got[1].FromName != "Sara" || !got[1].StartedAt.Equal(t0) {
		t.Errorf("the answered call: %+v", got[1])
	}
	if n, _ := svc.MissedCount(ctx, omarUser); n != 1 {
		t.Errorf("Omar's badge %d, want 1", n)
	}
	if n, _ := svc.MissedCount(ctx, saraUser); n != 0 {
		t.Errorf("Sara's badge %d, want 0 (her own call)", n)
	}

	// A late row (the message after nobody answered) makes the call be
	// read again: the same line, one more step.
	add(3, t1.Add(30*time.Second), 5, "not-available", "linx-messages", "PJSIP/d_sara0001-00000003", "", "ANSWERED", "100.3", "102", "n:102")
	if err := b.Read(ctx); err != nil {
		t.Fatal(err)
	}
	again := mine(omarUser, callhistory.Filter{})
	if len(again) != 2 || again[0].ID != got[0].ID || len(again[0].Steps) != 2 || again[0].EndedAt.Sub(again[0].StartedAt) != 35*time.Second {
		t.Fatalf("after the late row: %+v", again)
	}
	if w := again[0].Words(); w[0] != "Rang Omar (102) · nobody answered in 30 s" {
		t.Errorf("words %q", w)
	}

	// Opening Call history clears the badge; a newer missed call counts.
	if err := svc.Seen(ctx, omarUser); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.MissedCount(ctx, omarUser); n != 0 {
		t.Errorf("after looking: %d", n)
	}

	// Omar's phone not connected: Sara reaches his voicemail and leaves a
	// message.
	t2 := now.Add(time.Minute)
	add(4, t2, 20, omar.ID.String(), "linx-voicemail", "PJSIP/d_sara0001-00000005", "", "ANSWERED", "100.5", "102", "n:102")
	if err := b.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.MissedCount(ctx, omarUser); n != 1 {
		t.Errorf("Omar's badge after the voicemail: %d", n)
	}
	m := voicemail.Message{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, BoxID: omar.ID, Source: "100.5", CallerNumber: "101",
		CallerName: "Sara", ReceivedAt: t2, Duration: 12 * time.Second, Audio: make([]byte, 12*voicemail.SampleRate), CreatedAt: t2}
	if _, err := s.AddVoicemail(ctx, m, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	vm := mine(omarUser, callhistory.Filter{Missed: true})
	if len(vm) != 2 || vm[0].VoicemailID == nil || *vm[0].VoicemailID != m.ID || vm[0].Result != callhistory.ResultVoicemail {
		t.Fatalf("missed, with the voicemail: %+v", vm)
	}
	if w := vm[0].Words(); !strings.HasSuffix(w[len(w)-1], "left a message (0:12)") {
		t.Errorf("words %q", w)
	}

	// Everyone's: pages, number search, dates.
	all := func(f callhistory.Filter) ([]callhistory.Listed, *callhistory.Cursor) {
		t.Helper()
		items, next, err := svc.All(ctx, tenant, f)
		if err != nil {
			t.Fatal(err)
		}
		return items, next
	}
	page, next := all(callhistory.Filter{Limit: 2})
	if len(page) != 2 || next == nil {
		t.Fatalf("first page: %d, next %v", len(page), next)
	}
	cur, err := callhistory.ParseCursor(next.String())
	if err != nil {
		t.Fatal(err)
	}
	rest, next := all(callhistory.Filter{Limit: 2, Before: &cur})
	if len(rest) != 1 || next != nil || rest[0].ID != got[1].ID {
		t.Errorf("second page: %+v, next %v", rest, next)
	}
	if items, _ := all(callhistory.Filter{Number: "10 2"}); len(items) != 3 {
		t.Errorf("number search: %d calls", len(items))
	}
	if items, _ := all(callhistory.Filter{Number: "999"}); len(items) != 0 {
		t.Errorf("number search for nothing: %d calls", len(items))
	}
	from := now.Add(-90 * time.Minute)
	if items, _ := all(callhistory.Filter{From: &from}); len(items) != 2 {
		t.Errorf("since 90 minutes ago: %d calls", len(items))
	}
	if items, _ := all(callhistory.Filter{Party: &sara.ID, Missed: true}); len(items) != 0 {
		t.Errorf("Sara missed %d calls", len(items))
	}

	var csv strings.Builder
	if err := svc.WriteCSV(ctx, &csv, callhistory.Filter{TenantID: tenant}, time.UTC); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(csv.String()), "\n"); len(lines) != 4 || !strings.HasPrefix(lines[0], "started,direction") {
		t.Errorf("CSV:\n%s", csv.String())
	}

	// Keeping: 30 days at least; older calls go, and so do Asterisk's
	// rows once read and a day old.
	if err := svc.SetKeepDays(ctx, 10, audit); err == nil {
		t.Error("10 days accepted")
	}
	if err := svc.SetKeepDays(ctx, 30, audit); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE call_record SET started_at = started_at - interval '31 days' WHERE linkedid = '100.1'`); err != nil {
		t.Fatal(err)
	}
	tenants, err := CallHistory{s}.ExpireCallHistory(ctx, now.Add(25*time.Hour))
	if err != nil || len(tenants) != 1 {
		t.Fatalf("expire: %v, %v", tenants, err)
	}
	if items, _ := all(callhistory.Filter{}); len(items) != 2 {
		t.Errorf("after expiry: %d calls", len(items))
	}
	var raw int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM asterisk.cdr`).Scan(&raw); err != nil || raw != 0 {
		t.Errorf("Asterisk's rows left: %d, %v", raw, err)
	}
	u, err := svc.Store.CallHistoryUsage(ctx, tenant)
	if err != nil || u.Calls != 2 || u.Bytes <= 0 {
		t.Errorf("usage %+v, %v", u, err)
	}
}

// asteriskPool connects as linx_asterisk, the role Asterisk uses.
func asteriskPool(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	if _, err := pool.Exec(ctx, `ALTER ROLE linx_asterisk WITH LOGIN PASSWORD 'asterisk-test'`); err != nil {
		t.Fatal(err)
	}
	cfg := pool.Config().ConnConfig
	ap, err := pgxpool.New(ctx, fmt.Sprintf("postgres://linx_asterisk:asterisk-test@%s:%d/%s?sslmode=disable", cfg.Host, cfg.Port, cfg.Database))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ap.Close)
	return ap
}
