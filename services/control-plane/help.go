package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/help"
	"linxpbx.com/linx/internal/helpanswers"
)

// Help without a session: the sign-in guides, limited per address like
// sign-in (docs/HELP.md §6). A Help page asks for the list, a guide and its
// pictures, so this leaves room for reading but not for scraping.
const (
	helpPublicPerMinute = 120
	helpPublicBurst     = 60
)

// registerHelpHandlers adds Help (docs/HELP.md §3, §6): the guide list, a
// guide, search and pictures. Hand-written (api/oapi-codegen-config.yaml):
// the same addresses serve people who aren't signed in (only the public
// sign-in guides) and people who are (by role), and pictures aren't JSON.
// What a caller may read is worked out here from their session on every
// request; lib is nil when the guides couldn't be read, and Help then says
// it isn't available. answers (written answers, docs/HELP.md §4) says who
// writes answers for the guide list, and answers questions.
func registerHelpHandlers(mux *http.ServeMux, authn *auth.Authenticator, lib *help.Library, answers *helpanswers.Service, tenant uuid.UUID, log *slog.Logger) {
	public := auth.NewLimiters(helpPublicPerMinute, helpPublicBurst)
	handle := func(pattern string, h func(w http.ResponseWriter, r *http.Request, role string)) {
		mux.Handle(pattern, authn.OptionalSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := helpRole(r.Context())
			if role == "" {
				key := auth.IPKey(auth.ClientIPFromContext(r.Context()))
				now := time.Now()
				if !public.Allow(key, now) {
					auth.SetRateLimitHeaders(w.Header(), public.Status(key, now))
					apihttp.WriteProblem(w, http.StatusTooManyRequests, "rate_limited",
						"Too many requests from this address. Wait a moment and try again.")
					return
				}
			}
			if lib == nil {
				apihttp.WriteProblem(w, http.StatusServiceUnavailable, "help_unavailable",
					"Help isn't available on this server right now.")
				return
			}
			h(w, r, role)
		})))
	}
	notFound := func(w http.ResponseWriter) {
		apihttp.WriteProblem(w, http.StatusNotFound, "not_found", "There's no such page in Help.")
	}

	handle("GET /api/v1/help/guides", func(w http.ResponseWriter, r *http.Request, role string) {
		w.Header().Set("Cache-Control", "no-store")
		list := helpGuideList{Guides: []helpGuideSummary{}}
		by, err := answers.Available(r.Context(), tenant, role)
		if err != nil {
			// Help still works without them.
			log.Error("reading the help answers setting failed", "err", err)
		}
		list.AnswersBy = by
		for _, g := range lib.Guides(role) {
			list.Guides = append(list.Guides, helpGuideSummary{Name: g.Name, Title: g.Title, Section: g.Section, Screens: nonNil(g.Screens)})
		}
		writeJSON(w, http.StatusOK, list)
	})

	handle("GET /api/v1/help/guides/{name}", func(w http.ResponseWriter, r *http.Request, role string) {
		w.Header().Set("Cache-Control", "no-store")
		g, ok := lib.Guide(role, r.PathValue("name"))
		if !ok {
			notFound(w)
			return
		}
		writeJSON(w, http.StatusOK, helpGuide{Name: g.Name, Title: g.Title, Section: g.Section, Blocks: g.Blocks})
	})

	handle("GET /api/v1/help/search", func(w http.ResponseWriter, r *http.Request, role string) {
		w.Header().Set("Cache-Control", "no-store")
		q := r.URL.Query().Get("q")
		if len(q) > help.MaxQuestionLen {
			apihttp.WriteProblem(w, http.StatusBadRequest, "question_too_long", "Ask in 500 characters or fewer.")
			return
		}
		writeJSON(w, http.StatusOK, helpSearchResults{Results: lib.Search(role, q)})
	})

	handle("GET /api/v1/help/pictures/{file}", func(w http.ResponseWriter, r *http.Request, role string) {
		data, etag, ok := lib.Picture(role, r.PathValue("file"))
		if !ok {
			w.Header().Set("Cache-Control", "no-store")
			notFound(w)
			return
		}
		// Kept by this browser only, and checked again each time: a
		// 304 costs a few bytes on a slow link.
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write(data)
	})

	// Written answers: a full session only (with its CSRF token, as any
	// change), never an API key.
	mux.Handle("POST /api/v1/help/answer", apihttp.NoStore(apihttp.LimitBody(authn.Middleware(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			p, ok := auth.PrincipalFromContext(r.Context())
			uid, err := uuid.Parse(p.ID)
			if !ok || p.Type != auth.TypeUser || p.Pending || err != nil {
				apihttp.WriteProblem(w, http.StatusUnauthorized, "sign_in_required", "Sign in to get written answers.")
				return
			}
			var body struct {
				Question string `json:"question"`
			}
			if !decodeJSON(w, r, &body) {
				return
			}
			serveAnswer(w, r, answers, tenant, helpanswers.Question{Text: body.Question, Role: helpRole(r.Context()), UserID: uid}, log)
		})))))
}

