package main

import (
	"context"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/callhistory"
)

// callsCSVTime is how long a call history download may take to send (up
// to callhistory.CSVMax calls on a slow link).
const callsCSVTime = 10 * time.Minute

// registerCallHandlers serves Admin → Calls' Download (CSV) (ADR-070,
// docs/ui/SCREENS_PHASE1F.md §13.2): GET /calls' filters, written as it's
// read rather than held in memory. Hand-written (excluded from code
// generation): the body is CSV. Times are in the server's time zone.
func registerCallHandlers(mux *http.ServeMux, authn *auth.Authenticator, svc *callhistory.Service,
	timeZone func(context.Context) (string, error), log *slog.Logger) {
	mux.Handle("GET /api/v1/calls/csv", apihttp.NoStore(authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireScope(w, r, "calls:read") {
			return
		}
		p, _ := auth.PrincipalFromContext(r.Context())
		q := r.URL.Query()
		f := callhistory.Filter{TenantID: p.TenantID, Number: q.Get("number")}
		if len(f.Number) > 40 {
			apihttp.WriteProblem(w, http.StatusBadRequest, "number_invalid", "A number is at most 40 characters.")
			return
		}
		if v := q.Get("extension_id"); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				apihttp.WriteProblem(w, http.StatusBadRequest, "extension_id_invalid", "extension_id must be an extension's id.")
				return
			}
			f.Party = &id
		}
		if v := q.Get("missed"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				apihttp.WriteProblem(w, http.StatusBadRequest, "missed_invalid", "missed must be true or false.")
				return
			}
			f.Missed = b
		}
		for _, x := range []struct {
			name string
			to   **time.Time
		}{{"from", &f.From}, {"to", &f.To}} {
			if v := q.Get(x.name); v != "" {
				t, err := time.Parse(time.RFC3339, v)
				if err != nil {
					apihttp.WriteProblem(w, http.StatusBadRequest, x.name+"_invalid", x.name+" must be a date and time (RFC 3339).")
					return
				}
				*x.to = &t
			}
		}
		loc := time.UTC
		if tz, err := timeZone(r.Context()); err == nil {
			if l, err := time.LoadLocation(tz); err == nil {
				loc = l
			}
		}
		from, to := "start", time.Now().In(loc).Format(time.DateOnly)
		if f.From != nil {
			from = f.From.In(loc).Format(time.DateOnly)
		}
		if f.To != nil {
			to = f.To.Add(-time.Second).In(loc).Format(time.DateOnly)
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(callsCSVTime))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
			map[string]string{"filename": "linx-calls-" + from + "-to-" + to + ".csv"}))
		if err := svc.WriteCSV(r.Context(), w, f, loc); err != nil && r.Context().Err() == nil {
			// The header is out: the file simply ends early.
			log.Error("writing the call history download failed", "err", err)
		}
	}))))
}
