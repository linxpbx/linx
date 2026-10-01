package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"linxpbx.com/linx/internal/hostinfo"
	"linxpbx.com/linx/internal/install"
	"linxpbx.com/linx/internal/installer"
	"linxpbx.com/linx/internal/ops"
	"linxpbx.com/linx/internal/publicip"
)

// The web-first install's terminal (docs/INSTALL.md §1 and §3,
// docs/ui/INSTALL_SCREENS.md §1): checks, Docker, the installer's stack,
// then the linx-setup service, which does the rest with the browser; the
// terminal only shows the link and follows the service's progress.

// webEnv is what the web install touches beyond setupEnv, so tests can
// fake it.
type webEnv struct {
	// routeAddress is the default route's source address: where port 6464
	// is published (installer.DefaultRouteAddress).
	routeAddress  func() (netip.Addr, bool)
	publicAddress func(ctx context.Context) (netip.Addr, error)
	statePath     string
	now           func() time.Time
	// secureCheck says whether https://<domain> works from here (nil: not
	// checked).
	secureCheck func(ctx context.Context, domain string, port int) error
	// wait pauses between looks at the service's progress; false once ctx
	// has ended.
	wait func(ctx context.Context, d time.Duration) bool
	// repairPath is the repair link's state (install.RepairPath).
	repairPath string
	// execute runs a plan on the host (nil: installer.Plan.Execute with
	// the setup runner; tests record instead).
	execute func(ctx context.Context, p installer.Plan) error
}

func realWebEnv() webEnv {
	return webEnv{
		routeAddress: installer.DefaultRouteAddress,
		publicAddress: func(ctx context.Context) (netip.Addr, error) {
			ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			return publicip.Lookup(ctx, &http.Client{Timeout: 8 * time.Second}, publicip.TraceURL)
		},
		statePath:   install.HostPath,
		repairPath:  install.RepairPath,
		now:         time.Now,
		secureCheck: secureAddressWorks,
		wait: func(ctx context.Context, d time.Duration) bool {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(d):
				return true
			}
		},
	}
}

type webOptions struct {
	dryRun, newLink, noSignIn, replaceDocker bool
}

// linkWait is how long the terminal waits for the service's first link.
const linkWait = 90 * time.Second

