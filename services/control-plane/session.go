package main

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

// registerSessionHandlers adds the sign-in flow (docs/WEB.md §4) to mux:
// endpoints that set or clear the session and CSRF cookies, which a typed
// JSON response from the generated server can't do, so they're hand-written
// (api/oapi-codegen-config.yaml excludes their operation ids from
// generation; api/openapi.yaml documents them). Registered directly on the
// top mux, next to "/api/v1/" (the generated handler): Go's ServeMux
// prefers the more specific method+path pattern over that subtree wildcard.
func registerSessionHandlers(mux *http.ServeMux, authn *auth.Authenticator, accounts *auth.Accounts, tenant uuid.UUID) {
	mux.Handle("POST /api/v1/session", apihttp.NoStore(apihttp.LimitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body signInBody
		if !decodeJSON(w, r, &body) {
			return
		}
		out, err := accounts.SignIn(r.Context(), tenant, body.Email, body.Password, authn.IPs.ClientIP(r), r.UserAgent())
		if err != nil {
			writeAccountError(w, err)
			return
		}
		setSessionCookies(w, out)
		writeJSON(w, http.StatusOK, statusBody(out))
	}))))

	mux.Handle("GET /api/v1/setup-links/{token}", apihttp.NoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := accounts.CheckSetupLink(r.Context(), r.PathValue("token"), authn.IPs.ClientIP(r))
		if err != nil {
			writeAccountError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, setupLinkInfoBody{Email: u.Email, Name: u.Name, Role: u.Role,
			HasSecondStep: u.HasSecondStep(), Passkeys: accounts.WebAuthn != nil})
	})))

	mux.Handle("POST /api/v1/setup-links/{token}", apihttp.NoStore(apihttp.LimitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body setupLinkBody
		if !decodeJSON(w, r, &body) {
			return
		}
		out, err := accounts.CompleteSetup(r.Context(), r.PathValue("token"), body.Password, body.PasswordOnly, authn.IPs.ClientIP(r), r.UserAgent())
		if err != nil {
			writeAccountError(w, err)
			return
		}
		setSessionCookies(w, out)
		writeJSON(w, http.StatusOK, statusBody(out))
	}))))

	// These two act on the caller's own existing session, so they go
	// through the normal cookie-authentication and CSRF checks first.
	mux.Handle("POST /api/v1/session/mfa", apihttp.NoStore(apihttp.LimitBody(authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body mfaCodeBody
		if !decodeJSON(w, r, &body) {
			return
		}
		if err := accounts.VerifyMFA(r.Context(), body.Code); err != nil {
			writeAccountError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sessionStatusBody{Status: "signed_in"})
	})))))

	mux.Handle("DELETE /api/v1/session", apihttp.NoStore(authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := accounts.SignOut(r.Context()); err != nil {
			writeAccountError(w, err)
			return
		}
		clearSessionCookies(w)
		w.WriteHeader(http.StatusNoContent)
	}))))

	// "Confirm it's you" (docs/ADMIN.md §7): doesn't touch the session or
	// CSRF cookies, so it could be a generated handler, but it's kept next
	// to the rest of the sign-in state machine for the same reason
	// verifySessionMfa is (excluded from code generation).
	mux.Handle("POST /api/v1/session/confirm", apihttp.NoStore(apihttp.LimitBody(authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body confirmSessionBody
		if !decodeJSON(w, r, &body) {
			return
		}
		if err := accounts.Confirm(r.Context(), body.Password, body.Code); err != nil {
			writeAccountError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sessionStatusBody{Status: "signed_in"})
	})))))

	registerPasskeyHandlers(mux, authn, accounts, tenant)
}

type confirmSessionBody struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

type signInBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type setupLinkBody struct {
	Password     string `json:"password"`
	PasswordOnly bool   `json:"password_only"`
}

type setupLinkInfoBody struct {
	Email         string `json:"email"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	HasSecondStep bool   `json:"has_second_step"`
	Passkeys      bool   `json:"passkeys_available"`
}

type mfaCodeBody struct {
	Code string `json:"code"`
}

type sessionStatusBody struct {
	Status        string   `json:"status"`
	Methods       []string `json:"methods,omitempty"`
	RecoveryCodes []string `json:"recovery_codes,omitempty"`
}

func statusBody(out auth.SessionOutcome) sessionStatusBody {
	return sessionStatusBody{Status: out.Status, Methods: out.Methods, RecoveryCodes: out.RecoveryCodes}
}

// setSessionCookies sets the __Host- session and CSRF cookies (docs/WEB.md
// §4). __Host- requires Secure, Path=/ and no Domain attribute, which is
// exactly what a same-origin cookie needs here.
func setSessionCookies(w http.ResponseWriter, out auth.SessionOutcome) {
	maxAge := int(time.Until(out.Session.ExpiresAt).Seconds())
	http.SetCookie(w, &http.Cookie{
		Name: auth.SessionCookieName, Value: out.Token, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: auth.CSRFCookieName, Value: out.CSRF, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: false, SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookieName, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: auth.CSRFCookieName, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: false, SameSite: http.SameSiteStrictMode})
}

// decodeJSON reads a JSON body. Only application/json is taken: a page on
// another site can post a form or text/plain cross-site without asking,
// but not JSON, so this is what stops another site signing a visitor's
// browser in to an account of its choosing (sign-in and setup links have no
// session yet, so no CSRF token to check).
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		apihttp.WriteProblem(w, http.StatusUnsupportedMediaType, "content_type_invalid", "Send the request body as application/json.")
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, "request_invalid", "That request body isn't valid JSON.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAccountError(w http.ResponseWriter, err error) {
	var e *apihttp.Error
	if errors.As(err, &e) {
		apihttp.WriteError(w, e)
		return
	}
	apihttp.WriteProblem(w, http.StatusInternalServerError, "internal", "Something went wrong. Please try again.")
}
