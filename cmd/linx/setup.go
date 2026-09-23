package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"slices"
	"strings"

	"linxpbx.com/linx/internal/hostinfo"
	"linxpbx.com/linx/internal/installer"
)

// setupEnv is everything setup touches on the host, so tests can fake it.
type setupEnv struct {
	detect      func() hostinfo.Info
	runner      installer.Runner
	stdin       io.Reader
	interactive bool // stdin is a terminal
	isRoot      bool
	lanAddress  func() netip.Addr
	savedConfig func() ([]byte, error) // reads installer.ConfigPath
	readFile    func(string) ([]byte, error)
}

func realSetupEnv() setupEnv {
	fi, err := os.Stdin.Stat()
	return setupEnv{
		detect:      hostinfo.Detect,
		runner:      installer.ExecRunner{},
		stdin:       os.Stdin,
		interactive: err == nil && fi.Mode()&os.ModeCharDevice != 0,
		isRoot:      os.Geteuid() == 0,
		lanAddress:  installer.LANAddress,
		savedConfig: func() ([]byte, error) { return os.ReadFile(installer.ConfigPath) },
		readFile:    os.ReadFile,
	}
}

const setupUsage = `Usage: sudo linx setup [--config FILE] [--dry-run]

Checks this server, installs Docker if needed, and saves your answers to
` + installer.ConfigPath + `.

  --config FILE  use answers from FILE instead of asking questions
  --dry-run      show what setup would do without changing anything
`

