package main

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

// registerPasskeyHandlers adds the passkey ceremonies (ADR-051,
// docs/ADMIN.md §5). Each is a pair: POST .../options returns what the
// browser hands to navigator.credentials and sets the short-lived
// __Host-linx_passkey cookie that binds the challenge to this browser; the
// POST without /options sends back the device's answer. Hand-written like
// the rest of the sign-in flow: they set and clear cookies. Listing,
// renaming and removing passkeys are ordinary generated handlers.
func registerPasskeyHandlers(mux *http.ServeMux, authn *auth.Authenticator, accounts *auth.Accounts, tenant uuid.UUID) {
	handle := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, apihttp.NoStore(apihttp.LimitBody(h)))
	}
	session := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, apihttp.NoStore(apihttp.LimitBody(authn.Middleware(h))))
	}

	// Sign in with a passkey: no email, a whole sign-in.
	handle("POST /api/v1/session/passkey/options", func(w http.ResponseWriter, r *http.Request) {
		if !emptyJSON(w, r) {
			return
		}
		writeCeremony(w)(accounts.BeginPasskeySignIn(r.Context(), tenant, authn.IPs.ClientIP(r)))
	})
	handle("POST /api/v1/session/passkey", func(w http.ResponseWriter, r *http.Request) {
		var body passkeyAnswerBody
		if !decodeJSON(w, r, &body) {
			return
		}
		out, err := accounts.FinishPasskeySignIn(r.Context(), tenant, ceremonyToken(r), body.Credential, authn.IPs.ClientIP(r), r.UserAgent())
		clearCeremonyCookie(w)
		if err != nil {
			writeAccountError(w, err)
			return
		}
		setSessionCookies(w, out)
		writeJSON(w, http.StatusOK, statusBody(out))
	})

	// A passkey as the second step of a password sign-in, or for "confirm
	// it's you": the same check of one of the person's own passkeys.
	check := func(confirm bool) (http.HandlerFunc, http.HandlerFunc) {
		refusePending := func(w http.ResponseWriter, r *http.Request) bool {
			if p, _ := auth.PrincipalFromContext(r.Context()); confirm && p.Pending {
				apihttp.WriteProblem(w, http.StatusForbidden, "sign_in_unfinished", "Finish signing in first.")
				return true
			}
			return false
		}
		return func(w http.ResponseWriter, r *http.Request) {
				if !emptyJSON(w, r) || refusePending(w, r) {
					return
				}
				writeCeremony(w)(accounts.BeginPasskeyCheck(r.Context()))
			}, func(w http.ResponseWriter, r *http.Request) {
				var body passkeyAnswerBody
				if !decodeJSON(w, r, &body) || refusePending(w, r) {
					return
				}
				err := accounts.FinishPasskeyCheck(r.Context(), ceremonyToken(r), body.Credential)
				clearCeremonyCookie(w)
				if err != nil {
					writeAccountError(w, err)
					return
				}
				writeJSON(w, http.StatusOK, sessionStatusBody{Status: "signed_in"})
			}
	}
	mfaOptions, mfaFinish := check(false)
	session("POST /api/v1/session/mfa/passkey/options", mfaOptions)
	session("POST /api/v1/session/mfa/passkey", mfaFinish)
	confirmOptions, confirmFinish := check(true)
	session("POST /api/v1/session/confirm/passkey/options", confirmOptions)
	session("POST /api/v1/session/confirm/passkey", confirmFinish)

	// Add a passkey to my own account.
	session("POST /api/v1/me/passkeys/options", func(w http.ResponseWriter, r *http.Request) {
		if !emptyJSON(w, r) {
			return
		}
		writeCeremony(w)(accounts.BeginPasskeyRegistration(r.Context()))
	})
	session("POST /api/v1/me/passkeys", func(w http.ResponseWriter, r *http.Request) {
		var body passkeyAnswerBody
		if !decodeJSON(w, r, &body) {
			return
		}
		p, codes, err := accounts.FinishPasskeyRegistration(r.Context(), ceremonyToken(r), body.Name, body.Credential)
		clearCeremonyCookie(w)
		if err != nil {
			writeAccountError(w, err)
			return
		}
		added := controlplaneapi.PasskeyAdded{Passkey: controlplaneapi.ToPasskey(p)}
		if len(codes) > 0 {
			added.RecoveryCodes = &codes
		}
		writeJSON(w, http.StatusCreated, added)
	})

	// The "Passkey" choice on a set-password link.
	handle("POST /api/v1/setup-links/{token}/passkey/options", func(w http.ResponseWriter, r *http.Request) {
		if !emptyJSON(w, r) {
			return
		}
		writeCeremony(w)(accounts.BeginSetupLinkPasskey(r.Context(), r.PathValue("token"), authn.IPs.ClientIP(r)))
	})
	handle("POST /api/v1/setup-links/{token}/passkey", func(w http.ResponseWriter, r *http.Request) {
		var body passkeyAnswerBody
		if !decodeJSON(w, r, &body) {
			return
		}
		out, err := accounts.FinishSetupLinkPasskey(r.Context(), r.PathValue("token"), ceremonyToken(r), body.Name,
			body.Credential, authn.IPs.ClientIP(r), r.UserAgent())
		clearCeremonyCookie(w)
		if err != nil {
			writeAccountError(w, err)
			return
		}
		setSessionCookies(w, out)
		writeJSON(w, http.StatusOK, statusBody(out))
	})

	// A passkey as the second step of an emailed password reset (ADR-067):
	// the new password comes with the answer, and changes only if it passes.
	handle("POST /api/v1/reset-links/{token}/passkey/options", func(w http.ResponseWriter, r *http.Request) {
		if !emptyJSON(w, r) {
			return
		}
		writeCeremony(w)(accounts.BeginResetPasskey(r.Context(), r.PathValue("token"), authn.IPs.ClientIP(r)))
	})
	handle("POST /api/v1/reset-links/{token}/passkey", func(w http.ResponseWriter, r *http.Request) {
		var body resetPasskeyBody
		if !decodeJSON(w, r, &body) {
			return
		}
		out, err := accounts.CompleteReset(r.Context(), r.PathValue("token"), body.Password,
			auth.ResetProof{Ceremony: ceremonyToken(r), Credential: body.Credential}, authn.IPs.ClientIP(r), r.UserAgent())
		clearCeremonyCookie(w)
		if err != nil {
			writeAccountError(w, err)
			return
		}
		setSessionCookies(w, out)
		writeJSON(w, http.StatusOK, statusBody(out))
	})
}

