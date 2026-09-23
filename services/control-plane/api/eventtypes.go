package api

import "linxpbx.com/linx/internal/webhook"

// eventTypes is the event catalog (webhook.EventTypes) as API objects.
var eventTypes = func() []EventType {
	out := make([]EventType, 0, len(webhook.EventTypes))
	for _, e := range webhook.EventTypes {
		out = append(out, EventType{Name: e.Name, Description: e.Description})
	}
	return out
}()
