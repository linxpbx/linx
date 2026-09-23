package apihttp

import (
	"context"
	"errors"
	"net/http"

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
			WriteProblem(w, opts.StatusCode, "request_invalid", err.Error())
		},
	})
}
