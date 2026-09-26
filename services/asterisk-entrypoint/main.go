// Command asterisk-entrypoint renders Asterisk's configuration from
// environment variables (internal/asteriskconf), starts asterisk, and stays
// beside it to reload its TLS certificates when they're renewed
// (internal/certs.Watcher): the phones' one from linx-certd, and the browser
// websocket's one from the control plane; and PJSIP when the control plane
// renders the trunks again (trunkWatcher). It also reports every trunk's
// registration and keep-alive state for the control plane (statusWriter,
// internal/trunkstatus). It forwards docker stop's SIGTERM to Asterisk and
// exits with Asterisk's exit status.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/trunkstatus"
	"linxpbx.com/linx/internal/version"
)

const service = "linx-asterisk"

const asteriskBin = "/usr/sbin/asterisk"

// certCheckInterval is how often the certificate is checked. Renewals
// happen 30 days before expiry, so a minute is plenty;
// LINX_CERT_CHECK_INTERVAL overrides it for tests.
const certCheckInterval = time.Minute

// controlPlaneWait is how long Asterisk's start waits for what the control
// plane writes for it: the browser websocket's certificate, and the trunks.
const controlPlaneWait = 2 * time.Minute

// trunkCheckInterval is how often the trunk file is checked for changes.
const trunkCheckInterval = 2 * time.Second

// statusInterval is how often trunks' state is read from Asterisk's
// console for the control plane.
const statusInterval = 10 * time.Second

// waitForCertificate waits up to limit for dir/current/fullchain.pem.
func waitForCertificate(dir string, limit time.Duration, log *slog.Logger) bool {
	return waitForFile(filepath.Join(dir, certs.CurrentLink, certs.FullchainFile), limit, log,
		"the browser websocket certificate", "browsers can't call until Asterisk restarts")
}

// waitForFile waits up to limit for path, which the control plane writes.
func waitForFile(path string, limit time.Duration, log *slog.Logger, what, without string) bool {
	deadline := time.Now().Add(limit)
	logged := false
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			log.Warn("no "+what+" yet; starting without it ("+without+")", "path", path)
			return false
		}
		if !logged {
			log.Info("waiting for the control plane to write "+what, "path", path)
			logged = true
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// trunkWatcher reloads PJSIP when the control plane's rendered trunks
// (internal/trunkconf: pjsip_trunks.conf, which pjsip.conf includes, and
// the pinned certificates) change, after rebuilding the TLS transport's CA
// list from the pinned certificates. Reloading keeps phones' connections,
// registrations and calls (docs/TRUNKS.md §4).
type trunkWatcher struct {
	cfg    asteriskconf.Config
	reload func(context.Context) error
	log    *slog.Logger
	loaded [sha256.Size]byte
}

// state is a hash of both files' contents (a missing file counts as empty).
func (w *trunkWatcher) state() [sha256.Size]byte {
	h := sha256.New()
	for _, name := range []string{asteriskconf.TrunksFile, asteriskconf.PinnedCAFile} {
		b, _ := os.ReadFile(filepath.Join(w.cfg.TrunksDir, name))
		fmt.Fprintf(h, "%d:", len(b))
		h.Write(b)
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// Start builds the CA list from what's there now, and records it as what
// Asterisk is about to load (a change while it's starting gets a spare
// reload, never a missed one).
func (w *trunkWatcher) Start() {
	w.loaded = w.state()
	if skipped, err := w.cfg.WriteTrunkCA(); err != nil {
		w.log.Error("building the trunks' CA list", "err", err)
	} else if skipped > 0 {
		w.log.Warn("pinned trunk certificates that can't be read were left out", "count", skipped)
	}
}

// Check reloads if the trunks changed; a failed reload is retried at the
// next check.
func (w *trunkWatcher) Check(ctx context.Context) {
	s := w.state()
	if s == w.loaded {
		return
	}
	skipped, err := w.cfg.WriteTrunkCA()
	if err != nil {
		w.log.Error("building the trunks' CA list; retrying", "err", err)
		return
	}
	if skipped > 0 {
		w.log.Warn("pinned trunk certificates that can't be read were left out", "count", skipped)
	}
	if err := w.reload(ctx); err != nil {
		w.log.Error("reloading the trunks failed; retrying at the next check", "err", err)
		return
	}
	w.loaded = s
	w.log.Info("trunks reloaded")
}

func (w *trunkWatcher) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Check(ctx)
		}
	}
}

