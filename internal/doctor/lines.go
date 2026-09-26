package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/trunkstatus"
)

// linesQuery reads what the "Phone lines" checks need (docs/TRUNKS.md
// §10): the country, every trunk (never its password), and extensions
// whose number clashes with the country's numbering.
const linesQuery = `SELECT json_build_object(
	'country', (SELECT country FROM pbx_setting),
	'trunks', coalesce((SELECT json_agg(json_build_object(
		'id', t.id, 'name', t.name, 'kind', t.kind, 'host', t.host, 'port', t.port, 'transport', t.transport,
		'media', t.media_encryption, 'wireguard', t.wireguard_profile_id IS NOT NULL, 'enabled', t.enabled,
		'outbound', t.outbound_priority, 'pinned', CASE WHEN t.cert_trust = 'pinned' THEN coalesce(t.pinned_certificate, '') ELSE '' END,
		'status', t.status, 'detail', t.status_detail, 'confirmed_by', coalesce(t.unencrypted_confirmed_by, ''))
		ORDER BY t.outbound_priority NULLS LAST, t.name) FROM trunk t), '[]'::json),
	'clashes', (SELECT count(*) FROM extension e, pbx_setting s
		WHERE e.deleted_at IS NULL AND numbering_extension_clash(s.country, e.number) IS NOT NULL))`

type lineRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Transport   string `json:"transport"`
	Media       string `json:"media"`
	WireGuard   bool   `json:"wireguard"`
	Enabled     bool   `json:"enabled"`
	Outbound    *int   `json:"outbound"`
	Pinned      string `json:"pinned"`
	Status      string `json:"status"`
	Detail      string `json:"detail"`
	ConfirmedBy string `json:"confirmed_by"`
}

type linesState struct {
	Country string    `json:"country"`
	Trunks  []lineRow `json:"trunks"`
	Clashes int       `json:"clashes"`
}

// Lines checks the phone lines to the outside world (docs/TRUNKS.md §10):
// each line works (asked of Asterisk now, the same way the control plane
// learns it), which lines aren't encrypted, pinned certificates' expiry,
// that emergency calls have a way out, and that no extension number
// clashes with the country's numbers.
func Lines(ctx context.Context, env Env) []Result {
	var rs results
	if status, _, err := containerState(ctx, env.Runner, postgresContainer); err != nil || status != "running" {
		return rs // reported under Services
	}
	b, err := env.Runner.Run(ctx, nil, "docker", psqlArgs(postgresContainer, linesQuery)...)
	var st linesState
	if err == nil {
		err = json.Unmarshal(bytes.TrimSpace(b), &st)
	}
	if err != nil {
		rs.fail("Can't read the phone lines from the database.",
			"The API service sets the database up when it starts: sudo docker logs --tail 50 "+controlPlaneContainer)
		return rs
	}
	rs.ok(fmt.Sprintf("Phone numbers are read as dialled in %s.", numbering.CountryName(st.Country, numbering.Countries[st.Country])))

	// What Asterisk says now; the database's copy if it can't be asked.
	live := map[string]trunkstatus.Trunk{}
	asked := false
	if status, health, err := containerState(ctx, env.Runner, asteriskContainer); err == nil && status == "running" && health != "starting" {
		regs, err1 := asteriskCLI(ctx, env, "pjsip show registrations")
		contacts, err2 := asteriskCLI(ctx, env, "pjsip show contacts")
		if err1 == nil && err2 == nil {
			live, asked = trunkstatus.Merge(trunkstatus.ParseRegistrations(regs), trunkstatus.ParseContacts(contacts)), true
		}
	}

	if len(st.Trunks) == 0 {
		rs.warn("No phone lines to the outside yet: only calls between extensions work, and emergency calls can't go out.",
			"Add one: sudo linx trunk add")
	}
	outboundUp := false
	outbound := 0
	now := time.Now()
	if env.Now != nil {
		now = env.Now()
	}
	for _, t := range st.Trunks {
		label := fmt.Sprintf("Line %q (%s)", t.Name, t.Host)
		if !t.Enabled {
			rs.ok(label + " is turned off.")
			continue
		}
		status, detail := t.Status, t.Detail
		if asked {
			status, detail = trunkstatus.Decide(t.Kind == "registration", live["trunk-"+t.ID])
		}
		if t.Outbound != nil {
			outbound++
			if !trunkstatus.Down(status) {
				outboundUp = true
			}
		}
		fix := fmt.Sprintf("Test it: sudo linx trunk test %q", t.Name)
		switch {
		case trunkstatus.Up(status):
			rs.ok(fmt.Sprintf("%s works: %s", label, detail))
		case trunkstatus.Down(status):
			rs.fail(fmt.Sprintf("%s isn't working: %s", label, detail), fix)
		default:
			rs.warn(fmt.Sprintf("%s: %s", label, detail), "Run doctor again in a minute. If it stays like this: "+fix)
		}
		if !t.WireGuard && (t.Transport != "tls" || t.Media != "srtp") {
			who := ""
			if t.ConfirmedBy != "" {
				who = " (" + t.ConfirmedBy + " confirmed it)"
			}
			rs.warn(fmt.Sprintf("%s isn't encrypted%s: its calls can be listened to on the way.", label, who),
				"If the provider offers TLS, switch to it; sudo linx trunk test shows whether it does.")
		}
		if t.Pinned != "" {
			pinnedExpiry(&rs, label, t.Pinned, now)
		}
	}

	switch {
	case len(st.Trunks) == 0:
	case outbound == 0:
		rs.fail("No line is set up for outgoing calls, so emergency calls can't go out.",
			"Choose one: sudo linx trunk add, or set the order with PUT /api/v1/outbound-routing.")
	case !outboundUp:
		rs.fail("Every line for outgoing calls is down, so emergency calls can't go out.", "See the lines above.")
	default:
		rs.ok("Emergency numbers can be called (they're always allowed, on the first line that works).")
	}

	if st.Clashes > 0 {
		rs.warn(fmt.Sprintf("%s can't be dialled: their numbers are also outside or emergency numbers here.", count(st.Clashes, "extension")),
			"Give them other numbers (extensions can't start with 0 or be an emergency or service number).")
	}
	return rs
}

// pinnedExpiry warns before a pinned certificate expires: after that,
// Asterisk refuses the line.
func pinnedExpiry(rs *results, label, pemText string, now time.Time) {
	certs, err := parseCerts([]byte(pemText))
	if err != nil || len(certs) == 0 {
		rs.fail(label+"'s pinned certificate can't be read, so Linx can't connect to it.", "Pin it again: sudo linx trunk remove, then add")
		return
	}
	soonest := certs[0]
	for _, c := range certs[1:] {
		if c.NotAfter.Before(soonest.NotAfter) {
			soonest = c
		}
	}
	left := soonest.NotAfter.Sub(now)
	switch {
	case left <= 0:
		rs.fail(fmt.Sprintf("%s's pinned certificate expired on %s.", label, soonest.NotAfter.Format("2 Jan 2006")),
			"Put a new certificate on it, then pin that one.")
	case left < 30*24*time.Hour:
		rs.warn(fmt.Sprintf("%s's pinned certificate expires on %s (%s).", label, soonest.NotAfter.Format("2 Jan 2006"), days(left)),
			"Put a new certificate on it before then, and pin that one.")
	}
}
