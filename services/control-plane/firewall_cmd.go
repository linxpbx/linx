package main

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"time"

	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/firewallsync"
	"linxpbx.com/linx/internal/store"
	"linxpbx.com/linx/internal/trunk"
	"linxpbx.com/linx/internal/trunkconf"
)

const firewallUsage = `Usage:
  linx-control-plane firewall addresses

addresses  Print, one per line, "<set> <address>": the IP-authenticated
           trunks' resolved addresses the host firewall's trunk_addresses
           and trunk_plain_addresses sets should hold right now
           (docs/TRUNKS.md §12), and "keep" first if a trunk's name
           didn't resolve (withdraw nothing this time). linx-firewall-sync
           runs this through docker exec, the same trust as linx user;
           nothing else calls it.
`

// firewallTrunks is the database access the firewall command needs.
type firewallTrunks interface {
	AllTrunks(ctx context.Context) ([]trunk.Trunk, error)
}

// runFirewallCommand runs `firewall ...` against the database from the
// container's own configuration, the same way runUserCommand does.
func runFirewallCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, firewallUsage)
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, db.ConfigFromEnv(os.Getenv))
	if err != nil {
		fmt.Fprintf(stderr, "Can't reach the Linx database: %v\n", err)
		return 1
	}
	defer pool.Close()
	resolve := func(ctx context.Context, host string) []netip.Addr { return trunkconf.ResolveHost(ctx, host, nil) }
	return firewallCommand(ctx, store.New(pool), resolve, args, stdout, stderr)
}

func firewallCommand(ctx context.Context, st firewallTrunks, resolve func(context.Context, string) []netip.Addr,
	args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "addresses" {
		fmt.Fprintf(stderr, "Unknown firewall command.\n\n%s", firewallUsage)
		return 2
	}
	trunks, err := st.AllTrunks(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Can't read trunks: %v\n", err)
		return 1
	}
	unresolved := false
	tls, plain := trunk.FirewallAddresses(ctx, trunks, func(ctx context.Context, host string) []netip.Addr {
		addrs := resolve(ctx, host)
		if len(addrs) == 0 {
			unresolved = true
		}
		return addrs
	})
	if unresolved {
		fmt.Fprintln(stdout, firewallsync.KeepLine)
	}
	for _, a := range tls {
		fmt.Fprintf(stdout, "trunk_addresses %s\n", a)
	}
	for _, a := range plain {
		fmt.Fprintf(stdout, "trunk_plain_addresses %s\n", a)
	}
	return 0
}
