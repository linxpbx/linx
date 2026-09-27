package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/backupschedule"
)

type fakeDownloadStore struct {
	d      *backupschedule.Download
	sealed []byte
}

func (f *fakeDownloadStore) Download(context.Context, uuid.UUID) (backupschedule.Download, bool, error) {
	if f.d == nil {
		return backupschedule.Download{}, false, nil
	}
	return *f.d, true, nil
}
func (f *fakeDownloadStore) PutDownload(_ context.Context, d backupschedule.Download, _ auth.AuditEntry) error {
	f.d = &d
	return nil
}
func (f *fakeDownloadStore) TakeDownload(_ context.Context, now time.Time) (backupschedule.Download, bool, error) {
	if f.d == nil || f.d.Status != backupschedule.DownloadPending {
		return backupschedule.Download{}, false, nil
	}
	f.d.Status = backupschedule.DownloadPreparing
	return *f.d, true, nil
}
func (f *fakeDownloadStore) FinishDownload(_ context.Context, d backupschedule.Download, sealed []byte) error {
	f.d.Status, f.d.Size, f.d.SnapshotTime, f.d.ExpiresAt, f.d.UpdatedAt = d.Status, d.Size, d.SnapshotTime, d.ExpiresAt, d.UpdatedAt
	f.sealed = sealed
	return nil
}
func (f *fakeDownloadStore) DownloadPassword(context.Context, uuid.UUID) ([]byte, error) {
	return f.sealed, nil
}
func (f *fakeDownloadStore) ExpireDownloads(context.Context, time.Time) error  { return nil }
func (f *fakeDownloadStore) WriteAudit(context.Context, auth.AuditEntry) error { return nil }

// backupFileEnv is the test API server with the backup-file handlers on
// their own mux in front of it, and a signed-in person whose session can be
// turned into a system admin's.
type backupFileEnv struct {
	*testEnv
	svc     *backupschedule.Service
	dl      *fakeDownloadStore
	srv     string
	cookies []*http.Cookie
	csrf    string
	userID  uuid.UUID
}

func newBackupFileEnv(t *testing.T) *backupFileEnv {
	env := newTestEnv(t)
	dl := &fakeDownloadStore{}
	svc := &backupschedule.Service{Store: &fakeBackupStore{tenant: env.store.tenant}, Restores: &fakeRestoreStore{}, Sealer: plainSealer{},
		Downloads: dl, Transfer: &backupschedule.Transfer{Dir: t.TempDir()}, Now: time.Now}
	mux := http.NewServeMux()
	registerBackupFileHandlers(mux, env.authn, svc)
	mux.Handle("/", env.srv.Config.Handler)
	env.srv.Config.Handler = mux
	user, cookies, csrf := signedInPerson(t, env, "admin@example.com", nil)
	return &backupFileEnv{testEnv: env, svc: svc, dl: dl, srv: env.srv.URL, cookies: cookies, csrf: csrf, userID: user}
}

// become makes the person's session role's, confirmed confirmedAgo ago.
func (e *backupFileEnv) become(role string, confirmedAgo time.Duration) {
	for id, s := range e.store.sessions {
		if s.UserID == e.userID {
			at := time.Now().Add(-confirmedAgo)
			s.Role, s.MFAVerified, s.ConfirmedAt = role, true, &at
			e.store.sessions[id] = s
		}
	}
}

func (e *backupFileEnv) do(method, path, contentType string, body io.Reader, withCSRF bool) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, c := range e.cookies {
		req.AddCookie(c)
	}
	if withCSRF {
		req.Header.Set(auth.CSRFHeaderName, e.csrf)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func problemCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var p struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&p)
	return p.Code
}