// resetPasskeyBody is a reset's new password and the device's answer.
type resetPasskeyBody struct {
	Password   string          `json:"password"`
	Credential json.RawMessage `json:"credential"`
}

// passkeyAnswerBody is the device's answer, as PublicKeyCredential's
// toJSON() gives it, and a name when adding a passkey.
type passkeyAnswerBody struct {
	Name       string          `json:"name,omitempty"`
	Credential json.RawMessage `json:"credential"`
}

// emptyJSON takes an options request's body: nothing, or an empty JSON
// object. Content-Type is still required to be JSON when there's a body,
// like every other sign-in endpoint.
func emptyJSON(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength == 0 {
		return true
	}
	var body struct{}
	return decodeJSON(w, r, &body)
}

func ceremonyToken(r *http.Request) string {
	c, err := r.Cookie(auth.PasskeyCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// writeCeremony sets the challenge cookie and returns the options, or the
// error.
func writeCeremony(w http.ResponseWriter) func(auth.PasskeyCeremony, error) {
	return func(c auth.PasskeyCeremony, err error) {
		if err != nil {
			writeAccountError(w, err)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: auth.PasskeyCookieName, Value: c.Token, Path: "/", MaxAge: int(auth.PasskeyCeremonyTTL.Seconds()),
			Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
		writeJSON(w, http.StatusOK, c.Options)
	}
}

func clearCeremonyCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: auth.PasskeyCookieName, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}
