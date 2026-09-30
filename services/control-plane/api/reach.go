package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/reach"
)

// "Check it" (docs/SIMPLER.md §2.3): the checks from this server, and the
// one-time phone links for the outside half (internal/reach).

// reachWait is how long GET /system/reach-links/{id}?after= holds a
// request open before answering unchanged (under nginx's 60 s default).
const reachWait = 25 * time.Second

// SetReach connects Check it. Without it, its endpoints answer 503.
func (s *Server) SetReach(check func(context.Context) []reach.Line, links *reach.Links) {
	s.reachCheck, s.reachLinks = check, links
	// Tries on the phone's page, per address: enough for a person, not for
	// guessing a code.
	s.reachLimit = auth.NewLimiters(10, 5)
}

var (
	errReachOff = &apihttp.Error{Status: http.StatusServiceUnavailable, Code: "reach_unavailable",
		Detail: "Check it isn't available on this server."}
	errReachInvalid = &apihttp.Error{Status: http.StatusNotFound, Code: "reach_link_invalid",
		Detail: "This link can't be used. Links work once, for 10 minutes: make a new one with Check it."}
	errReachTooMany = &apihttp.Error{Status: http.StatusTooManyRequests, Code: "too_many_requests",
		Detail: "Too many tries. Wait a minute."}
)

// recast copies between two types with the same JSON shape (the generated
// types' nested structs).
func recast(in, out any) {
	b, _ := json.Marshal(in)
	_ = json.Unmarshal(b, out)
}

func (s *Server) CheckReach(ctx context.Context, _ CheckReachRequestObject) (CheckReachResponseObject, error) {
	if s.reachCheck == nil {
		return CheckReachdefaultApplicationProblemPlusJSONResponse{StatusCode: errReachOff.Status, Body: problem(errReachOff)}, nil
	}
	var out ReachCheck
	recast(struct {
		Lines []reach.Line `json:"lines"`
	}{s.reachCheck(ctx)}, &out)
	out.CheckedAt = time.Now().UTC().Truncate(time.Second)
	return CheckReach200JSONResponse(out), nil
}

// SetDNSRecords connects the DNS records by hand. Without it, its endpoint
// answers 503.
func (s *Server) SetDNSRecords(records func(context.Context) reach.Records) { s.dnsRecords = records }

func (s *Server) GetDnsRecords(ctx context.Context, _ GetDnsRecordsRequestObject) (GetDnsRecordsResponseObject, error) {
	if s.dnsRecords == nil {
		return GetDnsRecordsdefaultApplicationProblemPlusJSONResponse{StatusCode: errReachOff.Status, Body: problem(errReachOff)}, nil
	}
	var out DnsRecords
	recast(s.dnsRecords(ctx), &out)
	return GetDnsRecords200JSONResponse(out), nil
}

func (s *Server) reachLinkOut(k reach.Link) ReachLink {
	var out ReachLink
	recast(k, &out)
	out.Url = s.reachLinks.URL(k)
	return out
}

func (s *Server) CreateReachLink(ctx context.Context, _ CreateReachLinkRequestObject) (CreateReachLinkResponseObject, error) {
	if s.reachLinks == nil {
		return CreateReachLinkdefaultApplicationProblemPlusJSONResponse{StatusCode: errReachOff.Status, Body: problem(errReachOff)}, nil
	}
	return CreateReachLink201JSONResponse(s.reachLinkOut(s.reachLinks.New())), nil
}

func (s *Server) GetReachLink(ctx context.Context, req GetReachLinkRequestObject) (GetReachLinkResponseObject, error) {
	if s.reachLinks == nil {
		return GetReachLinkdefaultApplicationProblemPlusJSONResponse{StatusCode: errReachOff.Status, Body: problem(errReachOff)}, nil
	}
	seen, wait := -1, time.Duration(0)
	if req.Params.After != nil {
		seen, wait = *req.Params.After, reachWait
	}
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	k, ok := s.reachLinks.Wait(wctx, req.Id, seen)
	if !ok {
		e := &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "That link is gone: make a new one."}
		return GetReachLinkdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return GetReachLink200JSONResponse(s.reachLinkOut(k)), nil
}

// reachAllowed takes one try for the caller's address.
func (s *Server) reachAllowed(ctx context.Context) bool {
	return s.reachLimit.Allow(auth.IPKey(auth.ClientIPFromContext(ctx)), time.Now())
}

func (s *Server) ClaimReachLink(ctx context.Context, req ClaimReachLinkRequestObject) (ClaimReachLinkResponseObject, error) {
	if s.reachLinks == nil {
		return ClaimReachLinkdefaultApplicationProblemPlusJSONResponse{StatusCode: errReachOff.Status, Body: problem(errReachOff)}, nil
	}
	if !s.reachAllowed(ctx) {
		return ClaimReachLinkdefaultApplicationProblemPlusJSONResponse{StatusCode: errReachTooMany.Status, Body: problem(errReachTooMany)}, nil
	}
	c, err := s.reachLinks.Claim(ctx, req.Code, auth.ClientIPFromContext(ctx))
	if errors.Is(err, reach.ErrNoLink) {
		return ClaimReachLinkdefaultApplicationProblemPlusJSONResponse{StatusCode: errReachInvalid.Status, Body: problem(errReachInvalid)}, nil
	} else if err != nil {
		return nil, err
	}
	out := ReachClaim{Domain: c.Domain, Address: c.Address.String()}
	out.Turn.Urls, out.Turn.Username, out.Turn.Credential = c.TURN.URLs, c.TURN.Username, c.TURN.Password
	return ClaimReachLink200JSONResponse(out), nil
}

func (s *Server) ReportReachRelay(ctx context.Context, req ReportReachRelayRequestObject) (ReportReachRelayResponseObject, error) {
	if s.reachLinks == nil {
		return ReportReachRelaydefaultApplicationProblemPlusJSONResponse{StatusCode: errReachOff.Status, Body: problem(errReachOff)}, nil
	}
	if !s.reachAllowed(ctx) {
		return ReportReachRelaydefaultApplicationProblemPlusJSONResponse{StatusCode: errReachTooMany.Status, Body: problem(errReachTooMany)}, nil
	}
	detail := ""
	if req.Body.Detail != nil {
		detail = *req.Body.Detail
	}
	if err := s.reachLinks.ReportRelay(req.Code, req.Body.Ok, detail); err != nil {
		return ReportReachRelaydefaultApplicationProblemPlusJSONResponse{StatusCode: errReachInvalid.Status, Body: problem(errReachInvalid)}, nil
	}
	return ReportReachRelay204Response{}, nil
}
