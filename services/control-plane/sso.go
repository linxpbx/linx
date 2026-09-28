package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/sso"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

// registerCompanyHandlers adds company sign-in (ADR-052, docs/ADMIN.md §6):
// what the sign-in page offers, starting a flow (sign in, link my account,
// "confirm it's you") and the provider's callback. Hand-written like the
// rest of the sign-in flow: they set and clear cookies, and the callback
// answers with a redirect, not JSON.
// passkeysMoved (may be nil) says whether this server was restored from a
// backup made at another domain (docs/INSTALL.md §8).
func registerCompanyHandlers(mux *http.ServeMux, authn *auth.Authenticator, accounts *auth.Accounts, providers *sso.Service, tenant uuid.UUID,
	passkeysMoved func(context.Context) bool, log *slog.Logger) {
	mux.Handle("GET /api/v1/sign-in-options", apihttp.NoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buttons, err := providers.Buttons(r.Context(), tenant)
		if err != nil {
			writeAccountError(w, err)
			return
		}
		required, err := accounts.CompanySignInRequired(r.Context(), tenant)
		if err != nil {
			writeAccountError(w, err)
			return
		}
		out := controlplaneapi.SignInOptions{Company: controlplaneapi.ToCompanyButtons(buttons),
			CompanySignInRequired: required, PasskeysAvailable: accounts.WebAuthn != nil}
		if passkeysMoved != nil && passkeysMoved(r.Context()) {
			yes := true
			out.PasskeysMoved = &yes
		}
		writeJSON(w, http.StatusOK, out)
	})))

	start := func(purpose string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body companyStartBody
			if !decodeJSON(w, r, &body) {
				return
			}
			st, err := accounts.BeginCompany(r.Context(), tenant, body.ProviderID, purpose, authn.IPs.ClientIP(r))
			if err != nil {
				writeAccountError(w, err)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: auth.CompanyCookieName, Value: st.Cookie, Path: "/", MaxAge: int(auth.CompanyFlowTTL.Seconds()),
				// Lax: the provider sends the browser back cross-site.
				Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			writeJSON(w, http.StatusOK, controlplaneapi.CompanyRedirect{Url: st.URL})
		}
	}
	mux.Handle("POST /api/v1/session/company", apihttp.NoStore(apihttp.LimitBody(start(auth.CompanySignIn))))
	mux.Handle("POST /api/v1/session/confirm/company", apihttp.NoStore(apihttp.LimitBody(authn.Middleware(start(auth.CompanyConfirm)))))
	mux.Handle("POST /api/v1/me/sso-links", apihttp.NoStore(apihttp.LimitBody(authn.Middleware(start(auth.CompanyLink)))))

	mux.Handle("GET "+sso.CallbackPath, apihttp.NoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		cookie := ""
		if c, err := r.Cookie(auth.CompanyCookieName); err == nil {
			cookie = c.Value
		}
		http.SetCookie(w, &http.Cookie{Name: auth.CompanyCookieName, Value: "", Path: "/", MaxAge: -1,
			Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		purpose, res, err := accounts.FinishCompany(r.Context(), q.Get("state"), cookie, q.Get("code"), q.Get("error"),
			authn.IPs.ClientIP(r), r.UserAgent())
		if err == nil && res.Session != nil {
			setSessionCookies(w, *res.Session)
		}
		http.Redirect(w, r, companyRedirect(purpose, res, err, log), http.StatusSeeOther)
	})))
}

type companyStartBody struct {
	ProviderID uuid.UUID `json:"provider_id"`
}

// companyRedirect is where the web app picks up after the provider: only a
// short result or error code in the query, never anything from the
// provider.
func companyRedirect(purpose string, res auth.CompanyResult, err error, log *slog.Logger) string {
	v := url.Values{}
	if err != nil {
		code := "company_failed"
		var e *apihttp.Error
		if errors.As(err, &e) {
			code = e.Code
		} else {
			log.Error("company sign-in", "err", err)
		}
		v.Set("company_error", code)
	}
	switch purpose {
	case auth.CompanyLink:
		if err == nil {
			v.Set("company", res.Status)
		}
		return "/account?" + v.Encode()
	case auth.CompanyConfirm:
		// A small window the "Confirm it's you" dialog opened; it tells the
		// dialog and closes.
		if err == nil {
			v.Set("result", res.Status)
		}
		return "/company-done?" + v.Encode()
	}
	if err == nil {
		return "/"
	}
	return "/?" + v.Encode()
}
