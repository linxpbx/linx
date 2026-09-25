package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/store"
)

const userUsage = `Usage:
  linx user create --email EMAIL --name NAME --role ROLE [--extension NUMBER]
  linx user list
  linx user setup-link EMAIL
  linx user unlock EMAIL

create      Add a person and print a one-time set-password link (24
            hours), valid once. Hand it to them yourself (there's no email
            sending yet).
              --role       system_admin, admin, user or reporter
              --extension  the extension number they answer on the web client
list        Show every person (never passwords).
setup-link  Issue a fresh one-time set-password link for an existing
            person, e.g. after their old one expired.
unlock      Let someone sign in again at once after too many wrong
            passwords or codes, instead of waiting (list shows who's
            locked). Their password stays the same.
`

// userAdmin is the database access the user command needs.
type userAdmin interface {
	auth.UserStore
	DefaultTenant(ctx context.Context) (uuid.UUID, error)
	ExtensionByNumber(ctx context.Context, tenant uuid.UUID, number string) (pbx.Extension, error)
}

// runUserCommand runs `user ...` against the database from the container's
// own configuration. `linx user` on the host runs it through docker exec,
// like `linx api-key`, so only someone who controls Docker on the server
// can use it.
func runUserCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, userUsage)
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
	return userCommand(ctx, st, &auth.Accounts{Store: st, Now: time.Now}, args, stdout, stderr)
}

func userCommand(ctx context.Context, st userAdmin, accounts *auth.Accounts, args []string, stdout, stderr io.Writer) int {
	tenant, err := st.DefaultTenant(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the Linx database: %v\n", err)
		return 1
	}
	ctx = auth.WithPrincipal(ctx, auth.SystemPrincipal(tenant))
	switch args[0] {
	case "create":
		return userCreate(ctx, st, accounts, tenant, args[1:], stdout, stderr)
	case "list":
		return userList(ctx, accounts, stdout, stderr)
	case "setup-link":
		return userSetupLink(ctx, st, accounts, tenant, args[1:], stdout, stderr)
	case "unlock":
		return userUnlock(ctx, st, accounts, tenant, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown user command %q.\n\n%s", args[0], userUsage)
		return 2
	}
}

func userCreate(ctx context.Context, st userAdmin, accounts *auth.Accounts, tenant uuid.UUID, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	email := fs.String("email", "", "their email address")
	name := fs.String("name", "", "their name")
	role := fs.String("role", "", "system_admin, admin, user or reporter")
	extension := fs.String("extension", "", "the extension number they answer on the web client")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "Unexpected %q. Put the name in quotes: --name \"Jamie Lee\".\n", fs.Arg(0))
		return 2
	}
	in := auth.UserInput{Email: *email, Name: *name, Role: *role}
	if *extension != "" {
		e, err := st.ExtensionByNumber(ctx, tenant, *extension)
		if err != nil {
			fmt.Fprintf(stderr, "There is no extension %q.\n", *extension)
			return 1
		}
		in.ExtensionID = &e.ID
	}
	u, token, err := accounts.CreateUser(ctx, in)
	if err != nil {
		var e *apihttp.Error
		if errors.As(err, &e) {
			fmt.Fprintln(stderr, e.Detail)
			return 2
		}
		fmt.Fprintf(stderr, "Couldn't create the person: %v\n", err)
		return 1
	}
	printSetupLink(stdout, u, token)
	return 0
}

func userList(ctx context.Context, accounts *auth.Accounts, stdout, stderr io.Writer) int {
	var all []auth.User
	var before *uuid.UUID
	for {
		page, err := accounts.ListUsers(ctx, before, 200)
		if err != nil {
			fmt.Fprintf(stderr, "Couldn't list people: %v\n", err)
			return 1
		}
		all = append(all, page...)
		if len(page) < 200 {
			break
		}
		before = &page[len(page)-1].ID
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, `No people yet. Add one with: linx user create --email "..." --name "..." --role admin`)
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EMAIL\tNAME\tROLE\tSTATUS\tMFA\tID")
	for _, u := range all {
		status := "active"
		if u.DisabledAt != nil {
			status = "disabled"
		} else if u.LockedUntil != nil && time.Now().Before(*u.LockedUntil) {
			status = "locked until " + u.LockedUntil.Local().Format("15:04")
		}
		mfa := "off"
		if u.MFAEnabled {
			mfa = "on"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", u.Email, u.Name, u.Role, status, mfa, u.ID)
	}
	_ = tw.Flush()
	return 0
}

func userSetupLink(ctx context.Context, st userAdmin, accounts *auth.Accounts, tenant uuid.UUID, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Say who it's for: linx user setup-link EMAIL (see linx user list).")
		return 2
	}
	u, err := st.UserByEmail(ctx, tenant, strings.ToLower(strings.TrimSpace(args[0])))
	if err != nil {
		fmt.Fprintln(stderr, "There is no person with that email.")
		return 1
	}
	token, err := accounts.CreateSetupLink(ctx, u.ID)
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't create the link: %v\n", err)
		return 1
	}
	printSetupLink(stdout, u, token)
	return 0
}

func userUnlock(ctx context.Context, st userAdmin, accounts *auth.Accounts, tenant uuid.UUID, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Say who to unlock: linx user unlock EMAIL (see linx user list).")
		return 2
	}
	u, err := st.UserByEmail(ctx, tenant, strings.ToLower(strings.TrimSpace(args[0])))
	if err != nil {
		fmt.Fprintln(stderr, "There is no person with that email.")
		return 1
	}
	wasLocked := u.LockedUntil != nil && time.Now().Before(*u.LockedUntil)
	if _, err := accounts.UnlockUser(ctx, u.ID); err != nil {
		fmt.Fprintf(stderr, "Couldn't unlock them: %v\n", err)
		return 1
	}
	if wasLocked {
		fmt.Fprintf(stdout, "%s (%s) can sign in again now.\n", u.Name, u.Email)
	} else {
		fmt.Fprintf(stdout, "%s (%s) wasn't locked. Their wrong-try count is reset.\n", u.Name, u.Email)
	}
	return 0
}

func printSetupLink(stdout io.Writer, u auth.User, token string) {
	domain := os.Getenv("LINX_DOMAIN")
	if domain == "" {
		domain = "<your domain>"
	}
	fmt.Fprintf(stdout, "%s (%s, %s) can now set their password. Send them this link yourself — it works once, for 24 hours:\n\n  https://meet.%s/setup/%s\n\n", u.Name, u.Email, u.Role, domain, token)
}
