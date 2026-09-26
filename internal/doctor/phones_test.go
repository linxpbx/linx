package doctor

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/installer"
)

const (
	astCLI    = "docker exec linx-asterisk asterisk -rx "
	odbcShow  = "\nODBC DSN Settings\n-----------------\n\n  Name:   asterisk\n  DSN:    asterisk\n    Number of active connections: 1 (out of 5)\n    Logging: Disabled\n"
	ariUp     = "Connection ID  Type  RemoteAddr  State Apps\n-----\nlinx  persistent  172.18.0.4:8089  Up  linx\n"
	ariDown   = "Connection ID  Type  RemoteAddr  State Apps\n-----\nlinx  persistent  N/A  Down  linx\n"
	transport = "\nTransport:  <TransportId........>  <Type>  <cos>  <tos>  <BindAddress....................>\n" +
		"==========================================================================================\n\n" +
		"Transport:  transport-tls             tls      0      0  0.0.0.0:5061\n" +
		"Transport:  transport-wss             wss      0      0  172.22.0.4:8089\n\nObjects found: 2\n"
	// Asterisk's sockets: 5061 everywhere (Docker publishes it on the LAN
	// address), the browser websocket on its linx-sipws address
	// (172.22.0.4) only, and a connection to Postgres (not listening).
	procTCP = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:13C5 00000000:0000 0A 00000000:00000000 00:00000000 00000000   100        0 1 1\n" +
		"   1: 040016AC:1F99 00000000:0000 0A 00000000:00000000 00:00000000 00000000   100        0 2 1\n" +
		"   2: 030014AC:D2F0 020014AC:1538 01 00000000:00000000 00:00000000 00000000   100        0 3 1\n"
	procTCPCmd = "docker exec linx-asterisk cat /proc/net/tcp"
	nftSet     = `{"nftables": [{"metainfo": {"version": "1.0.9"}}, {"set": {"family": "inet", "name": "phone_networks", "table": "linx",` +
		` "type": "ipv4_addr", "flags": ["interval"], "elem": [{"prefix": {"addr": "192.168.1.0", "len": 24}}]}}]}`
	nftGet = "nft -j list set inet linx phone_networks"
)

var testLAN = installer.LAN{Address: netip.MustParseAddr("192.168.1.20"), Network: netip.MustParsePrefix("192.168.1.0/24")}

func dockerPorts(host string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "5061/tcp -> %s:5061\n", host)
	for p := 10000; p <= 10199; p++ {
		fmt.Fprintf(&b, "%d/udp -> %s:%d\n", p, host, p)
	}
	return b.String()
}

// addPhones makes the fixture a healthy phone system on 192.168.1.0/24.
func addPhones(t *testing.T, f *fixture) {
	t.Helper()
	f.runner[inspect+"linx-asterisk"] = "running healthy\n"
	f.runner[astCLI+"odbc show asterisk"] = odbcShow
	f.runner[astCLI+"ari show websocket sessions"] = ariUp
	f.runner[astCLI+"pjsip show transports"] = transport
	f.runner[procTCPCmd] = procTCP
	f.runner["docker port linx-asterisk"] = dockerPorts("192.168.1.20")
	f.runner[psqlCmd+phoneQuery] = `{"views": 4, "other": 0}`
	f.runner["docker ps --format {{.Names}} {{.Ports}}"] = "linx-asterisk 192.168.1.20:5061->5061/tcp, 192.168.1.20:10000-10199->10000-10199/udp\nlinx-postgres \n"
	f.runner["ss -Hlntu"] = "tcp LISTEN 0 4096 0.0.0.0:22 0.0.0.0:*\nudp UNCONN 0 0 127.0.0.53%lo:53 0.0.0.0:*\n"
	f.runner[nftGet] = nftSet
	f.runner["systemctl is-enabled linx-firewall.service"] = "enabled\n"
	f.env.LAN = func() installer.LAN { return testLAN }
	f.env.LookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "sip.lab.example.com" {
			return nil, errors.New("no such host")
		}
		return []netip.Addr{testLAN.Address}, nil
	}
	// Asterisk serves whatever certd has.
	f.env.TLSLeaf = func(ctx context.Context, addr, name string, roots *x509.CertPool) (*x509.Certificate, error) {
		if addr != "192.168.1.20:5061" || name != "sip.lab.example.com" {
			return nil, fmt.Errorf("dialled %s for %s", addr, name)
		}
		b, err := copyFromContainer(ctx, f.runner, certdContainer, fullchainPath)
		if err != nil {
			return nil, err
		}
		cs, err := parseCerts(b)
		if err != nil {
			return nil, err
		}
		if _, err := cs[0].Verify(x509.VerifyOptions{DNSName: name, Roots: roots, Intermediates: pool(cs[1:]), CurrentTime: now}); err != nil {
			return nil, err
		}
		return cs[0], nil
	}
}

