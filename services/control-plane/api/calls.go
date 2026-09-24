package api

import (
	"context"

	"linxpbx.com/linx/internal/pbx"
)

func toCallParty(p pbx.CallParty) CallParty {
	return CallParty{Extension: p.Extension, DeviceId: p.DeviceID, Name: p.Name}
}

func (s *Server) ListActiveCalls(_ context.Context, _ ListActiveCallsRequestObject) (ListActiveCallsResponseObject, error) {
	out := ActiveCallList{Items: []ActiveCall{}, PhoneEngineConnected: s.calls.Connected()}
	for _, c := range s.calls.ActiveCalls() {
		a := ActiveCall{Id: c.ID, From: toCallParty(c.From), To: c.To, State: ActiveCallState(c.State),
			StartedAt: c.StartedAt, AnsweredAt: c.AnsweredAt}
		if c.AnsweredBy != nil {
			by := toCallParty(*c.AnsweredBy)
			a.AnsweredBy = &by
		}
		out.Items = append(out.Items, a)
	}
	return ListActiveCalls200JSONResponse(out), nil
}
