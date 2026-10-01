package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/install"
)

// ServerSettingsSource is setup on the server in settings mode, as the
// control plane's install bridge relays it (install.Server; docs/INSTALL.md
// §7).
type ServerSettingsSource interface {
	ServerSettings() *install.ServerView
	PreviewServerSettings(ctx context.Context, c install.ServerChange) (install.ServerPreview, error)
	ChangeServerSettings(ctx context.Context, c install.ServerChange) ([]install.FieldError, error)
}

// SetServerSettings connects System → Server settings.
func (s *Server) SetServerSettings(src ServerSettingsSource) { s.serverSettings = src }

var errSystemAdminOnly = &apihttp.Error{Status: http.StatusForbidden, Code: "system_admin_only",
	Detail: "Only a system admin, signed in with a browser, can see or change this server's own settings."}

// systemAdminSession is the only caller the Server settings page is for: a
// person's session (API keys never), a system admin's.
func systemAdminSession(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return p, errNoPrincipal
	}
	if _, session := auth.SessionFromContext(ctx); !session || p.Role != auth.RoleSystemAdmin {
		return p, errSystemAdminOnly
	}
	return p, nil
}

func (s *Server) GetServerSettings(ctx context.Context, _ GetServerSettingsRequestObject) (GetServerSettingsResponseObject, error) {
	if _, err := systemAdminSession(ctx); err != nil {
		e, _ := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetServerSettingsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := ServerSettings{}
	if s.serverSettings == nil {
		return GetServerSettings200JSONResponse(out), nil
	}
	v := s.serverSettings.ServerSettings()
	if v == nil {
		return GetServerSettings200JSONResponse(out), nil
	}
	view, err := serverSettingsView(*v)
	if err != nil {
		return nil, err
	}
	out.Open, out.Settings = true, &view
	return GetServerSettings200JSONResponse(out), nil
}

// serverSettingsView is the host's view in the API's shape.
func serverSettingsView(v install.ServerView) (ServerSettingsView, error) {
	type item struct {
		Title  string `json:"title"`
		State  string `json:"state"`
		Detail string `json:"detail,omitempty"`
	}
	steps := make([]item, 0, len(v.Steps))
	for _, st := range v.Steps {
		steps = append(steps, item{Title: st.Title, State: st.State, Detail: st.Detail})
	}
	keep := v.Keep
	if keep == nil {
		keep = []install.KeepItem{}
	}
	profiles := v.Profiles
	if profiles == nil {
		profiles = []install.ProfileOption{}
	}
	doors := v.FrontDoors
	if doors == nil {
		doors = []string{}
	}
	b, err := json.Marshal(map[string]any{
		"where": v.Where, "front_door": v.FrontDoor, "proxy_address": v.ProxyAddress, "turn_udp_port": v.TURNUDPPort, "front_doors": doors,
		"public_address": v.PublicAddress, "lan_address": v.LANAddress, "problem": v.Problem, "repair": v.Repair, "no_sign_in": v.NoSignIn,
		"domain": v.Domain, "provider": v.Provider, "token_saved": v.Token != "", "dns_by_hand": v.DNSByHand,
		"profile": v.Profile, "profiles": profiles, "profile_pick": v.ProfilePick, "profile_reason": v.ProfileReason,
		"portainer": v.Portainer, "portainer_allowed": v.PortainerAllowed,
		"apply": map[string]string{"state": v.Apply.State, "detail": v.Apply.Detail}, "steps": steps, "keep": keep,
		"expires_at": v.ExpiresAt,
	})
	if err != nil {
		return ServerSettingsView{}, err
	}
	var out ServerSettingsView
	return out, json.Unmarshal(b, &out)
}

// serverChange is the request body as setup on the server takes it.
func serverChange(b ServerSettingsChange) install.ServerChange {
	c := install.ServerChange{Profile: string(b.Profile), Portainer: b.Portainer}
	if b.Token != nil {
		c.Token = *b.Token
	}
	if b.DnsKey != nil {
		k := &install.DNSKey{Provider: string(b.DnsKey.Provider)}
		if b.DnsKey.Token != nil {
			k.Token = *b.DnsKey.Token
		}
		if b.DnsKey.Key != nil {
			k.Fields = *b.DnsKey.Key
		}
		c.Key = k
	}
	c.DNSByHand = b.DnsByHand
	if b.Domain != nil {
		c.Domain = *b.Domain
	}
	if b.FrontDoor != nil {
		c.FrontDoor = string(*b.FrontDoor)
	}
	if b.ProxyAddress != nil {
		c.ProxyAddress = *b.ProxyAddress
	}
	if b.TurnUdpPort != nil {
		c.TURNUDPPort = *b.TurnUdpPort
	}
	if b.DoorDone != nil {
		c.DoorDone = *b.DoorDone
	}
	return c
}

var errServerSettingsClosed = &apihttp.Error{Status: http.StatusConflict, Code: "server_settings_closed",
	Detail: "These settings can be changed only while setup has the page open. Run  sudo linx setup  on the server, then try again."}

// serverSettingsError is setup's refusal (or absence) as the API says it.
func serverSettingsError(err error) *apihttp.Error {
	var refused *install.Refused
	switch {
	case errors.As(err, &refused):
		return &apihttp.Error{Status: http.StatusConflict, Code: "server_settings_refused", Detail: refused.Detail}
	case errors.Is(err, install.ErrNotConnected):
		return &apihttp.Error{Status: http.StatusConflict, Code: "server_settings_closed",
			Detail: "Setup on the server isn't connected. Run  sudo linx setup  on the server, then try again."}
	}
	return nil
}

func (s *Server) PreviewServerSettings(ctx context.Context, req PreviewServerSettingsRequestObject) (PreviewServerSettingsResponseObject, error) {
	fail := func(e *apihttp.Error) (PreviewServerSettingsResponseObject, error) {
		return PreviewServerSettingsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	if _, err := systemAdminSession(ctx); err != nil {
		e, _ := apiError(err)
		if e == nil {
			return nil, err
		}
		return fail(e)
	}
	out, err := previewServerSettings(ctx, s.serverSettings, serverChange(*req.Body))
	if e, ok := err.(*apihttp.Error); ok {
		return fail(e)
	}
	if err != nil {
		return nil, err
	}
	return PreviewServerSettings200JSONResponse(out), nil
}

// previewServerSettings asks setup on the server what c needs.
func previewServerSettings(ctx context.Context, src ServerSettingsSource, c install.ServerChange) (ServerSettingsPreview, error) {
	if src == nil || src.ServerSettings() == nil {
		return ServerSettingsPreview{}, errServerSettingsClosed
	}
	p, err := src.PreviewServerSettings(ctx, c)
	if e := serverSettingsError(err); e != nil {
		return ServerSettingsPreview{}, e
	}
	if err != nil {
		return ServerSettingsPreview{}, err
	}
	out := ServerSettingsPreview{Address: p.Address, Warnings: p.Warnings, Steps: p.Steps,
		Errors: []ServerSettingsFieldError{}, AddRecords: []struct {
			Name  string `json:"name"`
			Type  string `json:"type"`
			Value string `json:"value"`
		}{}}
	for _, e := range p.Errors {
		out.Errors = append(out.Errors, ServerSettingsFieldError{Field: e.Field, Message: e.Message})
	}
	for _, r := range p.AddRecords {
		out.AddRecords = append(out.AddRecords, struct {
			Name  string `json:"name"`
			Type  string `json:"type"`
			Value string `json:"value"`
		}{Name: r.Name, Type: r.Type, Value: r.Value})
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	if out.Steps == nil {
		out.Steps = []string{}
	}
	if p.Setup != nil {
		b, err := json.Marshal(map[string]any{"files": orEmpty(p.Setup.Files), "steps": orEmpty(p.Setup.Steps)})
		if err != nil {
			return out, err
		}
		if err := json.Unmarshal(b, &out.Setup); err != nil {
			return out, err
		}
	}
	return out, nil
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (s *Server) ChangeServerSettings(ctx context.Context, req ChangeServerSettingsRequestObject) (ChangeServerSettingsResponseObject, error) {
	fail := func(e *apihttp.Error) (ChangeServerSettingsResponseObject, error) {
		return ChangeServerSettingsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	p, err := systemAdminSession(ctx)
	if err == nil {
		err = auth.RequireConfirmed(ctx, s.now())
	}
	if err != nil {
		e, _ := apiError(err)
		if e == nil {
			return nil, err
		}
		return fail(e)
	}
	e, err := s.changeServerSettings(ctx, serverChange(*req.Body), p.Actor(), &p.TenantID)
	if err != nil {
		return nil, err
	}
	if e != nil {
		return fail(e)
	}
	return ChangeServerSettings202Response{}, nil
}

// changeServerSettings asks setup on the server to make c, audited as
// actor's.
func (s *Server) changeServerSettings(ctx context.Context, c install.ServerChange, actor string, tenant *uuid.UUID) (*apihttp.Error, error) {
	if s.serverSettings == nil || s.serverSettings.ServerSettings() == nil {
		return errServerSettingsClosed, nil
	}
	// Never the key itself: only that one was given, and for which company.
	detail := map[string]any{"profile": c.Profile, "portainer": c.Portainer, "token_replaced": c.Token != "" || c.Key != nil}
	if c.Key != nil && c.Key.Provider != "" {
		detail["dns_company"] = c.Key.Provider
	}
	if c.DNSByHand != nil {
		detail["dns_by_hand"] = *c.DNSByHand
	}
	if c.Domain != "" {
		detail["domain"] = c.Domain
	}
	if c.FrontDoor != "" {
		detail["front_door"] = c.FrontDoor
	}
	if v := s.serverSettings.ServerSettings(); v.Repair {
		detail["repair_page"] = true
	}
	entry := auth.AuditEntry{TenantID: tenant, Actor: actor, IP: auth.ClientIPFromContext(ctx),
		Action: "system.server_settings", Target: "server", Result: auth.ResultOK, Detail: detail}
	errs, err := s.serverSettings.ChangeServerSettings(ctx, c)
	if e := serverSettingsError(err); e != nil {
		s.auditServerSettings(ctx, entry, e.Detail)
		return e, nil
	}
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		s.auditServerSettings(ctx, entry, errs[0].Message)
		code := "server_settings_invalid"
		if errs[0].Field == "token" {
			code = "token_refused"
		}
		return &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: code, Detail: errs[0].Message}, nil
	}
	s.auditServerSettings(ctx, entry, "")
	return nil, nil
}

func (s *Server) auditServerSettings(ctx context.Context, e auth.AuditEntry, refused string) {
	if s.writeAudit == nil {
		return
	}
	if refused != "" {
		e.Result = auth.ResultFailed
		e.Detail["reason"] = refused
	}
	_ = s.writeAudit(context.WithoutCancel(ctx), e)
}

// RepairActor is who the audit log names for a change made through a
// repair link that skips the sign-in: whoever ran setup on the server.
const RepairActor = "setup:no-sign-in"

// RepairSettingsHandler is the Server settings page for a repair link
// that skips the sign-in (`sudo linx setup --new-link --no-sign-in`,
// docs/INSTALL.md §7): GET, POST and POST /preview under
// install.RepairSettingsAPI, the same as the API's own operations but
// without a session. Only install.Server.RepairHandler reaches it, for the
// browser holding such a link's cookie; setup on the server, as root,
// vouched for it.
func (s *Server) RepairSettingsHandler(tenant uuid.UUID) http.Handler {
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	decode := func(w http.ResponseWriter, r *http.Request) (install.ServerChange, bool) {
		var b ServerSettingsChange
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&b); err != nil {
			apihttp.WriteError(w, &apihttp.Error{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "That request isn't valid."})
			return install.ServerChange{}, false
		}
		return serverChange(b), true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+install.RepairSettingsAPI, func(w http.ResponseWriter, r *http.Request) {
		out := ServerSettings{}
		if v := s.serverSettings.ServerSettings(); v != nil {
			view, err := serverSettingsView(*v)
			if err != nil {
				apihttp.WriteError(w, &apihttp.Error{Status: http.StatusInternalServerError, Code: "internal", Detail: "Something went wrong."})
				return
			}
			out.Open, out.Settings = true, &view
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST "+install.RepairSettingsAPI+"/preview", func(w http.ResponseWriter, r *http.Request) {
		c, ok := decode(w, r)
		if !ok {
			return
		}
		out, err := previewServerSettings(r.Context(), s.serverSettings, c)
		if e, ok := err.(*apihttp.Error); ok {
			apihttp.WriteError(w, e)
			return
		}
		if err != nil {
			apihttp.WriteError(w, &apihttp.Error{Status: http.StatusInternalServerError, Code: "internal", Detail: "Something went wrong."})
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST "+install.RepairSettingsAPI, func(w http.ResponseWriter, r *http.Request) {
		c, ok := decode(w, r)
		if !ok {
			return
		}
		e, err := s.changeServerSettings(r.Context(), c, RepairActor, &tenant)
		if err != nil {
			e = &apihttp.Error{Status: http.StatusInternalServerError, Code: "internal", Detail: "Something went wrong."}
		}
		if e != nil {
			apihttp.WriteError(w, e)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	return apihttp.NoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Port 6464 is reached directly: the address is the visitor's own.
		if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
			r = r.WithContext(auth.WithClientIP(r.Context(), ap.Addr().Unmap()))
		}
		mux.ServeHTTP(w, r)
	}))
}
