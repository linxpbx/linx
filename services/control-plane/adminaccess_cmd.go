package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/settings"
	"linxpbx.com/linx/internal/store"
	"linxpbx.com/linx/internal/weburl"
)

const adminAccessUsage = `Usage:
  linx admin-access          Show where admins may sign in from
  linx admin-access open     Let admins sign in from anywhere again

open is the way back in when "Only from my home/office network" shuts
every admin out (after moving Linx to a new server or a new network, say):
nothing in the browser can undo it from outside those networks. Admins
still need their password and their passkey or authenticator code. The
addresses you'd listed are kept, so you can turn the restriction back on
once this place's network is added.
`

// runAdminAccessCommand runs `admin-access ...` against the database from
// the container's own configuration. `linx admin-access` on the host runs
// it through docker exec, like `linx user`, so only someone who controls
// Docker on the server can use it: that's what makes it safe to skip "only
// from my home/office network" and "confirm it's you" (docs/ADMIN.md §3).
func runAdminAccessCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, adminAccessUsage)
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
	st := store.New(pool)
	return adminAccessCommand(ctx, st, &settings.Service{Store: st, Now: time.Now}, args, stdout, stderr)
}

func adminAccessCommand(ctx context.Context, st tenantSource, svc *settings.Service, args []string, stdout, stderr io.Writer) int {
	tenant, err := st.DefaultTenant(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the Linx database: %v\n", err)
		return 1
	}
	ctx = auth.WithPrincipal(ctx, auth.SystemPrincipal(tenant))
	cur, err := svc.Get(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the settings: %v\n", err)
		return 1
	}
	if len(args) == 0 || args[0] == "status" {
		if !cur.AdminNetworkRestricted {
			fmt.Fprintln(stdout, "Admins may sign in from anywhere (with their password and a passkey or authenticator code).")
			return 0
		}
		fmt.Fprintf(stdout, "Admins may sign in only from: %s, and the phone networks from setup.\n", networkList(cur))
		fmt.Fprintln(stdout, "Signed in from anywhere else, they see only the Dialer and Team. To let them in from anywhere: sudo linx admin-access open")
		return 0
	}
	if args[0] != "open" || len(args) != 1 {
		fmt.Fprintf(stderr, "Unknown admin-access command %q.\n\n%s", strings.Join(args, " "), adminAccessUsage)
		return 2
	}
	if cur.AdminNetworkRestricted {
		open := false
		if _, err := svc.Update(ctx, settings.Patch{AdminNetworkRestricted: &open}); err != nil {
			fmt.Fprintf(stderr, "Couldn't change it: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "Admins can sign in from anywhere again (still with their password and a passkey or authenticator code).")
		fmt.Fprintf(stdout, "The addresses that were allowed (%s) are kept for when you turn the restriction back on.\n", networkList(cur))
	} else {
		fmt.Fprintln(stdout, "Admins could already sign in from anywhere. Nothing changed.")
	}
	fmt.Fprintf(stdout, "\nSign in here:\n\n  %s\n\n", signInURL())
	fmt.Fprintln(stdout, "Can't sign in at all (forgotten password, lost authenticator)? Make a new set-password link: sudo linx user setup-link EMAIL")
	return 0
}

func networkList(s settings.Settings) string {
	if len(s.AdminNetworks) == 0 {
		return "no addresses"
	}
	parts := make([]string, len(s.AdminNetworks))
	for i, n := range s.AdminNetworks {
		parts[i] = n.String()
	}
	return strings.Join(parts, ", ")
}

func signInURL() string {
	if a := weburl.FromEnv(os.Getenv); a != "" {
		return a + "/"
	}
	return "https://<your domain>/"
}
