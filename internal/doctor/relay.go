package doctor

import (
	"context"
	"net/netip"
	"slices"
	"strings"
)

const coturnContainer = "linx-coturn"

// mediaNetwork is compose.yaml's network between coturn and Asterisk.
const mediaNetwork = "linx-media"

// coturnConf is where coturn's entrypoint renders its configuration
// (internal/turnconf's default).
const coturnConf = "/tmp/linx-coturn/turnserver.conf"

// Relay checks the relay for browsers away from home (ADR-039, docs/WEB.md
// §2): coturn running and healthy (its health check proves it answers and
// serves the current certificate), relaying to Asterisk's audio and to
// nothing else, on a network with nothing else on it. Whether it can be
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
// and allows exactly one: Asterisk's on linx-media, as it is right now.
func relayPeers(ctx context.Context, env Env, rs *results) {
	conf, err := env.Runner.Run(ctx, nil, "docker", "exec", coturnContainer, "cat", coturnConf)
	if err != nil {
		rs.fail("Couldn't read the call relay's settings.", relayLogs)
		return
	}
	allowed, denyAll := RelayPeers(string(conf))
	out, err := env.Runner.Run(ctx, nil, "docker", "inspect", "--type", "container", "--format",
		`{{with index .NetworkSettings.Networks "`+mediaNetwork+`"}}{{.IPAddress}}{{end}}`, asteriskContainer)
	asterisk, perr := netip.ParseAddr(strings.TrimSpace(string(out)))
	switch {
	case err != nil || perr != nil:
		rs.fail("The phone system isn't on the call relay's network ("+mediaNetwork+"), so calls from outside have no audio.",
			"Restart Linx to restore its settings: "+composeCmd+" up --detach")
	case !denyAll:
		rs.fail("The call relay isn't limited to the phone system: it could be used to reach other addresses.",
			"Linx never sets this up; restart it to restore its settings: sudo docker restart "+coturnContainer)
	case len(allowed) != 1 || allowed[0] != asterisk:
		rs.fail("The call relay is set up for an address that isn't the phone system's ("+asterisk.String()+").",
			"It follows the phone system's address within a minute; if this stays, restart it: sudo docker restart "+coturnContainer)
	default:
		rs.ok("The call relay only passes audio to and from the phone system.")
	}
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
// coturn and Asterisk.
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
	want := []string{asteriskContainer, coturnContainer}
	switch {
	case len(f) == 0 || f[0] != "true":
		rs.fail("The call relay's network ("+mediaNetwork+") can reach outside the server.",
			"Linx never sets this up; remove it and restart Linx: "+composeCmd+" down && "+composeCmd+" up --detach")
	case !slices.Equal(members, want):
		rs.fail("Something other than the call relay and the phone system is on their private network ("+strings.Join(members, ", ")+").",
			"Disconnect it: sudo docker network disconnect "+mediaNetwork+" <name>")
	default:
		rs.ok("The call relay's network is private to it and the phone system.")
	}
}
