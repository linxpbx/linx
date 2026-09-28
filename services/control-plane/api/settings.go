package api

import (
	"context"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/settings"
	"linxpbx.com/linx/internal/trunk"
)

// prefixStrings renders admin_networks the way ipStrings renders a
// credential's allowed_ips: a bare address without its /32 or /128.
func prefixStrings(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p.IsSingleIP() {
			out = append(out, p.Addr().String())
		} else {
			out = append(out, p.String())
		}
	}
	return out
}

func toNumberingRanges(rs []settings.Range) []NumberingRange {
	out := make([]NumberingRange, 0, len(rs))
	for _, r := range rs {
		out = append(out, NumberingRange{Kind: NumberingRangeKind(r.Kind), From: r.From, To: r.To})
	}
	return out
}

func fromNumberingRanges(rs []NumberingRange) []settings.Range {
	out := make([]settings.Range, 0, len(rs))
	for _, r := range rs {
		out = append(out, settings.Range{Kind: string(r.Kind), From: r.From, To: r.To})
	}
	return out
}

func toSettings(s settings.Settings) Settings {
	return Settings{
		Country: s.Country, ExtensionDigits: s.ExtensionDigits, ExtensionRanges: toNumberingRanges(s.ExtensionRanges),
		SiteKind: SettingsSiteKind(s.SiteKind), SimpleMode: s.SimpleMode,
		AdminNetworkRestricted: s.AdminNetworkRestricted, AdminNetworks: prefixStrings(s.AdminNetworks),
		DefaultCallPermissionLevelId: s.DefaultCallPermissionLevelID, SetupStep: s.SetupStep, SetupCompletedAt: s.SetupCompletedAt,
		CompanySignInRequired: &s.CompanySignInRequired,
	}
}

