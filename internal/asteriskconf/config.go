// Package asteriskconf renders Asterisk's *.conf files at container start
// (docs/PBX.md §2), from a small Config read from environment variables. It
// never edits a file by hand: every start renders a fresh set from scratch,
// like linx-certd's manager renders its own state.
package asteriskconf

import (
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
}

// ConfigFromEnv reads the configuration from the environment, defaulting
// every path to the layout compose.yaml mounts.
func ConfigFromEnv(getenv func(string) string) Config {
	return Config{
		ConfDir:        envOr(getenv, "LINX_ASTERISK_CONF_DIR", "/etc/asterisk"),
		VarDir:         envOr(getenv, "LINX_ASTERISK_VAR_DIR", "/var/lib/asterisk"),
		RunDir:         envOr(getenv, "LINX_ASTERISK_RUN_DIR", "/var/run/asterisk"),
		ScratchDir:     envOr(getenv, "LINX_ASTERISK_SCRATCH_DIR", "/tmp/asterisk"),
		CertsDir:       envOr(getenv, "LINX_CERTS_DIR", "/var/lib/linx/certs"),
		SIPPort:        5061,
		DBHost:         envOr(getenv, "LINX_DB_HOST", "postgres"),
		DBPort:         envOr(getenv, "LINX_DB_PORT", "5432"),
		DBName:         envOr(getenv, "LINX_DB_NAME", "linx"),
		DBPasswordFile: envOr(getenv, "LINX_ASTERISK_DB_PASSWORD_FILE", "/run/secrets/linx_asterisk_db_password"),
	}
}

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

// Render writes every *.conf file this slice needs into c.ConfDir, creating
// c.RunDir and c.ScratchDir's subdirectories along the way. Dialplan and ARI
// configuration arrive in a later Phase 1B step.
func (c Config) Render() error {
	for _, dir := range []string{c.ConfDir, c.RunDir, c.VarDir, filepath.Join(c.ScratchDir, "spool"), filepath.Join(c.ScratchDir, "log")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	dbPassword, err := os.ReadFile(c.DBPasswordFile)
	if err != nil {
		return fmt.Errorf("asterisk database password: %w", err)
	}

	files := map[string]string{
		"asterisk.conf":   c.asteriskConf(),
		"logger.conf":     loggerConf,
		"modules.conf":    modulesConf,
		"manager.conf":    managerConf,
		"http.conf":       httpConf,
		"extensions.conf": extensionsConf,
		"pjsip.conf":      c.pjsipConf(),
		"sorcery.conf":    sorceryConf,
		"extconfig.conf":  extconfigConf,
		"odbcinst.ini":    odbcinstIni,
		"odbc.ini":        c.odbcIni(),
		"res_odbc.conf":   c.resOdbcConf(strings.TrimSpace(string(dbPassword))),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(c.ConfDir, name), []byte(content), 0o640); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

func (c Config) asteriskConf() string {
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint at container start. Do not edit by
; hand: the next restart overwrites this file.

[directories](!)
astetcdir => %s
astmoddir => /usr/lib/asterisk/modules
astvarlibdir => %s
astdbdir => %s
astkeydir => %s
astdatadir => /var/lib/asterisk/data
astagidir => /var/lib/asterisk/agi-bin
astspooldir => %s
astrundir => %s
astlogdir => %s

[options]
verbose = 3
console = no
`, c.ConfDir, c.VarDir, c.VarDir, c.VarDir, filepath.Join(c.ScratchDir, "spool"), c.RunDir, filepath.Join(c.ScratchDir, "log"))
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
// otherwise (docs/PBX.md §2, ADR-031). ARI (res_ari) is the integration
// point, wired up in Phase 1B step 4.
const managerConf = `[general]
enabled = no
`

// httpConf keeps Asterisk's built-in HTTP server (ARI's REST/websocket
// transport) off until step 4 configures it with a step-ca certificate.
const httpConf = `[general]
enabled=no
`

// extensionsConf is empty: the dialplan (ring-all, *43, "not available")
// arrives in Phase 1B step 4.
const extensionsConf = `[general]
[globals]
`
