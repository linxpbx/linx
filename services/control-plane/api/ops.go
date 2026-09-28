package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/ops"
)

// OpsSource is System → Status's server helper (internal/ops.Hub): the
// services' state, their logs and Restart (docs/ADMIN.md §9, ADR-056).
type OpsSource interface {
	Snapshot() ops.Snapshot
	Refresh(ctx context.Context) error
	Logs(ctx context.Context, service string, lines int) ([]ops.LogLine, error)
	Restart(ctx context.Context, service string) (string, error)
}

// SetOps connects the server helper, where restarts are audited, and the
// certificate's expiry as linx-certd last reported it (zero: none yet).
// Without it, System → Status says the helper isn't connected.
func (s *Server) SetOps(o OpsSource, audit func(context.Context, auth.AuditEntry) error, certExpiry func() time.Time) {
	s.ops, s.writeAudit, s.certExpiry = o, audit, certExpiry
}

// opsStatus fills in the services' state, the helper and the certificate.
func (s *Server) opsStatus(ctx context.Context, check bool, out *SystemStatus) {
	out.Containers = []ServiceHealth{}
	if s.certExpiry != nil {
		if exp := s.certExpiry(); !exp.IsZero() {
			out.Certificate = &struct {
				ExpiresAt time.Time `json:"expires_at"`
			}{ExpiresAt: exp.UTC()}
		}
	}
	if s.ops == nil {
		return
	}
	if check {
		// Best effort: an old state is still worth showing.
		_ = s.ops.Refresh(ctx)
	}
	snap := s.ops.Snapshot()
	out.Helper.Connected = snap.Connected
	if !snap.CheckedAt.IsZero() {
		t := snap.CheckedAt.UTC()
		out.Helper.CheckedAt = &t
	}
	for _, c := range snap.Containers {
		svc, ok := ops.Lookup(c.Service)
		if !ok {
			continue
		}
		h := ServiceHealth{Service: svc.Name, Label: svc.Label, State: ServiceHealthState(c.Summary()),
			Restarts: c.Restarts, CanRestart: svc.Restart, Optional: svc.Optional}
		if !c.StartedAt.IsZero() {
			t := c.StartedAt.UTC()
			h.StartedAt = &t
		}
		out.Containers = append(out.Containers, h)
		switch {
		case svc.Name == "postgres":
			// "database" is already ok: this answer came from it.
		case h.State == ServiceHealthStateRunning:
			out.Services[svc.Name] = SystemStatusServicesOk
		case h.State == ServiceHealthStateMissing && svc.Optional:
		case h.State == ServiceHealthStateStopped || h.State == ServiceHealthStateMissing:
			out.Services[svc.Name] = SystemStatusServicesDown
		default:
			out.Services[svc.Name] = SystemStatusServicesDegraded
		}
	}
}

func opsProblem(err error) (*apihttp.Error, bool) {
	switch {
	case errors.Is(err, ops.ErrNotConnected):
		return &apihttp.Error{Status: http.StatusServiceUnavailable, Code: "helper_unavailable",
			Detail: "The server helper isn't running, so this can't be done from the browser. On the server, run: sudo linx setup"}, true
	case errors.Is(err, ops.ErrUnknownService):
		return &apihttp.Error{Status: http.StatusNotFound, Code: "unknown_service", Detail: "There's no Linx service with that name."}, true
	case errors.Is(err, ops.ErrNotAllowed):
		return &apihttp.Error{Status: http.StatusConflict, Code: "not_restartable",
			Detail: "This service can't be restarted from the browser. On the server, run: sudo linx doctor"}, true
	case errors.Is(err, ops.ErrTooSoon):
		return &apihttp.Error{Status: http.StatusTooManyRequests, Code: "restarted_recently",
			Detail: "It was restarted moments ago. Wait half a minute before trying again."}, true
	case errors.Is(err, ops.ErrBusy):
		return &apihttp.Error{Status: http.StatusServiceUnavailable, Code: "helper_busy",
			Detail: "The server helper is busy. Try again in a moment."}, true
	case errors.Is(err, context.Canceled):
		return nil, false
	}
	return &apihttp.Error{Status: http.StatusBadGateway, Code: "helper_failed", Detail: "The server helper couldn't do it: " + err.Error()}, true
}

func (s *Server) GetServiceLog(ctx context.Context, req GetServiceLogRequestObject) (GetServiceLogResponseObject, error) {
	if s.ops == nil {
		e, _ := opsProblem(ops.ErrNotConnected)
		return GetServiceLogdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	n := 0
	if req.Params.Lines != nil {
		n = *req.Params.Lines
	}
	lines, err := s.ops.Logs(ctx, req.Service, n)
	if err != nil {
		e, ok := opsProblem(err)
		if !ok {
			return nil, err
		}
		return GetServiceLogdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := ServiceLog{Service: req.Service, Lines: make([]struct {
		Text string     `json:"text"`
		Time *time.Time `json:"time,omitempty"`
	}, 0, len(lines))}
	for _, l := range lines {
		line := struct {
			Text string     `json:"text"`
			Time *time.Time `json:"time,omitempty"`
		}{Text: l.Text}
		if !l.Time.IsZero() {
			t := l.Time
			line.Time = &t
		}
		out.Lines = append(out.Lines, line)
	}
	return GetServiceLog200JSONResponse(out), nil
}

func (s *Server) RestartService(ctx context.Context, req RestartServiceRequestObject) (RestartServiceResponseObject, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	fail := func(err error) (RestartServiceResponseObject, error) {
		e, ok := opsProblem(err)
		if !ok {
			return nil, err
		}
		return RestartServicedefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	if s.ops == nil {
		return fail(ops.ErrNotConnected)
	}
	entry := auth.AuditEntry{TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: "system.service_restart", Target: "service:" + req.Service, Result: auth.ResultOK}
	if _, known := ops.Lookup(req.Service); !known {
		return fail(ops.ErrUnknownService)
	}
	// Written before asking: restarting the control plane ends this
	// request, and a restart that happened must be in the log.
	if s.writeAudit != nil {
		if err := s.writeAudit(ctx, entry); err != nil {
			return nil, err
		}
	}
	result, err := s.ops.Restart(ctx, req.Service)
	if err != nil {
		if s.writeAudit != nil {
			entry.Result, entry.Detail = auth.ResultFailed, map[string]any{"reason": err.Error()}
			_ = s.writeAudit(context.WithoutCancel(ctx), entry)
		}
		return fail(err)
	}
	return RestartService200JSONResponse(ServiceRestart{Service: req.Service, Result: ServiceRestartResult(result)}), nil
}
