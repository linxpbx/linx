// Package asteriskconf renders Asterisk's *.conf files at container start
// (docs/PBX.md §2), from a small Config read from environment variables. It
// never edits a file by hand: every start renders a fresh set from scratch,
// like linx-certd's manager renders its own state.
package asteriskconf

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the Asterisk container's configuration.
type Config struct {
	// ConfDir is where *.conf files are rendered (a tmpfs mount: the
	// container's root filesystem is read-only).
	ConfDir string
	// VarDir is astvarlibdir/astdbdir/astkeydir: the asterisk-state volume,
	// which holds the local registrations database and generated keys.
	VarDir string
	// RunDir holds the console socket and PID file (a tmpfs mount).
	RunDir string
	// ScratchDir holds the spool and log directories (a tmpfs mount): spool
	// is unused this slice, and logs go to stdout, not files.
	ScratchDir string
	// CertsDir is linx-certd's certs volume (docs/ops/CERT_RELOAD.md):
	// CertsDir/current/{fullchain,privkey}.pem is always the deployed pair.
	CertsDir string
	// SIPPort is the PJSIP TLS transport's bind port.
	SIPPort int
	// DBHost, DBPort and DBName are Postgres's address (the same database
	// control-plane uses; ADR-032). Asterisk only ever reads the asterisk
	// schema's realtime views there, as the linx_asterisk role.
	DBHost, DBPort, DBName string
	// DBPasswordFile is the linx_asterisk role's password (docs/PBX.md §3;
	// Docker secret linx_asterisk_db_password), set by the control plane at
	// every startup so it always matches what's rendered here.
	DBPasswordFile string
	// DataDir is astdatadir: sound prompts, ARI's REST model and docs, shipped
	// in the image (read-only) rather than in the asterisk-state volume, so an
	// image update always brings its own copy.
	DataDir string
	// ARIURL is the control plane's ARI websocket, which Asterisk connects out
	// to (ARI outbound websocket, ADR-034). Asterisk has no ARI listener.
	ARIURL string
	// ARIPasswordFile is the password Asterisk presents to the control plane
	// (Docker secret linx_ari_password).
	ARIPasswordFile string
	// CARootFile is the internal CA's root certificate: Asterisk checks the
	// control plane's step-ca certificate against it.
	CARootFile string
}

// ConfigFromEnv reads the configuration from the environment, defaulting
// every path to the layout compose.yaml mounts.
func ConfigFromEnv(getenv func(string) string) Config {
	return Config{
		ConfDir:         envOr(getenv, "LINX_ASTERISK_CONF_DIR", "/etc/asterisk"),
		VarDir:          envOr(getenv, "LINX_ASTERISK_VAR_DIR", "/var/lib/asterisk"),
		RunDir:          envOr(getenv, "LINX_ASTERISK_RUN_DIR", "/var/run/asterisk"),
		ScratchDir:      envOr(getenv, "LINX_ASTERISK_SCRATCH_DIR", "/tmp/asterisk"),
		CertsDir:        envOr(getenv, "LINX_CERTS_DIR", "/var/lib/linx/certs"),
		SIPPort:         5061,
		DBHost:          envOr(getenv, "LINX_DB_HOST", "postgres"),
		DBPort:          envOr(getenv, "LINX_DB_PORT", "5432"),
		DBName:          envOr(getenv, "LINX_DB_NAME", "linx"),
		DBPasswordFile:  envOr(getenv, "LINX_ASTERISK_DB_PASSWORD_FILE", "/run/secrets/linx_asterisk_db_password"),
		DataDir:         envOr(getenv, "LINX_ASTERISK_DATA_DIR", "/usr/share/asterisk"),
		ARIURL:          envOr(getenv, "LINX_ARI_URL", "wss://linx-ari:8089/ari"),
		ARIPasswordFile: envOr(getenv, "LINX_ARI_PASSWORD_FILE", "/run/secrets/linx_ari_password"),
		CARootFile:      envOr(getenv, "LINX_CA_ROOT_FILE", "/etc/linx/ca/root_ca.crt"),
	}
}

// ARIApp is the ARI application the control plane serves: every event
// Asterisk sends over the outbound websocket is addressed to it.
const ARIApp = "linx"

