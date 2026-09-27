package backupschedule

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

type fakeRestores struct {
	setupDone bool
	req       *Restore
	sealed    []byte
	audits    []auth.AuditEntry
}

func (f *fakeRestores) SetupCompleted(context.Context) (bool, error) { return f.setupDone, nil }

func (f *fakeRestores) Restore(_ context.Context, _ uuid.UUID) (Restore, bool, error) {
	if f.req == nil {
		return Restore{}, false, nil
	}
	return *f.req, true, nil
}

func (f *fakeRestores) PutRestore(_ context.Context, r Restore, sealed []byte, a auth.AuditEntry) error {
	f.req, f.sealed = &r, sealed
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeRestores) TakeRestore(_ context.Context, now time.Time) (Restore, []byte, bool, error) {
	if f.req == nil || f.req.Status != RestorePending {
		return Restore{}, nil, false, nil
	}
	f.req.Status, f.req.UpdatedAt = RestoreRunning, now
	sealed := f.sealed
	f.sealed = nil
	return *f.req, sealed, true, nil
}

func (f *fakeRestores) FailRestore(_ context.Context, id uuid.UUID, msg string, now time.Time) error {
	if f.req != nil && f.req.ID == id {
		f.req.Status, f.req.Error, f.req.UpdatedAt = RestoreFailed, msg, now
	}
	return nil
}

func (f *fakeRestores) RecordRestored(_ context.Context, a auth.AuditEntry) error {
	f.audits = append(f.audits, a)
	return nil
}

// fakeSealer "seals" by prefixing the row id, so opening with the wrong row
// fails like the real one.
type fakeSealer struct{}

func (fakeSealer) Seal(rowID string, p []byte) ([]byte, error) {
	return append([]byte(rowID+"|"), p...), nil
}
func (fakeSealer) Open(rowID string, b []byte) ([]byte, error) {
	rest, ok := strings.CutPrefix(string(b), rowID+"|")
	if !ok {
		return nil, errors.New("wrong row")
	}
	return []byte(rest), nil
}

var restoreNow = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func restoreService(f *fakeRestores) *Service {
	return &Service{Store: &fakeStore{}, Restores: f, Sealer: fakeSealer{}, Now: func() time.Time { return restoreNow }}
}

// sessionCtx is a signed-in person's request, confirmed confirmedAgo ago.
func sessionCtx(role string, confirmedAgo time.Duration) context.Context {
	tenant := uuid.New()
	at := restoreNow.Add(-confirmedAgo)
	s := auth.UserSession{ID: uuid.New(), TenantID: tenant, UserID: uuid.New(), Role: role, MFAVerified: true, ConfirmedAt: &at}
	return auth.WithSession(auth.WithPrincipal(context.Background(), s.Principal()), s)
}

var goodRestore = RestoreInput{Source: "folder", Location: "/var/backups/linx", Password: "pw"}

