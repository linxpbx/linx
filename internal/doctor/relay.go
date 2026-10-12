package doctor

import (
	"context"
	"net/netip"
	"slices"
	"strings"
)

const coturnContainer = "linx-coturn"

// livekitContainer is the video server for group calls (ADR-088), the
// relay's other destination.
const livekitContainer = "linx-livekit"

// mediaNetwork is compose.yaml's network between coturn and Asterisk.
const mediaNetwork = "linx-media"

// coturnConf is where coturn's entrypoint renders its configuration
// (internal/turnconf's default).
const coturnConf = "/tmp/linx-coturn/turnserver.conf"

// Relay checks the relay for browsers away from home (ADR-039, docs/WEB.md
// §2): coturn running and healthy (its health check proves it answers and
// serves the current certificate), relaying to Asterisk's audio, to the
// video server (ADR-088) and between Linx's own devices through itself
// (ADR-087), and to nothing else, on a network with nothing else on it. Whether it can be
// reached from the internet depends on the front door, checked with it.
func Relay(ctx context.Context, env Env) []Result {
	var rs results
	service(ctx, env, &rs, coturnContainer, "The call relay")
	if status, _, err := containerState(ctx, env.Runner, coturnContainer); err != nil || status != "running" {
		return rs
	}
	relayPeers(ctx, env, &rs)
	relayNetwork(ctx, env, &rs)
	return rs
}

const relayLogs = "Look at its log for the reason: sudo docker logs --tail 50 " + coturnContainer

// relayPeers checks coturn's rendered configuration denies every address
// and allows only Linx's own on linx-media, as they are right now:
// Asterisk's (which it must), its own (one device's relay to another's)
// and the video server's.
func relayPeers(ctx context.Context, env Env, rs *results) {
	conf, err := env.Runner.Run(ctx, nil, "docker", "exec", coturnContainer, "cat", coturnConf)
	if err != nil {
		rs.fail("Couldn't read the call relay's settings.", relayLogs)
		return
	}
	allowed, denyAll := RelayPeers(string(conf))
	asterisk, aerr := mediaAddress(ctx, env, asteriskContainer)
	ours := map[netip.Addr]bool{asterisk: true}
	for _, c := range []string{coturnContainer, livekitContainer} {
		if a, err := mediaAddress(ctx, env, c); err == nil {
			ours[a] = true
		}
	}
	strangers := slices.ContainsFunc(allowed, func(a netip.Addr) bool { return !ours[a] })
	switch {
	case aerr != nil:
		rs.fail("The phone system isn't on the call relay's network ("+mediaNetwork+"), so calls from outside have no audio.",
			"Restart Linx to restore its settings: "+composeCmd+" up --detach")
	case !denyAll:
		rs.fail("The call relay isn't limited to the phone system: it could be used to reach other addresses.",
			"Linx never sets this up; restart it to restore its settings: sudo docker restart "+coturnContainer)
	case !slices.Contains(allowed, asterisk) || strangers:
		rs.fail("The call relay is set up for an address that isn't the phone system's ("+asterisk.String()+") or the video server's.",
			"It follows their addresses within a minute; if this stays, restart it: sudo docker restart "+coturnContainer)
	default:
		rs.ok("The call relay only passes calls to and from the phone system and the video server.")
	}
}

// mediaAddress is a container's address on linx-media.
func mediaAddress(ctx context.Context, env Env, container string) (netip.Addr, error) {
	out, err := env.Runner.Run(ctx, nil, "docker", "inspect", "--type", "container", "--format",
		`{{with index .NetworkSettings.Networks "`+mediaNetwork+`"}}{{.IPAddress}}{{end}}`, container)
	if err != nil {
		return netip.Addr{}, err
	}
	return netip.ParseAddr(strings.TrimSpace(string(out)))
}

// RelayPeers reads a turnserver.conf: the allowed peer addresses, and
// whether every IPv4 and IPv6 address is otherwise denied.
func RelayPeers(conf string) (allowed []netip.Addr, denyAll bool) {
	var deny4, deny6 bool
	for _, line := range strings.Split(conf, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "allowed-peer-ip":
			a, err := netip.ParseAddr(v)
			if err != nil {
				// A range or anything else Linx doesn't write: never an
				// exact match, so it's reported.
				a = netip.IPv4Unspecified()
			}
			allowed = append(allowed, a)
		case "denied-peer-ip":
			deny4 = deny4 || v == "0.0.0.0-255.255.255.255"
			deny6 = deny6 || v == "::-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"
		}
	}
	return allowed, deny4 && deny6
}

// relayNetwork checks linx-media is internal (no route out) and holds only
// coturn, Asterisk and the video server.
func relayNetwork(ctx context.Context, env Env, rs *results) {
	out, err := env.Runner.Run(ctx, nil, "docker", "network", "inspect", "--format",
		`{{.Internal}}{{range .Containers}} {{.Name}}{{end}}`, mediaNetwork)
	if err != nil {
		rs.fail("The call relay's network ("+mediaNetwork+") is missing.", "Restart Linx to restore it: "+composeCmd+" up --detach")
		return
	}
	f := strings.Fields(string(out))
	members := f[min(1, len(f)):]
	slices.Sort(members)
	ours := []string{asteriskContainer, coturnContainer, livekitContainer}
	strangers := slices.ContainsFunc(members, func(m string) bool { return !slices.Contains(ours, m) })
	switch {
	case len(f) == 0 || f[0] != "true":
		rs.fail("The call relay's network ("+mediaNetwork+") can reach outside the server.",
			"Linx never sets this up; remove it and restart Linx: "+composeCmd+" down && "+composeCmd+" up --detach")
	case strangers:
		rs.fail("Something other than the call relay, the phone system and the video server is on their private network ("+strings.Join(members, ", ")+").",
			"Disconnect it: sudo docker network disconnect "+mediaNetwork+" <name>")
	default:
		rs.ok("The call relay's network is private to it, the phone system and the video server.")
	}
}
