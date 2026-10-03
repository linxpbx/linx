package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/enroll"
	"linxpbx.com/linx/internal/stepca"
	"linxpbx.com/linx/internal/store"
)

// The phone's own side of setting itself up (ADR-073, docs/PHASE2.md §4).
// Hand-written, and outside /api/v1, because these two are the only places
// where the caller is a phone proving itself with a certificate instead of
// an API credential or a browser session:
//
//	POST /v1/enroll        a setup code and a certificate request → a certificate
//	POST /v1/device-token  a certificate and a signed proof → a 15-minute token
//
// Both are open to the internet, so both are rate-limited per address, take
// a small body only, and answer every kind of failure the same way.
const (
	enrollPath      = "/v1/enroll"
	deviceTokenPath = "/v1/device-token"
)

func registerEnrollHandlers(mux *http.ServeMux, ips *auth.ClientIPResolver, svc *enroll.Service, log *slog.Logger) {
	// A phone sets itself up once and asks for a token every 15 minutes at
	// most, so these are generous for a real phone and tight for a guesser.
	setup := auth.NewLimiters(10, 5)
	tokens := auth.NewLimiters(60, 20)

	withIP := func(limits *auth.Limiters, h func(http.ResponseWriter, *http.Request)) http.Handler {
		return apihttp.NoStore(apihttp.LimitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ips.ClientIP(r)
			if !limits.Allow(auth.IPKey(ip), time.Now()) {
				apihttp.WriteProblem(w, http.StatusTooManyRequests, "rate_limited", "Too many tries. Wait a moment and try again.")
				return
			}
			h(w, r.WithContext(auth.WithClientIP(r.Context(), ip)))
		})))
	}

	mux.Handle("POST "+enrollPath, withIP(setup, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token      string `json:"token"`
			Code       string `json:"code"`
			CSR        string `json:"csr"` // base64 DER
			AppVersion string `json:"app_version"`
			OSVersion  string `json:"os_version"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		csr, err := base64.StdEncoding.DecodeString(body.CSR)
		if err != nil || len(csr) == 0 {
			apihttp.WriteProblem(w, http.StatusBadRequest, "csr_invalid", "The certificate request couldn't be read.")
			return
		}
		out, err := svc.Redeem(r.Context(), enroll.RedeemRequest{
			Token: body.Token, Code: body.Code, CSR: csr,
			AppVersion: body.AppVersion, OSVersion: body.OSVersion,
		})
		if err != nil {
			writeEnrollError(w, err, log)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"device_id":      out.DeviceID,
			"device_name":    out.DeviceName,
			"person_name":    out.PersonName,
			"extension":      out.Extension,
			"certificate":    out.Certificate,
			"ca":             out.CARoot,
			"cert_not_after": out.CertNotAfter,
			"set_up_again":   out.ExpiresAt,
		})
	}))

	mux.Handle("POST "+deviceTokenPath, withIP(tokens, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Certificate string `json:"certificate"` // base64 DER
			Proof       string `json:"proof"`       // compact JWS, ES256
			CSR         string `json:"csr"`         // base64 DER, to renew
			AppVersion  string `json:"app_version"`
			OSVersion   string `json:"os_version"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		cert, err := base64.StdEncoding.DecodeString(body.Certificate)
		if err != nil || len(cert) == 0 {
			apihttp.WriteProblem(w, http.StatusUnauthorized, "device_proof_invalid", "This phone couldn't prove who it is. Set it up again.")
			return
		}
		var csr []byte
		if body.CSR != "" {
			if csr, err = base64.StdEncoding.DecodeString(body.CSR); err != nil {
				apihttp.WriteProblem(w, http.StatusBadRequest, "csr_invalid", "The certificate request couldn't be read.")
				return
			}
		}
		out, err := svc.DeviceToken(r.Context(), enroll.TokenRequest{
			Certificate: cert, Proof: body.Proof, CSR: csr,
			AppVersion: body.AppVersion, OSVersion: body.OSVersion,
		})
		if err != nil {
			writeEnrollError(w, err, log)
			return
		}
		res := map[string]any{
			"token":          out.Token,
			"expires_at":     out.ExpiresAt,
			"device_id":      out.DeviceID,
			"cert_not_after": out.CertNotAfter,
			"set_up_again":   out.SetUpAgain,
		}
		if out.Certificate != "" {
			res["certificate"] = out.Certificate
		}
		writeJSON(w, http.StatusOK, res)
	}))
}

// writeEnrollError keeps the phone's answer vague on purpose: a wrong,
// used, cancelled or unknown setup code all look the same, so nothing can
// be learned by trying. Anything unexpected is logged and comes back as a
// plain failure.
func writeEnrollError(w http.ResponseWriter, err error, log *slog.Logger) {
	var e *apihttp.Error
	if errors.As(err, &e) {
		apihttp.WriteError(w, e)
		return
	}
	log.Error("setting up a phone failed", "err", err)
	apihttp.WriteProblem(w, http.StatusServiceUnavailable, "unavailable", "Linx couldn't finish this just now. Try again in a moment.")
}

// newEnroll builds the phone setup service: the internal CA's linx-devices
// provisioner (7-day certificates, ADR-077) and the CA root the app pins.
// It returns an error when that provisioner's password or the root isn't
// there, which is how a development run outside the container looks.
func newEnroll(cfg ariConfig, st *store.Store, tokens *auth.Tokens, log *slog.Logger) (*enroll.Service, error) {
	password, err := readSecret(devicesPasswordFile(os.Getenv))
	if err != nil {
		return nil, fmt.Errorf("internal CA devices provisioner password: %w", err)
	}
	roots, err := stepca.LoadRoots(cfg.CARootFile)
	if err != nil {
		return nil, err
	}
	root, err := os.ReadFile(cfg.CARootFile)
	if err != nil {
		return nil, fmt.Errorf("internal CA root: %w", err)
	}
	return &enroll.Service{
		Store:  st,
		Tokens: tokens,
		CA:     stepca.NewClient(cfg.CAURL, stepca.DevicesProvisioner, []byte(password), roots),
		CARoot: root,
		Now:    time.Now,
		Log:    log,
	}, nil
}

func devicesPasswordFile(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv("LINX_CA_DEVICES_PASSWORD_FILE")); v != "" {
		return v
	}
	return "/run/secrets/linx_ca_devices_password"
}
