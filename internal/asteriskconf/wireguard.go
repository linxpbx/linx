package asteriskconf

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Trunks through a WireGuard tunnel (docs/TRUNKS.md §7) come in a file of
// their own per tunnel, WireGuardTrunkFile(interface) in TrunksDir, whose
// first line is WireGuardHeader followed by the tunnel's address and every
// address its trunks use. Asterisk only gets a tunnel's trunks while the
// kernel sends to each of those addresses from the tunnel's address, that
// is, into the tunnel: until linx-wireguard has brought it up (after
// Asterisk restarts, say), Asterisk has no such trunk, and nothing meant
// for the tunnel can leave unencrypted by the normal route.
const (
	WireGuardHeader = "; linx-wireguard:"
	// WireGuardTrunksFile is what the entrypoint builds in ConfDir from the
	// tunnels that pass, and pjsip.conf includes after TrunksFile.
	WireGuardTrunksFile = "pjsip_wireguard.conf"
)

// WireGuardTrunkFile is the name of the file for a tunnel's trunks.
func WireGuardTrunkFile(iface string) string { return "wg-" + iface + ".conf" }

// SourceFor is the address the kernel would send to dst from.
type SourceFor func(dst netip.Addr) (netip.Addr, error)

// KernelSource asks the kernel without sending anything: a connected UDP
// socket gets the route's source address.
func KernelSource(dst netip.Addr) (netip.Addr, error) {
	c, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(dst, 9)))
	if err != nil {
		return netip.Addr{}, err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap(), nil
}

// WireGuardTrunks builds WireGuardTrunksFile's contents from the tunnel
// files in TrunksDir whose addresses all route into their tunnel, and
// lists the tunnels held back.
func (c Config) WireGuardTrunks(source SourceFor) (content []byte, held []string, err error) {
	names, err := filepath.Glob(filepath.Join(c.TrunksDir, WireGuardTrunkFile("*")))
	if err != nil {
		return nil, nil, err
	}
	slices.Sort(names)
	var out bytes.Buffer
	out.WriteString("; Built by linx-asterisk-entrypoint: the trunks of every WireGuard tunnel\n; that's up (docs/TRUNKS.md §7).\n")
	for _, name := range names {
		b, err := os.ReadFile(name)
		if err != nil {
			return nil, nil, err
		}
		if why := routedIntoTunnel(b, source); why != "" {
			held = append(held, filepath.Base(name)+": "+why)
			continue
		}
		out.WriteString("\n")
		out.Write(b)
	}
	return out.Bytes(), held, nil
}

// routedIntoTunnel checks a tunnel file's header against the kernel,
// returning why not ("" when every address goes into the tunnel).
func routedIntoTunnel(file []byte, source SourceFor) string {
	line, _, _ := bytes.Cut(file, []byte("\n"))
	rest, ok := strings.CutPrefix(string(line), WireGuardHeader)
	if !ok {
		return "no header"
	}
	f := strings.Fields(rest)
	if len(f) < 2 {
		return "no addresses"
	}
	tunnel, err := netip.ParseAddr(f[0])
	if err != nil {
		return "bad tunnel address"
	}
	for _, s := range f[1:] {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return "bad address " + s
		}
		src, err := source(a)
		if err != nil {
			return fmt.Sprintf("no route to %s", a)
		}
		if src != tunnel {
			return fmt.Sprintf("%s isn't routed into the tunnel yet", a)
		}
	}
	return ""
}

// WriteWireGuardTrunks writes WireGuardTrunksFile, reporting whether it
// changed.
func (c Config) WriteWireGuardTrunks(content []byte) (bool, error) {
	path := filepath.Join(c.ConfDir, WireGuardTrunksFile)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
		return false, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, content, 0o640); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// WireGuardHeaderLine is the header trunkconf writes (kept here so both
// sides agree on it).
func WireGuardHeaderLine(tunnel netip.Addr, addrs []netip.Addr) string {
	var b strings.Builder
	b.WriteString(WireGuardHeader + " " + tunnel.String())
	for _, a := range addrs {
		b.WriteString(" " + a.String())
	}
	return b.String()
}
