// Command asterisk-entrypoint renders Asterisk's configuration from
// environment variables (internal/asteriskconf), starts asterisk, and stays
// beside it to reload its TLS certificate when linx-certd renews it
// (certwatch.go). It forwards docker stop's SIGTERM to Asterisk and exits
// with Asterisk's exit status.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/version"
)

const service = "linx-asterisk"

const asteriskBin = "/usr/sbin/asterisk"

// certCheckInterval is how often the certificate is checked. Renewals
// happen 30 days before expiry, so a minute is plenty;
// LINX_CERT_CHECK_INTERVAL overrides it for tests.
const certCheckInterval = time.Minute

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

	interval := certCheckInterval
	if v := os.Getenv("LINX_CERT_CHECK_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			log.Error("LINX_CERT_CHECK_INTERVAL: want a duration like 30s", "value", v)
			os.Exit(2)
		}
		interval = d
	}
	watcher := &certWatcher{certsDir: cfg.CertsDir, reload: reloadPJSIP, log: log}
	watcher.start()

	// Signals Docker sends are forwarded to Asterisk; Asterisk's own exit
	// ends the container.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)

	cmd := exec.Command(asteriskBin, "-f")
	cmd.Env, cmd.Stdout, cmd.Stderr = env, os.Stdout, os.Stderr
	log.Info("start asterisk", "argv", cmd.Args)
	if err := cmd.Start(); err != nil {
		log.Error("start asterisk", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go watcher.run(ctx, interval)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	for {
		select {
		case s := <-sigs:
			_ = cmd.Process.Signal(s)
		case err := <-done:
			cancel()
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				log.Info("asterisk exited", "status", exit.ExitCode())
				os.Exit(max(exit.ExitCode(), 1))
			}
			if err != nil {
				log.Error("asterisk", "err", err)
				os.Exit(1)
			}
			log.Info("asterisk exited")
			os.Exit(0)
		}
	}
}

// reloadPJSIP makes Asterisk read its TLS certificate again, through its
// console socket (the same way the health check talks to it).
func reloadPJSIP(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, asteriskBin, "-rx", "module reload res_pjsip.so").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), "reloaded successfully") {
		return fmt.Errorf("unexpected answer: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
