package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
	removeFile    func(string) error
	now           func() time.Time
	// wait pauses between looks at the service's progress; false once ctx
	// has ended.
	wait func(ctx context.Context, d time.Duration) bool
}

func realWebEnv() webEnv {
	return webEnv{
		routeAddress: installer.DefaultRouteAddress,
		publicAddress: func(ctx context.Context) (netip.Addr, error) {
			ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			return publicip.Lookup(ctx, &http.Client{Timeout: 8 * time.Second}, publicip.TraceURL)
		},
		statePath:  install.HostPath,
		removeFile: os.Remove,
		now:        time.Now,
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
	dryRun, newLink, replaceDocker bool
}

// linkWait is how long the terminal waits for the service's first link.
const linkWait = 90 * time.Second

func runWebSetup(ctx context.Context, o webOptions, stdout, stderr io.Writer, env setupEnv) int {
	if !o.dryRun && !env.isRoot {
		fmt.Fprintln(stderr, "linx setup changes system settings and must run as root. Try: sudo linx setup")
		return 1
	}
	if cfg, err := loadSetupConfig("", env); err == nil && cfg.Installed() {
		fmt.Fprintf(stdout, "Linx is already installed here, at https://meet.%s.\n"+
			"To change its settings, edit %s and run:\n\n  sudo linx setup --config %s\n\nNothing was changed.\n",
			cfg.Domain.Name, installer.ConfigPath, installer.ConfigPath)
		return 0
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
		if err := w.removeFile(w.statePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stderr, "Can't remove the old link:", err)
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
	if user := installer.PortUser(ctx, env.runner, install.Port); user != "" {
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
	phases := []struct {
		title string
		plan  installer.Plan
	}{
		{"Installing Docker", dockerPlan},
		{"Installing the linx command", installer.CLIPlan(env.executable, env.resolve)},
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
		if err == nil && (s.Secret != "" || s.View.SessionHash != "" || s.View.Ended != "") {
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
	text := p.Text
	if n := 58 - len([]rune(text)); n > 0 {
		text += strings.Repeat(" ", n)
	}
	fmt.Fprintf(w, "  %s %s %s\n", mark, text, clock(p.At))
}

func clock(t time.Time) string { return t.Local().Format("15:04") }

// printLink shows the link at each address it can be opened from
// (docs/ui/INSTALL_SCREENS.md §1).
func printLink(w io.Writer, env setupEnv, st install.HostState) {
	link := func(a string) string {
		return fmt.Sprintf("http://%s/install/%s", netip.AddrPortFrom(netip.MustParseAddr(a), install.Port), st.Secret)
	}
	fmt.Fprintln(w, "\nOpen this link in a browser to finish setting up Linx:")
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
	fmt.Fprintln(w, "\nIt works once, for one hour, in the first browser that opens it.")
	if local.IsValid() && publicip.IsPublic(local) {
		fmt.Fprintf(w, "If it doesn't open, allow TCP port %d in your server provider's firewall\n"+
			"(this server's own firewall is already open for it).\n", install.Port)
	}
}

// runInstallService is `linx install-service`: the web install on the
// host, run by `linx setup` as the linx-setup systemd service. Not for
// running by hand.
func runInstallService(ctx context.Context, stderr io.Writer, env setupEnv) int {
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx install-service runs as the linx-setup service; run sudo linx setup instead.")
		return 1
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(stderr, nil))
	lan := env.lan()
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
		OnEnd: func(ctx context.Context, reason string) {
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
	f := install.Facts{Where: install.WhereRented, Hardware: hardware(env.detect())}
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
