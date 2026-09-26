package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/trunk"
)

// wireguard runs `trunk wireguard add|list|remove`: the tunnels phone lines
// can connect through (docs/TRUNKS.md §7).
func (c *trunkCmd) wireguard(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(c.errw, trunkUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return c.wireguardAdd(ctx, args[1:])
	case "list":
		return c.wireguardList(ctx)
	case "remove":
		return c.wireguardRemove(ctx, args[1:])
	}
	fmt.Fprintf(c.errw, "Unknown command %q.\n\n%s", "wireguard "+args[0], trunkUsage)
	return 2
}

func (c *trunkCmd) profiles(ctx context.Context) ([]trunk.WireGuardProfile, error) {
	var out []trunk.WireGuardProfile
	var before *uuid.UUID
	for {
		page, err := c.svc.ListWireGuardProfiles(ctx, before, 100)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < 100 {
			return out, nil
		}
		before = &page[len(page)-1].ID
	}
}

// findProfile is the profile named name (case-insensitively), or with that
// id.
func (c *trunkCmd) findProfile(ctx context.Context, name string) (trunk.WireGuardProfile, bool, error) {
	profiles, err := c.profiles(ctx)
	if err != nil {
		return trunk.WireGuardProfile{}, false, err
	}
	for _, p := range profiles {
		if strings.EqualFold(p.Name, name) || p.ID.String() == name {
			return p, true, nil
		}
	}
	return trunk.WireGuardProfile{}, false, nil
}

// wireguardAdd imports a provider's wg-quick file from standard input: it
// holds a private key, so it never goes on the command line or the screen.
func (c *trunkCmd) wireguardAdd(ctx context.Context, args []string) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(c.errw, `Name the tunnel and give the provider's WireGuard file as input:
  sudo linx trunk wireguard add "Provider VPN" < provider.conf`)
		return 2
	}
	if c.interactive {
		fmt.Fprintln(c.errw, `Give the provider's WireGuard file as input, so its private key isn't typed or shown:
  sudo linx trunk wireguard add "`+args[0]+`" < provider.conf`)
		return 2
	}
	conf, err := io.ReadAll(io.LimitReader(c.in, 64<<10))
	if err != nil || len(strings.TrimSpace(string(conf))) == 0 {
		fmt.Fprintln(c.errw, "No WireGuard file came in. Run: sudo linx trunk wireguard add NAME < provider.conf")
		return 2
	}
	p, err := c.svc.CreateWireGuardProfile(ctx, trunk.WireGuardProfileInput{Name: args[0], Config: string(conf)})
	if err != nil {
		fmt.Fprintf(c.errw, "Couldn't save it: %s\n", explain(err))
		return 1
	}
	c.applied(ctx)
	fmt.Fprintf(c.out, "Saved WireGuard tunnel %q to %s:%d.\n", p.Name, p.PeerEndpointHost, p.PeerEndpointPort)
	for _, n := range p.Notes {
		fmt.Fprintf(c.out, "Note: %s\n", n)
	}
	fmt.Fprintf(c.out, "Linx's public key for it (if the provider asks): %s\n", p.PublicKey)
	fmt.Fprintf(c.out, "Connect a line through it with: sudo linx trunk add --wireguard %q\n", p.Name)
	return 0
}

func (c *trunkCmd) wireguardList(ctx context.Context) int {
	profiles, err := c.profiles(ctx)
	if err != nil {
		fmt.Fprintf(c.errw, "Can't read the Linx database: %v\n", err)
		return 1
	}
	if len(profiles) == 0 {
		fmt.Fprintln(c.out, "No WireGuard tunnels. Add one with: sudo linx trunk wireguard add NAME < provider.conf")
		return 0
	}
	trunks, err := c.all(ctx)
	if err != nil {
		fmt.Fprintf(c.errw, "Can't read the Linx database: %v\n", err)
		return 1
	}
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROVIDER\tSTATUS\tLINES\tLINX'S PUBLIC KEY")
	for _, p := range profiles {
		var lines []string
		for _, t := range trunks {
			if t.WireGuardProfileID != nil && *t.WireGuardProfileID == p.ID {
				lines = append(lines, t.Name)
			}
		}
		if len(lines) == 0 {
			lines = []string{"-"}
		}
		fmt.Fprintf(w, "%s\t%s:%d\t%s\t%s\t%s\n", p.Name, p.PeerEndpointHost, p.PeerEndpointPort, p.Status,
			strings.Join(lines, ", "), p.PublicKey)
	}
	w.Flush()
	for _, p := range profiles {
		if p.StatusDetail != "" {
			fmt.Fprintf(c.out, "\n%s: %s", p.Name, p.StatusDetail)
		}
	}
	fmt.Fprintln(c.out)
	return 0
}

func (c *trunkCmd) wireguardRemove(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("trunk wireguard remove", flag.ContinueOnError)
	fs.SetOutput(c.errw)
	yes := fs.Bool("yes", false, "don't ask to confirm")
	var name []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		name = append(name, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(name) != 1 {
		fmt.Fprintln(c.errw, `Name the tunnel to remove: linx trunk wireguard remove "Provider VPN"`)
		return 2
	}
	p, ok, err := c.findProfile(ctx, name[0])
	if err != nil {
		fmt.Fprintf(c.errw, "Can't read the Linx database: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(c.errw, "There is no WireGuard tunnel %q (see linx trunk wireguard list).\n", name[0])
		return 1
	}
	if !*yes {
		if !c.interactive {
			fmt.Fprintln(c.errw, "Add --yes to remove it without a terminal to confirm on.")
			return 2
		}
		if !c.confirm(fmt.Sprintf("Remove WireGuard tunnel %q?", p.Name)) {
			fmt.Fprintln(c.out, "Nothing removed.")
			return 1
		}
	}
	if err := c.svc.DeleteWireGuardProfile(ctx, p.ID); err != nil {
		fmt.Fprintf(c.errw, "Couldn't remove it: %s\n", explain(err))
		return 1
	}
	c.applied(ctx)
	fmt.Fprintf(c.out, "Removed %q.\n", p.Name)
	return 0
}
