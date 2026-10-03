package api

import (
	"context"
	"errors"

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
	t, err := s.enroll.CreateTicket(ctx, in)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateEnrollmentdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateEnrollment201JSONResponse(NewEnrollment{
		Enrollment: toEnrollment(t.Ticket), Token: t.Token, Code: t.Code,
	}), nil
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
