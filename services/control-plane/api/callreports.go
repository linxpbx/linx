package api

import (
	"context"
	"errors"
	"net/http"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/callreport"
)

var errNoCallReports = errors.New("call reports are not configured")

// SetCallReports gives the server somewhere to keep what phones saw of
// their calls.
func (s *Server) SetCallReports(c *callreport.Service) { s.callReports = c }

func (s *Server) SendMyCallReport(ctx context.Context, req SendMyCallReportRequestObject) (SendMyCallReportResponseObject, error) {
	fail := func(e *apihttp.Error) (SendMyCallReportResponseObject, error) {
		return SendMyCallReportdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	if s.callReports == nil {
		return nil, errNoCallReports
	}
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok || p.DeviceID == nil {
		return fail(&apihttp.Error{Status: http.StatusBadRequest, Code: "not_a_phone",
			Detail: "This only works for the Linx app on a phone that has been set up."})
	}
	err := s.callReports.Save(ctx, p.TenantID, *p.DeviceID, callreport.Report{
		CallID: req.Body.CallId, Direction: string(req.Body.Direction), Peer: req.Body.Peer,
		StartedAt: req.Body.StartedAt, EndedAt: req.Body.EndedAt, Text: req.Body.Report,
	})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return fail(e)
	}
	return SendMyCallReport204Response{}, nil
}

func (s *Server) ListDeviceCallReports(ctx context.Context, req ListDeviceCallReportsRequestObject) (ListDeviceCallReportsResponseObject, error) {
	fail := func(e *apihttp.Error) (ListDeviceCallReportsResponseObject, error) {
		return ListDeviceCallReportsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	if s.callReports == nil {
		return nil, errNoCallReports
	}
	// The device is looked up the way GET /devices/{id} does, so a phone
	// in another company, or none at all, is simply not found.
	d, err := s.pbx.GetDevice(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return fail(e)
	}
	reports, err := s.callReports.List(ctx, d.TenantID, d.ID)
	if err != nil {
		return nil, err
	}
	out := ListDeviceCallReports200JSONResponse{Items: make([]CallReport, 0, len(reports))}
	for _, r := range reports {
		out.Items = append(out.Items, CallReport{Id: r.ID, CallId: r.CallID, Direction: CallReportDirection(r.Direction),
			Peer: r.Peer, StartedAt: r.StartedAt, EndedAt: r.EndedAt, Report: r.Text, CreatedAt: r.CreatedAt})
	}
	return out, nil
}
