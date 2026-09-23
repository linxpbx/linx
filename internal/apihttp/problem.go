// Package apihttp holds API infrastructure shared across control-plane HTTP
// handlers: error responses, pagination and request validation (docs/API.md §2).
package apihttp

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Problem is an RFC 9457 application/problem+json error body (docs/API.md §2).
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Code     string `json:"code"`
	Detail   string `json:"detail"`
	Instance string `json:"instance,omitempty"`
}

// WriteProblem writes a problem+json error body. detail must be plain
// language and never include internal detail such as a stack trace or a
// database error (docs/API.md §2).
func WriteProblem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Code:   code,
		Detail: detail,
	})
}

// ResponseErrorHandler builds a strict-handler ResponseErrorHandlerFunc
// (github.com/oapi-codegen/oapi-codegen strict server) that writes a
// problem+json 500 and logs the error. Handlers signal caller-caused
// problems as typed 4xx response objects instead of a Go error, so anything
// reaching this is unexpected.
func ResponseErrorHandler(log *slog.Logger) func(w http.ResponseWriter, r *http.Request, err error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		log.Error("api handler error", "path", r.URL.Path, "err", err)
		WriteProblem(w, http.StatusInternalServerError, "internal",
			"Something went wrong. Please try again; check the server logs if it keeps happening.")
	}
}

// Error is a caller-caused failure that becomes a problem+json response.
type Error struct {
	Status       int
	Code, Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// WriteError writes e as problem+json. A 401 also carries the
// WWW-Authenticate challenge RFC 6750 asks for.
func WriteError(w http.ResponseWriter, e *Error) {
	if e.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="linx"`)
	}
	WriteProblem(w, e.Status, e.Code, e.Detail)
}