func runWebSetup(ctx context.Context, o webOptions, stdout, stderr io.Writer, env setupEnv) int {
	if !o.dryRun && !env.isRoot {
		fmt.Fprintln(stderr, "linx setup changes system settings and must run as root. Try: sudo linx setup")
		return 1
	}
	if cfg, err := loadSetupConfig("", env); err == nil && cfg.Installed() {
		return runServerSettings(ctx, o, cfg, stdout, stderr, env)
	}
	// Ctrl-C (or a dropped SSH session) only stops this terminal's view:
	// the install carries on in the linx-setup service.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	w := env.web
	active := unitActive(ctx, env.runner)
	if active && o.newLink && !o.dryRun {
		fmt.Fprintln(stdout, "Cancelling the link and the browser using it…")
		_, _ = env.runner.Run(ctx, nil, "systemctl", "stop", install.Unit)
		if err := install.Cancel(w.statePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stderr, "Can't cancel the old link:", err)
			return 1
		}
		active = false
	}
	if active {
		return followInstall(ctx, stdout, env, true)
	}

	// 1. The server.
	fmt.Fprintln(stdout, "Checking this server")
	host := env.detect()
	findings := installer.CheckHost(host)
	for _, f := range findings {
		fmt.Fprintf(stdout, "  %s %s\n", findingMark(f.Level), f.Message)
	}
	if installer.HasFailure(findings) {
		fmt.Fprintln(stderr, "\nSetup can't continue until the problems above are fixed.")
		return 1
	}
	if user := installer.PortUser(ctx, env.runner, install.Port); user != "" && !installerRunning(ctx, env.runner) {
		fmt.Fprintf(stdout, "  ✕ Port %d is used by %s. Linx's installer needs it: stop that, then run setup again.\n", install.Port, user)
		return 1
	}
	if user := installer.PortUser(ctx, env.runner, installer.PublicPort); user != "" {
		fmt.Fprintf(stdout, "  ! Port 443 is used by %s. In the browser, choose the program in front of Linx\n"+
			"    (nginx, Caddy, …), or stop it so Linx can take port 443 itself.\n", user)
	} else {
		fmt.Fprintf(stdout, "  ✓ Ports 443 and %d are free\n", install.Port)
	}
	address, ok := w.routeAddress()
	if !ok {
		fmt.Fprintln(stderr, "\nCan't tell this server's network address. Check it's connected, with a default route, and run setup again.")
		return 1
	}

	// 2. Docker, the linx command, and the installer's stack.
	var dockerPlan installer.Plan
	docker := installer.DetectDocker(ctx, env.runner)
	action, msg := docker.Assess()
	switch action {
	case installer.DockerBlocked:
		fmt.Fprintln(stderr, "\n"+msg)
		return 1
	case installer.DockerInstall, installer.DockerUpgrade:
		if docker.Source == installer.SourceDistro && !o.replaceDocker {
			if n := runningContainers(ctx, env.runner); n > 0 {
				fmt.Fprintf(stderr, "\n%s\nThis server's own Docker package is running %d container(s). Linx replaces it with\n"+
					"Docker's official one, which restarts them (their images and data are kept).\n"+
					"Re-run with --replace-docker when that's all right:  sudo linx setup --replace-docker\n", msg, n)
				return 1
			}
		}
		dp, err := installer.DockerPlan(ctx, env.runner, host)
		if err != nil {
			fmt.Fprintln(stderr, "\nCan't install Docker:", err)
			return 1
		}
		dockerPlan = dp
	}
	imageTag, err := installer.ImageTag(env.commit)
	if err != nil && !o.dryRun {
		fmt.Fprintln(stderr, "\nCan't set up the Linx services:", err)
		return 1
	}
	// The first page's own certificate names every address the link is
	// printed at: this server's, and its public one when that differs.
	addrs := []netip.Addr{address}
	if w.publicAddress != nil {
		if pub, err := w.publicAddress(ctx); err == nil && pub != address {
			addrs = append(addrs, pub)
		}
	}
	now := time.Now()
	if w.now != nil {
		now = w.now()
	}
	tlsPlan, err := installer.FirstPageTLSPlan(addrs, env.readFile, now)
	if err != nil {
		fmt.Fprintln(stderr, "\nCan't make the installer's certificate:", err)
		return 1
	}
	swapPlan, err := installer.SwapPlan(host, env.readFile)
	if err != nil {
		fmt.Fprintln(stderr, "\nCan't check the swap settings:", err)
		return 1
	}
	phases := []struct {
		title string
		plan  installer.Plan
	}{
		{"Adding swap (this server has little memory)", swapPlan},
		{"Installing Docker", dockerPlan},
		{"Installing the linx command", append(installer.CLIPlan(env.executable, env.resolve),
			installer.HelperBinariesPlan(env.executable, env.resolve, env.stat)...)},
		{"Making the installer's temporary certificate", tlsPlan},
		{"Downloading Linx and starting the installer", installer.InstallStackPlan(imageTag, address)},
	}
	if o.dryRun {
		fmt.Fprintln(stdout, "\nSetup will:")
		for _, ph := range phases {
			for _, s := range ph.plan {
				fmt.Fprintf(stdout, "  - %s\n", s.Title)
				if s.Cmd != nil {
					fmt.Fprintf(stdout, "      $ %s\n", s.Cmd)
				}
			}
		}
		fmt.Fprintf(stdout, "  - Start the web install (%s) and print its link\n", install.Unit)
		fmt.Fprintln(stdout, "\nDry run: nothing was changed.")
		return 0
	}
	for _, ph := range phases {
		if len(ph.plan) == 0 {
			continue
		}
		fmt.Fprintf(stdout, "%s … ", ph.title)
		if err := ph.plan.Execute(ctx, env.runner, func(installer.Step) {}); err != nil {
			fmt.Fprintln(stdout, "failed")
			reportStepError(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "done")
	}

	// 3. The linx-setup service: the rest happens with the browser.
	fmt.Fprint(stdout, "Starting the web install … ")
	_, _ = env.runner.Run(ctx, nil, "systemctl", "reset-failed", install.Unit)
	if out, err := env.runner.Run(ctx, nil, "systemd-run", "--unit", install.Unit, "--description", "Linx setup: the web install",
		"--collect", "--property", "Restart=on-failure", "--property", "RestartSec=5",
		installer.CLIPath, "install-service"); err != nil {
		fmt.Fprintln(stdout, "failed")
		fmt.Fprintf(stderr, "Can't start %s: %v\n%s\n", install.Unit, err, indent(strings.TrimSpace(string(out))))
		return 1
	}
	fmt.Fprintln(stdout, "done")
	return followInstall(ctx, stdout, env, false)
}

