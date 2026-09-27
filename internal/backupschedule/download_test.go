package backupschedule

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

type fakeDownloads struct {
	d      *Download
	sealed []byte
	audits []string
}

func (f *fakeDownloads) Download(context.Context, uuid.UUID) (Download, bool, error) {
	if f.d == nil {
		return Download{}, false, nil
	}
	return *f.d, true, nil
}

func (f *fakeDownloads) PutDownload(_ context.Context, d Download, a auth.AuditEntry) error {
	f.d, f.sealed = &d, nil
	f.audits = append(f.audits, a.Action)
	return nil
}

func (f *fakeDownloads) TakeDownload(_ context.Context, now time.Time) (Download, bool, error) {
	if f.d == nil || f.d.Status != DownloadPending {
		return Download{}, false, nil
	}
	f.d.Status, f.d.UpdatedAt = DownloadPreparing, now
	return *f.d, true, nil
}

func (f *fakeDownloads) FinishDownload(_ context.Context, d Download, sealed []byte) error {
	if f.d == nil || f.d.ID != d.ID || f.d.Status != DownloadPreparing {
		return nil
	}
	f.d.Status, f.d.Error, f.d.Size, f.d.SnapshotID, f.d.SnapshotTime, f.d.UpdatedAt, f.d.ExpiresAt =
		d.Status, d.Error, d.Size, d.SnapshotID, d.SnapshotTime, d.UpdatedAt, d.ExpiresAt
	f.sealed = sealed
	return nil
}

func (f *fakeDownloads) DownloadPassword(context.Context, uuid.UUID) ([]byte, error) {
	return f.sealed, nil
}

func (f *fakeDownloads) ExpireDownloads(_ context.Context, now time.Time) error {
	if f.d != nil && f.d.ExpiresAt != nil && !now.Before(*f.d.ExpiresAt) {
		f.sealed = nil
	}
	return nil
}

func (f *fakeDownloads) WriteAudit(_ context.Context, a auth.AuditEntry) error {
	f.audits = append(f.audits, a.Action)
	return nil
}

type downloadWorld struct {
	svc *Service
	dl  *fakeDownloads
	rs  *fakeRestores
	dir string
	now *time.Time
}

func newDownloadWorld(t *testing.T) *downloadWorld {
	now := restoreNow
	w := &downloadWorld{dl: &fakeDownloads{}, rs: &fakeRestores{}, dir: t.TempDir(), now: &now}
	w.svc = &Service{Store: &fakeStore{}, Restores: w.rs, Sealer: fakeSealer{}, Downloads: w.dl,
		Transfer: &Transfer{Dir: w.dir}, Now: func() time.Time { return *w.now }}
	return w
}

