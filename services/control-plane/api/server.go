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
	"linxpbx.com/linx/internal/backupschedule"
	"linxpbx.com/linx/internal/helpanswers"
	"linxpbx.com/linx/internal/moved"
	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/reach"
	"linxpbx.com/linx/internal/settings"
	"linxpbx.com/linx/internal/sso"
	"linxpbx.com/linx/internal/trunk"
	"linxpbx.com/linx/internal/turn"
	"linxpbx.com/linx/internal/webhook"
)

// Server implements StrictServerInterface.
type Server struct {
	// Check it (reach.go).
	reachCheck func(context.Context) []reach.Line
	reachLinks *reach.Links
	reachLimit *auth.Limiters

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
	settings  *settings.Service
	audit     AuditLogSource
	sso       *sso.Service
	backups   *backupschedule.Service
	now       func() time.Time

	// System → Status's server helper (SetOps).
	ops OpsSource
	// System → Server settings (SetServerSettings).
	serverSettings ServerSettingsSource
	moved          *moved.Service
	// Help's written answers (SetHelpAnswers).
	helpAnswers *helpanswers.Service
	writeAudit  func(context.Context, auth.AuditEntry) error
	certExpiry  func() time.Time
}

// AuditLogSource is the database access /audit-log needs.
type AuditLogSource interface {
	ListAuditLog(ctx context.Context, tenant uuid.UUID, f auth.AuditLogFilter, before *uuid.UUID, limit int) ([]auth.AuditLogEntry, error)
}

// NumberingSource is the country Linx is set up in and the outgoing-call
// decision (internal/store), for /outbound-routing and /route-test
// (docs/TRUNKS.md §5).
type NumberingSource interface {
	Country(ctx context.Context) (string, error)
	Classify(ctx context.Context, home, dialled string) (numbering.Result, error)
	Route(ctx context.Context, extension uuid.UUID, dialled string) (numbering.Route, error)
	ExtensionByNumber(ctx context.Context, tenant uuid.UUID, number string) (pbx.Extension, error)
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
func NewServer(spec *openapi3.T, store CredentialStore, webhooks *webhook.Service, alerts *alert.Service, pbxSvc *pbx.Service, trunks *trunk.Service, numbering NumberingSource, calls CallSource, accounts *auth.Accounts, turnIssuer *turn.Issuer, team *pbx.Team, settingsSvc *settings.Service, audit AuditLogSource, ssoSvc *sso.Service, backups *backupschedule.Service) *Server {
	return &Server{spec: spec, store: store, webhooks: webhooks, alerts: alerts, pbx: pbxSvc, trunks: trunks, numbering: numbering,
		calls: calls, accounts: accounts, turn: turnIssuer, team: team, settings: settingsSvc, audit: audit, sso: ssoSvc, backups: backups, now: time.Now}
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
	if p.AdminNetworkRestricted {
		me.AdminNetworkRestricted = &p.AdminNetworkRestricted
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
				me.Passkeys, me.HasPassword, me.PasswordOnly = &u.PasskeyCount, ptr(u.HasPassword()), ptr(!u.HasSecondStep())
				me.RecoveryCodesLeft = ptr(len(u.RecoveryCodeHashes))
				me.ExtensionId = u.ExtensionID
				me.CompanySignIn = &u.CompanyLogins
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