func apiCode(err error) string {
	var e *apihttp.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestRequestRestore(t *testing.T) {
	f := &fakeRestores{}
	svc := restoreService(f)
	r, err := svc.RequestRestore(sessionCtx(auth.RoleSystemAdmin, time.Minute), goodRestore)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != RestorePending || r.Snapshot != "latest" || f.req == nil {
		t.Fatalf("request = %+v", r)
	}
	if !strings.HasPrefix(string(f.sealed), "backup_restore:"+r.ID.String()+"|") {
		t.Errorf("password stored as %q, want it sealed", f.sealed)
	}
	if a := f.audits[0]; a.Action != "backup.restore_requested" || strings.Contains(strings.ToLower(a.Target+a.Actor), "pw") {
		t.Errorf("audit = %+v", a)
	}
	for k, v := range f.audits[0].Detail {
		if v == "pw" {
			t.Errorf("audit detail %s holds the password", k)
		}
	}

	// A second one while the first is waiting is refused.
	if _, err := svc.RequestRestore(sessionCtx(auth.RoleSystemAdmin, time.Minute), goodRestore); apiCode(err) != "restore_in_progress" {
		t.Errorf("second request: %v", err)
	}

	// The agent takes it once, with the password; the row no longer has it.
	taken, found, err := svc.TakeRestore(context.Background())
	if err != nil || !found || taken.Password != "pw" || taken.Status != RestoreRunning || f.sealed != nil {
		t.Fatalf("TakeRestore() = %+v, %v, %v (sealed now %q)", taken, found, err, f.sealed)
	}
	if _, found, _ := svc.TakeRestore(context.Background()); found {
		t.Error("taken twice")
	}

	// Pending reports nothing due while it runs, then fails it.
	if due, _, err := svc.Pending(context.Background()); due || err != nil {
		t.Errorf("Pending() while restoring = %v, %v", due, err)
	}
	if err := svc.FailRestore(context.Background(), taken.ID, "wrong password"); err != nil {
		t.Fatal(err)
	}
	st, _ := svc.RestoreStatus(sessionCtx(auth.RoleSystemAdmin, time.Minute))
	if st.Status != RestoreFailed || st.Error != "wrong password" {
		t.Errorf("status = %+v", st)
	}
	// A failed one can be tried again.
	if _, err := svc.RequestRestore(sessionCtx(auth.RoleSystemAdmin, time.Minute), goodRestore); err != nil {
		t.Errorf("retry: %v", err)
	}
	if due, trigger, _ := svc.Pending(context.Background()); !due || trigger != TriggerRestore {
		t.Errorf("Pending() = %v %q, want the restore", due, trigger)
	}
}

func TestRequestRestoreRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx       context.Context
		in        RestoreInput
		setupDone bool
		code      string
	}{
		"admin":          {sessionCtx(auth.RoleAdmin, time.Minute), goodRestore, false, "system_admin_only"},
		"api key":        {auth.WithPrincipal(context.Background(), auth.SystemPrincipal(uuid.New())), goodRestore, false, "system_admin_only"},
		"not confirmed":  {sessionCtx(auth.RoleSystemAdmin, time.Hour), goodRestore, false, "confirm_required"},
		"setup finished": {sessionCtx(auth.RoleSystemAdmin, time.Minute), goodRestore, true, "restore_after_setup"},
		"relative folder": {sessionCtx(auth.RoleSystemAdmin, time.Minute),
			RestoreInput{Source: "folder", Location: "backups", Password: "pw"}, false, "restore_location_invalid"},
		"backend string": {sessionCtx(auth.RoleSystemAdmin, time.Minute),
			RestoreInput{Source: "folder", Location: "sftp:u@h:/x", Password: "pw"}, false, "restore_location_invalid"},
		"bad destination": {sessionCtx(auth.RoleSystemAdmin, time.Minute),
			RestoreInput{Source: "destination", Location: "../x", Password: "pw"}, false, "restore_location_invalid"},
		"bad snapshot": {sessionCtx(auth.RoleSystemAdmin, time.Minute),
			RestoreInput{Source: "folder", Location: "/b", Snapshot: "x;y", Password: "pw"}, false, "restore_snapshot_invalid"},
		"no password": {sessionCtx(auth.RoleSystemAdmin, time.Minute),
			RestoreInput{Source: "folder", Location: "/b"}, false, "restore_password_missing"},
	} {
		f := &fakeRestores{setupDone: tc.setupDone}
		_, err := restoreService(f).RequestRestore(tc.ctx, tc.in)
		if got := apiCode(err); got != tc.code {
			t.Errorf("%s: code %q (%v), want %q", name, got, err, tc.code)
		}
		if f.req != nil {
			t.Errorf("%s: stored a refused request", name)
		}
	}
}

func TestRestoreStatusShowsAStuckRestoreAsFailed(t *testing.T) {
	f := &fakeRestores{req: &Restore{ID: uuid.New(), Status: RestoreRunning, UpdatedAt: restoreNow.Add(-RestoreStaleAfter - time.Minute)}}
	svc := restoreService(f)
	st, err := svc.RestoreStatus(sessionCtx(auth.RoleSystemAdmin, time.Minute))
	if err != nil || st.Status != RestoreFailed || st.Error == "" {
		t.Fatalf("status = %+v, %v", st, err)
	}
	// ... and a new one may be asked for.
	if _, err := svc.RequestRestore(sessionCtx(auth.RoleSystemAdmin, time.Minute), goodRestore); err != nil {
		t.Errorf("after a stuck restore: %v", err)
	}
}