// statusWriter asks Asterisk's console for every trunk's registration and
// keep-alive state and writes it where the control plane reads it
// (internal/trunkstatus): ARI has nothing for outbound registrations.
type statusWriter struct {
	dir     string
	console func(ctx context.Context, command string) (string, error)
	now     func() time.Time
	log     *slog.Logger
	failing bool
}

// Write asks once and writes the file. While Asterisk doesn't answer,
// nothing is written: the file ages, and the control plane treats trunks
// as unknown rather than down.
func (w *statusWriter) Write(ctx context.Context) {
	regs, err := w.console(ctx, "pjsip show registrations")
	var contacts string
	if err == nil {
		contacts, err = w.console(ctx, "pjsip show contacts")
	}
	if err == nil {
		err = trunkstatus.Write(w.dir, trunkstatus.File{WrittenAt: w.now().UTC(),
			Trunks: trunkstatus.Merge(trunkstatus.ParseRegistrations(regs), trunkstatus.ParseContacts(contacts))})
	}
	if err != nil {
		if !w.failing {
			w.log.Warn("can't report the trunks' state", "err", err)
			w.failing = true
		}
		return
	}
	if w.failing {
		w.log.Info("reporting the trunks' state again")
		w.failing = false
	}
}

func (w *statusWriter) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Write(ctx)
		}
	}
}

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
	// Asterisk reads current/{fullchain,privkey}.pem only when the module
	// using it loads; reloading that module makes it read them again without
	// dropping connected phones or calls (checked against the real image:
	// PJSIP's transport and the web server's open websockets stay up, only
	// the certificate changes).
	watchers := []*certs.Watcher{{Dir: cfg.CertsDir, Reload: reloadModule("res_pjsip.so"), Log: log.With("cert", "phones")}}
	if cfg.SIPWSHost != "none" {
		watchers = append(watchers, &certs.Watcher{Dir: cfg.SIPWSCertsDir, Reload: reloadModule("http"),
			Log: log.With("cert", "browser websocket")})
	}
	// Asterisk's web server (the browser websocket) doesn't start if its
	// certificate is missing when Asterisk starts, and a later reload
	// doesn't bring it back. The control plane writes that certificate
	// moments after it starts, so wait for it; if it never comes, start
	// anyway: phones on the LAN don't need it.
	if cfg.SIPWSHost != "none" {
		waitForCertificate(cfg.SIPWSCertsDir, controlPlaneWait, log)
	}
	// Likewise the trunks: the control plane renders them at every start
	// (an empty file when there are none). If it never comes, phones work
	// and trunks join as soon as it's written.
	waitForFile(filepath.Join(cfg.TrunksDir, asteriskconf.TrunksFile), controlPlaneWait, log,
		"the trunks", "no trunks until the control plane writes them")
	trunks := &trunkWatcher{cfg: cfg, reload: reloadModule("res_pjsip.so"), log: log.With("config", "trunks")}
	trunks.Start()
	for _, w := range watchers {
		w.Start()
	}

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
	for _, w := range watchers {
		go w.Run(ctx, interval)
	}
	go trunks.Run(ctx, min(interval, trunkCheckInterval))
	if dir := os.Getenv("LINX_TRUNK_STATUS_DIR"); dir != "none" {
		if dir == "" {
			dir = "/var/lib/linx/trunk-status"
		}
		status := &statusWriter{dir: dir, console: console, now: time.Now, log: log.With("report", "trunk status")}
		go status.Run(ctx, min(interval, statusInterval))
	}
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
				if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
					log.Error("asterisk was killed", "signal", ws.Signal().String(), "core_dumped", ws.CoreDump())
					os.Exit(1)
				}
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

// console runs one command on Asterisk's console and returns its output.
func console(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, asteriskBin, "-rx", command).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", command, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// reloadModule makes Asterisk reload a module, and with it the TLS
// certificate that module reads (res_pjsip.so: the phones' transport; http:
// the browser websocket's listener), through its console socket (the same
// way the health check talks to it).
func reloadModule(module string) func(context.Context) error {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, asteriskBin, "-rx", "module reload "+module).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		if !strings.Contains(string(out), "reloaded successfully") {
			return fmt.Errorf("unexpected answer: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
}
