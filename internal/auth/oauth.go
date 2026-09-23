package auth

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// TokenPath is where OAuth clients get access tokens (docs/API.md §3).
const TokenPath = "/oauth/token"

// oauthError is an RFC 6749 §5.2 error response.
type oauthError struct {
	status      int
	Code        string `json:"error"`
	Description string `json:"error_description"`
	basic       bool
}

// TokenHandler serves POST /oauth/token: the OAuth 2.0 client credentials
// grant (RFC 6749 §4.4). Clients authenticate with HTTP Basic or with
// client_id/client_secret in the form, not both. There are no refresh tokens.
func (a *Authenticator) TokenHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		now := a.Now()
		ip := a.IPs.ClientIP(r)
		ctx := WithClientIP(r.Context(), ip)

		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeOAuthError(w, &oauthError{status: http.StatusMethodNotAllowed, Code: "invalid_request",
				Description: "Use POST."})
			return
		}
		ipKey := IPKey(ip)
		if a.Failures.Exhausted(ipKey, now) {
			SetRateLimitHeaders(w.Header(), a.Failures.Status(ipKey, now))
			writeOAuthError(w, &oauthError{status: http.StatusTooManyRequests, Code: "invalid_request",
				Description: "Too many failed attempts from your address. Wait a minute and try again."})
			return
		}
		if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/x-www-form-urlencoded" {
			writeOAuthError(w, &oauthError{status: http.StatusBadRequest, Code: "invalid_request",
				Description: "Send the request as application/x-www-form-urlencoded."})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if err := r.ParseForm(); err != nil {
			writeOAuthError(w, &oauthError{status: http.StatusBadRequest, Code: "invalid_request",
				Description: "The form could not be read."})
			return
		}
		form := r.PostForm
		for k, v := range form {
			if len(v) > 1 {
				writeOAuthError(w, &oauthError{status: http.StatusBadRequest, Code: "invalid_request",
					Description: "The " + k + " parameter is repeated."})
				return
			}
		}

		clientID, secret, basic, oerr := clientCredentials(r, form)
		if oerr != nil {
			writeOAuthError(w, oerr)
			return
		}

		fail := func(f *failure, oe *oauthError) {
			a.Failures.Allow(ipKey, now)
			a.auditFailure(ctx, ip, "oauth.token_failed", f)
			oe.basic = basic
			writeOAuthError(w, oe)
		}
		invalidClient := &oauthError{status: http.StatusUnauthorized, Code: "invalid_client",
			Description: "The client id or secret is not valid."}

		rawSecret, ok := ParseClientSecret(secret)
		if !ok || !ValidPublicID(clientID) {
			fail(&failure{reason: "malformed_client"}, invalidClient)
			return
		}
		cred, err := a.Store.CredentialByPublicID(ctx, TypeOAuthClient, clientID)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				a.Log.Error("oauth client lookup failed", "err", err)
				writeOAuthError(w, &oauthError{status: http.StatusServiceUnavailable, Code: "temporarily_unavailable",
					Description: "Linx can't check credentials right now. Try again shortly."})
				return
			}
			SecretMatches(make([]byte, 32), rawSecret)
			fail(&failure{reason: "unknown_client"}, invalidClient)
			return
		}
		if f := checkCredential(&cred, rawSecret, ip, now); f != nil {
			oe := invalidClient
			if f.reason != "wrong_secret" {
				oe = &oauthError{status: f.err.Status, Code: "invalid_client", Description: f.err.Detail}
			}
			fail(f, oe)
			return
		}

		// A valid client past its call rate is throttled like an API key.
		p := cred.Principal()
		if !a.Calls.Allow(p.Actor(), now) {
			SetRateLimitHeaders(w.Header(), a.Calls.Status(p.Actor(), now))
			writeOAuthError(w, &oauthError{status: http.StatusTooManyRequests, Code: "invalid_request",
				Description: "Too many requests from this client. Wait a moment and try again."})
			return
		}

		if gt := form.Get("grant_type"); gt != "client_credentials" {
			writeOAuthError(w, &oauthError{status: http.StatusBadRequest, Code: "unsupported_grant_type",
				Description: "Only grant_type=client_credentials is supported."})
			return
		}

		scopes := p.Scopes
		if requested := strings.Fields(form.Get("scope")); len(requested) > 0 {
			for _, s := range requested {
				if !p.Has(s) {
					writeOAuthError(w, &oauthError{status: http.StatusBadRequest, Code: "invalid_scope",
						Description: "This client doesn't hold the " + s + " scope."})
					return
				}
			}
			scopes = Effective(requested, p.Role)
		}
		if len(scopes) == 0 {
			writeOAuthError(w, &oauthError{status: http.StatusBadRequest, Code: "invalid_scope",
				Description: "This client holds no scopes."})
			return
		}

		token, claims, err := a.Tokens.Issue(cred.ID, cred.TenantID, scopes, now)
		if err != nil {
			a.Log.Error("issuing access token failed", "err", err)
			writeOAuthError(w, &oauthError{status: http.StatusInternalServerError, Code: "server_error",
				Description: "Something went wrong. Please try again."})
			return
		}
		a.touch(ctx, cred, ip, now)
		a.audit(ctx, AuditEntry{
			TenantID: &cred.TenantID,
			Actor:    p.Actor(),
			IP:       ip,
			Action:   "oauth.token_issued",
			Target:   "token:" + claims.JTI,
			Result:   ResultOK,
			Detail:   map[string]any{"scopes": scopes},
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": token,
			"token_type":   "Bearer",
			"expires_in":   int(AccessTokenTTL.Seconds()),
			"scope":        strings.Join(scopes, " "),
		})
	})
}

// clientCredentials reads the client id and secret from HTTP Basic
// (form-urlencoded per RFC 6749 §2.3.1) or from the form.
func clientCredentials(r *http.Request, form url.Values) (id, secret string, basic bool, oe *oauthError) {
	inForm := form.Has("client_id") || form.Has("client_secret")
	if u, p, ok := r.BasicAuth(); ok {
		if inForm {
			return "", "", true, &oauthError{status: http.StatusBadRequest, Code: "invalid_request",
				Description: "Send the client credentials once: HTTP Basic or the form, not both."}
		}
		id, err1 := url.QueryUnescape(u)
		secret, err2 := url.QueryUnescape(p)
		if err1 != nil || err2 != nil {
			return "", "", true, &oauthError{status: http.StatusUnauthorized, Code: "invalid_client",
				Description: "The client id or secret is not valid.", basic: true}
		}
		return id, secret, true, nil
	}
	if r.Header.Get("Authorization") != "" {
		return "", "", false, &oauthError{status: http.StatusBadRequest, Code: "invalid_request",
			Description: "Authenticate with HTTP Basic or client_id/client_secret in the form."}
	}
	if !inForm {
		return "", "", false, &oauthError{status: http.StatusUnauthorized, Code: "invalid_client",
			Description: "Send client_id and client_secret, or use HTTP Basic."}
	}
	return form.Get("client_id"), form.Get("client_secret"), false, nil
}

func writeOAuthError(w http.ResponseWriter, e *oauthError) {
	if e.status == http.StatusUnauthorized && e.basic {
		w.Header().Set("WWW-Authenticate", `Basic realm="linx"`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(e)
}
