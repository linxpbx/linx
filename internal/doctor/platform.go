package doctor

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"strings"
	"syscall"
	"time"

	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/installer"
)

// Section is a group of results under one heading.
type Section struct {
	Name    string
	Results []Result
}

// Run runs every check, grouped for display. If Docker itself is down,
// nothing else can be checked, so that's the only result.
func Run(ctx context.Context, env Env, cfg installer.Config) []Section {
	if rs := docker(ctx, env); rs != nil {
		return []Section{{"Docker", rs}}
	}
	return []Section{
		{"Services", Services(ctx, env)},
		{"Certificates", certificates(ctx, env, cfg)},
		{"Database, access and alerts", Database(ctx, env)},
		{"Phone system", Phones(ctx, env, cfg)},
		{"Phone lines", Lines(ctx, env)},
		{"Calls from outside", append(Relay(ctx, env), FrontDoor(ctx, env, cfg)...)},
		{"Secrets", Secrets(env)},
	}
}

func docker(ctx context.Context, env Env) []Result {
	if _, err := env.Runner.Run(ctx, nil, "docker", "version", "--format", "{{.Server.Version}}"); err != nil {
		var rs results
		rs.fail("Docker isn't running, so Linx can't run either.",
			"Start it: sudo systemctl start docker. If it isn't installed, run: sudo linx setup")
		return rs
	}
	return nil
}

// Container names from deploy/compose/compose.yaml.
const (
	postgresContainer     = "linx-postgres"
	controlPlaneContainer = "linx-control-plane"
)

// Services checks the containers Phase 1 added: the database and the API
// service. (The certificate services are checked under Certificates.)
func Services(ctx context.Context, env Env) []Result {
	var rs results
	service(ctx, env, &rs, postgresContainer, "The database")
	service(ctx, env, &rs, controlPlaneContainer, "The API service")
	return rs
}

func service(ctx context.Context, env Env, rs *results, container, label string) {
	status, health, err := containerState(ctx, env.Runner, container)
	logs := "Look at its log for the reason: sudo docker logs --tail 50 " + container
	switch {
	case err != nil:
		rs.fail(fmt.Sprintf("%s (%s) isn't installed.", label, container), rerunSetup)
	case status != "running":
		rs.fail(fmt.Sprintf("%s (%s) isn't running (it's %s).", label, container, status),
			"Start it: "+composeCmd+" up --detach. If it stops again: "+logs)
	case health == "starting":
		rs.warn(label+" is still starting.", "Run linx doctor again in a minute.")
	case health != "" && health != "healthy":
		rs.fail(label+" is running but not answering.", logs)
	default:
		rs.ok(label + " is running and answering.")
	}
}

// platformQuery reads everything Database reports in one round trip, as
// JSON (so titles and messages with any characters in them parse safely).
const platformQuery = `SELECT json_build_object(
	'schema', (SELECT coalesce(max(version), 0) FROM schema_migrations),
	'api_keys', (SELECT count(*) FROM api_key WHERE revoked_at IS NULL AND expires_at > now()),
	'alert_channels', (SELECT count(*) FROM alert_channel WHERE enabled),
	'open_alerts', (SELECT coalesce(json_agg(json_build_object(
		'severity', severity, 'title', title, 'message', message, 'since', first_seen_at)
		ORDER BY first_seen_at), '[]') FROM alert WHERE status = 'open'))`

// psqlArgs runs one read-only query as the linx user over the container's
// local socket (the Postgres image trusts local connections), printing just
// the value.
func psqlArgs(container, query string) []string {
	return []string{"exec", container, "psql", "--username", "linx", "--dbname", "linx",
		"--no-psqlrc", "--quiet", "--tuples-only", "--no-align", "--command", query}
}

type platformState struct {
	Schema        int `json:"schema"`
	APIKeys       int `json:"api_keys"`
	AlertChannels int `json:"alert_channels"`
	OpenAlerts    []struct {
		Severity, Title, Message string
		Since                    time.Time
	} `json:"open_alerts"`
}

