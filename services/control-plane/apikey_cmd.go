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
	"linxpbx.com/linx/internal/store"
)

const apiKeyUsage = `Usage:
  linx api-key create --name NAME --role ROLE [--scopes all] [--expires-in-days 365] [--allow-ip ADDR]...
  linx api-key list
  linx api-key revoke ID

create  Make a new API key. It is printed once; copy it somewhere safe.
          --role     system_admin, admin, user or reporter
          --scopes   comma-separated; "all" is every non-sensitive scope the
                     role allows. Sensitive scopes must be named, e.g.
                     --scopes all,api_keys:write
          --allow-ip only accept the key from this address or range (repeatable)
list    Show every key (never the secret).
revoke  Stop a key working immediately. ID is the id or the linx_... prefix
        shown by list.
`

// keyAdmin is the database access the api-key command needs.
type keyAdmin interface {
	DefaultTenant(ctx context.Context) (uuid.UUID, error)
	CreateCredential(ctx context.Context, c auth.Credential, audit auth.AuditEntry) error
	ListCredentials(ctx context.Context, kind string, tenant uuid.UUID, before *uuid.UUID, limit int) ([]auth.Credential, error)
	CredentialByPublicID(ctx context.Context, kind, publicID string) (auth.Credential, error)
	RevokeCredential(ctx context.Context, kind string, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) (auth.Credential, error)
}

// runAPIKeyCommand runs `api-key ...` against the database from the
// container's own configuration. `linx api-key` on the host runs it through
// docker exec, so only someone who controls Docker on the server can use it.
func runAPIKeyCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, apiKeyUsage)
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
	return apiKeyCommand(ctx, store.New(pool), args, stdout, stderr, time.Now())
}

func apiKeyCommand(ctx context.Context, st keyAdmin, args []string, stdout, stderr io.Writer, now time.Time) int {
	tenant, err := st.DefaultTenant(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the Linx database: %v\n", err)
		return 1
	}
	switch args[0] {
	case "create":
		return apiKeyCreate(ctx, st, tenant, args[1:], stdout, stderr, now)
	case "list":
		return apiKeyList(ctx, st, tenant, stdout, stderr, now)
	case "revoke":
		return apiKeyRevoke(ctx, st, tenant, args[1:], stdout, stderr, now)
	default:
		fmt.Fprintf(stderr, "Unknown api-key command %q.\n\n%s", args[0], apiKeyUsage)
		return 2
	}
}

type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

