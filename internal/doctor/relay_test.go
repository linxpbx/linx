package doctor

import (
	"context"
	"net/netip"
	"testing"

	"linxpbx.com/linx/internal/installer"
)

const (
	relayConf = "listening-port=3478\nrelay-ip=172.23.0.3\ndenied-peer-ip=0.0.0.0-255.255.255.255\n" +
		"denied-peer-ip=::-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff\nallowed-peer-ip=172.23.0.2\nno-tcp-relay\n"
	relayConfCmd = "docker exec linx-coturn cat /tmp/linx-coturn/turnserver.conf"
	astMediaCmd  = `docker inspect --type container --format {{with index .NetworkSettings.Networks "linx-media"}}{{.IPAddress}}{{end}} linx-asterisk`
	mediaNetCmd  = "docker network inspect --format {{.Internal}}{{range .Containers}} {{.Name}}{{end}} linx-media"
)

// addRelay makes the fixture's call relay healthy.
func addRelay(f *fixture) {
	f.runner[inspect+"linx-coturn"] = "running healthy\n"
	f.runner[relayConfCmd] = relayConf
	f.runner[astMediaCmd] = "172.23.0.2\n"
	f.runner[mediaNetCmd] = "true linx-coturn linx-asterisk\n"
}

func TestRelay(t *testing.T) {
	f := newFixture(t, false, now.Add(80*day), "*.lab.example.com")
	addRelay(f)
	rs := Relay(context.Background(), f.env)
	if worst(rs) != installer.OK {
		t.Fatalf("want all ok:\n%s", dump(rs))
	}
	want(t, rs, installer.OK, "only passes audio to and from the phone system")
	want(t, rs, installer.OK, "private to it and the phone system")

	for _, tc := range []struct {
		name, key, value, msg string
	}{
		{"not running", inspect + "linx-coturn", "exited \n", "isn't running"},
		{"unhealthy", inspect + "linx-coturn", "running unhealthy\n", "running but not answering"},
		{"old address", astMediaCmd, "172.23.0.9\n", "isn't the phone system's"},
		{"Asterisk off the network", astMediaCmd, "\n", "isn't on the call relay's network"},
		{"not denying", relayConfCmd, "allowed-peer-ip=172.23.0.2\n", "could be used to reach other addresses"},
		{"two peers", relayConfCmd, relayConf + "allowed-peer-ip=172.23.0.4\n", "isn't the phone system's"},
		{"a range", relayConfCmd, "denied-peer-ip=0.0.0.0-255.255.255.255\ndenied-peer-ip=::-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff\nallowed-peer-ip=172.23.0.0-172.23.0.255\n", "isn't the phone system's"},
		{"not internal", mediaNetCmd, "false linx-coturn linx-asterisk\n", "can reach outside the server"},
		{"a stranger", mediaNetCmd, "true linx-coturn linx-asterisk linx-other\n", "linx-other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, false, now.Add(80*day), "*.lab.example.com")
			addRelay(f)
			f.runner[tc.key] = tc.value
			want(t, Relay(context.Background(), f.env), installer.Fail, tc.msg)
		})
	}
}

func TestRelayPeers(t *testing.T) {
	allowed, denyAll := RelayPeers(relayConf)
	if !denyAll || len(allowed) != 1 || allowed[0] != netip.MustParseAddr("172.23.0.2") {
		t.Errorf("%v %v", allowed, denyAll)
	}
}
