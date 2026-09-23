package api

import "context"

type principalContextKey struct{}

// WithPrincipal returns a context carrying the calling principal, as set by
// authentication middleware (docs/API.md §3). Build-order step 3 adds real
// authentication; until then, main.go installs a fixed system principal.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFromContext returns the principal set by WithPrincipal. ok is
// false if no authentication middleware ran.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok
}
