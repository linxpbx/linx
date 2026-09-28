package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/settings"
	"linxpbx.com/linx/internal/store"
)

// runSuggestSiteCommand is `suggest-site home|business`, run by the web
// install (docker exec, like `user create --first-admin`) right after the
// first admin exists: the setup wizard's Place step then starts from the
// install's "Where is this server?" answer (docs/ui/INSTALL_SCREENS.md
// §3.5). It only fills in a place nobody has chosen yet. Not for people.
func runSuggestSiteCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, db.ConfigFromEnv(os.Getenv))
	if err != nil {
		fmt.Fprintf(stderr, "Can't reach the Linx database: %v\n", err)
		return 1
	}
	defer pool.Close()
	st := store.New(pool)
	return suggestSiteCommand(ctx, st, &settings.Service{Store: st, Now: time.Now}, args, stdout, stderr)
}

func suggestSiteCommand(ctx context.Context, st tenantSource, svc *settings.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != "home" && args[0] != "business") {
		fmt.Fprintln(stderr, "Usage: suggest-site home|business")
		return 2
	}
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
	if cur.SiteKind != "" {
		fmt.Fprintf(stdout, "The place is already chosen (%s): left as it is.\n", cur.SiteKind)
		return 0
	}
	kind := args[0]
	if _, err := svc.Update(ctx, settings.Patch{SiteKind: &kind}); err != nil {
		fmt.Fprintf(stderr, "Couldn't save it: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "The setup wizard starts from %q.\n", kind)
	return 0
}