func runSetup(ctx context.Context, args []string, stdout, stderr io.Writer, env setupEnv) int {
	fl := flag.NewFlagSet("setup", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	configFile := fl.String("config", "", "")
	dryRun := fl.Bool("dry-run", false, "")
	if err := fl.Parse(args); err != nil || fl.NArg() > 0 {
		fmt.Fprint(stderr, setupUsage)
		return 2
	}

	cfg, err := loadSetupConfig(*configFile, env)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ask := *configFile == ""
	if !*dryRun && !env.isRoot {
		fmt.Fprintln(stderr, "linx setup changes system settings and must run as root. Try: sudo linx setup")
		return 1
	}
	if ask && !env.interactive {
		fmt.Fprintln(stderr, "linx setup needs answers. Run it in a terminal, or pass --config setup.yaml.")
		return 1
	}
	p := &prompter{in: bufio.NewReader(env.stdin), out: stdout}

	// 1. Host checks.
	fmt.Fprintln(stdout, "Checking this server…")
	host := env.detect()
	findings := installer.CheckHost(host)
	for _, f := range findings {
		fmt.Fprintf(stdout, "  %-8s %s\n", f.Level, f.Message)
	}
	if installer.HasFailure(findings) {
		fmt.Fprintln(stderr, "\nSetup can't continue until the problems above are fixed.")
		return 1
	}

	// 2. Resource profile.
	suggested, reason := installer.SuggestProfile(host)
	fmt.Fprintf(stdout, "\nSuggested resource profile: %s (%s).\n", suggested, reason)
	profile := suggested
	if cfg.ResourceProfile != installer.ProfileAuto {
		profile = cfg.ResourceProfile
	}
	if ask {
		labels := map[string]string{}
		for _, pr := range installer.Profiles {
			labels[pr] = installer.ProfileDescription[pr]
		}
		if profile, err = p.choose("Which resource profile should Linx use?", installer.Profiles, labels, profile); err != nil {
			return inputError(stderr, err)
		}
	}
	cfg.ResourceProfile = profile
	if profile == suggested {
		cfg.ResourceProfile = installer.ProfileAuto
	}

	// 3. Docker.
	var plan installer.Plan
	fmt.Fprintln(stdout, "\nChecking Docker…")
	docker := installer.DetectDocker(ctx, env.runner)
	action, msg := docker.Assess()
	fmt.Fprintln(stdout, "  "+msg)
	switch action {
	case installer.DockerBlocked:
		return 1
	case installer.DockerInstall, installer.DockerUpgrade:
		if ask {
			q := "Install Docker from Docker's official repository now?"
			if docker.Source == installer.SourceDistro {
				q = "Replace it with Docker's official version? Your containers, images and volumes are kept; running containers restart."
			}
			ok, err := p.confirm(q, true)
			if err != nil {
				return inputError(stderr, err)
			}
			cfg.Docker.Install = ok
		}
		if !cfg.Docker.Install {
			fmt.Fprintln(stderr, "Linx needs Docker. Run setup again when you're ready to install it (in setup.yaml: docker.install: true).")
			return 1
		}
		dp, err := installer.DockerPlan(ctx, env.runner, host)
		if err != nil {
			fmt.Fprintln(stderr, "Can't install Docker:", err)
			return 1
		}
		plan = append(plan, dp...)
	}

	// 4. Optional container management UI.
	if ask {
		fmt.Fprint(stdout, "\nA container management screen lets you see and control Docker containers from a web browser.\n"+
			"It has full control of this server, so Linx makes it reachable from your local network only.\n")
		labels := map[string]string{
			installer.ContainerUINone:      "don't install one (recommended if you're unsure)",
			installer.ContainerUIPortainer: "Portainer CE, with a generated admin password",
		}
		if cfg.ContainerUI, err = p.choose("Install a container management screen?", installer.ContainerUIs, labels, cfg.ContainerUI); err != nil {
			return inputError(stderr, err)
		}
	}
	var portainer installer.PortainerSetup
	if cfg.ContainerUI == installer.ContainerUIPortainer {
		portainer = installer.PortainerPlan(env.lanAddress())
		plan = append(plan, portainer.Plan...)
	}

	plan = append(plan, installer.Step{Title: "Save your answers to " + installer.ConfigPath, File: &installer.File{
		Path: installer.ConfigPath, Data: cfg.Marshal(), Mode: 0o600, DirMode: 0o755,
	}})

	// 5. Confirm and apply.
	fmt.Fprintln(stdout, "\nSetup will:")
	for i, s := range plan {
		fmt.Fprintf(stdout, "  %2d. %s\n", i+1, s.Title)
		if *dryRun && s.Cmd != nil {
			fmt.Fprintf(stdout, "        $ %s\n", s.Cmd)
		}
	}
	if *dryRun {
		fmt.Fprintln(stdout, "\nDry run: nothing was changed.")
		return 0
	}
	if ask {
		ok, err := p.confirm("Go ahead?", true)
		if err != nil {
			return inputError(stderr, err)
		}
		if !ok {
			fmt.Fprintln(stdout, "Nothing was changed.")
			return 1
		}
	}
	fmt.Fprintln(stdout)
	err = plan.Execute(ctx, env.runner, func(s installer.Step) { fmt.Fprintf(stdout, "  → %s\n", s.Title) })
	if err != nil {
		var se *installer.StepError
		fmt.Fprintf(stderr, "\nSetup stopped: %v\n", err)
		if errors.As(err, &se) && se.Output != "" {
			fmt.Fprintf(stderr, "Last output:\n%s\n", indent(se.Output))
		}
		fmt.Fprintln(stderr, "Fix the problem above and run setup again; steps that already finished are safe to repeat.")
		return 1
	}

	// 6. Summary.
	fmt.Fprintf(stdout, "\nPrerequisites are ready. Resource profile: %s.\n", profile)
	if cfg.ContainerUI == installer.ContainerUIPortainer {
		printPortainer(stdout, portainer)
	}
	return 0
}

func printPortainer(w io.Writer, s installer.PortainerSetup) {
	fmt.Fprintln(w, "\nPortainer (container management):")
	if s.LocalOnly {
		fmt.Fprintln(w, "  This server has no local-network address, so Portainer only listens on the server itself.")
		fmt.Fprintln(w, "  From your computer run:  ssh -L 9443:127.0.0.1:9443 <you>@<this server>")
		fmt.Fprintln(w, "  then open:               https://localhost:9443")
	} else {
		fmt.Fprintf(w, "  Address:  %s (from your local network only)\n", s.URL)
	}
	fmt.Fprintln(w, "  Username: admin")
	fmt.Fprintf(w, "  Password: %s\n", s.Password)
	fmt.Fprintln(w, "  The password is also saved in /etc/linx/secrets/portainer_admin_password (root only).")
	fmt.Fprintln(w, "  Your browser will warn about Portainer's own certificate the first time; that's expected on your local network.")
	fmt.Fprintln(w, "  Portainer can control everything on this server. Never forward its port on your router.")
}

func loadSetupConfig(path string, env setupEnv) (installer.Config, error) {
	var (
		b   []byte
		err error
	)
	if path != "" {
		if b, err = env.readFile(path); err != nil {
			return installer.Config{}, fmt.Errorf("reading %s: %w", path, err)
		}
	} else if b, err = env.savedConfig(); err != nil {
		// No saved answers yet (or not readable without sudo): start fresh.
		return installer.DefaultConfig(), nil
	}
	return installer.ParseConfig(strings.NewReader(string(b)))
}

func inputError(w io.Writer, err error) int {
	fmt.Fprintln(w, "\nSetup cancelled:", err)
	return 1
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

// prompter asks questions on the terminal.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func (p *prompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func (p *prompter) confirm(q string, def bool) (bool, error) {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	for {
		fmt.Fprintf(p.out, "%s %s ", q, hint)
		s, err := p.line()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(p.out, "Please answer y or n.")
	}
}

// choose shows numbered options and returns the chosen one. Enter picks def.
func (p *prompter) choose(q string, options []string, labels map[string]string, def string) (string, error) {
	fmt.Fprintln(p.out, q)
	for i, o := range options {
		mark := " "
		if o == def {
			mark = "*"
		}
		fmt.Fprintf(p.out, "  %s %d) %-12s %s\n", mark, i+1, o, labels[o])
	}
	for {
		fmt.Fprintf(p.out, "Choose 1-%d [Enter = %s]: ", len(options), def)
		s, err := p.line()
		if err != nil {
			return "", err
		}
		if s == "" {
			return def, nil
		}
		if slices.Contains(options, strings.ToLower(s)) {
			return strings.ToLower(s), nil
		}
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n >= 1 && n <= len(options) {
			return options[n-1], nil
		}
		fmt.Fprintln(p.out, "Please enter one of the numbers shown.")
	}
}
