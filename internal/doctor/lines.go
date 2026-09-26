package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/installer"
	"linxpbx.com/linx/internal/nftset"
	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/trunk"
	"linxpbx.com/linx/internal/trunkstatus"
)

// linesQuery reads what the "Phone lines" checks need (docs/TRUNKS.md
// §10): the country, every trunk (never its password), extensions whose
// number clashes with the country's numbering, and every WireGuard
// tunnel's state (never its keys).
const linesQuery = `SELECT json_build_object(
	'country', (SELECT country FROM pbx_setting),
	'trunks', coalesce((SELECT json_agg(json_build_object(
		'id', t.id, 'name', t.name, 'kind', t.kind, 'host', t.host, 'port', t.port, 'transport', t.transport,
		'media', t.media_encryption, 'wireguard', t.wireguard_profile_id IS NOT NULL, 'enabled', t.enabled,
		'outbound', t.outbound_priority, 'pinned', CASE WHEN t.cert_trust = 'pinned' THEN coalesce(t.pinned_certificate, '') ELSE '' END,
		'status', t.status, 'detail', t.status_detail, 'confirmed_by', coalesce(t.unencrypted_confirmed_by, ''))
		ORDER BY t.outbound_priority NULLS LAST, t.name) FROM trunk t), '[]'::json),
	'clashes', (SELECT count(*) FROM extension e, pbx_setting s
		WHERE e.deleted_at IS NULL AND numbering_extension_clash(s.country, e.number) IS NOT NULL),
	'tunnels', coalesce((SELECT json_agg(json_build_object(
		'name', w.name, 'status', w.status, 'detail', w.status_detail,
		'used', EXISTS (SELECT 1 FROM trunk t WHERE t.wireguard_profile_id = w.id AND t.enabled))
		ORDER BY w.name) FROM wireguard_profile w), '[]'::json))`

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

type tunnelRow struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Used   bool   `json:"used"`
}

type linesState struct {
	Country string      `json:"country"`
	Trunks  []lineRow   `json:"trunks"`
	Clashes int         `json:"clashes"`
	Tunnels []tunnelRow `json:"tunnels"`
}

// wireguardContainer is compose.yaml's linx-wireguard (docs/TRUNKS.md §7).
const wireguardContainer = "linx-wireguard"

// wireGuardModule exists while the host's kernel has WireGuard loaded.
const wireGuardModule = "/sys/module/wireguard"

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

	tunnels(ctx, env, &rs, st.Tunnels)
	firewallSync(ctx, env, &rs, st.Trunks)

	if st.Clashes > 0 {
		rs.warn(fmt.Sprintf("%s can't be dialled: their numbers are also outside or emergency numbers here.", count(st.Clashes, "extension")),
			"Give them other numbers (extensions can't start with 0 or be an emergency or service number).")
	}
	return rs
}

