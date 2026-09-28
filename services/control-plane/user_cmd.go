package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
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
  linx user reset-2fa EMAIL

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
reset-2fa   For someone who lost both their second sign-in step (their
            authenticator app or passkeys) and their recovery codes,
            including the last system admin, whom nobody can reset from the
            browser. Turns off their authenticator app and passkeys and
            signs them out everywhere; they sign in with their password and
            set up a new one (admins must, before anything else). Their
            password stays the same. Recorded in the activity log, and your
            alert channels are told.
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
	// A one-off alert, sent by the running control plane's alert engine
	// at its next tick (this process only records it).
	engine := &alert.Engine{Store: st, Sender: &alert.Sender{Now: time.Now}}
	return userCommand(ctx, st, &auth.Accounts{Store: st, Now: time.Now}, engine, args, stdout, stderr)
}

// announcer tells the alert channels about something that happened once
// (alert.Engine.Announce).
type announcer interface {
	Announce(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string) error
}

func userCommand(ctx context.Context, st userAdmin, accounts *auth.Accounts, alerts announcer, args []string, stdout, stderr io.Writer) int {
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
	case "reset-2fa":
		return userReset2FA(ctx, st, accounts, alerts, tenant, args[1:], stdout, stderr)
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

// userReset2FA is the way back in for someone who lost every second step
// and every recovery code (docs/ADMIN.md §7): the same reset as People →
// "Reset authenticator", from the server, where it's allowed for anyone —
// the last system admin included — because only someone who controls the
// server can run it.
func userReset2FA(ctx context.Context, st userAdmin, accounts *auth.Accounts, alerts announcer, tenant uuid.UUID, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Say whose: linx user reset-2fa EMAIL (see linx user list).")
		return 2
	}
	u, err := st.UserByEmail(ctx, tenant, strings.ToLower(strings.TrimSpace(args[0])))
	if err != nil {
		fmt.Fprintln(stderr, "There is no person with that email.")
		return 1
	}
	if !u.HasSecondStep() {
		fmt.Fprintf(stdout, "%s (%s) has no authenticator app or passkey to reset. They can sign in with their password.\n", u.Name, u.Email)
		fmt.Fprintln(stdout, "If they've forgotten it: sudo linx user setup-link "+u.Email)
		return 0
	}
	if _, err := accounts.ResetMFA(ctx, u.ID); err != nil {
		fmt.Fprintf(stderr, "Couldn't reset it: %v\n", err)
		return 1
	}
	if alerts != nil {
		msg := fmt.Sprintf("Someone on the server turned off the authenticator app and passkeys of %s (%s, %s) with sudo linx user reset-2fa. "+
			"If nobody you know did this, someone may have control of your server.", u.Name, u.Email, u.Role)
		if err := alerts.Announce(ctx, tenant, "user.mfa_reset_cli:"+uuid.Must(uuid.NewV7()).String(), alert.SeverityWarning,
			"A second sign-in step was reset on the server", msg, ""); err != nil {
			fmt.Fprintf(stderr, "Reset, but couldn't tell your alert channels: %v\n", err)
		}
	}
	fmt.Fprintf(stdout, "Done. %s (%s) is signed out everywhere and has no authenticator app or passkey now.\n", u.Name, u.Email)
	if slices.Contains([]string{auth.RoleSystemAdmin, auth.RoleAdmin}, u.Role) {
		fmt.Fprintln(stdout, "They sign in with their password, then must set up a new passkey or authenticator app before anything else.")
	} else {
		fmt.Fprintln(stdout, "They sign in with their password, then can add a new passkey or authenticator app in My account.")
	}
	fmt.Fprintln(stdout, "If they've forgotten the password too: sudo linx user setup-link "+u.Email)
	return 0
}

func printSetupLink(stdout io.Writer, u auth.User, token string) {
	domain := os.Getenv("LINX_DOMAIN")
	if domain == "" {
		domain = "<your domain>"
	}
	fmt.Fprintf(stdout, "%s (%s, %s) can now set their password. Send them this link yourself — it works once, for 24 hours:\n\n  https://%s/setup/%s\n\n", u.Name, u.Email, u.Role, domain, token)
}
