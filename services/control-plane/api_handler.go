package main

import (
	"context"
	"log/slog"
	"net/http"

	controlplaneapi "linxpbx.com/linx/services/control-plane/api"

	"linxpbx.com/linx/internal/apihttp"
)

// newAPIHandler builds the /api/v1 handler: request validation against
// api/openapi.yaml, then the strict server (docs/API.md §2, build-order
// step 2). It fails if the embedded spec doesn't parse or validate, since a
// broken spec means the whole API is broken.
func newAPIHandler(log *slog.Logger) (http.Handler, error) {
	spec, err := controlplaneapi.GetSpec()
	if err != nil {
		return nil, err
	}
	if err := spec.Validate(context.Background()); err != nil {
		return nil, err
	}
	// No `servers:` entries in the spec (it's deployed at whatever domain the
	// admin configured), so there's no server/host to validate requests against.
	spec.Servers = nil

	strict := controlplaneapi.NewStrictHandlerWithOptions(
		controlplaneapi.NewServer(spec),
		nil,
		controlplaneapi.StrictHTTPServerOptions{
			ResponseErrorHandlerFunc: apihttp.ResponseErrorHandler(log),
		},
	)
	handler := controlplaneapi.HandlerFromMux(strict, http.NewServeMux())
	handler = withTemporarySystemPrincipal(handler)
	handler = apihttp.Validator(spec)(handler)
	handler = apihttp.LimitBody(handler)
	return handler, nil
}

// withTemporarySystemPrincipal stands in for authentication (build-order
// step 3, docs/API.md §3): every caller is treated as an unrestricted
// system principal. Removed once real API keys, OAuth clients and sessions
// populate the principal instead.
func withTemporarySystemPrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := controlplaneapi.WithPrincipal(r.Context(), controlplaneapi.Principal{
			Id:     "system",
			Type:   controlplaneapi.System,
			Scopes: []string{"*"},
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
