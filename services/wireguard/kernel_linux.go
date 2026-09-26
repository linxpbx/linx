package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"linxpbx.com/linx/internal/wgconf"
)

// tunnelMTU leaves room for WireGuard's own headers over a 1500-byte link,
// as wg-quick does.
const tunnelMTU = 1420

// kernel is Net over netlink and the kernel's WireGuard (wgctrl).
type kernel struct {
	wg *wgctrl.Client
}

func newKernel() (*kernel, error) {
	c, err := wgctrl.New()
	if err != nil {
		return nil, err
	}
	return &kernel{wg: c}, nil
}

func (k *kernel) Close() error { return k.wg.Close() }

func (k *kernel) Links() ([]Link, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	var out []Link
	for _, l := range links {
		li := Link{Name: l.Attrs().Name, Type: l.Type()}
		addrs, err := netlink.AddrList(l, netlink.FAMILY_V4)
		if err != nil && !errors.Is(err, netlink.ErrDumpInterrupted) {
			return nil, err
		}
		for _, a := range addrs {
			if p, ok := prefixOf(a.IPNet); ok {
				li.Prefixes = append(li.Prefixes, p.Masked())
			}
		}
		out = append(out, li)
	}
	return out, nil
}

func (k *kernel) ApplyTunnel(t wgconf.Tunnel, allowed []netip.Addr) error {
	link, err := netlink.LinkByName(t.Interface)
	var notFound netlink.LinkNotFoundError
	if errors.As(err, &notFound) {
		err = netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{Name: t.Interface, MTU: tunnelMTU}})
		if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) {
			return errNoWireGuard
		}
		if err != nil {
			return fmt.Errorf("creating %s: %w", t.Interface, err)
		}
		link, err = netlink.LinkByName(t.Interface)
	}
	if err != nil {
		return err
	}
	if link.Type() != "wireguard" {
		return fmt.Errorf("%s exists and isn't a WireGuard interface", t.Interface)
	}

	cfg, err := deviceConfig(t, allowed)
	if err != nil {
		return err
	}
	// Keep the peer (and its session) when it's the same one; remove any
	// other.
	if dev, err := k.wg.Device(t.Interface); err == nil {
		for _, p := range dev.Peers {
			if p.PublicKey != cfg.Peers[0].PublicKey {
				cfg.Peers = append(cfg.Peers, wgtypes.PeerConfig{PublicKey: p.PublicKey, Remove: true})
			}
		}
	}
	if err := k.wg.ConfigureDevice(t.Interface, cfg); err != nil {
		return fmt.Errorf("configuring %s: %w", t.Interface, err)
	}

	own := &netlink.Addr{IPNet: &net.IPNet{IP: t.Address.AsSlice(), Mask: net.CIDRMask(32, 32)}}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if !a.IPNet.IP.Equal(own.IP) {
			netlink.AddrDel(link, &a)
		}
	}
	if err := netlink.AddrReplace(link, own); err != nil {
		return fmt.Errorf("addressing %s: %w", t.Interface, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return err
	}

	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4,
		&netlink.Route{Table: wgconf.RouteTable, LinkIndex: link.Attrs().Index}, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_OIF)
	if err != nil && !errors.Is(err, netlink.ErrDumpInterrupted) {
		return err
	}
	for _, r := range routes {
		if p, ok := prefixOf(r.Dst); !ok || p.Bits() != 32 || !slices.Contains(allowed, p.Addr()) {
			netlink.RouteDel(&r)
		}
	}
	for _, a := range allowed {
		r := &netlink.Route{LinkIndex: link.Attrs().Index, Table: wgconf.RouteTable, Scope: netlink.SCOPE_LINK,
			Dst: &net.IPNet{IP: a.AsSlice(), Mask: net.CIDRMask(32, 32)}, Src: t.Address.AsSlice()}
		if err := netlink.RouteReplace(r); err != nil {
			return fmt.Errorf("routing %s into %s: %w", a, t.Interface, err)
		}
	}
	return nil
}

// deviceConfig is t as wgctrl applies it: one peer, allowed only.
func deviceConfig(t wgconf.Tunnel, allowed []netip.Addr) (wgtypes.Config, error) {
	priv, err := wgtypes.ParseKey(t.PrivateKey)
	if err != nil {
		return wgtypes.Config{}, errors.New("its private key isn't a WireGuard key")
	}
	pub, err := wgtypes.ParseKey(t.PeerPublicKey)
	if err != nil {
		return wgtypes.Config{}, errors.New("the provider's public key isn't a WireGuard key")
	}
	var psk wgtypes.Key // all zero: none
	if t.PresharedKey != "" {
		if psk, err = wgtypes.ParseKey(t.PresharedKey); err != nil {
			return wgtypes.Config{}, errors.New("its preshared key isn't a WireGuard key")
		}
	}
	mark := wgconf.FirewallMark
	keepalive := time.Duration(t.Keepalive) * time.Second
	peer := wgtypes.PeerConfig{PublicKey: pub, PresharedKey: &psk, PersistentKeepaliveInterval: &keepalive,
		ReplaceAllowedIPs: true, AllowedIPs: []net.IPNet{}}
	if t.Endpoint.IsValid() {
		peer.Endpoint = net.UDPAddrFromAddrPort(t.Endpoint)
	}
	for _, a := range allowed {
		peer.AllowedIPs = append(peer.AllowedIPs, net.IPNet{IP: a.AsSlice(), Mask: net.CIDRMask(32, 32)})
	}
	return wgtypes.Config{PrivateKey: &priv, FirewallMark: &mark, Peers: []wgtypes.PeerConfig{peer}}, nil
}

func (k *kernel) RemoveTunnel(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return err
	}
	return netlink.LinkDel(link) // its routes go with it
}

func (k *kernel) EnsureRule() error {
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil && !errors.Is(err, netlink.ErrDumpInterrupted) {
		return err
	}
	for _, r := range rules {
		if r.Table == wgconf.RouteTable && r.Mark == wgconf.FirewallMark && r.Invert {
			return nil
		}
	}
	r := netlink.NewRule()
	r.Family = netlink.FAMILY_V4
	r.Table = wgconf.RouteTable
	r.Mark = wgconf.FirewallMark
	mask := uint32(0xffffffff)
	r.Mask = &mask
	r.Invert = true
	r.Priority = wgconf.RulePriority
	return netlink.RuleAdd(r)
}

func (k *kernel) Device(name string) (Device, error) {
	dev, err := k.wg.Device(name)
	if err != nil {
		return Device{}, err
	}
	var d Device
	for _, p := range dev.Peers {
		if p.LastHandshakeTime.After(d.LastHandshake) {
			d.LastHandshake = p.LastHandshakeTime
		}
		d.RxBytes += p.ReceiveBytes
		d.TxBytes += p.TransmitBytes
	}
	return d, nil
}

func prefixOf(n *net.IPNet) (netip.Prefix, bool) {
	if n == nil {
		return netip.Prefix{}, false
	}
	a, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	ones, _ := n.Mask.Size()
	return netip.PrefixFrom(a.Unmap(), ones), true
}
