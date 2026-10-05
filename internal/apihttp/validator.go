package apihttp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	oapimw "github.com/oapi-codegen/nethttp-middleware"
)

// Authorize checks an operation's security requirement: the scopes the spec
// lists for it. It returns an *Error to refuse the request.
type Authorize func(r *http.Request, scopes []string) error

// Validator returns middleware that checks every request against spec
// before it reaches a handler — security requirements first (via
// authorize), then unknown fields, missing required fields, wrong types,
// out-of-range parameters — and rejects a mismatch with problem+json
// instead of a handler ever seeing it (docs/API.md §2, §3).
func Validator(spec *openapi3.T, authorize Authorize) func(http.Handler) http.Handler {
	return oapimw.OapiRequestValidatorWithOptions(spec, &oapimw.Options{
		Options: openapi3filter.Options{
			AuthenticationFunc: func(_ context.Context, in *openapi3filter.AuthenticationInput) error {
				return authorize(in.RequestValidationInput.Request, in.Scopes)
			},
		},
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, opts oapimw.ErrorHandlerOpts) {
			var e *Error
			if errors.As(err, &e) {
				WriteError(w, e)
				return
			}
			// A body over the limit (LimitBody catches a declared
			// Content-Length up front; a chunked body only trips here, as
			// the validator reads it). Say so with 413, not the 401/400 the
			// validator would otherwise infer from a read that failed during
			// its security or schema phase. The string check is the fallback
			// for when the read error is stringified rather than wrapped.
			if bodyTooLarge(err) {
				WriteProblem(w, http.StatusRequestEntityTooLarge, "request_too_large",
					"That request is larger than Linx accepts.")
				return
			}
			WriteProblem(w, opts.StatusCode, "request_invalid", err.Error())
		},
	})
}

// bodyTooLarge reports whether err is a request body that exceeded the
// cap (LimitBody's http.MaxBytesReader). The validator reads the body
// during its security and schema phases, so a chunked over-cap body first
// surfaces as this error here; it must become 413, not the 401/400 the
// validator would otherwise infer from a read that failed. The string
// check is the fallback for when the read error was stringified rather
// than wrapped (`errors.As` then can't see the type).
func bodyTooLarge(err error) bool {
	var tooBig *http.MaxBytesError
	return errors.As(err, &tooBig) || strings.Contains(err.Error(), "http: request body too large")
}
