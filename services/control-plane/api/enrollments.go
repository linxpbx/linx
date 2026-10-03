package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/email"
	"linxpbx.com/linx/internal/enroll"
)

// Setting up an iPhone or iPad (ADR-073, docs/PHASE2.md §4): the admin's and
// the person's side. The phone's own side is hand-written
// (services/control-plane/enroll.go), because it is answered with a
// certificate, not with an API credential.

// SetEnroll gives the server the phone setup service.
func (s *Server) SetEnroll(svc *enroll.Service) { s.enroll = svc }

var errNoEnroll = errors.New("phone setup service not set")

func toEnrollment(t enroll.Ticket) Enrollment {
	return Enrollment{
		Id: t.ID, UserId: t.UserID, PersonName: t.PersonName, Extension: t.Number,
		Name: t.DeviceName, Kind: DeviceKind(t.Kind), Delivery: EnrollmentDelivery(t.Delivery),
		CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
	}
}

func (s *Server) ListEnrollments(ctx context.Context, _ ListEnrollmentsRequestObject) (ListEnrollmentsResponseObject, error) {
	if s.enroll == nil {
		return nil, errNoEnroll
	}
	list, err := s.enroll.Tickets(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListEnrollmentsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := EnrollmentList{Items: make([]Enrollment, 0, len(list))}
	for _, t := range list {
		out.Items = append(out.Items, toEnrollment(t))
	}
	return ListEnrollments200JSONResponse(out), nil
}

func (s *Server) CreateEnrollment(ctx context.Context, req CreateEnrollmentRequestObject) (CreateEnrollmentResponseObject, error) {
	if s.enroll == nil {
		return nil, errNoEnroll
	}
	in := enroll.TicketInput{DeviceName: req.Body.Name}
	if req.Body.UserId != nil {
		in.UserID = *req.Body.UserId
	}
	if req.Body.Kind != nil {
		in.Kind = string(*req.Body.Kind)
	}
	if req.Body.Delivery != nil {
		in.Delivery = string(*req.Body.Delivery)
	}
	if req.Body.SendEmail != nil && *req.Body.SendEmail {
		in.Delivery = enroll.DeliveryEmail
	}
	t, err := s.enroll.CreateTicket(ctx, in)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateEnrollmentdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	url := s.webAddress + "/set-up-phone#" + t.Token
	out := NewEnrollment{Enrollment: toEnrollment(t.Ticket), Token: t.Token, Code: t.Code, SetupUrl: &url}
	if req.Body.SendEmail != nil && *req.Body.SendEmail {
		out.Email = s.phoneSetupEmail(ctx, t, url)
	}
	return CreateEnrollment201JSONResponse(out), nil
}

// phoneSetupEmail sends the person the link and the code, as People's
// invite does (docs/PHASE2.md §4). Not queued is said in the answer, never
// an error: the code itself was made and is on the screen.
func (s *Server) phoneSetupEmail(ctx context.Context, t enroll.NewTicket, url string) *InviteEmail {
	u, err := s.accounts.GetUser(ctx, t.UserID)
	if err != nil {
		msg := "Linx couldn't find this person's email address."
		return &InviteEmail{To: "", Error: &msg}
	}
	out := &InviteEmail{To: u.Email}
	if s.email == nil {
		msg := email.ErrOff.Detail
		out.Error = &msg
		return out
	}
	host := strings.TrimPrefix(s.webAddress, "https://")
	minutes := int(time.Until(t.ExpiresAt).Round(time.Minute).Minutes())
	if minutes < 1 {
		minutes = 1
	}
	c := email.Content{Subject: "Set up " + t.DeviceName + " on Linx",
		Text: fmt.Sprintf("Hello %s,\n\nOpen this on %s to set it up for Linx, the phone system at %s:\n%s\n\n"+
			"Or open Linx on it and type this code: %s\n\n"+
			"It works once, and only for the next %d minutes. Nobody's password is in it.\n\n"+
			"If you weren't expecting this, tell your admin.",
			u.Name, t.DeviceName, host, url, t.Code, minutes)}
	p, _ := auth.PrincipalFromContext(ctx)
	_, err = s.email.Enqueue(ctx, t.TenantID, email.KindPhoneSetup, []string{u.Email}, c,
		auth.AuditEntry{Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx), Action: "email.queue", Result: auth.ResultOK,
			Detail: map[string]any{"user": t.UserID.String(), "enrollment_id": t.ID.String()}})
	if err != nil {
		var e *apihttp.Error
		msg := "Linx couldn't queue the email. Show the code instead."
		if errors.As(err, &e) {
			msg = e.Detail
		}
		out.Error = &msg
		return out
	}
	out.Queued = true
	return out
}

func (s *Server) CancelEnrollment(ctx context.Context, req CancelEnrollmentRequestObject) (CancelEnrollmentResponseObject, error) {
	if s.enroll == nil {
		return nil, errNoEnroll
	}
	if err := s.enroll.CancelTicket(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CancelEnrollmentdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CancelEnrollment204Response{}, nil
}

func (s *Server) ListMyPhones(ctx context.Context, _ ListMyPhonesRequestObject) (ListMyPhonesResponseObject, error) {
	if s.enroll == nil {
		return nil, errNoEnroll
	}
	phones, err := s.enroll.MyPhones(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListMyPhonesdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := DeviceList{Items: make([]Device, 0, len(phones))}
	for _, d := range phones {
		list.Items = append(list.Items, toDevice(d))
	}
	return ListMyPhones200JSONResponse(list), nil
}

func (s *Server) RevokeMyPhone(ctx context.Context, req RevokeMyPhoneRequestObject) (RevokeMyPhoneResponseObject, error) {
	if s.enroll == nil {
		return nil, errNoEnroll
	}
	if err := s.enroll.RevokeMyPhone(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return RevokeMyPhonedefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return RevokeMyPhone204Response{}, nil
}