func TestUploadRestoreFile(t *testing.T) {
	e := newBackupFileEnv(t)
	upload := func(ct string, csrf bool) *http.Response {
		return e.do(http.MethodPut, "/api/v1/backup-restore/file", ct, strings.NewReader("TAR-BYTES"), csrf)
	}

	// A person without backups:write.
	if resp := upload("application/octet-stream", true); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a person uploaded: %d", resp.StatusCode)
	}
	e.become(auth.RoleSystemAdmin, time.Minute)
	if resp := upload("application/octet-stream", false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no CSRF token: %d", resp.StatusCode)
	}
	if resp := upload("text/plain", true); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain: %d", resp.StatusCode)
	}
	e.become(auth.RoleAdmin, time.Minute)
	if resp := upload("application/octet-stream", true); problemCode(t, resp) != "system_admin_only" {
		t.Fatal("an admin (not system admin) uploaded")
	}
	e.become(auth.RoleSystemAdmin, time.Hour)
	if resp := upload("application/octet-stream", true); problemCode(t, resp) != "confirm_required" {
		t.Fatal("uploaded without a fresh confirm")
	}
	e.become(auth.RoleSystemAdmin, time.Minute)
	resp := upload("application/octet-stream", true)
	defer resp.Body.Close()
	var got struct {
		UploadID string `json:"upload_id"`
		Size     int64  `json:"size"`
	}
	if resp.StatusCode != http.StatusCreated || json.NewDecoder(resp.Body).Decode(&got) != nil || got.Size != 9 {
		t.Fatalf("upload: %d %+v", resp.StatusCode, got)
	}
	b, err := os.ReadFile(e.svc.Transfer.UploadPath(uuid.MustParse(got.UploadID)))
	if err != nil || string(b) != "TAR-BYTES" {
		t.Fatalf("stored %q %v", b, err)
	}
}

func TestUploadTooLarge(t *testing.T) {
	e := newBackupFileEnv(t)
	e.become(auth.RoleSystemAdmin, time.Minute)
	// Straight to the handler: a real client won't send a body shorter than
	// its Content-Length, and the handler answers before reading it anyway.
	req := httptest.NewRequest(http.MethodPut, "/api/v1/backup-restore/file", strings.NewReader("x"))
	req.ContentLength = backupschedule.MaxUploadSize + 1
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(auth.CSRFHeaderName, e.csrf)
	for _, c := range e.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.testEnv.srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "upload_too_large") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestDownloadBackupFile(t *testing.T) {
	e := newBackupFileEnv(t)
	e.become(auth.RoleAdmin, time.Minute)
	get := func() *http.Response { return e.do(http.MethodGet, "/api/v1/backup-download/file", "", nil, false) }
	if code := problemCode(t, get()); code != "download_not_ready" {
		t.Fatalf("nothing asked for: %s", code)
	}

	// Asked for by this admin, made by the agent.
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: auth.TypeUser, ID: e.userID.String(), TenantID: e.store.tenant,
		Role: auth.RoleAdmin, Scopes: auth.Scopes})
	d, err := e.svc.RequestDownload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = e.svc.TakeDownload(ctx)
	if _, err := e.svc.PutDownloadFile(ctx, d.ID, bytes.NewReader([]byte("THE-BACKUP-FILE"))); err != nil {
		t.Fatal(err)
	}
	snap := time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC)
	if err := e.svc.FinishDownload(ctx, backupschedule.DownloadResult{ID: d.ID, OK: true, SnapshotTime: snap, Password: "pw"}); err != nil {
		t.Fatal(err)
	}

	resp := get()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "THE-BACKUP-FILE" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename=linx-backup-2026-09-27-0300.tar` {
		t.Errorf("Content-Disposition %q", cd)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", resp.Header.Get("Cache-Control"))
	}

	// Resumable.
	req, _ := http.NewRequest(http.MethodGet, e.srv+"/api/v1/backup-download/file", nil)
	req.Header.Set("Range", "bytes=4-")
	for _, c := range e.cookies {
		req.AddCookie(c)
	}
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusPartialContent || string(part) != "BACKUP-FILE" {
		t.Fatalf("range: %d %q", r2.StatusCode, part)
	}

	// Someone else, even with the scope, can't.
	e.userID = uuid.Nil
	d.RequestedBy = "user:someone-else"
	e.dl.d.RequestedBy = "user:someone-else"
	if code := problemCode(t, get()); code != "download_not_ready" {
		t.Fatalf("another admin: %s", code)
	}
}
