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
}

// ConfigFromEnv reads the configuration from the environment, defaulting
// every path to the layout compose.yaml mounts.
func ConfigFromEnv(getenv func(string) string) Config {
	return Config{
		ConfDir:    envOr(getenv, "LINX_ASTERISK_CONF_DIR", "/etc/asterisk"),
		VarDir:     envOr(getenv, "LINX_ASTERISK_VAR_DIR", "/var/lib/asterisk"),
		RunDir:     envOr(getenv, "LINX_ASTERISK_RUN_DIR", "/var/run/asterisk"),
		ScratchDir: envOr(getenv, "LINX_ASTERISK_SCRATCH_DIR", "/tmp/asterisk"),
		CertsDir:   envOr(getenv, "LINX_CERTS_DIR", "/var/lib/linx/certs"),
		SIPPort:    5061,
	}
}

func envOr(getenv func(string) string, key, fallback string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return fallback
}

// Render writes every *.conf file this slice needs into c.ConfDir, creating
// c.RunDir and c.ScratchDir's subdirectories along the way. Realtime (ODBC),
// dialplan and ARI configuration arrive in later Phase 1B steps.
func (c Config) Render() error {
	for _, dir := range []string{c.ConfDir, c.RunDir, c.VarDir, filepath.Join(c.ScratchDir, "spool"), filepath.Join(c.ScratchDir, "log")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	files := map[string]string{
		"asterisk.conf":   c.asteriskConf(),
		"logger.conf":     loggerConf,
		"modules.conf":    modulesConf,
		"manager.conf":    managerConf,
		"http.conf":       httpConf,
		"extensions.conf": extensionsConf,
		"pjsip.conf":      c.pjsipConf(),
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
; the asterisk schema's realtime views (docs/PBX.md §3), added in Phase 1B
; step 2 — this slice only brings the TLS transport up.

[transport-tls]
type=transport
protocol=tls
bind=0.0.0.0:%d
cert_file=%s
priv_key_file=%s
method=tlsv1_2
`, c.SIPPort, filepath.Join(c.CertsDir, "current", "fullchain.pem"), filepath.Join(c.CertsDir, "current", "privkey.pem"))
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