func findingMark(l installer.Level) string {
	switch l {
	case installer.OK:
		return "✓"
	case installer.Warn:
		return "!"
	}
	return "✕"
}

func reportStepError(stderr io.Writer, err error) {
	var se *installer.StepError
	fmt.Fprintf(stderr, "\nSetup stopped: %v\n", err)
	if errors.As(err, &se) && se.Output != "" {
		fmt.Fprintf(stderr, "Last output:\n%s\n", indent(se.Output))
	}
	fmt.Fprintln(stderr, "Fix the problem above and run setup again; steps that already finished are safe to repeat.")
}

func unitActive(ctx context.Context, r installer.Runner) bool {
	_, err := r.Run(ctx, nil, "systemctl", "is-active", "--quiet", install.Unit)
	return err == nil
}

// installerRunning reports whether port 6464's container is Linx's own
// installer (install mode, from a setup started before, e.g. before
// --new-link): starting the installer again replaces it, so the port isn't
// taken by something else (found in the install demo).
func installerRunning(ctx context.Context, r installer.Runner) bool {
	out, err := r.Run(ctx, nil, "docker", "inspect", "--format", "{{join .Config.Cmd \" \"}}", "linx-control-plane")
	return err == nil && strings.TrimSpace(string(out)) == "install-server"
}

func runningContainers(ctx context.Context, r installer.Runner) int {
	out, err := r.Run(ctx, nil, "docker", "ps", "--quiet")
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(out)))
}

// followInstall prints the link (or who holds it) and the service's
// progress as it happens, until the page closes, the service stops or the
// terminal is closed. again: setup was already running (a second `sudo
// linx setup`), so every line so far is shown.
func followInstall(ctx context.Context, stdout io.Writer, env setupEnv, again bool) int {
	w := env.web
	var st install.HostState
	deadline := w.now().Add(linkWait)
	for {
		s, err := install.LoadHostState(w.statePath)
		// A service just started always writes a state that's still open
		// (a new link, or the one it carries on): until then the file can
		// still hold the old, ended one (after --new-link).
		if err == nil && (s.Secret != "" || s.View.SessionHash != "" || s.View.Ended != "") && (again || s.View.Ended == "") {
			st = s
			break
		}
		if !w.now().Before(deadline) || !unitActive(ctx, env.runner) {
			fmt.Fprintf(stdout, "\nThe web install didn't start. See what happened with:  journalctl --unit %s\n", install.Unit)
			return 1
		}
		if !w.wait(ctx, 500*time.Millisecond) {
			return 0
		}
	}

	switch {
	case st.Secret != "":
		printLink(stdout, env, st)
	case st.View.SessionHash != "" && st.View.Ended == "":
		fmt.Fprintf(stdout, "\nBeing set up in another browser (opened %s).\n"+
			"To start again there:  sudo linx setup --new-link\n", clock(st.View.ClaimedAt))
	}
	if st.View.Ended == "" {
		fmt.Fprintln(stdout, "\nWaiting for you in the browser. You can close this window; setup carries on.\n"+
			"Run  sudo linx setup  again to see where it's up to.")
	}
	fmt.Fprintln(stdout)
	shown := 0
	if !again {
		shown = len(st.Progress)
		for _, p := range st.Progress {
			printProgress(stdout, p)
		}
	}
	for {
		for _, p := range st.Progress[min(shown, len(st.Progress)):] {
			printProgress(stdout, p)
		}
		shown = len(st.Progress)
		if st.View.Ended != "" {
			return 0
		}
		if !unitActive(ctx, env.runner) {
			fmt.Fprintf(stdout, "\nSetup stopped. Run  sudo linx setup  again; to see why:  journalctl --unit %s\n", install.Unit)
			return 1
		}
		if !w.wait(ctx, time.Second) {
			return 0
		}
		if s, err := install.LoadHostState(w.statePath); err == nil {
			st = s
		}
	}
}

