// Package api implements the Linx public API (docs/API.md) as a
// StrictServerInterface over the server generated in gen.go from
// api/openapi.yaml (`make api` regenerates it).
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/trunk"
	"linxpbx.com/linx/internal/turn"
	"linxpbx.com/linx/internal/webhook"
)

// Server implements StrictServerInterface.
type Server struct {
	spec      *openapi3.T
	store     CredentialStore
	webhooks  *webhook.Service
	alerts    *alert.Service
	pbx       *pbx.Service
	trunks    *trunk.Service
	numbering NumberingSource
	calls     CallSource
	accounts  *auth.Accounts
	turn      *turn.Issuer
	team      *pbx.Team
	now       func() time.Time
}

// NumberingSource is the country Linx is set up in (internal/store's
// Store.Country), for GET /outbound-routing (docs/TRUNKS.md §5).
type NumberingSource interface {
	Country(ctx context.Context) (string, error)
}

// CallSource is the live call state /calls/active reports (the control
// plane's pbx.CallTracker).
type CallSource interface {
	ActiveCalls() []pbx.ActiveCall
	Connected() bool
}

// NewServer builds a Server. spec is served as-is at GET /openapi.json, so
// callers must pass the same document the server was validated against.
// turnIssuer makes relay credentials for browsers (nil: no relay, so
// /me/web-phone and /me/turn-credentials answer 503).
func NewServer(spec *openapi3.T, store CredentialStore, webhooks *webhook.Service, alerts *alert.Service, pbxSvc *pbx.Service, trunks *trunk.Service, numbering NumberingSource, calls CallSource, accounts *auth.Accounts, turnIssuer *turn.Issuer, team *pbx.Team) *Server {
	return &Server{spec: spec, store: store, webhooks: webhooks, alerts: alerts, pbx: pbxSvc, trunks: trunks, numbering: numbering,
		calls: calls, accounts: accounts, turn: turnIssuer, team: team, now: time.Now}
}

func (s *Server) GetOpenapiSpec(_ context.Context, _ GetOpenapiSpecRequestObject) (GetOpenapiSpecResponseObject, error) {
	b, err := s.spec.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshalling openapi spec: %w", err)
	}
	var doc OpenApiDocument
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("decoding openapi spec: %w", err)
	}
	return GetOpenapiSpec200JSONResponse(doc), nil
}

func (s *Server) GetMe(ctx context.Context, _ GetMeRequestObject) (GetMeResponseObject, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return GetMedefaultApplicationProblemPlusJSONResponse{
			StatusCode: http.StatusInternalServerError,
			Body: Problem{
				Type:   "about:blank",
				Title:  http.StatusText(http.StatusInternalServerError),
				Status: http.StatusInternalServerError,
				Code:   "internal",
				Detail: "No principal on the request. Authentication middleware is missing.",
			},
		}, nil
	}
	me := Principal{Id: p.ID, Type: PrincipalType(p.Type), Scopes: p.Scopes}
	if me.Scopes == nil {
		me.Scopes = []string{}
	}
	if p.Role != "" {
		me.Role = &p.Role
	}
	if p.TenantID != uuid.Nil {
		me.TenantId = &p.TenantID
	}
	if p.Type == auth.TypeUser {
		me.Pending = &p.Pending
		if uid, err := uuid.Parse(p.ID); err == nil {
			if u, err := s.accounts.GetUser(ctx, uid); err == nil {
				me.Email, me.Name, me.MfaEnabled = &u.Email, &u.Name, &u.MFAEnabled
				me.ExtensionId = u.ExtensionID
				s.meExtras(ctx, &me, u.ExtensionID, u.Presence)
			}
		}
	}
	return GetMe200JSONResponse(me), nil
}

func (s *Server) ListEventTypes(_ context.Context, req ListEventTypesRequestObject) (ListEventTypesResponseObject, error) {
	page := apihttp.ParsePagination(req.Params.Limit, req.Params.Cursor)

	start := 0
	if page.Cursor != "" {
		i, err := decodeEventTypeCursor(page.Cursor)
		if err != nil {
			return ListEventTypesdefaultApplicationProblemPlusJSONResponse{
				StatusCode: http.StatusBadRequest,
				Body: Problem{
					Type:   "about:blank",
					Title:  http.StatusText(http.StatusBadRequest),
					Status: http.StatusBadRequest,
					Code:   "cursor_invalid",
					Detail: "That cursor is not valid; start again without one.",
				},
			}, nil
		}
		start = i
	}
	if start > len(eventTypes) {
		start = len(eventTypes)
	}
	end := min(start+page.Limit, len(eventTypes))

	list := EventTypeList{Items: eventTypes[start:end]}
	if end < len(eventTypes) {
		cursor := encodeEventTypeCursor(end)
		list.NextCursor = &cursor
	}
	return ListEventTypes200JSONResponse(list), nil
}

// encodeEventTypeCursor and decodeEventTypeCursor turn the "resume after
// this index" position into the opaque string clients are told to treat as
// a cursor (docs/API.md §2), rather than a directly readable offset.
func encodeEventTypeCursor(index int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(index)))
}

func decodeEventTypeCursor(cursor string) (int, error) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(string(b))
}
