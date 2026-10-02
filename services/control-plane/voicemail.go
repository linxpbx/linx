package main

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/voicemail"
)

// Voicemail audio both ways (ADR-069, Phase 1F step 14): a message and a
// greeting as WAV for the player, and a greeting recorded in the browser.
// Hand-written (excluded from code generation): the bodies are audio, not
// JSON. Signed-in browser sessions only, like the rest of voicemail; the
// <audio> element sends the session cookie, and the upload its CSRF
// header (authn.Middleware checks it).

func registerVoicemailHandlers(mux *http.ServeMux, authn *auth.Authenticator, svc *voicemail.Service) {
	viewer := func(w http.ResponseWriter, r *http.Request) (voicemail.Viewer, bool) {
		p, ok := auth.PrincipalFromContext(r.Context())
		if !ok || p.Type != auth.TypeUser {
			apihttp.WriteProblem(w, http.StatusBadRequest, "not_a_session", "This only works for a signed-in browser session.")
			return voicemail.Viewer{}, false
		}
		if p.Pending {
			apihttp.WriteProblem(w, http.StatusForbidden, "sign_in_unfinished", "Finish signing in first.")
			return voicemail.Viewer{}, false
		}
		id, err := uuid.Parse(p.ID)
		if err != nil {
			apihttp.WriteProblem(w, http.StatusBadRequest, "not_a_session", "This only works for a signed-in browser session.")
			return voicemail.Viewer{}, false
		}
		return voicemail.ViewerFrom(p, id), true
	}
	pathID := func(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			apihttp.WriteProblem(w, http.StatusNotFound, "not_found", "There's no such voicemail.")
		}
		return id, err == nil
	}
	serveWAV := func(w http.ResponseWriter, r *http.Request, wav []byte, name string, at time.Time) {
		w.Header().Set("Content-Type", "audio/wav")
		if name != "" {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		}
		// Private: people's voices stay out of shared caches. NoStore
		// already says no-store; ServeContent answers the player's ranges.
		http.ServeContent(w, r, "", at, bytes.NewReader(wav))
	}

	mux.Handle("GET /api/v1/voicemail/{id}/audio", apihttp.NoStore(authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, ok := viewer(w, r)
		if !ok {
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		p, _ := auth.PrincipalFromContext(r.Context())
		m, err := svc.Audio(r.Context(), v, id, auth.AuditEntry{TenantID: &p.TenantID, Actor: p.Actor(),
			IP: auth.ClientIPFromContext(r.Context()), Result: auth.ResultOK})
		if err != nil {
			writeAccountError(w, err)
			return
		}
		name := ""
		if r.URL.Query().Get("download") == "1" || r.URL.Query().Get("download") == "true" {
			name = svc.FileName(r.Context(), m)
		}
		serveWAV(w, r, voicemail.WAV(m.Audio), name, m.ReceivedAt)
	}))))

	mux.Handle("GET /api/v1/voicemail-boxes/{id}/greetings/{kind}", apihttp.NoStore(authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, ok := viewer(w, r)
		if !ok {
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		wav, err := svc.Greeting(r.Context(), v, id, r.PathValue("kind"))
		if err != nil {
			writeAccountError(w, err)
			return
		}
		serveWAV(w, r, wav, "", time.Time{})
	}))))

	mux.Handle("PUT /api/v1/voicemail-boxes/{id}/greetings/{kind}", apihttp.NoStore(authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		v, ok := viewer(w, r)
		if !ok {
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || (mt != "audio/wav" && mt != "audio/x-wav") {
			apihttp.WriteProblem(w, http.StatusUnsupportedMediaType, "content_type_invalid", "Send the greeting as audio/wav.")
			return
		}
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, voicemail.MaxGreetingUpload))
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			apihttp.WriteProblem(w, http.StatusRequestEntityTooLarge, "greeting_invalid", "A greeting can be at most 30 seconds.")
			return
		}
		if err != nil {
			apihttp.WriteProblem(w, http.StatusBadRequest, "greeting_invalid", "The recording didn't arrive whole. Try again.")
			return
		}
		p, _ := auth.PrincipalFromContext(r.Context())
		audit := auth.AuditEntry{TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(r.Context()), Result: auth.ResultOK}
		if err := svc.SetGreeting(r.Context(), v, id, r.PathValue("kind"), b, audit); err != nil {
			writeAccountError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))))
}