// serveAnswer streams an answer as newline-delimited JSON (api/openapi.yaml
// answerHelpQuestion): a problem if it fails before anything is written.
func serveAnswer(w http.ResponseWriter, r *http.Request, answers *helpanswers.Service, tenant uuid.UUID, q helpanswers.Question, log *slog.Logger) {
	rc := http.NewResponseController(w)
	// Room for the answer's own time limit past the server's usual write
	// timeout.
	_ = rc.SetWriteDeadline(time.Now().Add(helpanswers.AnswerTimeout + 10*time.Second))
	enc := json.NewEncoder(w)
	started := false
	line := func(v any) error {
		if !started {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusOK)
			started = true
		}
		if err := enc.Encode(v); err != nil {
			return err
		}
		return rc.Flush()
	}
	a, err := answers.Ask(r.Context(), tenant, q, func(text string) error {
		return line(helpAnswerText{Text: text})
	})
	if err != nil {
		var f *helpanswers.Failure
		var ae *apihttp.Error
		switch {
		case started && errors.As(err, &f):
			_ = line(helpAnswerError{Error: f.Detail})
		case started:
			log.Error("writing a help answer failed", "err", err)
			_ = line(helpAnswerError{Error: "Something went wrong writing the answer."})
		case errors.As(err, &f):
			apihttp.WriteProblem(w, http.StatusBadGateway, "provider_failed", f.Detail)
		case errors.As(err, &ae):
			apihttp.WriteError(w, ae)
		default:
			log.Error("writing a help answer failed", "err", err)
			apihttp.WriteProblem(w, http.StatusInternalServerError, "internal", "Something went wrong writing the answer.")
		}
		return
	}
	done := helpAnswerDone{Done: true, By: a.By, Cut: a.Cut, Guides: []helpGuideRef{}}
	for _, g := range a.Guides {
		done.Guides = append(done.Guides, helpGuideRef{Name: g.Name, Title: g.Title})
	}
	_ = line(done)
}

// helpRole is who Help answers: "" for anyone not fully signed in (no
// session, one still waiting on its second step, or an API key), and an
// admin kept to a person's view by "admins only from my network" reads what
// a person reads.
func helpRole(ctx context.Context) string {
	p, ok := auth.PrincipalFromContext(ctx)
	switch {
	case !ok || p.Type != auth.TypeUser || p.Pending:
		return ""
	case p.AdminNetworkRestricted:
		return auth.RoleUser
	}
	return p.Role
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// The JSON shapes in api/openapi.yaml (HelpGuideList, HelpGuide,
// HelpSearchResults); help_test.go checks they still decode as the
// generated types.
type helpGuideSummary struct {
	Name    string   `json:"name"`
	Title   string   `json:"title"`
	Section string   `json:"section"`
	Screens []string `json:"screens"`
}

type helpGuideList struct {
	AnswersBy string             `json:"answers_by,omitempty"`
	Guides    []helpGuideSummary `json:"guides"`
}

// A written answer's lines (answerHelpQuestion).
type helpAnswerText struct {
	Text string `json:"text"`
}

type helpAnswerError struct {
	Error string `json:"error"`
}

type helpGuideRef struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

type helpAnswerDone struct {
	Done   bool           `json:"done"`
	Guides []helpGuideRef `json:"guides"`
	By     string         `json:"by"`
	Cut    bool           `json:"cut,omitempty"`
}

type helpGuide struct {
	Name    string       `json:"name"`
	Title   string       `json:"title"`
	Section string       `json:"section"`
	Blocks  []help.Block `json:"blocks"`
}

type helpSearchResults struct {
	Results []help.Result `json:"results"`
}
