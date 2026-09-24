// Command asterisk-entrypoint renders Asterisk's configuration from
// environment variables (internal/asteriskconf) and execs asterisk, so it
// becomes PID 1 and receives docker stop's SIGTERM directly.
package main

import (
	"log/slog"
	"os"
	"syscall"

	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/version"
)

const service = "linx-asterisk"

const asteriskBin = "/usr/sbin/asterisk"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
	log.Info("starting", "version", version.String(service))

	cfg := asteriskconf.ConfigFromEnv(os.Getenv)
	if err := cfg.Render(); err != nil {
		log.Error("render configuration", "err", err)
		os.Exit(2)
	}

	// unixODBC (res_odbc's driver) has no env var of its own for config
	// location; it reads ODBCINI (the odbc.ini file) and ODBCSYSINI (the
	// directory holding odbcinst.ini). Both must point at the rendered
	// config: the container's real /etc has none, and the root filesystem
	// is read-only. OPENSSL_CONF likewise: it sets the TLS 1.2 floor.
	env := append(os.Environ(), "ODBCINI="+cfg.ODBCIniPath(), "ODBCSYSINI="+cfg.ODBCSysIniDir(),
		"OPENSSL_CONF="+cfg.OpenSSLConfPath())

	argv := []string{asteriskBin, "-f"}
	log.Info("exec asterisk", "argv", argv)
	if err := syscall.Exec(asteriskBin, argv, env); err != nil {
		log.Error("exec asterisk", "err", err)
		os.Exit(1)
	}
}