// Database reports the schema version, whether anything can use the API,
// whether anyone will hear about problems, and every open alert. It reads
// the database directly (docker exec psql, read-only queries), so it works
// before any API key exists.
func Database(ctx context.Context, env Env) []Result {
	var rs results
	if status, _, err := containerState(ctx, env.Runner, postgresContainer); err != nil || status != "running" {
		return rs // already reported under Services
	}
	out, err := env.Runner.Run(ctx, nil, "docker", psqlArgs(postgresContainer, platformQuery)...)
	var st platformState
	if err == nil {
		err = json.Unmarshal([]byte(strings.TrimSpace(string(out))), &st)
	}
	if err != nil {
		rs.fail("Can't read Linx's settings from the database.",
			"The API service sets the database up when it starts; check it's running (above), then: sudo docker logs --tail 50 "+controlPlaneContainer)
		return rs
	}
	rs.ok(fmt.Sprintf("The database is set up (schema version %d).", st.Schema))

	if st.APIKeys == 0 {
		rs.warn("No API key exists yet, so nothing can manage Linx through its API.",
			`Create one: sudo linx api-key create --name "My laptop" --role admin`)
	} else {
		rs.ok(fmt.Sprintf("%s can use the API.", count(st.APIKeys, "API key")))
	}

	if st.AlertChannels == 0 {
		rs.warn("No alert channels are turned on, so Linx can't tell you when something breaks.",
			"Add one (ntfy, Gotify, Slack, Teams, Telegram or a webhook): see docs/API.md §5.")
	} else {
		rs.ok(fmt.Sprintf("%s will be told about problems.", count(st.AlertChannels, "alert channel")))
	}

	if len(st.OpenAlerts) == 0 {
		rs.ok("No open alerts.")
	}
	for _, a := range st.OpenAlerts {
		msg := fmt.Sprintf("Open alert since %s: %s", a.Since.Local().Format("2 Jan 15:04"), a.Title)
		fix := a.Message + " Linx closes the alert by itself once the problem is fixed."
		switch a.Severity {
		case "critical":
			rs.fail(msg, fix)
		case "warning":
			rs.warn(msg, fix)
		default:
			rs.ok(msg + ". " + a.Message)
		}
	}
	return rs
}

// secretFiles are the Docker secrets the installer writes, with the exact
// size a key must have (0: any non-empty value).
var secretFiles = []struct {
	path string
	size int64
	what string
}{
	{installer.DNSTokenPath, 0, "DNS provider token"},
	{installer.DBPasswordPath, 0, "database password"},
	{installer.DBEncryptionKeyPath, dbsecret.KeySize, "database encryption key"},
	{installer.JWTSigningKeyPath, ed25519.SeedSize, "API token signing key"},
	{installer.AsteriskDBPasswordPath, 0, "phone system database password"},
	{installer.ARIPasswordPath, 0, "phone system control connection password"},
	{installer.TURNSecretPath, 0, "call relay secret"},
	{installer.CAServicesPasswordPath, 0, "service certificate password"},
}

// Secrets checks the installer's secret files exist, are the right size,
// and aren't readable by everyone on the server.
func Secrets(env Env) []Result {
	var rs results
	bad := 0
	for _, s := range secretFiles {
		fi, err := env.Stat(s.path)
		switch {
		case err != nil:
			rs.fail("The "+s.what+" is missing ("+s.path+"). Linx can't start without it.", rerunSetup)
		case fi.Mode().Perm()&0o007 != 0:
			rs.fail("The "+s.what+" ("+s.path+") can be read by anyone on this server.",
				"Fix its permissions: sudo chmod 440 "+s.path)
		case !ownedByRoot(fi.Sys()):
			rs.fail("The "+s.what+" ("+s.path+") isn't owned by root.", rerunSetup)
		case fi.Size() == 0 || s.size != 0 && fi.Size() != s.size:
			rs.fail("The "+s.what+" ("+s.path+") is damaged (wrong size).",
				"Restore it from your backup. Otherwise, to make a new one: sudo linx setup --config "+installer.ConfigPath+
					" (anything it protected, such as stored webhook and alert channel secrets, has to be set up again).")
		default:
			continue
		}
		bad++
	}
	if bad == 0 {
		rs.ok("The secret files are present and readable only by root and Linx's services.")
	}
	return rs
}

// ownedByRoot reports whether a file's owner is root. Unknown (not a Unix
// stat) counts as yes: the permission check still applies.
func ownedByRoot(sys any) bool {
	st, ok := sys.(*syscall.Stat_t)
	return !ok || st.Uid == 0
}

func count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
