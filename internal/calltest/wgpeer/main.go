//go:build linux

// Command wgpeer is the call suite's WireGuard "provider" (docs/TRUNKS.md
// §7): run as root with NET_ADMIN in a container, it brings up wg0 with
// the given keys, listens for Linx's end, routes Linx's tunnel address
// into it, and waits. SIPp then runs in the same network namespace as the
// provider behind the tunnel.
//
// With -delete NAME it instead removes interface NAME from the namespace
// it runs in (the suite's stand-in for a tunnel vanishing) and exits.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func main() {
	key := flag.String("key", "", "this end's private key")
	peer := flag.String("peer", "", "Linx's public key")
	addr := flag.String("addr", "10.99.0.1", "this end's tunnel address")
	allowed := flag.String("allowed", "10.99.0.2", "Linx's tunnel address")
	port := flag.Int("port", 51820, "UDP port to listen on")
	del := flag.String("delete", "", "remove this interface and exit")
	flag.Parse()

	if *del != "" {
		l, err := netlink.LinkByName(*del)
		if err != nil {
			log.Fatal(err)
		}
		if err := netlink.LinkDel(l); err != nil {
			log.Fatal(err)
		}
		fmt.Println("deleted", *del)
		return
	}

	priv, err := wgtypes.ParseKey(*key)
	check(err)
	pub, err := wgtypes.ParseKey(*peer)
	check(err)
	local := netip.MustParseAddr(*addr)
	remote := netip.MustParseAddr(*allowed)

	check(netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{Name: "wg0", MTU: 1420}}))
	link, err := netlink.LinkByName("wg0")
	check(err)
	c, err := wgctrl.New()
	check(err)
	check(c.ConfigureDevice("wg0", wgtypes.Config{PrivateKey: &priv, ListenPort: port, Peers: []wgtypes.PeerConfig{{
		PublicKey: pub, ReplaceAllowedIPs: true, AllowedIPs: []net.IPNet{{IP: remote.AsSlice(), Mask: net.CIDRMask(32, 32)}},
	}}}))
	check(netlink.AddrAdd(link, &netlink.Addr{IPNet: &net.IPNet{IP: local.AsSlice(), Mask: net.CIDRMask(32, 32)}}))
	check(netlink.LinkSetUp(link))
	check(netlink.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Scope: netlink.SCOPE_LINK,
		Dst: &net.IPNet{IP: remote.AsSlice(), Mask: net.CIDRMask(32, 32)}, Src: local.AsSlice()}))
	fmt.Println("wg0 up")

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	t := time.NewTicker(5 * time.Second)
	for {
		select {
		case <-sigs:
			return
		case <-t.C:
			if d, err := c.Device("wg0"); err == nil && len(d.Peers) > 0 && !d.Peers[0].LastHandshakeTime.IsZero() {
				fmt.Println("handshake", d.Peers[0].LastHandshakeTime.UTC().Format(time.RFC3339))
			}
		}
	}
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
