package api

// eventTypes is the fixed catalog of webhook and admin-alert events this
// server can emit (docs/API.md §4). New entries ship with the feature that
// produces them.
var eventTypes = []EventType{
	{Name: "call.started", Description: "A call started."},
	{Name: "call.answered", Description: "A call was answered."},
	{Name: "call.ended", Description: "A call ended."},
	{Name: "call.missed", Description: "A call went unanswered."},
	{Name: "voicemail.created", Description: "A new voicemail was left."},
	{Name: "recording.ready", Description: "A call recording finished processing."},
	{Name: "presence.changed", Description: "A user's presence status changed."},
	{Name: "meeting.started", Description: "A video meeting started."},
	{Name: "meeting.ended", Description: "A video meeting ended."},
	{Name: "device.enrolled", Description: "A phone or app was enrolled."},
	{Name: "device.revoked", Description: "A phone or app's enrollment was revoked."},
	{Name: "trunk.down", Description: "A SIP trunk went down."},
	{Name: "trunk.up", Description: "A SIP trunk that was down came back up."},
	{Name: "webhook.test", Description: "Sent when an admin tests a webhook endpoint."},
	{Name: "alert.fired", Description: "An admin alert opened."},
	{Name: "alert.resolved", Description: "An admin alert cleared."},
}
