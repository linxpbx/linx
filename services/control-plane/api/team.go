package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
)

// TeamLivePath is the Team list's websocket (hand-written, services/
// control-plane/team.go), and TeamSubprotocol the subprotocol it speaks.
const (
	TeamLivePath    = "/api/v1/team/live"
	TeamSubprotocol = "linx.team.v1"
)

// TeamListBody is GET /team's body and each message on its websocket.
func TeamListBody(rows []pbx.TeamStatus) TeamList {
	out := TeamList{Items: make([]TeamMember, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, TeamMember{Extension: r.Extension, Name: r.Name, Status: TeamMemberStatus(r.Status), Since: r.Since})
	}
	return out
}

func (s *Server) ListTeam(ctx context.Context, _ ListTeamRequestObject) (ListTeamResponseObject, error) {
	p, _ := auth.PrincipalFromContext(ctx)
	rows, err := s.team.List(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	return ListTeam200JSONResponse(TeamListBody(rows)), nil
}

func (s *Server) SetMyPresence(ctx context.Context, req SetMyPresenceRequestObject) (SetMyPresenceResponseObject, error) {
	fail := func(e *apihttp.Error) (SetMyPresenceResponseObject, error) {
		return SetMyPresencedefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	p, uid, perr := signedInPerson(ctx)
	if perr != nil {
		return fail(perr)
	}
	err := s.team.SetPresence(ctx, p.TenantID, uid, string(req.Body.Presence))
	switch {
	case errors.Is(err, pbx.ErrPresenceInvalid):
		return fail(&apihttp.Error{Status: http.StatusBadRequest, Code: "presence_invalid", Detail: "Choose available, away or dnd."})
	case errors.Is(err, pbx.ErrNotFound):
		return fail(errNotSignedInPerson)
	case err != nil:
		return nil, err
	}
	return SetMyPresence204Response{}, nil
}

// meExtras fills in a signed-in person's extension number and status for
// GET /me.
func (s *Server) meExtras(ctx context.Context, me *Principal, extension *uuid.UUID, presence string) {
	if presence != "" {
		pr := Presence(presence)
		me.Presence = &pr
	}
	if extension == nil || s.pbx == nil {
		return
	}
	if e, err := s.pbx.GetExtension(ctx, *extension); err == nil && e.DeletedAt == nil {
		me.Extension = &e.Number
	}
}