func (s *Server) GetSettings(ctx context.Context, _ GetSettingsRequestObject) (GetSettingsResponseObject, error) {
	cur, err := s.settings.Get(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetSettingsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return GetSettings200JSONResponse(toSettings(cur)), nil
}

func (s *Server) UpdateSettings(ctx context.Context, req UpdateSettingsRequestObject) (UpdateSettingsResponseObject, error) {
	patch := settings.Patch{
		Country: req.Body.Country, ExtensionDigits: req.Body.ExtensionDigits, SimpleMode: req.Body.SimpleMode,
		AdminNetworkRestricted: req.Body.AdminNetworkRestricted, CompanySignInRequired: req.Body.CompanySignInRequired,
	}
	if req.Body.ExtensionRanges != nil {
		r := fromNumberingRanges(*req.Body.ExtensionRanges)
		patch.ExtensionRanges = &r
	}
	if req.Body.SiteKind != nil {
		v := string(*req.Body.SiteKind)
		patch.SiteKind = &v
	}
	if req.Body.AdminNetworks != nil {
		patch.AdminNetworks = req.Body.AdminNetworks
	}
	out, err := s.settings.Update(ctx, patch)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateSettingsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return UpdateSettings200JSONResponse(toSettings(out)), nil
}

func (s *Server) NextExtensionNumber(ctx context.Context, _ NextExtensionNumberRequestObject) (NextExtensionNumberResponseObject, error) {
	n, err := s.settings.NextNumber(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return NextExtensionNumberdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return NextExtensionNumber200JSONResponse{Number: n}, nil
}

func toSetupProgress(s settings.Settings) SetupProgress {
	return SetupProgress{Step: s.SetupStep, Completed: s.SetupCompletedAt != nil, CompletedAt: s.SetupCompletedAt}
}

func (s *Server) GetSetup(ctx context.Context, _ GetSetupRequestObject) (GetSetupResponseObject, error) {
	cur, err := s.settings.Get(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetSetupdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return GetSetup200JSONResponse(toSetupProgress(cur)), nil
}

// everyoneLevelCategories is the setup wizard's step 6, "what your phones
// can call" (docs/ADMIN.md §4): local, mobiles, other cities and free
// numbers on; abroad and premium off (emergency numbers are always allowed
// regardless of any level, so they're not one of these categories).
var everyoneLevelCategories = []string{"landline", "mobile", "national", "service", "toll_free"}

func (s *Server) UpdateSetup(ctx context.Context, req UpdateSetupRequestObject) (UpdateSetupResponseObject, error) {
	complete := req.Body.Complete != nil && *req.Body.Complete
	if complete {
		cur, err := s.settings.Get(ctx)
		if err != nil {
			return nil, err
		}
		if cur.DefaultCallPermissionLevelID == nil {
			level, err := s.trunks.CreateCallPermissionLevel(ctx, trunk.CallPermissionLevelInput{
				Name: "Everyone", AllowedCategories: everyoneLevelCategories,
			})
			if err != nil {
				e, err := apiError(err)
				if e == nil {
					return nil, err
				}
				return UpdateSetupdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
			}
			if err := s.settings.SetDefaultCallPermissionLevel(ctx, level.ID); err != nil {
				return nil, err
			}
		}
	}
	var completedAt *time.Time
	if complete {
		t := s.now().UTC()
		completedAt = &t
	}
	if err := s.settings.SetSetupStep(ctx, req.Body.Step, completedAt); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateSetupdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	cur, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	return UpdateSetup200JSONResponse(toSetupProgress(cur)), nil
}

func toAuditLogEntry(e auth.AuditLogEntry) AuditLogEntry {
	out := AuditLogEntry{Id: e.ID, At: e.At, Actor: e.Actor, Action: e.Action, Result: AuditLogEntryResult(e.Result)}
	if e.IP.IsValid() {
		ip := e.IP.String()
		out.Ip = &ip
	}
	if e.Target != "" {
		out.Target = &e.Target
	}
	if e.Detail != nil {
		out.Detail = &e.Detail
	}
	return out
}

func (s *Server) ListAuditLog(ctx context.Context, req ListAuditLogRequestObject) (ListAuditLogResponseObject, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	f := auth.AuditLogFilter{
		Actor: deref(req.Params.Actor), Action: deref(req.Params.Action), Target: deref(req.Params.Target),
		Since: req.Params.Since, Until: req.Params.Until,
	}
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(e auth.AuditLogEntry) uuid.UUID { return e.ID },
		func(before *uuid.UUID, limit int) ([]auth.AuditLogEntry, error) {
			return s.audit.ListAuditLog(ctx, p.TenantID, f, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListAuditLogdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := AuditLogEntryList{Items: make([]AuditLogEntry, 0, len(items)), NextCursor: next}
	for _, e := range items {
		list.Items = append(list.Items, toAuditLogEntry(e))
	}
	return ListAuditLog200JSONResponse(list), nil
}

func (s *Server) GetSystemStatus(ctx context.Context, req GetSystemStatusRequestObject) (GetSystemStatusResponseObject, error) {
	// Answering at all means the control plane and its database work; the
	// server helper adds every other service.
	out := SystemStatus{Services: map[string]SystemStatusServices{"control-plane": "ok", "database": "ok"}}
	s.opsStatus(ctx, req.Params.Check != nil && *req.Params.Check, &out)
	alerts, err := s.alerts.Alerts(ctx, alert.StatusOpen, nil, 100)
	if err != nil {
		return nil, err
	}
	out.OpenAlerts = make([]struct {
		Message  string                         `json:"message"`
		Severity SystemStatusOpenAlertsSeverity `json:"severity"`
		Since    time.Time                      `json:"since"`
		Title    string                         `json:"title"`
	}, 0, len(alerts))
	for _, a := range alerts {
		out.OpenAlerts = append(out.OpenAlerts, struct {
			Message  string                         `json:"message"`
			Severity SystemStatusOpenAlertsSeverity `json:"severity"`
			Since    time.Time                      `json:"since"`
			Title    string                         `json:"title"`
		}{Message: a.Message, Severity: SystemStatusOpenAlertsSeverity(a.Severity), Since: a.FirstSeenAt, Title: a.Title})
	}
	trunks, err := s.trunks.ListTrunks(ctx, nil, 200)
	if err != nil {
		return nil, err
	}
	out.Trunks = make([]struct {
		Id          uuid.UUID  `json:"id"`
		Name        string     `json:"name"`
		Status      string     `json:"status"`
		StatusSince *time.Time `json:"status_since,omitempty"`
	}, 0, len(trunks))
	for _, t := range trunks {
		out.Trunks = append(out.Trunks, struct {
			Id          uuid.UUID  `json:"id"`
			Name        string     `json:"name"`
			Status      string     `json:"status"`
			StatusSince *time.Time `json:"status_since,omitempty"`
		}{Id: t.ID, Name: t.Name, Status: t.Status, StatusSince: t.StatusSince})
	}
	profiles, err := s.trunks.ListWireGuardProfiles(ctx, nil, 200)
	if err != nil {
		return nil, err
	}
	out.WireguardProfiles = make([]struct {
		Id     uuid.UUID `json:"id"`
		Name   string    `json:"name"`
		Status string    `json:"status"`
	}, 0, len(profiles))
	for _, wp := range profiles {
		out.WireguardProfiles = append(out.WireguardProfiles, struct {
			Id     uuid.UUID `json:"id"`
			Name   string    `json:"name"`
			Status string    `json:"status"`
		}{Id: wp.ID, Name: wp.Name, Status: wp.Status})
	}
	return GetSystemStatus200JSONResponse(out), nil
}
