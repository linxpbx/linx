package webhook

import "slices"

// EventType is one entry in the event catalog.
type EventType struct{ Name, Description string }

// EventTypes is the fixed catalog of webhook and admin-alert events this
// server can emit (docs/API.md §4). New entries ship with the feature that
// produces them.
var EventTypes = []EventType{
	{"call.started", "A call started."},
	{"call.answered", "A call was answered."},
	{"call.ended", "A call ended."},
	{"call.missed", "A call went unanswered."},
	{"voicemail.created", "A new voicemail was left."},
	{"recording.ready", "A call recording finished processing."},
	{"presence.changed", "A user's presence status changed."},
	{"meeting.started", "A video meeting started."},
	{"meeting.ended", "A video meeting ended."},
	{"extension.created", "An extension was added."},
	{"extension.updated", "An extension was changed."},
	{"extension.deleted", "An extension was deleted."},
	{"device.created", "A phone or app was added."},
	{"device.updated", "A phone or app was changed."},
	{"device.revoked", "A phone or app's login was revoked."},
	{"device.registered", "A device came online."},
	{"device.unregistered", "A device went offline."},
	{"trunk.down", "A SIP trunk went down."},
	{"trunk.up", "A SIP trunk that was down came back up."},
	{TestEventType, "Sent when an admin tests a webhook endpoint."},
	{"alert.fired", "An admin alert opened."},
	{"alert.resolved", "An admin alert cleared."},
}

// KnownEventType reports whether name is in the catalog.
func KnownEventType(name string) bool {
	return slices.ContainsFunc(EventTypes, func(e EventType) bool { return e.Name == name })
}
