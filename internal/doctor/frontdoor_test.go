package doctor

import (
	"context"
	"crypto/x509"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/installer"
)

const nftFrontDoor = "nft -j list set inet linx front_door"

// addFrontDoor makes the fixture a Pangolin install: Pangolin at
// 192.168.1.30 passes the public names through to Linx, the relay allocates
// over TLS, coturn answers on UDP 443, DNS points at the home's address.
func addFrontDoor(f *fixture) {
	fk := &frontDoorFakes{}
	f.frontDoor = fk
	f.cfg.FrontDoor = installer.FrontDoorConfig{Kind: installer.FrontDoorPangolin, PangolinAddress: "192.168.1.30"}
	f.runner[nftFrontDoor] = `{"nftables": [{"set": {"family": "inet", "name": "front_door", "table": "linx", "type": "ipv4_addr", "elem": ["192.168.1.30"]}}]}`
	phoneLeaf, phoneDNS := f.env.TLSLeaf, f.env.LookupIP
	f.env.TLSLeaf = func(ctx context.Context, addr, name string, roots *x509.CertPool) (*x509.Certificate, error) {
		if addr != "192.168.1.30:443" {
			return phoneLeaf(ctx, addr, name, roots)
		}
		if fk.notPassedThrough == name {
			return &x509.Certificate{Raw: []byte("Pangolin's own certificate")}, nil
		}
		// Passed through: Linx answers, with certd's certificate.
		return phoneLeaf(ctx, "192.168.1.20:5061", "sip.lab.example.com", roots)
	}
	f.env.LookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		for _, h := range installer.PublicHosts {
			if host == h+".lab.example.com" && host != fk.notInDNS {
				return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil
			}
		}
		return phoneDNS(ctx, host)
	}
	f.env.ReadFile = func(p string) ([]byte, error) {
		if p == installer.TURNSecretPath {
			return []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567ABCDEFGHIJKLMNOPQRST\n"), nil
		}
		return nil, errors.New("no such file")
	}
	f.env.TURNOverTLS = func(_ context.Context, addr, name string, _ *x509.CertPool, user, pass string) error {
		fk.turnAddr, fk.turnName, fk.turnUser = addr, name, user
		if pass == "" {
			return errors.New("no password")
		}
		return fk.turnErr
	}
	f.env.STUNPing = func(_ context.Context, addr string) error {
		fk.stunAddr = addr
		return fk.stunErr
	}
}

type frontDoorFakes struct {
	notPassedThrough, notInDNS string
	turnErr, stunErr           error
	turnAddr, turnName         string
	turnUser, stunAddr         string
}

func TestFrontDoorPangolin(t *testing.T) {
	f := platformFixture(t, healthyState)
	fk := f.frontDoor
	rs := FrontDoor(context.Background(), f.env, f.cfg)
	if worst(rs) != installer.OK {
		t.Fatalf("healthy Pangolin front door:\n%s", dump(rs))
	}
	if fk.turnAddr != "192.168.1.30:443" || fk.turnName != "turn.lab.example.com" || !strings.HasSuffix(fk.turnUser, ":00000000-0000-0000-0000-000000000000") {
		t.Errorf("TURN over TLS went to %s for %s as %s", fk.turnAddr, fk.turnName, fk.turnUser)
	}
	if fk.stunAddr != "192.168.1.20:443" {
		t.Errorf("STUN went to %s", fk.stunAddr)
	}

	for _, tc := range []struct {
		name   string
		break_ func(f *fixture, fk *frontDoorFakes)
		want   string
	}{
		{"not passed through", func(_ *fixture, fk *frontDoorFakes) { fk.notPassedThrough = "api.lab.example.com" }, "api.lab.example.com through Pangolin (192.168.1.30) answers with another certificate"},
		{"relay refused", func(_ *fixture, fk *frontDoorFakes) { fk.turnErr = errors.New("TURN error 401") }, "The call relay doesn't work over TLS through Pangolin"},
		{"no UDP", func(_ *fixture, fk *frontDoorFakes) { fk.stunErr = errors.New("timeout") }, "doesn't answer on UDP port 443"},
		{"not in DNS", func(_ *fixture, fk *frontDoorFakes) { fk.notInDNS = "turn.lab.example.com" }, "These names aren't in DNS yet: turn.lab.example.com"},
		{"firewall", func(f *fixture, _ *frontDoorFakes) {
			f.runner[nftFrontDoor] = `{"nftables": [{"set": {"name": "front_door", "elem": []}}]}`
		}, "doesn't limit Linx's web port to Pangolin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := platformFixture(t, healthyState)
			tc.break_(f, f.frontDoor)
			rs := FrontDoor(context.Background(), f.env, f.cfg)
			if worst(rs) != installer.Fail || !strings.Contains(dump(rs), tc.want) {
				t.Errorf("want a failure %q, got:\n%s", tc.want, dump(rs))
			}
		})
	}
}

func TestFrontDoorNone(t *testing.T) {
	f := platformFixture(t, healthyState)
	f.cfg.FrontDoor = installer.FrontDoorConfig{Kind: installer.FrontDoorHomeOnly}
	rs := FrontDoor(context.Background(), f.env, f.cfg)
	if worst(rs) != installer.Warn || !strings.Contains(dump(rs), "no front door chosen") {
		t.Errorf("home only:\n%s", dump(rs))
	}
}