func code(err error) string {
	var e *apihttp.Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// makeReady runs a download through the agent's side: take, file, done.
func (w *downloadWorld) makeReady(t *testing.T, ctx context.Context) Download {
	t.Helper()
	d, err := w.svc.RequestDownload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	taken, found, err := w.svc.TakeDownload(context.Background())
	if err != nil || !found || taken.ID != d.ID {
		t.Fatalf("take: %+v %v %v", taken, found, err)
	}
	if _, err := w.svc.PutDownloadFile(context.Background(), d.ID, strings.NewReader("TAR")); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.FinishDownload(context.Background(), DownloadResult{ID: d.ID, OK: true, SnapshotID: "abc",
		SnapshotTime: restoreNow.Add(-time.Minute), Password: "repo-pw"}); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDownloadRequesterOnly(t *testing.T) {
	w := newDownloadWorld(t)
	alice := sessionCtx(auth.RoleAdmin, time.Minute)
	w.makeReady(t, alice)

	st, mine, err := w.svc.DownloadStatus(alice)
	if err != nil || !mine || st.Status != DownloadReady || st.Size != 3 {
		t.Fatalf("status %+v mine %v %v", st, mine, err)
	}
	f, _, err := w.svc.OpenDownload(alice)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "TAR" {
		t.Fatalf("file %q", b)
	}
	if pw, err := w.svc.DownloadPassword(alice); err != nil || pw != "repo-pw" {
		t.Fatalf("password %q %v", pw, err)
	}

	// Another admin sees that a file exists, but can't fetch it or its password.
	bob := sessionCtx(auth.RoleAdmin, time.Minute)
	if _, mine, _ := w.svc.DownloadStatus(bob); mine {
		t.Error("someone else's download is mine")
	}
	if _, _, err := w.svc.OpenDownload(bob); code(err) != "download_not_ready" {
		t.Errorf("bob opened it: %v", err)
	}
	if _, err := w.svc.DownloadPassword(bob); code(err) != "download_not_ready" {
		t.Errorf("bob saw the password: %v", err)
	}
	want := []string{"backup.download_requested", "backup.downloaded", "backup.password_shown"}
	if strings.Join(w.dl.audits, ",") != strings.Join(want, ",") {
		t.Errorf("audits %v", w.dl.audits)
	}
}

func TestDownloadNeedsConfirmation(t *testing.T) {
	w := newDownloadWorld(t)
	stale := sessionCtx(auth.RoleAdmin, time.Hour)
	if _, err := w.svc.RequestDownload(stale); code(err) != "confirm_required" {
		t.Fatalf("request without a fresh confirm: %v", err)
	}
	fresh := sessionCtx(auth.RoleAdmin, time.Minute)
	w.makeReady(t, fresh)
	// Showing the password again needs a fresh confirm too.
	*w.now = w.now.Add(20 * time.Minute)
	if _, err := w.svc.DownloadPassword(fresh); code(err) != "confirm_required" {
		t.Fatalf("password without a fresh confirm: %v", err)
	}
}

func TestDownloadOneAtATimeAndExpiry(t *testing.T) {
	w := newDownloadWorld(t)
	ctx := sessionCtx(auth.RoleAdmin, time.Minute)
	if _, err := w.svc.RequestDownload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.RequestDownload(ctx); code(err) != "download_in_progress" {
		t.Fatalf("second request: %v", err)
	}
	// Stale: the agent never reported.
	*w.now = w.now.Add(time.Hour)
	if st, _, _ := w.svc.DownloadStatus(ctx); st.Status != DownloadPending {
		t.Fatalf("a pending request isn't stale: %+v", st)
	}
	w.dl.d.Status = DownloadPreparing
	if st, _, _ := w.svc.DownloadStatus(ctx); st.Status != DownloadFailed {
		t.Fatalf("preparing for an hour: %+v", st)
	}
	ctx = sessionCtx(auth.RoleAdmin, -time.Hour) // confirmed "now", an hour after restoreNow
	d := w.makeReady(t, ctx)
	path := w.svc.Transfer.DownloadPath(d.ID)
	*w.now = w.now.Add(DownloadKeptFor)
	if st, mine, _ := w.svc.DownloadStatus(ctx); st.Status != DownloadNone || mine {
		t.Fatalf("expired: %+v", st)
	}
	if err := w.svc.SweepDownloads(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the expired file is still there")
	}
	if w.dl.sealed != nil {
		t.Error("the expired password is still there")
	}
}

func TestFinishDownloadFailures(t *testing.T) {
	w := newDownloadWorld(t)
	ctx := sessionCtx(auth.RoleAdmin, time.Minute)
	d, _ := w.svc.RequestDownload(ctx)
	_, _, _ = w.svc.TakeDownload(context.Background())
	// Reported ready, but the file never arrived.
	if err := w.svc.FinishDownload(context.Background(), DownloadResult{ID: d.ID, OK: true, Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	if w.dl.d.Status != DownloadFailed || w.dl.sealed != nil {
		t.Fatalf("%+v", w.dl.d)
	}
	// A file for a request that isn't being made is refused.
	if _, err := w.svc.PutDownloadFile(context.Background(), d.ID, strings.NewReader("x")); err == nil {
		t.Error("stored a file for a failed request")
	}
	if _, err := w.svc.PutDownloadFile(context.Background(), uuid.New(), strings.NewReader("x")); err == nil {
		t.Error("stored a file for an unknown request")
	}
}

func TestUploadForRestore(t *testing.T) {
	w := newDownloadWorld(t)
	admin := sessionCtx(auth.RoleSystemAdmin, time.Minute)

	if _, _, err := w.svc.AcceptUpload(sessionCtx(auth.RoleAdmin, time.Minute), strings.NewReader("x")); code(err) != "system_admin_only" {
		t.Fatalf("an admin uploaded: %v", err)
	}
	if _, _, err := w.svc.AcceptUpload(sessionCtx(auth.RoleSystemAdmin, time.Hour), strings.NewReader("x")); code(err) != "confirm_required" {
		t.Fatalf("uploaded without confirming: %v", err)
	}
	first, _, err := w.svc.AcceptUpload(admin, strings.NewReader("one"))
	if err != nil {
		t.Fatal(err)
	}
	id, n, err := w.svc.AcceptUpload(admin, strings.NewReader("backup"))
	if err != nil || n != 6 {
		t.Fatalf("%v %d %v", id, n, err)
	}
	if w.svc.Transfer.UploadExists(first) {
		t.Error("an earlier upload was kept")
	}
	if _, err := w.svc.RequestRestore(admin, RestoreInput{Source: "upload", Location: uuid.NewString(), Password: "pw"}); code(err) != "upload_missing" {
		t.Fatalf("restore from an unknown upload: %v", err)
	}
	r, err := w.svc.RequestRestore(admin, RestoreInput{Source: "upload", Location: id.String(), Password: "pw"})
	if err != nil || r.Source != "upload" {
		t.Fatalf("%+v %v", r, err)
	}
	f, err := w.svc.ReadUpload(id)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := w.svc.RemoveUpload(id); err != nil || w.svc.Transfer.UploadExists(id) {
		t.Fatalf("remove: %v", err)
	}
	w.rs.setupDone = true
	w.rs.req = nil
	if _, _, err := w.svc.AcceptUpload(admin, strings.NewReader("x")); code(err) != "restore_after_setup" {
		t.Fatalf("uploaded after setup: %v", err)
	}
}

func TestTransferLimitsAndSweep(t *testing.T) {
	dir := t.TempDir()
	tr := &Transfer{Dir: dir}
	if _, err := tr.save(filepath.Join(dir, "x"), strings.NewReader("12345"), 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
	keep, other := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{keep, other} {
		if _, err := tr.SaveDownload(id, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	up, _, _ := tr.SaveUpload(strings.NewReader("u"))
	tr.Sweep(keep, time.Now())
	if _, err := os.Stat(tr.DownloadPath(keep)); err != nil {
		t.Error("removed the kept download")
	}
	if _, err := os.Stat(tr.DownloadPath(other)); err == nil {
		t.Error("kept another download")
	}
	if !tr.UploadExists(up) {
		t.Error("removed a fresh upload")
	}
	tr.Sweep(keep, time.Now().Add(UploadKeptFor+time.Minute))
	if tr.UploadExists(up) {
		t.Error("kept an old upload")
	}
}