func pool(cs []*x509.Certificate) *x509.CertPool {
	p := x509.NewCertPool()
	for _, c := range cs {
		p.AddCert(c)
	}
	return p
}

func phonesFixture(t *testing.T) *fixture {
	return platformFixture(t, healthyState)
}

func (f *fixture) phones() []Result {
	return Phones(context.Background(), f.env, f.cfg)
}

func TestPhonesAllGreen(t *testing.T) {
	for _, staging := range []bool{true, false} {
		f := newFixture(t, staging, now.Add(80*day), "*.lab.example.com")
		f.runner[inspect+"linx-postgres"] = "running healthy\n"
		addPhones(t, f)
		rs := f.phones()
		if worst(rs) != installer.OK {
			t.Fatalf("staging=%v: want all ok, got:\n%s", staging, dump(rs))
		}
		for _, w := range []string{"The phone system is running and answering", "reads the phone settings from the database, and nothing else",
			"connected to the API service", "only accepts encrypted phone connections",
			"accepts browsers' connections encrypted, and only from inside the server", "local network address (192.168.1.20) only",
			"current certificate for sip.lab.example.com", "Nothing on this server offers unencrypted SIP",
			"only lets phones connect from your local network (192.168.1.0/24)", "find this server at sip.lab.example.com"} {
			want(t, rs, installer.OK, w)
		}
	}
}

func TestPhonesProblems(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(f *fixture)
		level  installer.Level
		text   string
	}{
		{"not running", func(f *fixture) { f.runner[inspect+"linx-asterisk"] = "exited \n" }, installer.Fail, "isn't running"},
		{"no database", func(f *fixture) {
			f.runner[astCLI+"odbc show asterisk"] = strings.Replace(odbcShow, "connections: 1", "connections: 0", 1)
		}, installer.Fail, "can't read extensions and phones"},
		{"grants widened", func(f *fixture) { f.runner[psqlCmd+phoneQuery] = `{"views": 4, "other": 1}` },
			installer.Fail, "isn't limited to what it needs"},
		{"ARI down", func(f *fixture) { f.runner[astCLI+"ari show websocket sessions"] = ariDown }, installer.Fail, "isn't connected to the API service"},
		{"UDP transport", func(f *fixture) {
			f.runner[astCLI+"pjsip show transports"] = transport + "Transport:  transport-udp             udp      0      0  0.0.0.0:5060\n"
		}, installer.Fail, "without encryption: transport-udp (udp 0.0.0.0:5060)"},
		{"websocket on 5060", func(f *fixture) {
			f.runner[astCLI+"pjsip show transports"] = strings.Replace(transport, "172.22.0.4:8089", "0.0.0.0:5060", 1)
		}, installer.Fail, "without encryption: transport-wss (wss 0.0.0.0:5060)"},
		{"no browser websocket yet", func(f *fixture) { f.runner[procTCPCmd] = strings.Replace(procTCP, ":1F99", ":1F9A", 1) },
			installer.Fail, "isn't accepting connections from browsers yet"},
		{"browser websocket everywhere", func(f *fixture) { f.runner[procTCPCmd] = strings.Replace(procTCP, "040016AC:1F99", "00000000:1F99", 1) },
			installer.Fail, "more addresses than its internal one"},
		{"plain HTTP", func(f *fixture) {
			f.runner[procTCPCmd] = procTCP + "   3: 040016AC:1F98 00000000:0000 0A 00000000:00000000 00:00000000 00000000   100        0 4 1\n"
		}, installer.Fail, "unencrypted web connection (port 8088)"},
		{"no transport", func(f *fixture) { f.runner[astCLI+"pjsip show transports"] = "No objects found.\n" }, installer.Fail, "isn't accepting phone connections"},
		{"published everywhere", func(f *fixture) { f.runner["docker port linx-asterisk"] = dockerPorts("0.0.0.0") },
			installer.Fail, "open on 0.0.0.0:5061"},
		{"LAN address changed", func(f *fixture) {
			f.env.LAN = func() installer.LAN {
				return installer.LAN{Address: netip.MustParseAddr("192.168.7.3"), Network: netip.MustParsePrefix("192.168.7.0/24")}
			}
		}, installer.Fail, "local network address is 192.168.7.3"},
		{"old certificate", func(f *fixture) {
			other := newFixture(t, true, now.Add(80*day), "*.lab.example.com")
			f.env.TLSLeaf = func(context.Context, string, string, *x509.CertPool) (*x509.Certificate, error) {
				b, _ := copyFromContainer(context.Background(), other.runner, certdContainer, fullchainPath)
				cs, _ := parseCerts(b)
				return cs[0], nil
			}
		}, installer.Fail, "older certificate"},
		{"TLS fails", func(f *fixture) {
			f.env.TLSLeaf = func(context.Context, string, string, *x509.CertPool) (*x509.Certificate, error) {
				return nil, errors.New("connection refused")
			}
		}, installer.Fail, "Couldn't make a secure connection"},
		{"5060 published", func(f *fixture) {
			f.runner["docker ps --format {{.Names}} {{.Ports}}"] = "freepbx 0.0.0.0:5060->5060/udp\n"
		}, installer.Fail, "freepbx opens port 5060"},
		{"5060 listening", func(f *fixture) { f.runner["ss -Hlntu"] = "udp UNCONN 0 0 0.0.0.0:5060 0.0.0.0:*\n" },
			installer.Fail, "listens on port 5060"},
		{"firewall not loaded", func(f *fixture) { delete(f.runner, nftGet) }, installer.Fail, "aren't loaded"},
		{"firewall for another network", func(f *fixture) { f.runner[nftGet] = strings.Replace(nftSet, "192.168.1.0", "10.0.0.0", 1) },
			installer.Fail, "connect from 10.0.0.0/24, but this server's local network is 192.168.1.0/24"},
		{"firewall not at boot", func(f *fixture) { f.runner["systemctl is-enabled linx-firewall.service"] = "disabled\n" },
			installer.Warn, "won't be after the server restarts"},
		{"no DNS record", func(f *fixture) {
			f.env.LookupIP = func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("NXDOMAIN") }
		}, installer.Warn, "sip.lab.example.com doesn't exist"},
		{"DNS elsewhere", func(f *fixture) {
			f.env.LookupIP = func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil
			}
		}, installer.Warn, "points to 203.0.113.9, not 192.168.1.20"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := phonesFixture(t)
			tt.break_(f)
			want(t, f.phones(), tt.level, tt.text)
		})
	}
}

