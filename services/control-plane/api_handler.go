package main

import (
	"context"
	"log/slog"
	"net/http"

	controlplaneapi "linxpbx.com/linx/services/control-plane/api"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/webhook"
)

// newAPIHandler builds the /api/v1 handler (docs/API.md §2, §3). Outermost
// first: body limit, authentication and rate limits, then validation
// against api/openapi.yaml (security requirements before anything else),
// then the strict server. It fails if the embedded spec doesn't parse or
// validate, since a broken spec means the whole API is broken.
func newAPIHandler(log *slog.Logger, store controlplaneapi.CredentialStore, authn *auth.Authenticator, webhooks *webhook.Service, alerts *alert.Service) (http.Handler, error) {
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
		controlplaneapi.NewServer(spec, store, webhooks, alerts),
		nil,
		controlplaneapi.StrictHTTPServerOptions{
			ResponseErrorHandlerFunc: apihttp.ResponseErrorHandler(log),
		},
	)
	handler := controlplaneapi.HandlerFromMux(strict, http.NewServeMux())
	handler = apihttp.Validator(spec, authn.Authorize)(handler)
	handler = authn.Middleware(handler)
	handler = apihttp.LimitBody(handler)
	return handler, nil
}