// tunnels checks the WireGuard tunnels (docs/TRUNKS.md §7): each one's
// handshake as linx-wireguard last reported it, the service itself, and
// the kernel module it needs.
func tunnels(ctx context.Context, env Env, rs *results, list []tunnelRow) {
	if len(list) == 0 {
		return
	}
	if env.Stat != nil {
		if _, err := env.Stat(wireGuardModule); err != nil {
			rs.fail("This server's kernel hasn't loaded WireGuard, so no tunnel can come up.",
				"Load it: sudo modprobe wireguard (sudo linx setup makes it load at every start)")
		}
	}
	if status, _, err := containerState(ctx, env.Runner, wireguardContainer); err != nil || status != "running" {
		rs.fail("Linx's WireGuard service isn't running, so tunnels can't come back after the phone system restarts.",
			"sudo docker logs --tail 50 "+wireguardContainer+", then: sudo docker start "+wireguardContainer)
	}
	for _, w := range list {
		label := fmt.Sprintf("WireGuard tunnel %q", w.Name)
		fix := "Check the provider's address and keys: sudo linx trunk wireguard list"
		switch {
		case w.Status == "up":
			rs.ok(fmt.Sprintf("%s is up: %s", label, w.Detail))
		case !w.Used:
			rs.warn(fmt.Sprintf("%s (no line uses it) is %s: %s", label, w.Status, w.Detail), fix)
		case w.Status == "down":
			rs.fail(fmt.Sprintf("%s is down: %s Its lines can't work.", label, w.Detail), fix)
		default:
			rs.warn(fmt.Sprintf("%s: %s", label, w.Detail), "Run doctor again in a minute. If it stays like this: "+fix)
		}
	}
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

// firewallSync checks the host firewall matches the IP-authenticated
// trunks in rows (docs/TRUNKS.md §12): linx-firewall-sync's timer runs,
// and its two sets hold exactly the addresses those trunks resolve to
// right now. Nothing to check with no such trunk: the sets are simply
// empty, and nothing depends on the timer yet.
func firewallSync(ctx context.Context, env Env, rs *results, rows []lineRow) {
	wantTLS, wantPlain := expectedFirewallAddrs(ctx, env, rows)
	if len(wantTLS) == 0 && len(wantPlain) == 0 {
		return
	}
	en, _ := env.Runner.Run(ctx, nil, "systemctl", "is-enabled", installer.FirewallSyncTimer)
	act, _ := env.Runner.Run(ctx, nil, "systemctl", "is-active", installer.FirewallSyncTimer)
	if strings.TrimSpace(string(en)) != "enabled" || strings.TrimSpace(string(act)) != "active" {
		rs.fail("The timer that keeps the firewall matching your phone-line providers isn't running: a provider that changes address could be refused.",
			"Turn it on: sudo systemctl enable --now "+installer.FirewallSyncTimer)
		return
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return env.Runner.Run(ctx, nil, name, args...)
	}
	haveTLS, err1 := nftset.List(ctx, run, installer.FirewallTable, installer.TrunkAddressSet)
	havePlain, err2 := nftset.List(ctx, run, installer.FirewallTable, installer.TrunkPlainAddressSet)
	if err1 != nil || err2 != nil {
		rs.fail("Can't read the firewall's phone-line provider addresses.",
			"Check the firewall rules are loaded: sudo systemctl restart "+installer.FirewallUnit)
		return
	}
	switch {
	case !sameAddrs(haveTLS, wantTLS) || !sameAddrs(havePlain, wantPlain):
		rs.warn("The firewall hasn't caught up with your phone-line providers' addresses yet.",
			"Run linx doctor again in a minute; if it stays like this: sudo systemctl restart "+installer.FirewallSyncTimer)
	default:
		rs.ok("The firewall only lets your phone-line providers reach the phone system, from their own addresses.")
	}
}

// expectedFirewallAddrs is trunk.FirewallAddresses over what linesQuery
// already read, so this doesn't need a second database query: it just
// reuses each row's kind, host, transport and WireGuard flag.
func expectedFirewallAddrs(ctx context.Context, env Env, rows []lineRow) (tls, plain []netip.Addr) {
	trunks := make([]trunk.Trunk, len(rows))
	for i, r := range rows {
		t := trunk.Trunk{Enabled: r.Enabled, Kind: r.Kind, Host: r.Host, Transport: r.Transport}
		if r.WireGuard {
			id := uuid.Nil // trunk.FirewallAddresses only asks whether this is nil
			t.WireGuardProfileID = &id
		}
		trunks[i] = t
	}
	resolve := func(ctx context.Context, host string) []netip.Addr {
		if a, err := netip.ParseAddr(host); err == nil {
			return []netip.Addr{a}
		}
		addrs, err := env.LookupIP(ctx, host)
		if err != nil {
			return nil
		}
		return addrs
	}
	return trunk.FirewallAddresses(ctx, trunks, resolve)
}

func sameAddrs(a, b []netip.Addr) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.SortFunc(a, netip.Addr.Compare)
	slices.SortFunc(b, netip.Addr.Compare)
	return slices.Equal(a, b)
}
