package api

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/store"
)

var errNoCallQuality = errors.New("call quality is not configured")

// CallQualityStore is where Linx's own judgement of each call is kept
// (internal/callquality, internal/store).
type CallQualityStore interface {
	CallQuality(ctx context.Context, tenant uuid.UUID, limit int) ([]store.CallQualityItem, error)
}

// SetCallQuality gives the server Linx's own record of how calls went.
func (s *Server) SetCallQuality(q CallQualityStore) { s.callQuality = q }

// mostCallQuality is the most records one answer carries.
const mostCallQuality = 500

func (s *Server) ListCallQuality(ctx context.Context, _ ListCallQualityRequestObject) (ListCallQualityResponseObject, error) {
	if s.callQuality == nil {
		return nil, errNoCallQuality
	}
	p, _ := auth.PrincipalFromContext(ctx)
	items, err := s.callQuality.CallQuality(ctx, p.TenantID, mostCallQuality)
	if err != nil {
		return nil, err
	}
	out := ListCallQuality200JSONResponse{Items: make([]CallQuality, 0, len(items))}
	for _, i := range items {
		q := CallQuality{Id: i.ID, Kind: CallQualityKind(i.Kind), DeviceId: i.DeviceID, DeviceName: i.DeviceName,
			Extension: i.Extension, Person: i.Person, CallId: i.CallID, StartedAt: i.StartedAt, EndedAt: i.EndedAt,
			Verdict: CallQualityVerdict(i.Verdict), Reasons: i.Reasons, PhoneReportId: i.PhoneReport}
		if q.Reasons == nil {
			q.Reasons = []string{}
		}
		q.Sound.InPackets, q.Sound.OutPackets = i.Sound.InPackets, i.Sound.OutPackets
		q.Sound.InLost, q.Sound.OutLost = float32(i.Sound.InLost), float32(i.Sound.OutLost)
		q.Sound.JitterMs, q.Sound.RttMs = float32(i.Sound.JitterMs), float32(i.Sound.RTTMs)
		if i.PhoneVerdict != "" {
			v := i.PhoneVerdict
			q.PhoneVerdict = &v
		}
		out.Items = append(out.Items, q)
	}
	return out, nil
}