func apiKeyCreate(ctx context.Context, st keyAdmin, tenant uuid.UUID, args []string, stdout, stderr io.Writer, now time.Time) int {
	fs := flag.NewFlagSet("api-key create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "what the key is for")
	role := fs.String("role", "", "system_admin, admin, user or reporter")
	scopes := fs.String("scopes", auth.ScopeAll, "comma-separated scopes")
	days := fs.Int("expires-in-days", 365, "days until the key stops working (at most 730)")
	var allow repeated
	fs.Var(&allow, "allow-ip", "address or range the key may be used from (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "Unexpected %q. Put the name in quotes: --name \"My laptop\".\n", fs.Arg(0))
		return 2
	}
	if *role == "" {
		fmt.Fprintf(stderr, "Choose a role with --role (%s).\n", strings.Join(auth.Roles, ", "))
		return 2
	}
	if *days < 1 {
		fmt.Fprintln(stderr, "--expires-in-days must be at least 1.")
		return 2
	}
	expires := now.Add(time.Duration(*days) * 24 * time.Hour)

	caller := auth.SystemPrincipal(tenant)
	cred, key, err := auth.NewCredential(caller, auth.CredentialRequest{
		Kind:       auth.TypeAPIKey,
		Name:       *name,
		Role:       *role,
		Scopes:     splitList(*scopes),
		ExpiresAt:  &expires,
		AllowedIPs: allow,
	}, now)
	if err != nil {
		var e *apihttp.Error
		if errors.As(err, &e) {
			fmt.Fprintln(stderr, e.Detail)
			return 2
		}
		fmt.Fprintf(stderr, "Couldn't create the key: %v\n", err)
		return 1
	}
	audit := auth.AuditEntry{
		TenantID: &tenant,
		Actor:    caller.Actor(),
		Action:   auth.TypeAPIKey + ".create",
		Target:   auth.TypeAPIKey + ":" + cred.ID.String(),
		Result:   auth.ResultOK,
		Detail:   map[string]any{"name": cred.Name, "role": cred.Role, "scopes": cred.Scopes, "expires_at": cred.ExpiresAt},
	}
	if err := st.CreateCredential(ctx, cred, audit); err != nil {
		fmt.Fprintf(stderr, "Couldn't save the key: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "API key created. Copy it now: it won't be shown again.\n\n  %s\n\n", key)
	fmt.Fprintf(stdout, "Name:     %s\nRole:     %s\nScopes:   %s\nExpires:  %s\n",
		cred.Name, cred.Role, strings.Join(cred.Scopes, ", "), cred.ExpiresAt.Format("2 January 2006"))
	if len(allow) > 0 {
		fmt.Fprintf(stdout, "Only from: %s\n", strings.Join(allow, ", "))
	}
	fmt.Fprintf(stdout, "\nUse it as:  Authorization: Bearer %s...\n", auth.APIKeyPrefix+cred.PublicID)
	return 0
}

func apiKeyList(ctx context.Context, st keyAdmin, tenant uuid.UUID, stdout, stderr io.Writer, now time.Time) int {
	var all []auth.Credential
	var before *uuid.UUID
	for {
		page, err := st.ListCredentials(ctx, auth.TypeAPIKey, tenant, before, 200)
		if err != nil {
			fmt.Fprintf(stderr, "Couldn't list keys: %v\n", err)
			return 1
		}
		all = append(all, page...)
		if len(page) < 200 {
			break
		}
		before = &page[len(page)-1].ID
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, "No API keys yet. Create one with: linx api-key create --name \"My laptop\" --role admin")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tNAME\tROLE\tSTATUS\tLAST USED\tID")
	for _, c := range all {
		status := "active until " + c.ExpiresAt.Format("2006-01-02")
		switch {
		case c.RevokedAt != nil:
			status = "revoked " + c.RevokedAt.Format("2006-01-02")
		case !now.Before(c.ExpiresAt):
			status = "expired " + c.ExpiresAt.Format("2006-01-02")
		}
		last := "never"
		if c.LastUsedAt != nil {
			last = c.LastUsedAt.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s...\t%s\t%s\t%s\t%s\t%s\n", auth.APIKeyPrefix+c.PublicID, c.Name, c.Role, status, last, c.ID)
	}
	_ = tw.Flush()
	return 0
}

func apiKeyRevoke(ctx context.Context, st keyAdmin, tenant uuid.UUID, args []string, stdout, stderr io.Writer, now time.Time) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Say which key to revoke: linx api-key revoke ID (see linx api-key list).")
		return 2
	}
	ref := strings.TrimSuffix(strings.TrimSpace(args[0]), "...")
	id, err := uuid.Parse(ref)
	if err != nil {
		publicID := strings.TrimPrefix(ref, auth.APIKeyPrefix)
		// Accept a whole key too, but only look at its public part.
		if len(publicID) > 12 {
			publicID = publicID[:12]
		}
		if !auth.ValidPublicID(publicID) {
			fmt.Fprintf(stderr, "%q isn't a key id or linx_... prefix (see linx api-key list).\n", args[0])
			return 2
		}
		c, err := st.CredentialByPublicID(ctx, auth.TypeAPIKey, publicID)
		if err != nil || c.TenantID != tenant {
			fmt.Fprintln(stderr, "There is no API key with that id.")
			return 1
		}
		id = c.ID
	}
	caller := auth.SystemPrincipal(tenant)
	c, err := st.RevokeCredential(ctx, auth.TypeAPIKey, tenant, id, now, auth.AuditEntry{
		TenantID: &tenant,
		Actor:    caller.Actor(),
		Action:   auth.TypeAPIKey + ".revoke",
		Target:   auth.TypeAPIKey + ":" + id.String(),
		Result:   auth.ResultOK,
	})
	if errors.Is(err, auth.ErrNotFound) {
		fmt.Fprintln(stderr, "There is no API key with that id.")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't revoke the key: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Revoked %q (%s...). It stops working now.\n", c.Name, auth.APIKeyPrefix+c.PublicID)
	return 0
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