// ARIUser is the username Asterisk presents to the control plane's ARI
// websocket, with the linx_ari_password secret.
const ARIUser = "asterisk"

// ODBCIniPath and ODBCSysIniDir are where Render puts unixODBC's config
// (ConfDir is the only writable directory this image has). The entrypoint
// points unixODBC at them with the ODBCINI and ODBCSYSINI environment
// variables before it execs asterisk.
func (c Config) ODBCIniPath() string   { return filepath.Join(c.ConfDir, "odbc.ini") }
func (c Config) ODBCSysIniDir() string { return c.ConfDir }

// odbcDriverPath is where the runtime image's psqlODBC package installs its
// driver, symlinked to this fixed, architecture-independent path by
// deploy/docker/asterisk.Dockerfile.
const odbcDriverPath = "/usr/lib/psqlodbcw.so"

func envOr(getenv func(string) string, key, fallback string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return fallback
}

// Render writes every *.conf file Asterisk needs into c.ConfDir, creating
// c.RunDir and c.ScratchDir's subdirectories along the way.
func (c Config) Render() error {
	for _, dir := range []string{c.ConfDir, c.RunDir, c.VarDir, filepath.Join(c.ScratchDir, "spool"), filepath.Join(c.ScratchDir, "log")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	dbPassword, err := readSecret(c.DBPasswordFile)
	if err != nil {
		return fmt.Errorf("asterisk database password: %w", err)
	}
	ariPassword, err := readSecret(c.ARIPasswordFile)
	if err != nil {
		return fmt.Errorf("ARI password: %w", err)
	}
	if !strings.HasPrefix(c.ARIURL, "wss://") {
		return fmt.Errorf("LINX_ARI_URL %q: must be wss:// (ARI never runs without TLS)", c.ARIURL)
	}

	files := map[string]string{
		"asterisk.conf":         c.asteriskConf(),
		"logger.conf":           loggerConf,
		"modules.conf":          modulesConf,
		"manager.conf":          managerConf,
		"http.conf":             httpConf,
		"ari.conf":              ariConf(rand.Text()),
		"websocket_client.conf": c.websocketClientConf(ariPassword),
		"extensions.conf":       extensionsConf,
		"func_odbc.conf":        funcOdbcConf,
		"pjsip.conf":            c.pjsipConf(),
		"sorcery.conf":          sorceryConf,
		"extconfig.conf":        extconfigConf,
		"odbcinst.ini":          odbcinstIni,
		"odbc.ini":              c.odbcIni(),
		"res_odbc.conf":         c.resOdbcConf(dbPassword),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(c.ConfDir, name), []byte(content), 0o640); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

// readSecret reads a Docker secret for use as an Asterisk config value.
// Asterisk's config syntax has no quoting: ";" starts a comment and a line
// break ends the value, so a secret containing either would be silently
// truncated (or worse, inject config). The installer's secrets never do.
func readSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" || strings.ContainsAny(s, ";\r\n") {
		return "", fmt.Errorf("%s: empty, or contains characters Asterisk config can't hold", path)
	}
	return s, nil
}

func (c Config) asteriskConf() string {
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint at container start. Do not edit by
; hand: the next restart overwrites this file.

[directories]
astetcdir => %s
astmoddir => /usr/lib/asterisk/modules
astvarlibdir => %s
astdbdir => %s
astkeydir => %s
astdatadir => %s
astagidir => %s
astspooldir => %s
astrundir => %s
astlogdir => %s

[options]
verbose = 3
console = no
`, c.ConfDir, c.VarDir, c.VarDir, c.VarDir, c.DataDir, filepath.Join(c.DataDir, "agi-bin"),
		filepath.Join(c.ScratchDir, "spool"), c.RunDir, filepath.Join(c.ScratchDir, "log"))
}

func (c Config) pjsipConf() string {
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint. Endpoints, AORs and auths come from
; the asterisk schema's realtime views over ODBC (docs/PBX.md §3;
; sorcery.conf, extconfig.conf, res_odbc.conf) — nothing else belongs here.

[transport-tls]
type=transport
protocol=tls
bind=0.0.0.0:%d
cert_file=%s
priv_key_file=%s
method=tlsv1_2
`, c.SIPPort, filepath.Join(c.CertsDir, "current", "fullchain.pem"), filepath.Join(c.CertsDir, "current", "privkey.pem"))
}

