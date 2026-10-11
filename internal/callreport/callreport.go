// Package callreport keeps what the Linx app saw of each call it made or
// took (owner, 2026-10-11): the words its Call details screen copies out and
// the call's SIP messages by their first line, sent by the phone itself
// after every call, for an admin to read on the phone's page.
//
// Small and short-lived on purpose (docs/RESOURCES.md): at most 16 KB each,
// kept seven days and no more than the newest hundred per phone. Saving a
// report clears what is past that, so nothing runs on a timer to tidy up.
package callreport

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
)

// Limits.
const (
	KeepFor      = 7 * 24 * time.Hour
	KeepPerPhone = 100
	MaxReport    = 16384
	MaxCallID    = 200
	MaxPeer      = 100
)

// What a record is about.
const (
	KindCall  = "call"
	KindCrash = "crash"
)

// Report is one call, or one crash of the app, as one phone saw it.
type Report struct {
	ID uuid.UUID
	// Kind is KindCall or KindCrash; empty means a call.
	Kind      string
	TenantID  uuid.UUID
	DeviceID  uuid.UUID
	CallID    string
	Direction string
	Peer      string
	StartedAt time.Time
	EndedAt   *time.Time
	Text      string
	CreatedAt time.Time
}

// Store is where reports live.
type Store interface {
	// SaveCallReport keeps r and drops that phone's reports created before
	// keepAfter or beyond its newest keep.
	SaveCallReport(ctx context.Context, r Report, keepAfter time.Time, keep int) error
	// CallReports is one phone's reports, newest first.
	CallReports(ctx context.Context, tenant, device uuid.UUID) ([]Report, error)
}

// Service checks what a phone sends before it is kept.
type Service struct {
	Store Store
	Now   func() time.Time
}

var errInvalid = func(detail string) error {
	return &apihttp.Error{Status: http.StatusBadRequest, Code: "invalid_call_report", Detail: detail}
}

// Save keeps a report sent by device.
func (s *Service) Save(ctx context.Context, tenant, device uuid.UUID, r Report) error {
	now := s.Now().UTC()
	if r.Kind == "" {
		r.Kind = KindCall
	}
	switch {
	case r.Kind != KindCall && r.Kind != KindCrash:
		return errInvalid("A record is about a call or a crash.")
	case r.Kind == KindCall && r.Direction != "incoming" && r.Direction != "outgoing":
		return errInvalid("A call is either incoming or outgoing.")
	case r.Kind == KindCrash && (r.Direction != "" || r.Peer != ""):
		return errInvalid("A crash has no direction and no other end.")
	case strings.TrimSpace(r.CallID) == "" || len(r.CallID) > MaxCallID:
		return errInvalid("The call's id is missing or too long.")
	case len(r.Peer) > MaxPeer:
		return errInvalid("The other end's number is too long.")
	case strings.TrimSpace(r.Text) == "" || len(r.Text) > MaxReport:
		return errInvalid("The report is empty or longer than 16 KB.")
	case !utf8.ValidString(r.Text) || !utf8.ValidString(r.Peer) || !utf8.ValidString(r.CallID):
		return errInvalid("The report isn't text.")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	r.ID, r.TenantID, r.DeviceID, r.CreatedAt = id, tenant, device, now
	return s.Store.SaveCallReport(ctx, r, now.Add(-KeepFor), KeepPerPhone)
}

// List is one phone's reports, newest first.
func (s *Service) List(ctx context.Context, tenant, device uuid.UUID) ([]Report, error) {
	return s.Store.CallReports(ctx, tenant, device)
}