func printProgress(w io.Writer, p install.Progress) {
	mark := "✓"
	switch {
	case p.Failed:
		mark = "✕"
	case p.Waiting:
		mark = "…"
	}
	// A line can carry words from outside (Let's Encrypt's, the DNS
	// company's): never escape sequences for this terminal.
	text := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, p.Text)
	if n := 58 - len([]rune(text)); n > 0 {
		text += strings.Repeat(" ", n)
	}
	fmt.Fprintf(w, "  %s %s %s\n", mark, text, clock(p.At))
}

func clock(t time.Time) string { return t.Local().Format("15:04") }

// printLink shows the link at each address it can be opened from
// (docs/ui/INSTALL_SCREENS.md §1).
func printLink(w io.Writer, env setupEnv, st install.HostState) {
	fmt.Fprintln(w, "\nOpen this link in a browser to finish setting up Linx:")
	printLinkAt(w, env, st, "/install")
	fmt.Fprintln(w, "\nIt works once, for four hours, in the first browser that opens it. Open it in the browser you'll\n"+
		"finish in: after that it won't open anywhere else, not even for you (sudo linx setup --new-link makes a new one).")
	printFingerprint(w, env)
	if local, ok := env.web.routeAddress(); ok && publicip.IsPublic(local) {
		fmt.Fprintf(w, "If it doesn't open, allow TCP port %d in your server provider's firewall\n"+
			"(this server's own firewall is already open for it).\n", install.Port)
	}
}

// printLinkAt shows a link to page/<secret> on port 6464 at each address
// it can be opened from.
func printLinkAt(w io.Writer, env setupEnv, st install.HostState, page string) {
	link := func(a string) string {
		return fmt.Sprintf("https://%s%s/%s", netip.AddrPortFrom(netip.MustParseAddr(a), install.Port), page, st.Secret)
	}
	local, _ := env.web.routeAddress()
	public := st.View.Facts.PublicAddress
	if local.IsValid() && publicip.IsPublic(local) {
		fmt.Fprintf(w, "\n  %s\n", link(local.String()))
	} else {
		if local.IsValid() {
			fmt.Fprintf(w, "\n  %s\n  (on a computer on the same network as this server)\n", link(local.String()))
		}
		if public != "" && public != local.String() {
			fmt.Fprintf(w, "\n  %s\n  (from anywhere else, if port %d on %s is sent to this server)\n", link(public), install.Port, public)
		}
	}
}

// printFingerprint shows the port 6464 certificate's fingerprint, to check
// in the browser.
func printFingerprint(w io.Writer, env setupEnv) {
	if b, err := env.readFile(install.FirstPageTLSDir + "/" + install.FirstPageCertFile); err == nil {
		if fp, err := install.Fingerprint(b); err == nil {
			fmt.Fprintf(w, "\nYour browser will warn that it doesn't know this page's certificate. That's expected:\n"+
				"Linx makes its own for this page. To be sure it's really this server, open the\n"+
				"certificate's details in the browser and check its SHA-256 fingerprint is:\n\n  %s\n", fp)
		}
	}
}

