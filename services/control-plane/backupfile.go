package main

import (
	"errors"
	"mime"
	"net/http"
	"time"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/backupschedule"
)

// Backup files through the browser (docs/BACKUP.md §8 step 5). Hand-written
// (excluded from code generation) because they stream: the generated
// handlers' request validation would read a whole upload into memory, and
// a typed response can't serve a resumable file. Their scopes are checked
// here, since the validator never sees them.

// backupFileTime is how long one upload or download may take: about 2 GB
// over a slow home connection. The server's own 30-second limits are for
// ordinary requests.
const backupFileTime = 2 * time.Hour

func registerBackupFileHandlers(mux *http.ServeMux, authn *auth.Authenticator, backups *backupschedule.Service) {
	mux.Handle("PUT /api/v1/backup-restore/file", apihttp.NoStore(authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if !requireScope(w, r, "backups:write") {
			return
		}
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/octet-stream" {
			apihttp.WriteProblem(w, http.StatusUnsupportedMediaType, "content_type_invalid", "Send the backup file as application/octet-stream.")
			return
		}
		if r.ContentLength > backupschedule.MaxUploadSize {
			apihttp.WriteProblem(w, http.StatusRequestEntityTooLarge, "upload_too_large", "That file is larger than a Linx backup file can be (about 2 GB).")
			return
		}
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(backupFileTime))
		body := http.MaxBytesReader(w, r.Body, backupschedule.MaxUploadSize+1)
		id, n, err := backups.AcceptUpload(r.Context(), body)
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) || errors.Is(err, backupschedule.ErrTooLarge) {
			apihttp.WriteProblem(w, http.StatusRequestEntityTooLarge, "upload_too_large", "That file is larger than a Linx backup file can be (about 2 GB).")
			return
		}
		if err != nil {
			writeAccountError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"upload_id": id.String(), "size": n})
	}))))

	mux.Handle("GET /api/v1/backup-download/file", apihttp.NoStore(authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireScope(w, r, "backups:write") {
			return
		}
		f, d, err := backups.OpenDownload(r.Context())
		if err != nil {
			writeAccountError(w, err)
			return
		}
		defer f.Close()
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(backupFileTime))
		made := d.UpdatedAt
		if d.SnapshotTime != nil {
			made = *d.SnapshotTime
		}
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
			map[string]string{"filename": "linx-backup-" + made.UTC().Format("2006-01-02-1504") + ".tar"}))
		http.ServeContent(w, r, "", d.UpdatedAt, f)
	}))))
}

// requireScope answers 403 unless the caller holds scope (a pending
// sign-in holds none).
func requireScope(w http.ResponseWriter, r *http.Request, scope string) bool {
	p, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		apihttp.WriteProblem(w, http.StatusUnauthorized, "unauthenticated", "Sign in first.")
		return false
	}
	if !p.Has(scope) {
		apihttp.WriteProblem(w, http.StatusForbidden, "scope_missing", "You don't have permission to do this ("+scope+").")
		return false
	}
	return true
}
