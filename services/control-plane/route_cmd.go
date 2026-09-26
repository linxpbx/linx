package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/store"
)

const routeUsage = `Usage:
  linx route test NUMBER [--from EXTENSION]

test  Show what Linx would do with a call to NUMBER, dialled the way
      you'd dial it on a mobile ("050 123 4567", "+44 20 7946 0958"),
      without making the call. With --from, it's the decision for a call
      from that extension, exactly as a real call would get it; without,
      it only says what kind of number it is.
`

// routeAdmin is the database access the route command needs.
type routeAdmin interface {
	DefaultTenant(ctx context.Context) (uuid.UUID, error)
	ExtensionByNumber(ctx context.Context, tenant uuid.UUID, number string) (pbx.Extension, error)
	Country(ctx context.Context) (string, error)
	Classify(ctx context.Context, home, dialled string) (numbering.Result, error)
	Route(ctx context.Context, extension uuid.UUID, dialled string) (numbering.Route, error)
}

// runRouteCommand runs `route ...` against the database from the
// container's own configuration (`linx route` on the host runs it through
// docker exec, like `linx user`).
func runRouteCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, routeUsage)
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
	return routeCommand(ctx, store.New(pool), args, stdout, stderr)
}

func routeCommand(ctx context.Context, st routeAdmin, args []string, stdout, stderr io.Writer) int {
	if args[0] != "test" {
		fmt.Fprintf(stderr, "Unknown route command %q.\n\n%s", args[0], routeUsage)
		return 2
	}
	fs := flag.NewFlagSet("route test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "the extension calling")
	// The number may come before or after --from.
	var number []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		number = append(number, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(number) != 1 {
		fmt.Fprintf(stderr, "Give one number to test, in quotes if it has spaces: linx route test \"050 123 4567\" --from 101\n")
		return 2
	}
	country, err := st.Country(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the Linx database: %v\n", err)
		return 1
	}
	if *from == "" {
		r, err := st.Classify(ctx, country, number[0])
		if err != nil {
			fmt.Fprintf(stderr, "Couldn't check the number: %v\n", err)
			return 1
		}
		if r.Category == numbering.Invalid {
			fmt.Fprintf(stdout, "%q isn't a number that can be called from %s.\n", number[0], numbering.CountryName(country, numbering.Countries[country]))
			return 0
		}
		fmt.Fprintf(stdout, "%s: %s.\nAdd --from EXTENSION to see whether that extension may call it.\n", r.Kind(), r.Pretty())
		return 0
	}
	tenant, err := st.DefaultTenant(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the Linx database: %v\n", err)
		return 1
	}
	ext, err := st.ExtensionByNumber(ctx, tenant, *from)
	if errors.Is(err, pbx.ErrNotFound) {
		fmt.Fprintf(stderr, "There is no extension %q.\n", *from)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the Linx database: %v\n", err)
		return 1
	}
	r, err := st.Route(ctx, ext.ID, number[0])
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't check the call: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, r.Explain(number[0], *from, country))
	return 0
}