// runInstallService is `linx install-service`: the web install on the
// host, run by `linx setup` as the linx-setup systemd service. Not for
// running by hand.
func runInstallService(ctx context.Context, args []string, stderr io.Writer, env setupEnv) int {
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx install-service runs as the linx-setup service; run sudo linx setup instead.")
		return 1
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(stderr, nil))
	lan := env.lan()
	imageTag, err := installer.ImageTag(env.commit)
	if err != nil {
		log.Error("which Linx images to use", "err", err)
		return 1
	}
	if len(args) >= 1 && args[0] == settingsFlag {
		// An installed server: the full Linx's Server settings page, and
		// the repair page on port 6464 with --repair.
		repair := len(args) == 2 && args[1] == repairFlag
		h := &install.SettingsHost{
			Apply: newWebSettings(newWebApply(env, lan, imageTag), repair),
			Log:   log,
			Dial:  func(ctx context.Context) (io.ReadWriteCloser, error) { return ops.DialBridge(ctx, "install-bridge") },
		}
		if repair {
			h.RepairPath = env.web.repairPath
		}
		err := h.Run(ctx)
		if repair && (err == nil || errors.Is(err, fs.ErrNotExist)) {
			// Time's up (or stopped): port 6464 closes again.
			if cerr := closeRepair(context.WithoutCancel(ctx), env); cerr != nil {
				log.Error("closing the repair page", "err", cerr)
			}
			if err != nil {
				// No repair link (setup closed it): nothing to serve, and
				// nothing a restart would fix.
				log.Warn("no repair link to serve", "err", err)
				return 0
			}
		}
		if err != nil {
			log.Error("the Server settings page", "err", err)
			return 1
		}
		return 0
	}
	address, ok := env.web.routeAddress()
	if !ok {
		log.Error("can't tell this server's network address")
		return 1
	}
	h := &install.Host{
		Path:  env.web.statePath,
		Log:   log,
		Now:   env.web.now,
		Facts: func(ctx context.Context) install.Facts { return installFacts(ctx, env, lan) },
		Dial:  func(ctx context.Context) (io.ReadWriteCloser, error) { return ops.DialBridge(ctx, "install-bridge") },
		Check: func(ctx context.Context, a install.Answers) (string, []install.FieldError, error) {
			base, err := loadSetupConfig("", env)
			if err != nil {
				base = installer.DefaultConfig()
			}
			cfg, errs := installer.WebConfig(base, a, lan, env.web.now())
			if len(errs) > 0 {
				return "", errs, nil
			}
			save := installer.Plan{{Title: "Save your answers", File: &installer.File{
				Path: installer.ConfigPath, Data: cfg.Marshal(), Mode: 0o600, DirMode: 0o755,
			}}}
			if err := save.Execute(ctx, env.runner, func(installer.Step) {}); err != nil {
				return "", nil, err
			}
			return installer.WebProgress(cfg), nil, nil
		},
		Cert:  &webCert{env: env, lan: lan, imageTag: imageTag, address: address},
		Apply: newWebApply(env, lan, imageTag),
		OnEnd: func(ctx context.Context, reason string) {
			if reason == install.EndedFinished || reason == install.EndedStopped {
				// The full stack replaced the installer's containers (same
				// project): "down" on install.yaml would stop them.
				_, _ = env.runner.Run(ctx, nil, "docker", "network", "rm", "linx-install")
				return
			}
			log.Info("the install page closed; stopping the installer", "reason", reason)
			if err := installer.InstallStackDown(ctx, env.runner); err != nil {
				log.Error("stopping the installer", "err", err)
			}
		},
	}
	if err := h.Start(ctx); err != nil {
		log.Error("starting the web install", "err", err)
		return 1
	}
	h.Run(ctx)
	return 0
}

// installFacts is what the install pages show as found, not asked.
func installFacts(ctx context.Context, env setupEnv, lan installer.LAN) install.Facts {
	f := install.Facts{Where: install.WhereRented, Hardware: hardware(env.detect()), TimeZone: installer.HostTimezone()}
	if lan.OK() {
		f.Where, f.LANAddress, f.LANNetwork = install.WhereHome, lan.Address.String(), lan.Network.String()
	}
	if a, err := env.web.publicAddress(ctx); err == nil {
		f.PublicAddress = a.String()
	}
	f.Port443 = installer.PortUser(ctx, env.runner, installer.PublicPort)
	return f
}

func hardware(h hostinfo.Info) string {
	parts := []string{fmt.Sprintf("%d processor cores", h.CPUs)}
	if h.MemBytes > 0 {
		parts = append(parts, fmt.Sprintf("%.0f GB memory", float64(h.MemBytes)/(1<<30)))
	}
	if h.DiskTotal > 0 {
		parts = append(parts, fmt.Sprintf("%.0f GB free", float64(h.DiskFree)/(1<<30)))
	}
	return strings.Join(parts, ", ")
}