// sorceryConf points PJSIP's endpoint/auth/aor objects at the realtime
// engine instead of pjsip.conf (docs/PBX.md §3, ADR-032). "ps_endpoints" etc.
// are realtime family names, resolved to the odbc DSN by extconfig.conf.
const sorceryConf = `; Rendered by linx-asterisk-entrypoint.
[res_pjsip]
endpoint=realtime,ps_endpoints
auth=realtime,ps_auths
aor=realtime,ps_aors
`

// extconfigConf maps each realtime family sorcery.conf asks for to the
// "asterisk" ODBC DSN (res_odbc.conf) and the asterisk-schema view of the
// same name (migration 0005).
const extconfigConf = `; Rendered by linx-asterisk-entrypoint.
[settings]
ps_endpoints => odbc,asterisk,ps_endpoints
ps_auths => odbc,asterisk,ps_auths
ps_aors => odbc,asterisk,ps_aors
`

// odbcinstIni declares the PostgreSQL ODBC driver at the fixed path
// deploy/docker/asterisk.Dockerfile symlinks it to, so this file doesn't
// need to know the runtime image's architecture-specific library path.
const odbcinstIni = `; Rendered by linx-asterisk-entrypoint.
[PostgreSQL]
Description=PostgreSQL ODBC driver
Driver=` + odbcDriverPath + `
`

// odbcIni defines the "asterisk" DSN res_odbc.conf connects through: this
// server's Postgres, read-only, and scoped to the asterisk schema so an
// unqualified "SELECT * FROM ps_endpoints" (what res_config_odbc sends)
// finds the realtime views without Asterisk needing to know the schema
// name.
func (c Config) odbcIni() string {
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint.
[asterisk]
Description=Linx phone system realtime data
Driver=PostgreSQL
Servername=%s
Port=%s
Database=%s
ReadOnly=Yes
ConnSettings=SET search_path TO asterisk
`, c.DBHost, c.DBPort, c.DBName)
}

// resOdbcConf is Asterisk's own side of the ODBC connection: the
// linx_asterisk role's credentials (its password is a Docker secret the
// control plane also reads, to keep this role's actual password in sync;
// docs/PBX.md §3) and pooling. No cache (ADR-032): every registration and
// call re-reads the database, so a revoked device stops at once.
func (c Config) resOdbcConf(dbPassword string) string {
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint.
[asterisk]
dsn=asterisk
username=linx_asterisk
password=%s
max_connections=5
pre-connect=yes
sanitysql=SELECT 1
`, dbPassword)
}

// loggerConf sends every log line to the container's stdout: the read-only
// root filesystem has no writable log directory, and `docker logs` is where
// Linx expects every service's logs (control-plane and certd use slog to
// stdout the same way).
const loggerConf = `[general]
; Queues aren't built into this image yet (docs/PBX.md §2), so there's
; nothing to log.
queue_log = no

[logfiles]
/dev/stdout => notice,warning,error,verbose
`

// modulesConf loads exactly the modules the build's menuselect step chose
// (deploy/docker/asterisk.Dockerfile): autoload can't load anything else.
const modulesConf = `[modules]
autoload=yes
`

// managerConf keeps the Manager Interface off: no AMI, over the network or
// otherwise (docs/PBX.md §2, ADR-031). ARI is the integration point.
const managerConf = `[general]
enabled = no
`

// httpConf keeps Asterisk's built-in HTTP server off for good: ARI runs over
// the websocket Asterisk opens to the control plane, REST requests included
// (ARI REST over websocket), so nothing in this container listens for ARI.
const httpConf = `[general]
enabled=no
`

// ariConf serves the "linx" ARI app over the outbound websocket to the
// control plane, subscribed to every channel, bridge, endpoint and device
// state event (docs/PBX.md §4). REST requests arriving on that websocket act
// as a read-only local user: the control plane can look but not change calls
// in this slice. The local user's password is random per start and unused
// (http.conf is off, so there's nowhere to log in with it), but ari.conf
// requires one.
func ariConf(localPassword string) string {
	return `; Rendered by linx-asterisk-entrypoint.
[general]
enabled = yes
pretty = no
websocket_write_timeout = 1000

[linx-local]
type = user
read_only = yes
password_format = plain
password = ` + localPassword + `

[linx]
type = outbound_websocket
websocket_client_id = linx-control-plane
apps = ` + ARIApp + `
subscribe_all = yes
local_ari_user = linx-local
`
}

