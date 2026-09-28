package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/install"
)

// ServerSettingsSource is setup on the server in settings mode, as the
// control plane's install bridge relays it (install.Server; docs/INSTALL.md
// §7).
type ServerSettingsSource interface {
	ServerSettings() *install.ServerView
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
	b, err := json.Marshal(map[string]any{
		"where": v.Where, "front_door": v.FrontDoor, "domain": v.Domain, "provider": v.Provider, "token_saved": v.Token != "",
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
	if s.serverSettings == nil || s.serverSettings.ServerSettings() == nil {
		return fail(&apihttp.Error{Status: http.StatusConflict, Code: "server_settings_closed",
			Detail: "These settings can be changed only while setup has the page open. Run  sudo linx setup  on the server, then try again."})
	}
	c := install.ServerChange{Profile: string(req.Body.Profile), Portainer: req.Body.Portainer}
	if req.Body.Token != nil {
		c.Token = *req.Body.Token
	}
	entry := auth.AuditEntry{TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: "system.server_settings", Target: "server", Result: auth.ResultOK,
		Detail: map[string]any{"profile": c.Profile, "portainer": c.Portainer, "token_replaced": c.Token != ""}}
	errs, err := s.serverSettings.ChangeServerSettings(ctx, c)
	var refused *install.Refused
	switch {
	case errors.As(err, &refused):
		s.auditServerSettings(ctx, entry, refused.Detail)
		return fail(&apihttp.Error{Status: http.StatusConflict, Code: "server_settings_refused", Detail: refused.Detail})
	case errors.Is(err, install.ErrNotConnected):
		return fail(&apihttp.Error{Status: http.StatusConflict, Code: "server_settings_closed",
			Detail: "Setup on the server isn't connected. Run  sudo linx setup  on the server, then try again."})
	case err != nil:
		return nil, err
	case len(errs) > 0:
		s.auditServerSettings(ctx, entry, errs[0].Message)
		e := &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: "token_refused", Detail: errs[0].Message}
		return fail(e)
	}
	s.auditServerSettings(ctx, entry, "")
	return ChangeServerSettings202Response{}, nil
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