// settingsFlag runs install-service in settings mode.
const settingsFlag = "--settings"

// ServerSettingsPath is the full Linx's Server settings page.
const ServerSettingsPath = "/admin/system/server"

// runServerSettings is `sudo linx setup` on an installed server, in the
// browser (docs/INSTALL.md §7, docs/ui/INSTALL_SCREENS.md §5.1): the
// linx-setup service opens the Server settings page for four hours, for a
// system admin to change this server's domain, front door, size, Portainer
// and DNS token. When https://<domain> doesn't answer from here (or with
// --new-link), the page is also opened on port 6464 behind a new one-time
// link: the repair page (§5.3).
func runServerSettings(ctx context.Context, o webOptions, cfg installer.Config, stdout, stderr io.Writer, env setupEnv) int {
	address := cfg.Address()
	url := address + ServerSettingsPath
	if o.dryRun {
		fmt.Fprintf(stdout, "Linx is installed at %s. Setup would open the Server settings page for four hours:\n  %s\n", address, url)
		fmt.Fprintf(stdout, "If that address doesn't work from this server, or with --new-link, it would also open it on port %d.\n\nDry run: nothing was changed.\n", install.Port)
		return 0
	}
	problem := ""
	if env.web.secureCheck != nil {
		if err := env.web.secureCheck(ctx, cfg.Domain.Name, cfg.FrontDoor.Port()); err != nil {
			problem = err.Error()
			fmt.Fprintf(stdout, "Linx is installed, but %s didn't answer from this server:\n  %v\n\n", address, err)
		}
	}
	if problem == "" {
		fmt.Fprintf(stdout, "Linx is installed at %s (working ✓).\n\n", address)
	}
	repair := problem != "" || o.newLink
	active := unitActive(ctx, env.runner)
	_, err := os.Stat(env.web.repairPath)
	if repairOpen := err == nil; active && (repair || repairOpen) {
		// A new repair link replaces whatever the service had open; with
		// the address working again, the repair page closes.
		_, _ = env.runner.Run(ctx, nil, "systemctl", "stop", install.Unit)
		active = false
	}
	if !active {
		if err := closeRepair(ctx, env); err != nil {
			fmt.Fprintln(stderr, "Can't close the last repair page:", err)
			return 1
		}
	}
	var rs install.RepairState
	if repair {
		var code int
		if rs, code = openRepair(ctx, o, cfg, problem, stdout, stderr, env); code != 0 {
			return code
		}
	}
	if !active {
		args := []string{"install-service", settingsFlag}
		if repair {
			args = append(args, repairFlag)
		}
		_, _ = env.runner.Run(ctx, nil, "systemctl", "reset-failed", install.Unit)
		if out, err := env.runner.Run(ctx, nil, "systemd-run", append([]string{"--unit", install.Unit, "--description", "Linx setup: the Server settings page",
			"--collect", "--property", "Restart=on-failure", "--property", "RestartSec=5",
			installer.CLIPath}, args...)...); err != nil {
			fmt.Fprintf(stderr, "Can't start %s: %v\n%s\n", install.Unit, err, indent(strings.TrimSpace(string(out))))
			if repair {
				_ = closeRepair(ctx, env)
			}
			return 1
		}
	}
	if !repair {
		fmt.Fprintf(stdout, "To change its domain, front door, size, Portainer or DNS token, sign in as a system admin at:\n\n  %s\n\n"+
			"That page can make changes for the next four hours. Nothing was reopened.\n", url)
		return 0
	}
	if problem != "" {
		fmt.Fprintf(stdout, "If %s opens in your browser anyway, use the Server settings page there:\n  %s\n\n", address, url)
	}
	fmt.Fprintln(stdout, "Otherwise open this link to fix it. It works once, for four hours, in the first browser that opens it:\n"+
		"open it in the browser you'll use, since after that it won't open anywhere else (--new-link makes a new one).")
	printRepairLink(stdout, env, rs)
	if rs.NoSignIn {
		fmt.Fprintln(stdout, "\nThis link skips the sign-in: anyone who opens it first can change this server's settings. Don't share it.")
	} else {
		fmt.Fprintln(stdout, "\nYou'll sign in there as a system admin, with your password and authenticator app or a recovery code:\n"+
			"passkeys can't work at that address. Passkey only? Run  sudo linx setup --new-link --no-sign-in  instead.")
	}
	fmt.Fprintf(stdout, "Port %d closes again when the link's time is up, or after  sudo linx setup  once the address works.\n", install.Port)
	return 0
}