// websocketClientConf is the outbound connection itself: TLS only, the
// control plane's certificate checked against the internal CA's root and its
// hostname, and a password proving to the control plane that this is
// Asterisk. Persistent connections retry forever on their own.
func (c Config) websocketClientConf(password string) string {
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint.
[linx-control-plane]
type = websocket_client
uri = %s
protocols = ari
username = %s
password = %s
connection_type = persistent
connection_timeout = 3000
reconnect_interval = 2000
reconnect_attempts = 30
tls_enabled = yes
ca_list_file = %s
verify_server_cert = yes
verify_server_hostname = yes
`, c.ARIURL, ARIUser, password, c.CARootFile)
}

// funcOdbcConf defines LINX_RING_TARGETS(number): a Dial() string ringing
// every enabled device of that extension ("PJSIP/d_a&PJSIP/d_b"), read from
// the linx_ring_targets view. No row at all means no such extension; one row
// with an empty string means the extension exists but has no devices. The
// dialplan tells the two apart with ${ODBCROWS}. Device usernames match
// ^d_[A-Za-z0-9]{8}$ (migration 0005), so nothing from the database can
// smuggle dial options into the string.
const funcOdbcConf = `; Rendered by linx-asterisk-entrypoint.
[RING_TARGETS]
prefix = LINX
dsn = asterisk
readsql = SELECT coalesce(string_agg('PJSIP/' || aor, '&' ORDER BY aor), '') FROM linx_ring_targets WHERE number = '${SQL_ESC(${ARG1})}' HAVING count(*) > 0
`

// extensionsConf is the whole dialplan (docs/PBX.md §4): *43 echo test,
// ring-all for extension numbers (2–6 digits, like the extension table's
// check), and spoken messages for everything that can't ring. A device's
// caller ID number is its extension (the ps_endpoints view sets it, and
// PJSIP ignores what the phone claims), so "calling yourself" is a plain
// comparison.
const extensionsConf = `; Rendered by linx-asterisk-entrypoint.
[general]
static = yes
writeprotect = yes
clearglobalvars = yes

[globals]

[linx-extensions]
exten => *43,1,Answer()
 same => n,Wait(0.5)
 same => n,Echo()
 same => n,Hangup()

exten => _XX,1,Goto(linx-ring,${EXTEN},1)
exten => _XXX,1,Goto(linx-ring,${EXTEN},1)
exten => _XXXX,1,Goto(linx-ring,${EXTEN},1)
exten => _XXXXX,1,Goto(linx-ring,${EXTEN},1)
exten => _XXXXXX,1,Goto(linx-ring,${EXTEN},1)

; Anything else a phone can dial: not a Linx number.
exten => _[0-9*#+].,1,Goto(linx-messages,not-in-use,1)
exten => _[0-9*#+],1,Goto(linx-messages,not-in-use,1)

[linx-ring]
exten => _X.,1,Set(TARGETS=${LINX_RING_TARGETS(${EXTEN})})
 same => n,GotoIf($[${ODBCROWS} < 1]?linx-messages,not-in-use,1)
 same => n,GotoIf($["${CALLERID(num)}" = "${EXTEN}"]?linx-messages,not-available,1)
 same => n,GotoIf($["${TARGETS}" = ""]?linx-messages,not-available,1)
 same => n,Dial(${TARGETS},30)
 same => n,GotoIf($["${DIALSTATUS}" = "ANSWER"]?done)
 same => n,Goto(linx-messages,not-available,1)
 same => n(done),Hangup()

[linx-messages]
exten => not-in-use,1,Answer()
 same => n,Wait(0.5)
 same => n,Playback(ss-noservice)
 same => n,Hangup()

exten => not-available,1,Answer()
 same => n,Wait(0.5)
 same => n,Playback(vm-nobodyavail)
 same => n,Hangup()
`