// An unencrypted trunk's own transport (ADR-023) isn't a phone connection.
func TestPhonesTrunkTransport(t *testing.T) {
	f := phonesFixture(t)
	f.runner[astCLI+"pjsip show transports"] = transport + "Transport:  transport-trunk-tcp       tcp      0      0  0.0.0.0:5062\n"
	want(t, f.phones(), installer.OK, "only accepts encrypted phone connections")
}

func TestPhonesWireGuardTransports(t *testing.T) {
	f := phonesFixture(t)
	f.runner[astCLI+"pjsip show transports"] = transport +
		"Transport:  transport-wg-tls          tls      0      0  0.0.0.0:5064\n" +
		"Transport:  transport-wg-udp          udp      0      0  0.0.0.0:5063\n"
	want(t, f.phones(), installer.OK, "only accepts encrypted phone connections")
	// On any other port, it's not Linx's.
	f.runner[astCLI+"pjsip show transports"] = transport + "Transport:  transport-wg-udp          udp      0      0  0.0.0.0:5060\n"
	want(t, f.phones(), installer.Fail, "accepts connections without encryption")
}

func TestPhonesStarting(t *testing.T) {
	f := phonesFixture(t)
	f.runner[inspect+"linx-asterisk"] = "running starting\n"
	for k := range f.runner {
		if strings.HasPrefix(k, astCLI) {
			delete(f.runner, k) // would fail (or, for real, wait) if asked
		}
	}
	rs := f.phones()
	want(t, rs, installer.Warn, "still starting")
	if worst(rs) != installer.Warn {
		t.Errorf("asked the phone system while it's starting:\n%s", dump(rs))
	}
}

func TestPhonesNoLAN(t *testing.T) {
	f := phonesFixture(t)
	f.env.LAN = func() installer.LAN { return installer.LAN{} }
	f.runner["docker port linx-asterisk"] = dockerPorts("127.0.0.1")
	f.runner[nftGet] = `{"nftables": [{"set": {"family": "inet", "name": "phone_networks", "table": "linx", "type": "ipv4_addr", "flags": ["interval"]}}]}`
	f.env.TLSLeaf = nil // never dialled
	rs := f.phones()
	want(t, rs, installer.OK, "open on this server itself only")
	want(t, rs, installer.OK, "keeps the phone ports closed")
	want(t, rs, installer.Warn, "isn't on a local network")
	if worst(rs) != installer.Warn {
		t.Errorf("want only the no-LAN warning:\n%s", dump(rs))
	}
}

func TestPhoneNetworksSet(t *testing.T) {
	got, err := phoneNetworksSet([]byte(`{"nftables": [{"set": {"elem": ["10.0.0.7", {"prefix": {"addr": "192.168.1.0", "len": 24}}]}}]}`), nil)
	if err != nil || len(got) != 2 || got[0].String() != "10.0.0.7/32" || got[1].String() != "192.168.1.0/24" {
		t.Errorf("got %v, %v", got, err)
	}
	if _, err := phoneNetworksSet([]byte(`{"nftables": []}`), nil); err == nil {
		t.Error("no set accepted")
	}
}

func TestListeningTCP(t *testing.T) {
	got := ListeningTCP(procTCP)
	want := []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:5061"), netip.MustParseAddrPort("172.22.0.4:8089")}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