// repairFlag runs install-service's settings mode with the repair page.
const repairFlag = "--repair"

// openRepair publishes the repair page on port 6464 and writes its new
// link (docs/INSTALL.md §7).
func openRepair(ctx context.Context, o webOptions, cfg installer.Config, problem string, stdout, stderr io.Writer, env setupEnv) (install.RepairState, int) {
	address, ok := env.web.routeAddress()
	if !ok {
		fmt.Fprintln(stderr, "Can't tell this server's network address. Check it's connected, with a default route, and run setup again.")
		return install.RepairState{}, 1
	}
	if user := installer.PortUser(ctx, env.runner, install.Port); user != "" {
		fmt.Fprintf(stderr, "Port %d is used by %s. The repair page needs it: stop that, then run setup again.\n", install.Port, user)
		return install.RepairState{}, 1
	}
	addrs := []netip.Addr{address}
	if env.web.publicAddress != nil {
		if pub, err := env.web.publicAddress(ctx); err == nil && pub != address {
			addrs = append(addrs, pub)
		}
	}
	tlsPlan, err := installer.FirstPageTLSPlan(addrs, env.readFile, env.web.now())
	if err != nil {
		fmt.Fprintln(stderr, "Can't make the repair page's certificate:", err)
		return install.RepairState{}, 1
	}
	fmt.Fprintf(stdout, "Opening the repair page on port %d … ", install.Port)
	if err := webExecute(ctx, env, append(tlsPlan, installer.RepairOpenPlan(cfg, env.lan(), address)...)); err != nil {
		fmt.Fprintln(stdout, "failed")
		reportStepError(stderr, err)
		_ = closeRepair(ctx, env)
		return install.RepairState{}, 1
	}
	rs := install.NewRepairState(env.web.now(), o.noSignIn, problem)
	if err := rs.Save(env.web.repairPath); err != nil {
		fmt.Fprintln(stdout, "failed")
		fmt.Fprintln(stderr, "Can't save the repair link:", err)
		_ = closeRepair(ctx, env)
		return install.RepairState{}, 1
	}
	fmt.Fprintln(stdout, "done")
	return rs, 0
}

// closeRepair closes port 6464 again after a repair page, if one was
// open: the firewall set emptied, the control plane without the override.
func closeRepair(ctx context.Context, env setupEnv) error {
	if _, err := os.Stat(env.web.repairPath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := webExecute(ctx, env, installer.RepairClosePlan()); err != nil {
		return err
	}
	return os.Remove(env.web.repairPath)
}

func webExecute(ctx context.Context, env setupEnv, p installer.Plan) error {
	if env.web.execute != nil {
		return env.web.execute(ctx, p)
	}
	return p.Execute(ctx, env.runner, func(installer.Step) {})
}

// printRepairLink shows the repair link at each address it can be opened
// from, and the page certificate's fingerprint.
func printRepairLink(w io.Writer, env setupEnv, rs install.RepairState) {
	st := install.HostState{Secret: rs.Secret}
	if env.web.publicAddress != nil {
		if pub, err := env.web.publicAddress(context.Background()); err == nil {
			st.View.Facts.PublicAddress = pub.String()
		}
	}
	printLinkAt(w, env, st, install.RepairPage)
	printFingerprint(w, env)
}

// secureAddressWorks checks https://<domain> answers with a certificate
// this server's own trust store accepts for that name.
func secureAddressWorks(ctx context.Context, domain string, port int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: domain, MinVersion: tls.VersionTLS12}}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(domain, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return c.Close()
}
