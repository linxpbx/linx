package apihttp

import (
	"context"
	"net/http"

	"github.com/getkin/kin-openapi/openapi3"
	oapimw "github.com/oapi-codegen/nethttp-middleware"
)

// Validator returns middleware that checks every request against spec
// before it reaches a handler — unknown fields, missing required fields,
// wrong types, out-of-range parameters — and rejects a mismatch with
// problem+json instead of a handler ever seeing it (docs/API.md §2).
func Validator(spec *openapi3.T) func(http.Handler) http.Handler {
	return oapimw.OapiRequestValidatorWithOptions(spec, &oapimw.Options{
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, opts oapimw.ErrorHandlerOpts) {
			WriteProblem(w, opts.StatusCode, "request_invalid", err.Error())
		},
	})
}
